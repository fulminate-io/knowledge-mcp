// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestReclaimRestartTailNoFalsePrune is the restart-tail false-prune guard, reduced
// to the half that is still reachable: the merge-reclaim hook must not over-remove on
// a RESTARTED engine — it reclaims ONLY genuinely-superseded constituents, the whole
// corpus stays searchable, and the invariant holds at every step.
//
// THE OTHER HALF WENT WITH ITS MECHANISM. It asserted that a fresh process's next
// ROLE-B ship pruned no prior-corpus id — a property of the reconcile-prune leg
// diffing Export against an empty locally-shipped set, and the ship leg, that prune
// leg and that set are all deleted. The hazard it guarded (a cold process retiring a corpus
// it did not build) is NOT dropped: it is reachable locally through PruneCache's
// live-set diff instead, and TestFreshProcessCannotRetireAPriorCorpus
// (restart_falseprune_test.go) is the successor that guards it there.
func TestReclaimRestartTailNoFalsePrune(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "restart-tail"
	dir := t.TempDir()

	// --- Process 1: seal + ship a corpus (populates server + L2). High count
	// target so no merge perturbs this seeding phase.
	corpus := vecContentDocs(6)
	dm1, _ := buildHNSWReclaimManagerOn(t, gt, name, dir, 1<<30)
	for _, d := range corpus {
		require.NoError(t, dm1.engine.Add([]searchengine.Document{d}))
	}
	priorIDs := exportedIDs(dm1.engine.Export())
	require.GreaterOrEqual(t, len(priorIDs), 6, "six single-doc segments sealed")
	require.NoError(t, dm1.writeNewBlobsToL2(dm1.engine.Export()))
	require.Len(t, dm1.cache.Keys(), len(priorIDs), "L2 holds the full prior corpus")
	dm1.engine.Close()

	// --- RESTART: fresh distManager over the SAME dir + SAME server. The cold process
	// starts with no ship bookkeeping and no imported generation — the condition that
	// triggered the original restart-tail false-prune: seeding the shipped set from the
	// full server listing while Export returned only a tail made the embed reconcile
	// prune the whole corpus on the first ship after restart.
	dm2, ic2 := buildHNSWReclaimManagerOn(t, gt, name, dir, 1<<30)
	defer dm2.engine.Close()

	// Reload the FULL corpus from L2.
	require.NoError(t, dm2.load(ctx))
	require.Equal(t, priorIDs, exportedIDs(dm2.engine.Export()), "restart reloads the full prior corpus")

	// A restarted engine that has merely RELOADED must have reclaimed nothing: the
	// reclaim hook fires on a merge, and no merge has happened yet.
	require.Empty(t, ic2.removedSet(), "a reload on a restarted engine reclaims nothing")
	assertLiveSetBackedByL2(t, dm2, ic2.removedSet(), nil, nil)

	// --- Now add docs to trip a background merge on the reloaded set. Only the
	// genuinely-superseded constituents may be reclaimed; the full corpus survives.
	extra := vecContentDocsSeed(4, 9000)
	for _, d := range extra {
		require.NoError(t, dm2.engine.Add([]searchengine.Document{d}))
	}
	warmExported(dm2)
	// Trigger a merge deterministically: delete two reloaded docs so their
	// single-doc segments become 100% dead (≥ the 0.33 dead-ratio) and thus
	// merge-eligible. This supersedes exactly those two constituents.
	dm2.engine.Delete(corpus[0].ID)
	dm2.engine.Delete(corpus[1].ID)
	waitForMerge(t, dm2.engine.MergeCount, "a merge must fire on the reloaded set")
	waitMergeQuiesce(dm2.engine.MergeCount)
	warmExported(dm2)

	// The merge genuinely reclaimed the superseded constituents (not a vacuous
	// pass), yet every still-live id is L2-backed and no live id was removed.
	require.NotEmpty(t, ic2.removedSet(), "the post-restart merge must reclaim the genuinely-superseded constituents")
	assertLiveSetBackedByL2(t, dm2, ic2.removedSet(), nil, nil)

	// The full LIVE corpus (original minus the two deleted + the new docs) is
	// searchable via self-recall.
	deleted := map[searchengine.ExternalID]struct{}{corpus[0].ID: {}, corpus[1].ID: {}}
	all := append(append([]searchengine.Document{}, corpus...), extra...)
	recall, leaked := hnswRecallOK(dm2, all, deleted)
	require.False(t, leaked, "no deleted doc may leak after restart-merge")
	require.GreaterOrEqual(t, recall, 0.95, "live corpus recoverable after restart + merge (recall=%.3f)", recall)
}
