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
// batched Fetch for the delta, parallel Import) before the search. After the HNSW
// load, a read-side coverage backstop (dm.recoverIfDegenerate) forces a recovery
// load() if the in-memory engine is degenerate relative to the server's shipped
// corpus — the durability net for a poisoned load floor. The two engine.Search
// calls run CONCURRENTLY over a bounded WaitGroup (the engines are independent and
// each is internally per-segment parallel), not serially.
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

	dm := m.managerFor(gt, name)
	bm := m.bm25ManagerFor(gt, name)

	// Load both engines' server delta cache-first. Load is idempotent (empty
	// delta = zero Fetch) so repeated searches do not re-pull.
	if err := dm.load(ctx); err != nil {
		return nil, err
	}
	// Read-side coverage backstop (HNSW arm only): after the load, detect a
	// degenerate in-memory engine (resident doc count << the server's shipped
	// corpus) and force a single-flighted recovery load(). A healthy engine pays
	// one in-memory count and zero extra RPC. Best-effort — a probe error logs and
	// leaves the search to run on the current (possibly degenerate) set.
	if err := dm.recoverIfDegenerate(ctx); err != nil {
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
// The caller turns ok=false into a LOUD error with rebuild guidance — never a
// silent empty success. Only the HNSW engine is consulted (BM25 has no vectors).
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
