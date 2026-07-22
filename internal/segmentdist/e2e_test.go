// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestSegmentDistributionE2E wires the whole distribution flow on the MOCK
// SegmentFormat (no real HNSW/BM25): an in-memory sharedServerFake modeling the
// segment registry (monotonic generation, idempotent-by-content-hash ship,
// list-delta, fetch, manifest publish + refcount-GC), a fakeSegmentSource view over
// it injected into the Manager, a mock-format SegmentedIndex engine, the
// diskSegmentCache, and the distManager.
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
	require.NoError(t, consMgr.reload(ctx, unloaded, false))
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

// buildManagerWithWriter is buildManager with an explicit writer_id on the source,
// so the e2e test can assert every outbound leg carries it (the view stamps its
// bound writer_id into the shared server's seenWriterIDs on Ship/PublishManifest).
func buildManagerWithWriter(
	engine *searchengine.SegmentedIndex[mockQuery, mockStats],
	svc *sharedServerFake,
	target *knowledgev1.GraphSelector,
	cacheDir, writerID string,
) *distManager[mockQuery, mockStats] {
	src := svc.viewFor(target, writerID)
	cache := newDiskSegmentCache(cacheDir, 0)
	return newDistManager(engine, src, cache, target, "")
}

// TestRegistryReclaimE2E is the end-to-end proof of the registry-model
// reclaim path against the in-process fake server: repeated ship + merge + PUBLISH
// cycles keep the server segment set BOUNDED (the merged-away constituents are
// refcount-GC'd by the publish, not accumulated forever — the original
// 438-gens/1.9GB symptom), a mid-stream restart preserves the corpus (a
// fully-reloaded publish reaps nothing), and EVERY outbound RPC carries the
// writer_id the server's last-connection liveness depends on.
func TestRegistryReclaimE2E(t *testing.T) {
	svc, _ := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "registryE2E"}
	ctx := context.Background()
	const writer = "abc123def4567890" // 16-hex machine-id shape

	// A producer with a merging engine: MinSegmentDocs=1 seals one segment per Add;
	// SegmentCountTarget=4 consolidates once >4 accumulate.
	prodEng := searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: 4,
	})
	defer prodEng.Close()
	prodMgr := buildManagerWithWriter(prodEng, svc, target, t.TempDir(), writer)

	// Repeated ship+publish cycles: seal+ship 8 small segments, publishing the
	// resident live set each pass. The background merger consolidates them; the
	// publish refcount-GCs the merged-away constituents.
	const n = 8
	for i := range n {
		require.NoError(t, prodEng.Add([]searchengine.Document{doc(fmt.Sprintf("d%d", i), fmt.Sprintf("body %d", i))}))
		_, err := prodMgr.shipAndPublish(ctx, nil, prodMgr.locallyShipped)
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool { return prodEng.MergeCount() > 0 },
		2*time.Second, 2*time.Millisecond, "background merge fires once >SegmentCountTarget segments accumulate")
	// One more cycle after the merge so the consolidated set is published + reconciled.
	_, err := prodMgr.shipAndPublish(ctx, nil, prodMgr.locallyShipped)
	require.NoError(t, err)

	// BOUNDED: the server segment count matches the engine's post-merge resident
	// live set — NOT the 8 pre-merge accumulation.
	require.Equal(t, len(prodEng.Export()), serverSegCount(t, svc, target),
		"server count is BOUNDED — it matches the post-merge resident live set, not the pre-merge accumulation")
	require.Less(t, serverSegCount(t, svc, target), n,
		"server holds far fewer than the 8 pre-merge segments — merged-away constituents reclaimed by the publish refcount-GC")

	// Every outbound RPC carried the writer_id (the last-connection liveness wiring).
	svc.mu.Lock()
	require.True(t, svc.seenWriterIDs[writer],
		"every outbound segment RPC carries writer_id so the server's __segment_writers last-seen stays fresh")
	require.Len(t, svc.seenWriterIDs, 1, "only this writer's id was seen")
	svc.mu.Unlock()

	priorCorpus := map[string]struct{}{}
	for _, b := range prodEng.Export() {
		priorCorpus[b.ID] = struct{}{}
	}

	// MID-STREAM RESTART: a fresh manager fully reloads the corpus, then publishes —
	// the fully-reloaded resident set passes the coverage gate, so the publish
	// references the WHOLE corpus and reaps nothing (restart-tail guard).
	restartEng := searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{MinSegmentDocs: 1})
	defer restartEng.Close()
	restartMgr := buildManagerWithWriter(restartEng, svc, target, t.TempDir(), writer)
	require.NoError(t, restartMgr.load(ctx))
	_, err = restartMgr.shipAndPublish(ctx, nil, restartMgr.locallyShipped)
	require.NoError(t, err)

	after := map[string]bool{}
	for _, m := range svc.listMetas(target, 0) {
		after[m.GetId()] = true
	}
	for id := range priorCorpus {
		require.True(t, after[id], "every prior-corpus segment survives the mid-stream restart publish")
	}
}
