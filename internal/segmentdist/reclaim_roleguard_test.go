// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestRebuildSwapReclaimsThenInvalidateLocalBackstops pins the ORDERING of the two
// surviving local reclamation steps on a deterministic rebuild: the layer swap's own
// merge hook evicts each retired blob from L2, and InvalidateLocal is a BACKSTOP over
// a set already reclaimed rather than the mechanism that reclaims it.
//
// IT USED TO BE A ROLE-SEPARATION TEST, and two of the three roles are gone. ROLE-A
// was the reset rebuild's SERVER-SIDE prune and ROLE-B was the embed tick's
// reconcile-prune against the locally-shipped set; both died with the rail, and a "these three
// sources touch disjoint id sets" property is not expressible over the one source
// that remains. The roleB subtest went with its role and owes no successor — the
// mechanism is absent, not weakened. What survives is the ordering above, which is
// genuinely local and was previously buried inside the roleA arm.
func TestRebuildSwapReclaimsThenInvalidateLocalBackstops(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	gt := kgtypes.GraphCode

	t.Run("roleA_det_rebuild_still_prunes_and_no_live_hook", func(t *testing.T) {
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

		// Ship an old degenerate corpus via the embed path, then deterministically
		// rebuild it with a different corpus → ROLE-A prunes the old ids.
		oldDocs := hnswVecDocs(searchCorpusN)
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, "roleA", oldDocs))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, "roleA"))
		oldIDs := l2HNSWIDs(mgr.cacheDir, "roleA")
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

	// The roleB arm was deleted here — see the doc above.
}

// segFilesAtDir returns the .seg content-hash ids under an explicit dir.
func segFilesAtDir(t *testing.T, dir string) map[string]struct{} {
	t.Helper()
	c := newDiskSegmentCache(dir, 0, adviceRandom)
	return diskCacheIDs(c)
}
