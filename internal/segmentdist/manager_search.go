// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// Search is the CONSUMER side of the Manager: it queries BOTH per-graph engines
// (HNSW over the query vector, BM25 over the query text) and fuses their ranked
// hit lists with standard reciprocal rank fusion, returning the top-k fused
// Hits (ranked node IDs + fused scores). The caller hydrates those IDs into full
// nodes via a RETURN_MODE_NODES read (search_engine_hydrate.go).
//
// Both engines are loaded cache-first (dm.load / bm.load — idempotent, parallel
// Import) before the search. There is NO per-search degeneracy backstop: load()
// imports the full L2 corpus, so resident is not left poisoned-degenerate after a
// load, and a genuinely cold L2 is healed OFF the hot path by the boot-delay
// one-shot + the periodic reconcile, not by any per-search probe (see the inline
// note below). The two engine.Search calls run CONCURRENTLY over a bounded
// WaitGroup (the engines are independent and each is internally per-segment
// parallel), not serially.
//
// Failure-mode note: searchengine.SegmentedIndex.Search returns nil for an empty
// segment set, so a graph whose segments are not yet built/shipped yields an
// empty fused list (not an error) and the hydrator renders zero rows. No
// redundant empty-set guard is needed.
//
// The HNSW arm is skipped when queryVec is empty (a text-only query). For THIS
// method that then makes the fused result just the BM25 ranking unchanged (RRF
// over a single list is the identity ranking). The claim is scoped to
// Manager.Search deliberately: SearchOverlay (manager_search_overlay.go) fuses a
// BM25 list that is itself a merge of two pools' hits, so on the same text-only
// query its result is that merged ranking — not an unchanged single-pool one.
func (m *Manager) Search(
	ctx context.Context,
	gt kgtypes.GraphType,
	name, queryText string,
	queryVec []byte,
	k int,
) ([]searchengine.Hit, error) {
	if k <= 0 {
		return nil, nil
	}

	hnswHits, bm25Hits, err := m.searchPoolArms(ctx, gt, name, queryText, queryVec, k)
	if err != nil {
		return nil, err
	}

	// TRIGGER SITE for the residency budget: a completed search, on the path that
	// just made a pool resident. It runs HERE rather than inside searchPoolArms
	// because that function still holds both pools' read locks — enforceResidencyBudget
	// evicts under a write lock, and Go's RWMutex is not reentrant. The graph just
	// served is excluded for the same reason, as the second belt.
	m.enforceResidencyBudget([]graphKey{{graphType: gt, graphName: name}})

	// NEXT-TOUCH CONVERGENCE for any segment whose mapping republication failed.
	// BOTH ARMS, and that is a CORRECTNESS requirement rather than tidiness:
	// reclaimMerged is wired through Options.OnMerge for both formats, so both
	// families accumulate pending remaps, while managerFor returns only the HNSW
	// instantiation and bm25ManagerFor the other. A single delegated call would
	// leave every BM25 pending remap undrained forever — the same silent forfeit
	// this drain exists to remove.
	//
	// Scope is the SEARCHED graph, not a global walk: a budget is global, a remap
	// repair is not, and an eager sweep would be a new operational surface.
	//
	// THE DRAIN'S FAILURES SURFACE HERE, AND THIS IS WHERE THAT DECISION BELONGS.
	// drainRemapPending RETURNS its write failures rather than absorbing them, so the
	// policy call — what a failed mapping repair means — is made at the level that can
	// see the whole operation instead of at the write site. This level's answer: the
	// SEARCH IS NOT FAILED. The corpus is correct and was correctly searched; the
	// repair only ever tried to restore a memory property, so failing a good search
	// over it would turn a lost optimisation into a user-visible outage. It is
	// announced at ERROR with the joined cause, once for both arms.
	//
	// THIS IS A TERMINUS, NOT A SWALLOW, and the distinction is why the shape must
	// not be "tidied" back into a bare call. The Put error is RETURNED from the write
	// site and RETURNED again from the drain; it stops here because this is the first
	// level with enough context to decide, and it is surfaced rather than dropped. A
	// future reader who replaces this with `arm.drainRemapPending()` and no handling
	// reintroduces exactly the silent forfeit the return value was added to remove.
	var repairErr error
	for _, arm := range []remapArm{m.managerFor(gt, name), m.bm25ManagerFor(gt, name)} {
		repairErr = errors.Join(repairErr, arm.drainRemapPending())
	}
	if repairErr != nil {
		slog.Error("segmentdist: segment mapping repairs FAILED to re-persist — the segments stay correct but heap-resident for the life of this process",
			"graph", gt, "name", name, "err", repairErr)
	}

	// NEXT-TOUCH CONVERGENCE for any merge reclaim this pool ABORTED, on the same
	// terms and for BOTH ARMS for the same reason: reclaimMerged is wired through
	// Options.OnMerge for both formats, so both families accumulate obligations.
	// A retained obligation is the ONLY record naming the constituents an abort
	// stranded — the merge that superseded them cannot run again, and the prune's
	// live set is force-loaded from the L2 index it diffs — so an undrained arm
	// leaves them on disk for the life of the process.
	//
	// ITS FAILURES STOP HERE TOO, and the answer is the same one the paragraph above
	// argues: the SEARCH IS NOT FAILED. The corpus was correctly searched, and what a
	// failed discharge forfeits is the reclaim of constituents that are still perfectly
	// readable — dead weight on disk, never a wrong answer. It is announced at ERROR
	// with the joined cause rather than dropped.
	var dischargeErr error
	for _, arm := range []reclaimArm{m.managerFor(gt, name), m.bm25ManagerFor(gt, name)} {
		dischargeErr = errors.Join(dischargeErr, arm.drainReclaimPending())
	}
	if dischargeErr != nil {
		slog.Error("segmentdist: aborted merge reclaims FAILED to discharge — their superseded constituents stay in this client's L2 cache",
			"graph", gt, "name", name, "err", dischargeErr)
	}

	return reciprocalRankFusion([][]searchengine.Hit{hnswHits, bm25Hits}, k), nil
}

