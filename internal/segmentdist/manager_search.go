// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
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
// Both engines are loaded cache-first (dm.load / bm.load — idempotent, one
// batched Fetch for the delta, parallel Import) before the search. There is NO
// per-search recoverIfDegenerate backstop: the L2-first load() imports the full L2
// corpus on the primary path, so resident is not left poisoned-degenerate after a
// load, and a genuinely cold/partial L2 is healed OFF the hot path by the
// boot-delay one-shot + the periodic reconcile (which drive recoverIfDegenerate's
// cheap server re-import), not by a List(0) on every search (see the inline note
// below). The two engine.Search calls run CONCURRENTLY over a bounded WaitGroup
// (the engines are independent and each is internally per-segment parallel), not
// serially.
//
// Failure-mode note: searchengine.SegmentedIndex.Search returns nil for an empty
// segment set, so a graph whose segments are not yet built/shipped yields an
// empty fused list (not an error) and the hydrator renders zero rows. No
// redundant empty-set guard is needed.
//
// The HNSW arm is skipped when queryVec is empty (a text-only query), in which
// case the fused result is just the BM25 ranking unchanged (RRF over a single
// list is the identity ranking).
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

	// Load both engines' L2-resident set L2-first (server-independent on a populated
	// cache; cold L2 falls through to the server). Load is idempotent — the l2Loaded
	// once-guard short-circuits a repeated load() — so repeated searches do not
	// re-pull. The per-search recoverIfDegenerate backstop was removed: with the
	// L2-first load() the primary path imports the full L2 corpus, so resident is no
	// longer left poisoned-degenerate after a load, and a genuinely cold/partial L2 is
	// healed off the hot path by the boot-delay one-shot + the periodic reconcile
	// (bootstrap), not by a List(0) on every search. recoverIfDegenerate remains for
	// the reconcile's direct use + its dedicated tests.
	if err := dm.load(ctx); err != nil {
		return nil, err
	}
	if err := bm.load(ctx); err != nil {
		return nil, err
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

	return reciprocalRankFusion([][]searchengine.Hit{hnswHits, bm25Hits}, k), nil
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
func (m *Manager) VectorByID(
	ctx context.Context,
	gt kgtypes.GraphType,
	name, externalID string,
) ([]byte, bool, error) {
	dm := m.managerFor(gt, name)
	if err := dm.load(ctx); err != nil {
		return nil, false, err
	}
	vec, ok := dm.engine.VectorByID(externalID)
	return vec, ok, nil
}
