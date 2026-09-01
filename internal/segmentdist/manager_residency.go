// SPDX-License-Identifier: Apache-2.0

// manager_residency.go — per-pool residency state and the eviction primitive.
//
// A pool's segments are decoded in RAM once loaded and stay there for the life of
// the process, so residency grows monotonically as new graphs are touched over a
// long session. This file bounds it: evictResident drops a cold pool's segments
// out of the engine and latches the pool `evicted`, and the next CONSUMER touch
// re-materializes it from the local L2 disk cache with no network round-trip.
//
// The accounting unit is RESIDENT HEAP BYTES — the engine's modeled Go heap,
// summed by searchengine.SegmentedIndex.ResidentHeapBytes over the per-segment
// membership index, the liveness bitset and whatever each payload declares it
// holds. It is a MODEL, and searchengine/residency.go documents its three terms
// and is the one place the model lives.
//
// A SEGMENT'S ON-DISK SIZE IS METERED SEPARATELY AND NEVER COMPARED. A mapped
// segment's blob is page cache — evictable, shared between processes, invisible
// to the garbage collector — so counting it against a heap budget meters memory
// the heap does not hold. Doing exactly that is the defect this unit replaced:
// the budget saturated on page-cache-backed bytes and every pool holding real
// heap became a standing eviction candidate, so eviction fired on the wrong
// pressure signal. residentSeg.mappedBytes still carries the encoded size and
// the budget pass reports it, for operators, beside the number it compares.
//
// EVICTION IS NOT A FALLBACK, and neither is its decline. evictResident either
// unloads a pool it has PROVEN it can restore from L2, or it does nothing at all
// and says so. There is no compensating path here that repairs, smooths or writes
// off a pool that could not be evicted or could not be reloaded — a pool whose
// restoration cannot be guaranteed locally is simply left resident, and a
// re-materialization that cannot complete errors rather than serving a short list.

package segmentdist

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// residencyArm is the non-generic residency view of one format's pool, following
// the precedent this package already documents twice — coverageArm
// (manager_reconcile_arms.go) and completenessArm (manager_completeness.go) both
// exist because distManager is generic over [Q, S] and the two live instantiations
// carry DIFFERENT type arguments, so Go cannot hold both in one slice. An interface
// whose method set mentions neither Q nor S can.
//
// It is a THIRD seam rather than a widening of either sibling: both of those files
// document their method sets as the union of what THEIR consumers read, and the
// budget pass is a different consumer with different needs. armFormat is the one
// method reused verbatim (manager_reconcile_arms.go); the other four are this
// ticket's.
type residencyArm interface {
	armFormat() string
	// residentBytes is the modeled Go heap — the quantity COMPARED against
	// the budget.
	residentBytes() int64
	// mappedBytes is the encoded blob total — REPORTED beside it, never
	// compared. Keeping both on the interface is what makes the distinction
	// visible at every call site instead of implied.
	mappedBytes() int64
	lastSearchTouch() int64
	isEvicted() bool
	evictResident() (int64, bool)
}

// Compile-time satisfaction assertions for BOTH live instantiations, so a future
// signature drift on any of the methods is a build failure here rather than a
// silently dropped arm at the budget pass.
var (
	_ residencyArm = (*distManager[[]byte, struct{}])(nil)
	_ residencyArm = (*distManager[bm25.Query, *bm25.CorpusStats])(nil)
)

// noteSearchTouch stamps this pool as touched by a CONSUMER SEARCH right now. It
// is the WRITE half of the hot/cold stamp; lastSearchTouch is the read half.
//
// Only the search path calls it. The background arms (reconcile, coverage probe,
// rebuild) walk the whole working set on a timer, so a touch stamped from one of
// them would keep every pool permanently hot and defeat eviction entirely.
func (m *distManager[Q, S]) noteSearchTouch() {
	m.lastSearchNanos.Store(time.Now().UnixNano())
}

// lastSearchTouch reads the last consumer-search touch stamp in Unix nanoseconds,
// or 0 for a pool no search has ever touched — which makes a background-loaded
// pool the coldest candidate, exactly as intended.
//
// It is a METHOD rather than a bare field read because the residencyArm seam
// cannot reach a field on a generic struct; armFormat (manager_reconcile_arms.go)
// exists for the same reason.
func (m *distManager[Q, S]) lastSearchTouch() int64 { return m.lastSearchNanos.Load() }

