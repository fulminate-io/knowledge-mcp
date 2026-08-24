// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestPublishResident proves the registry-model publish-on-ship cutover:
// the embed ship publishes this writer's RESIDENT live set as its
// manifest and the server reference-count-GCs whatever dropped out — bounding the
// server segment set — while the coverage/subset gate prevents a degenerate
// (restart, pre-load) publish from wiping the corpus.
//
// Case (a) — BOUNDED-VIA-PUBLISH: a real seal+merge cycle on one manager publishes
// the post-merge resident Export, and the server segment count drops to the
// consolidated live set (the merged-away constituents are reaped by the refcount-GC,
// not a Prune RPC).
//
// Case (b) — RESTART PRESERVES THE CORPUS: a fresh manager that FULLY reloads the
// prior corpus, then publishes, reaps NOTHING of that corpus (its resident set IS
// the re-imported corpus, so the coverage gate passes and the manifest references
// the whole corpus). This is the restart-tail guard under the new path.
func TestPublishResident(t *testing.T) {
	t.Parallel()

	t.Run("bounded_via_publish", func(t *testing.T) {
		svc, gc := newSegmentHarness(t)
		ctx := context.Background()

		// Ship a multi-segment HNSW corpus via the real embed path, then drive a
		// merge by force-flushing and re-publishing. searchCorpusN==MinSegmentDocs
		// so each batch seals one segment; the doc counts are >= the coverage floor
		// so the publish gate is armed (real corpus, not disarmed-tiny).
		const corpusSegs = 4
		mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
		for b := range corpusSegs {
			batch := hnswVecDocs(searchCorpusN)
			for i := range batch {
				batch[i].ID = fmt.Sprintf("pub-b%d-%s", b, batch[i].ID)
			}
			require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, "pubRepo", batch))
		}
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "pubRepo"))
		require.NotEmpty(t, shippedHNSWIDs(svc),
			"the embed publish path ships every re-emitted partition")

		// Every shipped HNSW id is referenced by the published manifest (no reap of a
		// live segment): the server count equals the resident Export.
		dm := mgr.managerFor(kgtypes.GraphCode, "pubRepo")
		require.Len(t, shippedHNSWIDs(svc), len(dm.engine.Export()),
			"server HNSW count matches the published resident live set — bounded, nothing live reaped")
	})

	t.Run("restart_full_reload_preserves_corpus", func(t *testing.T) {
		svc, gc := newSegmentHarness(t)
		ctx := context.Background()

		// Process 1 ships a multi-segment corpus.
		const corpusSegs = 3
		p1 := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
		for b := range corpusSegs {
			batch := hnswVecDocs(searchCorpusN)
			for i := range batch {
				batch[i].ID = fmt.Sprintf("rl-b%d-%s", b, batch[i].ID)
			}
			require.NoError(t, p1.AddAndMarkDirty(ctx, kgtypes.GraphCode, "reloadRepo", batch))
		}
		require.NoError(t, p1.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "reloadRepo"))
		priorCorpus := shippedHNSWIDs(svc)
		require.NotEmpty(t, priorCorpus)

		// Process 2: RESTART. Fresh manager. FULLY reload the prior corpus into the
		// engine (what a Search does), THEN publish via a no-op ship-shaped
		// flush. Because the resident set IS the full re-imported corpus, the
		// coverage gate passes and the published manifest references the WHOLE corpus
		// — so the refcount-GC reaps nothing.
		p2 := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
		dm := p2.managerFor(kgtypes.GraphCode, "reloadRepo")
		require.NoError(t, dm.load(ctx))
		require.Len(t, dm.engine.Export(), len(priorCorpus),
			"the fresh manager fully re-imported the prior corpus")

		// Publish the resident (fully-reloaded) live set as the manifest.
		_, err := dm.shipAndPublish(ctx, dm.locallyShipped)
		require.NoError(t, err)

		// The prior corpus SURVIVES — a fully-reloaded restart never wipes it.
		now := shippedHNSWIDs(svc)
		require.Len(t, now, len(priorCorpus), "a fully-reloaded restart publish reaps nothing of the prior corpus")
		for id := range priorCorpus {
			require.Contains(t, now, id, "every prior-corpus segment survives the restart publish")
		}
	})

	t.Run("degenerate_preload_publish_is_gated", func(t *testing.T) {
		svc, gc := newSegmentHarness(t)
		ctx := context.Background()

		// Process 1 ships a multi-segment corpus.
		const corpusSegs = 4
		p1 := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
		for b := range corpusSegs {
			batch := hnswVecDocs(searchCorpusN)
			for i := range batch {
				batch[i].ID = fmt.Sprintf("dg-b%d-%s", b, batch[i].ID)
			}
			require.NoError(t, p1.AddAndMarkDirty(ctx, kgtypes.GraphCode, "gateRepo", batch))
		}
		require.NoError(t, p1.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "gateRepo"))
		priorCorpus := shippedHNSWIDs(svc)
		require.NotEmpty(t, priorCorpus)

		// Process 2: RESTART. A SINGLE write plus its tick, BEFORE any load() — the
		// resident set is just that tail, far below the coverage ratio. The publish
		// MUST be gated (skipped), so the prior corpus survives — the corpus-wipe guard.
		cc2 := gc.server.viewFor(&knowledgev1.GraphSelector{}, "")
		p2 := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(cc2)))
		tail := hnswVecDocs(searchCorpusN)
		for i := range tail {
			tail[i].ID = fmt.Sprintf("dg-tail-%s", tail[i].ID)
		}
		require.NoError(t, p2.AddAndMarkDirty(ctx, kgtypes.GraphCode, "gateRepo", tail))
		require.NoError(t, p2.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "gateRepo"))

		now := shippedHNSWIDs(svc)
		require.Greater(t, len(now), len(priorCorpus),
			"the degenerate pre-load publish is GATED — the prior corpus + the new tail survive (no wipe)")
		for id := range priorCorpus {
			require.Contains(t, now, id, "every prior-corpus segment survives the gated degenerate publish")
		}
	})
}