// searchPoolArms runs one POOL's half of a search: the per-graph preamble
// (account binding, merge nudge, admission, both engine loads) followed by the
// concurrent HNSW + BM25 arms, returning the two arms' raw hit lists unfused.
// Fusing is the caller's job, which is what lets SearchOverlay merge a second
// pool's arms in before the single fusion.
func (m *Manager) searchPoolArms(
	ctx context.Context,
	gt kgtypes.GraphType,
	name, queryText string,
	queryVec []byte,
	k int,
) ([]searchengine.Hit, []searchengine.Hit, error) {
	// Fail closed on an in-session account switch: this Manager's cacheDir and
	// per-graph sources belong to the account it was built under, so serving
	// from them after the selection moved would hand account A's segments to a
	// session the user has told the client is account B.
	if err := m.checkAccountBinding(ctx); err != nil {
		return nil, nil, err
	}

	// A user just searched this graph, so ask the reconcile loop to pull its delta
	// now rather than at its next tick. It sits AFTER the k<=0 guard because such a
	// call is not a user search, and BEFORE the loads below because a search against a
	// cold or broken engine is precisely a moment when a pull is worth asking for.
	m.nudgeMerge(gt, name)
	// The same instant, on the same key, for the same reason: a search is the
	// direct interaction that admits a graph into this process's working set,
	// which is what lets the background loops touch it at all. Both recorders
	// sit behind the k<=0 guard because such a call is not a user search.
	if m.admitGraph != nil {
		m.admitGraph(gt, name)
	}

	dm := m.managerFor(gt, name)
	bm := m.bm25ManagerFor(gt, name)

	// HOLD THE RESIDENCY READ LOCK ACROSS BOTH LOADS AND BOTH ENGINE SEARCHES. The
	// deferred unlocks release at function return, i.e. after wg.Wait below, and
	// THAT SPAN MUST NOT BE NARROWED TO THE LOADS ALONE. load() and engine.Search
	// are two separate statements: an eviction landing between them leaves Search
	// reading an empty snapshot — zero hits, no error — which is a silent miss no
	// property of the engine's CAS publish prevents. Mutual exclusion is what closes
	// it; a minimum-idle-age heuristic would not.
	//
	// LOCK ORDER: a search takes TWO read locks (dm then bm) and eviction takes ONE
	// write lock at a time, so no cycle exists. The overlay path runs this function
	// for two graphs concurrently; each goroutine takes its own two read locks and
	// the argument is unchanged.
	dm.residencyMu.RLock()
	defer dm.residencyMu.RUnlock()
	bm.residencyMu.RLock()
	defer bm.residencyMu.RUnlock()

	// Stamp BOTH pools as searched. A search queries a graph's HNSW and BM25 arms
	// together, so evicting one format of a graph the user is actively searching
	// would be a half-eviction whose next search pays a reload anyway.
	dm.noteSearchTouch()
	bm.noteSearchTouch()

	// Load both engines' L2-resident set. Load is idempotent — the l2Loaded once-guard
	// short-circuits a repeated load() — so repeated searches do not re-load. There is
	// no per-search degeneracy backstop: load() imports the full L2 corpus, so resident
	// is not left poisoned-degenerate after a load, and a genuinely cold L2 is healed
	// off the hot path by the boot-delay one-shot + the periodic reconcile (bootstrap).
	// A cold L2 returns an empty engine here rather than reaching for anything else —
	// there is nothing else to reach for.
	if err := dm.load(ctx); err != nil {
		return nil, nil, err
	}
	if err := bm.load(ctx); err != nil {
		return nil, nil, err
	}

	// Run the two engine searches concurrently — each is independent and
	// internally per-segment parallel. Mirrors runRebuildFanOut's bounded
	// fan-out, scaled to the two fixed arms here.
	var (
		hnswHits []searchengine.Hit
		bm25Hits []searchengine.Hit
		wg       sync.WaitGroup
	)
	if len(queryVec) > 0 {
		wg.Go(func() {
			// Goroutine-local recover: a panic here (e.g. a nil engine for a
			// graph with no shipped segments) would otherwise crash the whole
			// process — the parent's recover cannot catch a child goroutine.
			// Log the stack and degrade this arm to empty.
			defer func() {
				if r := recover(); r != nil {
					slog.Error("segmentdist: PANIC in HNSW search arm",
						"graph", string(gt), "name", name,
						"panic", fmt.Sprintf("%v", r), "stack", string(debug.Stack()))
					hnswHits = nil
				}
			}()
			hnswHits = dm.engine.Search(queryVec, k)
		})
	}
	wg.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("segmentdist: PANIC in BM25 search arm",
					"graph", string(gt), "name", name,
					"panic", fmt.Sprintf("%v", r), "stack", string(debug.Stack()))
				bm25Hits = nil
			}
		}()
		bm25Hits = bm.engine.Search(bm25.NewQuery(queryText), k)
	})
	wg.Wait()

	return hnswHits, bm25Hits, nil
}