// isEvicted reports whether this pool's segments have been unloaded and not yet
// re-materialized.
func (m *distManager[Q, S]) isEvicted() bool { return m.evicted.Load() }

// residentBytes reports the MODELED Go heap this pool's engine currently holds
// — the budget's accounting unit. The model, its three terms and the single
// place its per-member constant lives are documented on
// searchengine.SegmentedIndex.ResidentHeapBytes; this method adds nothing to it
// and deliberately does not re-derive it.
//
// It is a model, not a measurement, and a caller must not read it as an exact
// byte count. It does NOT include a segment's on-disk size — that is mappedBytes,
// which is metered and reported separately and never compared against the budget.
func (m *distManager[Q, S]) residentBytes() int64 {
	return m.engine.ResidentHeapBytes()
}

// mappedBytes sums the on-disk size of every segment currently imported into
// this pool. Reported for operators, never compared against the residency
// budget: for a mapped segment those bytes are page cache rather than Go heap.
func (m *distManager[Q, S]) mappedBytes() int64 {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	var total int64
	for _, seg := range m.resident {
		total += int64(seg.mappedBytes)
	}
	return total
}

// markMaterialized is THE SINGLE OWNER OF THE evicted LATCH'S CLEAR: it records
// that this pool is fully resident again by clearing `evicted` and dropping the
// strict-reload id set. Nothing else in the package writes evicted false.
//
// Every path that makes a pool fully resident calls it: the strict
// re-materialization and every ordinary success return of load(), and
// forceCompleteLiveSet (prune_cache.go), which re-imports WITHOUT going through
// load() and would otherwise leave a fully-resident pool latched evicted forever.
//
// LOCKING — it deliberately does NOT take residencyMu, and that is a correctness
// requirement rather than an omission. It is called from inside load(), and a
// consumer holds residencyMu.RLock across its whole load-and-search span; taking
// Lock here would self-deadlock on Go's non-reentrant RWMutex. It needs no
// residencyMu of its own either: the caller's RLock already excludes eviction, and
// the only atomicity this write requires is over the (evicted, evictedIDs) PAIR,
// which resMu supplies. Holding resMu across both writes is what lets
// reclaimMerged test the latch and rewrite the set as one indivisible step.
func (m *distManager[Q, S]) markMaterialized() {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	m.evicted.Store(false)
	m.evictedIDs = nil
}

// loadIfResident is the BACKGROUND-arm entry point: it loads the pool as usual,
// EXCEPT that an evicted pool is declined (skipped=true) rather than
// re-materialized. It never clears the evicted latch.
//
// This is ticket constraint 2's prohibition in code — the reconcile, coverage-probe
// and rebuild arms run against the whole working set hourly, so an arm that loaded
// an evicted pool would undo every eviction at full decode (and, on a cold L2, full
// network) cost within one tick. A skipped arm reports that it did not measure; it
// does not report a healthy measurement it never took.
func (m *distManager[Q, S]) loadIfResident(ctx context.Context) (skipped bool, err error) {
	if m.evicted.Load() {
		return true, nil
	}
	return false, m.load(ctx)
}

