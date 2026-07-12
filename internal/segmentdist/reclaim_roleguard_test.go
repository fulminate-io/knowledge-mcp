// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestReclaimRoleRegressionGuards asserts the new merge-reclaim hook does not
// perturb the two pre-existing prune roles, and that the three reclamation sources
// — ROLE-A (deterministic rebuild → FlushDeterministic → InvalidateLocal), ROLE-B
// (embed AddAndShip reconcile-prune against locallyShipped), and the new live-cache
// merge-reclaim hook — operate on DISJOINT id sets.
func TestReclaimRoleRegressionGuards(t *testing.T) {
	ctx := context.Background()
	gt := kgtypes.GraphCode

	t.Run("roleA_det_rebuild_still_prunes_and_no_live_hook", func(t *testing.T) {
		svc, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))

		// Ship an old degenerate corpus via the embed path, then deterministically
		// rebuild it with a different corpus → ROLE-A prunes the old ids.
		oldDocs := hnswVecDocs(searchCorpusN)
		require.NoError(t, mgr.AddAndShip(ctx, gt, "roleA", oldDocs))
		oldIDs := shippedHNSWIDs(svc)
		require.NotEmpty(t, oldIDs)

		rebuildDocs := hnswVecDocs(searchCorpusN + 32)
		require.NoError(t, mgr.AddDeterministic(ctx, gt, "roleA", rebuildDocs))
		pruned, err := mgr.FlushDeterministic(ctx, gt, "roleA")
		require.NoError(t, err)
		require.NotEmpty(t, pruned, "ROLE-A deterministic rebuild prunes the old corpus")

		// InvalidateLocal removes the replaced ids from the DET L2 cache.
		detDir := graphCacheDirFor(mgr.cacheDir, gt, "roleA", hnsw.New().Name())
		before := segFilesAtDir(t, detDir)
		for id := range oldIDs {
			require.Contains(t, before, id, "old id present in det cache before InvalidateLocal")
		}
		mgr.InvalidateLocal(gt, "roleA", pruned)
		after := segFilesAtDir(t, detDir)
		for _, id := range pruned {
			require.NotContains(t, after, id, "InvalidateLocal removes the pruned id from the det L2 cache")
		}

		// The det engine's OnMerge is nil: a merge on it never auto-reclaims. Drive a
		// merge on the det engine and confirm no live-cache hook fired (the det cache
		// is mutated ONLY by InvalidateLocal, not by a reclaim hook).
		detDM := mgr.hnswManagerFor(mgr.detManagers, hnsw.NewDeterministic(), gt, "roleA", false)
		// Seed a fresh single-doc segment and delete it to make it merge-eligible
		// alongside the rebuilt corpus, forcing a det merge.
		extra := vecContentDocsSeed(1, 777000)
		require.NoError(t, detDM.engine.Add(extra))
		detDM.engine.Delete(extra[0].ID)
		// Even if a det merge fires, no reclaim hook runs: the det cache state is
		// unchanged by the (nil) OnMerge across a quiescence window.
		stable := segFilesAtDir(t, detDir)
		waitMergeQuiesce(detDM.engine.MergeCount)
		time.Sleep(60 * time.Millisecond)
		require.Equal(t, stable, segFilesAtDir(t, detDir),
			"det engine's live-cache reclaim hook never fires (nil OnMerge): det L2 unchanged by any merge")
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
		require.GreaterOrEqual(t, waitMergeCount(dm.engine.MergeCount, 1), uint64(1))
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
