// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestSegmentDistributionE2E wires the whole distribution flow on the MOCK
// SegmentFormat (no real HNSW/BM25): a real httptest SegmentService server (the
// generated handler over an in-memory blob store that mirrors the server
// contract — monotonic generation, idempotent-by-content-hash Put, list-delta,
// fetch; the real server handler lives in the SEPARATE knowledge-server module
// and is unit-tested there), a client GraphClient pointed at it, a mock-format
// SegmentedIndex engine, the diskSegmentCache, and the distManager.
//
// Sequence (the ticket's mock-format scenario): Add → ship → fresh load
// (delta-pull, cold Fetch, Import) → search hits → unload → search miss →
// reload-from-L2 (zero network) → search hits → second load (empty delta, zero
// Fetch) → concurrent load+search under -race.
func TestSegmentDistributionE2E(t *testing.T) {
	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "e2e"}
	ctx := context.Background()

	// Producer: Add docs to the engine and ship.
	prodEng := newMockEngine()
	require.NoError(t, prodEng.Add([]searchengine.Document{doc("d1", "alpha alpha")}))
	require.NoError(t, prodEng.Add([]searchengine.Document{doc("d2", "alpha beta")}))
	require.NoError(t, prodEng.Add([]searchengine.Document{doc("d3", "gamma")}))
	prodMgr, _ := buildManager(prodEng, gc, target, t.TempDir())
	_, err := prodMgr.ship(ctx, prodMgr.locallyShipped)
	require.NoError(t, err)

	svc.mu.Lock()
	require.Equal(t, uint64(3), svc.gen, "server stamped 3 monotonic generations")
	svc.mu.Unlock()

	// Belt-and-suspenders idempotency: a second ship of the full corpus writes
	// ZERO new generations because every content-hash id already exists.
	_, err = prodMgr.ship(ctx, prodMgr.locallyShipped)
	require.NoError(t, err)
	svc.mu.Lock()
	require.Equal(t, uint64(3), svc.gen, "idempotent: second ship stamps zero new generations")
	svc.mu.Unlock()

	// Consumer: a fresh engine + cache loads the delta cold.
	consEng := newMockEngine()
	consMgr, consCC := buildManager(consEng, gc, target, t.TempDir())
	require.NoError(t, consMgr.load(ctx))
	require.Equal(t, int64(1), consCC.fetchCalls.Load(), "cold load: one batched Fetch")
	require.Len(t, consEng.Search(mockQuery{term: "alpha"}, 10), 2, "search hits d1, d2")

	// Unload everything; search drops the unloaded hits.
	unloaded := consMgr.unloadUnderPressure(0)
	require.NotEmpty(t, unloaded)
	require.Empty(t, consEng.Search(mockQuery{term: "alpha"}, 10), "unloaded → search miss")

	// Reload from L2 — zero network Fetch.
	fetchBeforeReload := consCC.fetchCalls.Load()
	require.NoError(t, consMgr.reload(unloaded))
	require.Equal(t, fetchBeforeReload, consCC.fetchCalls.Load(), "reload-from-L2: zero Fetch")
	require.Len(t, consEng.Search(mockQuery{term: "alpha"}, 10), 2, "search hits return after reload")

	// Second load at advanced gen → empty delta → zero Fetch.
	fetchBeforeSecondLoad := consCC.fetchCalls.Load()
	require.NoError(t, consMgr.load(ctx))
	require.Equal(t, fetchBeforeSecondLoad, consCC.fetchCalls.Load(), "second load: empty delta, zero Fetch")

	// Concurrent load + search — the engine read path is lock-free (contract);
	// assert no data race (run under -race) and no panic.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_ = consMgr.load(ctx)
		})
		wg.Go(func() {
			_ = consEng.Search(mockQuery{term: "alpha"}, 10)
		})
	}
	wg.Wait()
	require.Len(t, consEng.Search(mockQuery{term: "alpha"}, 10), 2, "final search consistent after concurrent load+search")
}