// evictResident drops this pool's entire resident set out of the engine to reclaim
// memory, returning the blob bytes freed and whether it acted at all.
//
// It holds residencyMu.Lock for its WHOLE body, which is what makes it exclusive
// against the consumer load-and-search span (see the residencyMu field doc): a
// consumer's RLock spans load() and engine.Search as one unit, so eviction can
// never land between them.
//
// THE RE-MATERIALIZABILITY GATE (step 2 below) is a correctness gate, not caution.
// A pool is evicted only when EVERY resident id is present in L2, because the
// re-materialization is L2-strict: an id missing from L2 at reload time needs the
// server, and a server that cannot supply it turns the reload into an error. A pool
// whose restoration cannot be guaranteed locally must therefore not be evicted at
// all. Declining is not a fallback — it repairs nothing and hides nothing; it
// simply does not act, and says so at Warn.
//
// CRASH WINDOW: nothing durable is destroyed. The .seg files stay on disk untouched
// — that is exactly what makes the reload free of network — and resident,
// evictedIDs, l2Loaded and evicted are all per-process state a restart re-derives
// from L2 as it does today. Steps 3-6 are one atomic unit under residencyMu.Lock,
// so there is no arrangement in which the Unload happens and the latch resets do
// not.
func (m *distManager[Q, S]) evictResident() (freed int64, ok bool) {
	m.residencyMu.Lock()
	defer m.residencyMu.Unlock()

	// 1. Snapshot the resident ids, and the engine heap this pool holds RIGHT
	// NOW. The heap figure MUST be taken before step 3's Unload: afterwards the
	// entries are gone from the engine's set and ResidentHeapBytes would report
	// what remains, not what this eviction released — which is zero, and would
	// silently report every eviction as freeing nothing. Both reads happen under
	// the residencyMu this method already holds, so no concurrent import can
	// land between the snapshot and the unload.
	freed = m.engine.ResidentHeapBytes()
	m.resMu.Lock()
	ids := make([]searchengine.SegmentID, 0, len(m.resident))
	for id := range m.resident {
		ids = append(ids, id)
	}
	m.resMu.Unlock()
	if len(ids) == 0 {
		return 0, false
	}

	// 2. THE RE-MATERIALIZABILITY GATE. sizeOf reads the L2 index only — no disk
	// read, and recency-neutral, so asking how big the pool is never reorders the
	// LRU that decides what L2 drops next.
	missing := 0
	for _, id := range ids {
		if _, present := m.cache.sizeOf(id); !present {
			missing++
		}
	}
	if missing > 0 {
		slog.Warn("segmentdist: declining to evict a pool that cannot be re-materialized from L2",
			"target", m.target.GetName(), "format", m.format,
			"resident_ids", len(ids), "missing_from_l2", missing)
		return 0, false
	}

	// 3. Drop the segments from the searchable set (one CAS swap per the engine's
	// publish discipline — the old set and its entries stay reachable to any reader
	// already walking them, so a search racing this swap returns the full old set).
	m.engine.Unload(ids)

	// 4. Record the EXACT unloaded set for the strict reload, and empty the resident
	// tracking. 5. Clear the L2-first once-guard, or load() short-circuits forever
	// over an empty engine. 6. Latch evicted.
	m.resMu.Lock()
	// RE-FILTER AGAINST THE MAP AS IT IS NOW. ids was snapshotted under this lock
	// and the lock was RELEASED for the gate and the engine unload above, so a
	// quarantine landing in that window has already run forgetQuarantined and
	// removed its id from m.resident — but not from this stale slice. Assigning
	// the slice verbatim puts a withdrawn id back into the strict-reload replay
	// set, and the reload then hard-fails on bytes nobody intends to supply,
	// re-opening exactly the leg forgetQuarantined closes.
	//
	// It is the same "withdrawn is not missing" rule as forgetQuarantined, applied
	// to the one path that can reintroduce an id after it was dropped.
	kept := make([]searchengine.SegmentID, 0, len(ids))
	for _, id := range ids {
		if _, still := m.resident[id]; still {
			kept = append(kept, id)
		}
	}
	m.evictedIDs = kept
	m.resident = make(map[searchengine.SegmentID]residentSeg)
	m.l2Loaded.Store(false)
	m.evicted.Store(true)
	m.resMu.Unlock()

	slog.Info("segmentdist: evicted a cold pool's resident segments",
		"target", m.target.GetName(), "format", m.format,
		"segments", len(ids), "freed_bytes", freed)
	return freed, true
}

// residencyCandidate pairs one pool with the graph it belongs to, so the budget
// pass can skip by graph (the exclude set, the write backlog) while evicting by
// pool — a graph has one pool per format and they are evicted independently.
type residencyCandidate struct {
	key graphKey
	arm residencyArm
	// heapBytes is this arm's modeled heap, computed ONCE before the eviction
	// sort and read by both the running total and the comparator.
	//
	// IT IS NOT AN OPTIMISATION ALONE. residentBytes walks every resident
	// member, and the comparator's TIE-BREAK branch is the COMMON case — a pool
	// no consumer search has touched reads lastSearchTouch()==0, so untouched
	// pools all tie — which made the expensive branch run O(V log V) times.
	// Memoizing also removes a correctness hazard: sort.SliceStable requires a
	// consistent ordering, and calling residentBytes() inside the comparator
	// read a LIVE MUTABLE quantity that a concurrent import could change
	// mid-sort, making the comparator non-transitive. This field is the
	// snapshot the sort actually requires.
	heapBytes int64
}

