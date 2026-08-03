// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestReclaimRoleRegressionGuards asserts the merge-reclaim hook does not perturb the
// two pre-existing prune roles, and that the reclamation sources — ROLE-A (the reset
// rebuild's server-side prune, whose local eviction the swap's reclaim hook performs and
// InvalidateLocal backstops), ROLE-B (embed tick reconcile-prune against
// locallyShipped), and the live-cache merge-reclaim hook — operate on DISJOINT id sets.
func TestReclaimRoleRegressionGuards(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt := kgtypes.GraphCode

	t.Run("roleA_det_rebuild_still_prunes_and_no_live_hook", func(t *testing.T) {
		svc, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))

		// Ship an old degenerate corpus via the embed path, then deterministically
		// rebuild it with a different corpus → ROLE-A prunes the old ids.
		oldDocs := hnswVecDocs(searchCorpusN)
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, "roleA", oldDocs))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, "roleA"))
		oldIDs := shippedHNSWIDs(svc)
		require.NotEmpty(t, oldIDs)

		// The old layer's blobs are on local disk BEFORE the rebuild. This is the
		// known-positive that keeps the eviction assertions below non-vacuous — an
		// absence asserted over a directory that never held the file proves nothing.
		detDir := graphCacheDirFor(mgr.cacheDir, gt, "roleA", hnsw.New().Name())
		before := segFilesAtDir(t, detDir)
		for id := range oldIDs {
			require.Contains(t, before, id, "the old layer's blob is on local disk before the rebuild")
		}

		rebuildDocs := hnswVecDocs(searchCorpusN + 32)
		require.NoError(t, mgr.StageRebuildPartition(ctx, gt, "roleA", rebuildDocs, nil))
		res, err := mgr.FinalizeRebuild(ctx, gt, "roleA")
		require.NoError(t, err)
		pruned := res.HNSWSuperseded
		require.NotEmpty(t, pruned, "ROLE-A deterministic rebuild prunes the old corpus")

		// THE SWAP ITSELF NOW RECLAIMS THE LOCAL BLOB, and that is a consequence of the
		// reset finalizing at the SERVING engine. ReplaceLayer fires the merge hook with
		// the retired set (searchengine/layer_swap.go:195), and this engine's hook is
		// reclaimMerged, which cache.Removes every retired constituent. Under the
		// two-engine shape the reset wrote an engine carrying NO hook, so the eviction
		// had to wait for InvalidateLocal; here it has already happened by the time the
		// finalize returns.
		atSwap := segFilesAtDir(t, detDir)
		for _, id := range pruned {
			require.NotContains(t, atSwap, id,
				"the swap's reclaim hook must already have evicted retired blob %s from the L2 cache", id)
		}

		// InvalidateLocal is therefore a BACKSTOP on this path rather than the mechanism,
		// and it must stay idempotent over a set already reclaimed.
		mgr.InvalidateLocal(gt, "roleA", pruned)
		after := segFilesAtDir(t, detDir)
		for _, id := range pruned {
			require.NotContains(t, after, id, "InvalidateLocal leaves the pruned id evicted from the L2 cache")
		}

		// A LAST LEG USED TO ASSERT the rebuild engine's OnMerge was nil, so a merge on it
		// could never auto-reclaim and its L2 was mutated by InvalidateLocal alone. There
		// is no separate rebuild engine to hold that property, and the ROLE separation it
		// guarded is now the ordering asserted above: the swap reclaims, InvalidateLocal
		// backstops, and ROLE B (below) stays disjoint from both.
	})

	t.Run("roleB_embed_prunes_only_this_process_and_disjoint", func(t *testing.T) {
		// Embed engine with the instrumented seam so we can capture the merge-reclaim
		// removed-set and cross-check disjointness against the ROLE-B ship prune.
		dm, ic := buildHNSWReclaimManager(t, gt, "roleB", t.TempDir(), 1<<30)
		defer dm.engine.Close()

		// Seal several single-doc segments and ship each (this-process locallyShipped).
		docs := vecContentDocs(6)
		for _, d := range docs {
			require.NoError(t, dm.engine.Add([]searchengine.Document{d}))
			_, err := dm.ship(ctx, dm.locallyShipped)
			require.NoError(t, err)
		}
		dm.shipMu.Lock()
		require.NotEmpty(t, dm.locallyShipped, "this process shipped its own segments")
		shippedBeforeMerge := copyIDSet(dm.locallyShipped)
		dm.shipMu.Unlock()

		// Trigger a merge that supersedes some of those this-process-shipped segments.
		dm.engine.Delete(docs[0].ID)
		dm.engine.Delete(docs[1].ID)
		waitForMerge(t, dm.engine.MergeCount, "roleB dead-ratio merge must fire")
		waitMergeQuiesce(dm.engine.MergeCount)
		warmExported(dm)

		mergeReclaimed := ic.removedSet()
		require.NotEmpty(t, mergeReclaimed, "the merge-reclaim hook removed superseded constituents from L2")

		// A following ROLE-B ship reconciles against locallyShipped → prunes only the
		// this-process merged-away ids on the SERVER. The set it prunes is a subset of
		// what this process shipped — never anything it did not ship.
		pruned, err := dm.ship(ctx, dm.locallyShipped)
		require.NoError(t, err)
		for _, id := range pruned {
			require.Contains(t, shippedBeforeMerge, id,
				"ROLE-B ship prunes ONLY this-process-shipped ids (id %s)", id)
		}

		// Disjointness: the merge-reclaim removed-set (L2 disk) and the ROLE-B server
		// prune-set are about DIFFERENT resources (local L2 vs server blobs); the live
		// set retains neither orphaned nor double-counted ids.
		assertLiveSetBackedByL2(t, dm, mergeReclaimed, nil, nil)
	})
}

// segFilesAtDir returns the .seg content-hash ids under an explicit dir.
func segFilesAtDir(t *testing.T, dir string) map[string]struct{} {
	t.Helper()
	c := newDiskSegmentCache(dir, 0)
	return diskCacheIDs(c)
}

// copyIDSet snapshots a SegmentID set (the live maps mutate under shipMu).
func copyIDSet(m map[searchengine.SegmentID]struct{}) map[searchengine.SegmentID]struct{} {
	out := make(map[searchengine.SegmentID]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}