// VectorByID resolves a node's STORED binary vector from this graph's client-local
// HNSW segments — the query vector the "similar" search mode searches from. It
// loads the graph's HNSW engine cache-first (idempotent — empty delta = zero Fetch)
// exactly as Search does, so a fresh process resolves correctly without a prior
// search, then reads the vector off the engine's by-id seam (Phase 1
// SegmentedIndex.VectorByID).
//
// The (ok=false, err=nil) tuple distinguishes loaded-fine-but-no-such-id (the node
// is not embedded / not in any shipped segment yet) from a load failure (err!=nil).
// How ok=false is handled BELONGS TO THE CALLER, and the two callers read it
// oppositely and correctly:
//   - the mode:"similar" search claim turns ok=false into a LOUD error with rebuild
//     guidance — never a silent empty success, because a search that silently
//     returns nothing looks like "no similar nodes" rather than "no query vector";
//   - the propagation loop's leaf attachment treats ok=false as the ordinary
//     VECTORLESS case: the node is recorded for retry on a later pass, once its
//     vector has been embedded and shipped, and attachment proceeds for the rest.
//
// Only the HNSW engine is consulted (BM25 has no vectors).
//
// IT MATERIALIZES AN EVICTED POOL BUT DOES NOT STAMP THE SEARCH TOUCH, and the
// asymmetry is ticket constraint 2's "define hot/cold by last-search-touch" applied
// literally. Materializing is right for both callers — ok=false is a load-bearing
// answer the similar-mode claim turns into a loud error, so a silently-empty read
// here would be a wrong answer rather than a degradation — but neither caller is a
// user search, and the background propagation caller in particular must not be able
// to keep a pool hot. It takes the residency read lock across load+VectorByID for
// the same reason searchPoolArms does: the two are separate statements.
func (m *Manager) VectorByID(
	ctx context.Context,
	gt kgtypes.GraphType,
	name, externalID string,
) ([]byte, bool, error) {
	dm := m.managerFor(gt, name)
	dm.residencyMu.RLock()
	defer dm.residencyMu.RUnlock()
	if err := dm.load(ctx); err != nil {
		return nil, false, err
	}
	vec, ok := dm.engine.VectorByID(externalID)
	return vec, ok, nil
}