// EnforceResidencyBudget runs the budget pass with no exclusions. It is for callers
// that hold NO pool read locks and are serving NO pool — the background reconcile
// sweep. A caller that has just served a pool must use enforceResidencyBudget with
// that pool's graphKey in the exclude set instead.
//
// THE SWEEP CALLS IT ONCE PER PASS, NOT ONCE PER GRAPH, and that placement is the
// mechanism rather than a preference: the pass already walks every constructed pool
// and sorts them, so calling it inside the per-graph loop would re-walk and re-sort
// the whole set on every iteration to answer a question that does not change within
// one sweep.
//
// NO THRASH CYCLE, and the property belongs to the WHOLE ARRANGEMENT rather than to
// this call site. A sweep that evicted a pool and then reloaded it on the next tick
// would be a treadmill; it cannot happen because every background arm DECLINES an
// evicted pool (loadIfResident at the four probe/reconcile regions) and every
// ArmObservation consumer branches on Evicted rather than reading its zeros as a
// measurement.
// So a pool a background arm made resident is loaded AT MOST ONCE per process and
// then stays evicted until a CONSUMER touch — a search, a by-id vector read, a write
// — re-materializes it, which is the cost-bearer this ticket intends. Weaken either
// of those two mechanisms and this doc's claim stops being true.
func (m *Manager) EnforceResidencyBudget() { m.enforceResidencyBudget(nil) }

// enforceResidencyBudget evicts the COLDEST pools until the total resident blob
// bytes across every constructed pool is back inside residencyBudgetBytes.
//
// It hangs off existing paths (a completed search, the existing reconcile sweep)
// rather than a timer of its own, and it returns after one comparison when the
// total is already under budget — which is the overwhelmingly common case.
//
// ORDERING: coldest first, by last CONSUMER-SEARCH touch ascending. A pool no
// search has ever touched reads 0 and is therefore the coldest, which is correct
// rather than accidental: such a pool was made resident by a background arm, and
// ticket constraint 2 says a background arm must not count as a touch. Ties break
// on LARGER resident bytes first, so one pass sheds the most bytes it can.
//
// THREE SKIPS, and the first is a deadlock requirement rather than a policy
// preference:
//   - exclude — evictResident takes residencyMu.Lock, and Go's sync.RWMutex is not
//     reentrant, so a caller still holding an RLock on a pool it then evicts
//     self-deadlocks. Every trigger site therefore (a) calls this only AFTER
//     releasing its read locks and (b) names the graphKeys it just served here.
//     Both belts are kept: (a) alone is correct but invisible to a future caller,
//     (b) alone is checkable in a test.
//   - a pool already evicted — it has nothing left to free.
//   - a graph with a non-empty write backlog — its pools are about to be sealed and
//     written to L2 FROM THEIR RESIDENT SET, and evicting underneath that would make
//     the emptied resident set the whole of what gets written.
//
// It reads the two engine maps DIRECTLY rather than through
// managerFor/bm25ManagerFor, and it does NOT wait on the construction gate.
// Those accessors lazily CONSTRUCT an engine, and building engines for graphs this
// client does not maintain is the opposite of what a memory-reclaim pass is for; a
// construction still in flight has nothing resident to evict, and waiting for it
// would put the pass behind another graph's corpus copy. m.mu is released before
// anything is evicted.
func (m *Manager) enforceResidencyBudget(exclude []graphKey) {
	if m.residencyBudgetBytes <= 0 {
		return // eviction disabled: today's unbounded-residency behaviour, byte for byte
	}

	m.mu.Lock()
	candidates := make([]residencyCandidate, 0, len(m.managers)+len(m.bm25Managers))
	for k, gate := range m.managers {
		candidates = append(candidates, residencyCandidate{key: k, arm: gate.dm})
	}
	for k, gate := range m.bm25Managers {
		candidates = append(candidates, residencyCandidate{key: k, arm: gate.dm})
	}
	backlogged := make(map[graphKey]bool, len(m.dirty))
	for k, st := range m.dirty {
		if len(st.hnsw.pending) > 0 || len(st.hnsw.tails) > 0 ||
			len(st.bm25.pending) > 0 || len(st.bm25.tails) > 0 {
			backlogged[k] = true
		}
	}
	m.mu.Unlock()

	// Compute each arm's heap ONCE, here, and read it everywhere below. The
	// running total, the comparator and the reported figures all consume this
	// same snapshot, so the pass cannot disagree with itself mid-sort.
	var resident, mapped int64
	for i := range candidates {
		if candidates[i].arm.isEvicted() {
			continue
		}
		candidates[i].heapBytes = candidates[i].arm.residentBytes()
		resident += candidates[i].heapBytes
		mapped += candidates[i].arm.mappedBytes()
	}
	if resident <= m.residencyBudgetBytes {
		return
	}

	skip := make(map[graphKey]bool, len(exclude))
	for _, k := range exclude {
		skip[k] = true
	}
	victims := make([]residencyCandidate, 0, len(candidates))
	for _, c := range candidates {
		if skip[c.key] || backlogged[c.key] || c.arm.isEvicted() {
			continue
		}
		victims = append(victims, c)
	}
	sort.SliceStable(victims, func(i, j int) bool {
		ti, tj := victims[i].arm.lastSearchTouch(), victims[j].arm.lastSearchTouch()
		if ti != tj {
			return ti < tj
		}
		// Reads the value memoized before the sort rather than recomputing a
		// pool's heap here — see residencyCandidate.heapBytes for why that is
		// a correctness requirement and not merely cheaper.
		return victims[i].heapBytes > victims[j].heapBytes
	})

	before, pools := resident, 0
	for _, c := range victims {
		if resident <= m.residencyBudgetBytes {
			break // back under budget — stop, do not keep shedding
		}
		freed, ok := c.arm.evictResident()
		if !ok {
			continue // declined (see evictResident) — it has already said why
		}
		resident -= freed
		pools++
		slog.Info("segmentdist: evicted a cold pool for the residency budget",
			"graph", c.key.graphName, "graph_type", c.key.graphType, "format", c.arm.armFormat(),
			"freed_bytes", freed, "resident_bytes_after", resident)
	}
	// mapped_bytes is REPORTED, never compared: the comparison above uses the
	// heap number only. It is here so an operator can see how much page cache
	// the pools hold beside the heap the budget actually bounds.
	slog.Info("segmentdist: residency budget pass complete",
		"budget_bytes", m.residencyBudgetBytes, "resident_bytes_before", before,
		"resident_bytes_after", resident, "mapped_bytes", mapped,
		"pools_evicted", pools,
		"under_budget", resident <= m.residencyBudgetBytes)
}

