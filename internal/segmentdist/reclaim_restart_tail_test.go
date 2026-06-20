// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestReclaimRestartTailNoFalsePrune is the headline restart-tail false-prune
// guard: a fresh process (empty locallyShipped, importedGen 0) that reloads the
// full server corpus must NOT prune any prior-corpus id on its next ROLE-B ship,
// and the merge-reclaim hook must not over-remove on the restarted engine — it
// reclaims ONLY genuinely-superseded constituents. The whole corpus stays
// searchable and the invariant holds at every step.
func TestReclaimRestartTailNoFalsePrune(t *testing.T) {
	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "restart-tail"
	dir := t.TempDir()
	svc, gc := newSegmentHarness(t)

	// --- Process 1: seal + ship a corpus (populates server + L2). High count
	// target so no merge perturbs this seeding phase.
	corpus := vecContentDocs(6)
	dm1, _ := buildHNSWReclaimManagerOn(t, gc, gt, name, dir, 1<<30)
	for _, d := range corpus {
		require.NoError(t, dm1.engine.Add([]searchengine.Document{d}))
	}
	priorIDs := exportedIDSet(dm1)
	require.GreaterOrEqual(t, len(priorIDs), 6, "six single-doc segments shipped")
	_, err := dm1.ship(ctx, dm1.locallyShipped) // ROLE-B ship warms L2 + ships to server
	require.NoError(t, err)
	// Server now holds the full corpus.
	svc.mu.Lock()
	serverCount := len(svc.byKey[svc.key(graphSelector(gt, name))])
	svc.mu.Unlock()
	require.Equal(t, len(priorIDs), serverCount, "server holds the full prior corpus")
	dm1.engine.Close()

	// --- RESTART: fresh distManager over the SAME dir + SAME server. locallyShipped
	// is empty and importedGen is 0 (the cold-process condition that triggered the
	// original restart-tail false-prune: seeding shippedIDs from the full server
	// List(0) while Export returns only a tail made the embed reconcile prune the
	// whole corpus on the first ship after restart).
	dm2, ic2 := buildHNSWReclaimManagerOn(t, gc, gt, name, dir, 1<<30)
	defer dm2.engine.Close()
	require.Empty(t, dm2.locallyShipped, "fresh process: locallyShipped is empty")
	require.Equal(t, uint64(0), dm2.importedGen.Load(), "fresh process: importedGen is 0")

	// Reload the FULL corpus from the server/L2.
	require.NoError(t, dm2.load(ctx))
	require.Equal(t, priorIDs, exportedIDSet(dm2), "restart reloads the full prior corpus")

	// --- Fresh ROLE-B ship: must remove NOTHING from the prior corpus. (Export ==
	// the full reloaded corpus, locallyShipped empty → reconcilePrune prunes none.)
	_, err = dm2.ship(ctx, dm2.locallyShipped)
	require.NoError(t, err)
	for id := range priorIDs {
		_, removed := ic2.removedSet()[id]
		require.Falsef(t, removed, "ROLE-B ship after restart must NOT reclaim prior-corpus id %s", id)
	}
	require.Empty(t, ic2.removedSet(), "fresh ROLE-B ship reclaims nothing on a restarted engine")
	svc.mu.Lock()
	serverAfterShip := len(svc.byKey[svc.key(graphSelector(gt, name))])
	svc.mu.Unlock()
	require.Equal(t, len(priorIDs), serverAfterShip, "server corpus intact after the restart ship")
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
	require.GreaterOrEqual(t, waitMergeCount(dm2.engine.MergeCount, 1), uint64(1), "a merge must fire on the reloaded set")
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