// PoolEvicted reports whether EITHER format's pool for this graph is currently
// evicted. It is the ONE external residency predicate — the reporting band and the
// background deciders both consume it and neither derives its own.
//
// A graph with no constructed engine returns false: it has never been resident, so
// it has never been evicted. It reads the maps
// directly, constructing nothing and waiting on no gate.
func (m *Manager) PoolEvicted(gt kgtypes.GraphType, name string) bool {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	hnswGate, hasHNSW := m.managers[k]
	bm25Gate, hasBM25 := m.bm25Managers[k]
	m.mu.Unlock()

	return (hasHNSW && hnswGate.dm.isEvicted()) || (hasBM25 && bm25Gate.dm.isEvicted())
}

// forgetQuarantined drops a withdrawn segment from this pool's OWN residency
// bookkeeping — the resident map and, if it is listed there, the exact set the
// strict reload replays.
//
// WHY THE ENGINE-SIDE WITHDRAWAL IS NOT ENOUGH, traced rather than assumed. The
// engine's WithdrawSegment removes the id from the PUBLISHED SET; the eviction
// candidate walk above reads m.resident, which is this manager's own map and is
// untouched by that. So a quarantined id stayed resident here, and the
// re-materializability gate then asked the L2 cache for it — where quarantine
// had already dropped the index entry — counted it missing, and DECLINED THE
// EVICTION. Not once: forever, because nothing else was ever going to put those
// bytes back. The pool is pinned in memory by one bad segment.
//
// AND THE STRICT RELOAD IS THE SAME FACT ONE STEP LATER. evictedIDs is replayed
// with tolerateMisses=false, whose contract is that a miss is unrecoverable and
// must error — correct in general, and wrong for an id deliberately withdrawn,
// which is absent BY DECISION rather than by loss. Leaving it in the set makes
// every reload attempt hard-fail and the pool unsearchable until a restart.
//
// WITHDRAWN IS NOT MISSING, and that distinction is the whole fix: a withdrawn
// id leaves both sets, so the gates no longer look for something nobody intends
// to supply.
func (m *distManager[Q, S]) forgetQuarantined(id searchengine.SegmentID) {
	if m == nil {
		return
	}
	m.resMu.Lock()
	defer m.resMu.Unlock()
	delete(m.resident, id)
	if len(m.evictedIDs) == 0 {
		return
	}
	kept := make([]searchengine.SegmentID, 0, len(m.evictedIDs))
	for _, evicted := range m.evictedIDs {
		if evicted != id {
			kept = append(kept, evicted)
		}
	}
	m.evictedIDs = kept
}
