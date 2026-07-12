// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// serverSegCount returns how many segments the fake server holds for one target —
// the bounded-accumulation signal the reconcile test asserts on.
func serverSegCount(t *testing.T, svc *sharedServerFake, target *knowledgev1.GraphSelector) int {
	t.Helper()
	return len(svc.listMetas(target, 0))
}

// TestReconcileOnShipPrunesMergedAway is the bounded-server-segment proof.
// It drives the INCREMENTAL ship path: N=8 single-doc Adds each seal one small
// segment and ship() pushes it, so the server accumulates ~8 segments. The engine's
// background merger (SegmentCountTarget=4) consolidates the 8 small segments into
// one. The next ship() RE-SHIPS the consolidated segment (the existing new-id diff)
// and PRUNES the 8 merged-away ids (reconcile-on-ship), so the server segment count
// drops back to ~1 — BOUNDED, not ~8. Without the reconcile the server would
// accumulate every pre-merge segment forever.
func TestReconcileOnShipPrunesMergedAway(t *testing.T) {
	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "boundedRepo"}
	ctx := context.Background()

	// MinSegmentDocs=1 → each Add seals one segment; SegmentCountTarget=4 → once
	// more than 4 accumulate the background merger consolidates them all down.
	prodEng := searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: 4,
	})
	defer prodEng.Close()

	mgr, cc := buildManager(prodEng, gc, target, t.TempDir())

	// Incrementally seal + ship 8 small segments. The background merger
	// (SegmentCountTarget=4) may fire DURING this loop once more than 4 accumulate;
	// when it does, the very next ship() reconciles (re-ships the consolidated blob,
	// prunes the merged-away ids) — that asynchrony is exactly what we are proving
	// keeps the server bounded, so we do NOT assert a fixed mid-loop prune count.
	// Accumulate every ship()'s returned pruned-id set CUMULATIVELY across the whole
	// test. The merger is signal-driven (engine seal→signalMerge; merge.go), so it
	// can consolidate before any single mid-loop ship — pinning NotEmpty to one
	// specific ship() would flake. The cumulative union proves the new ship()→
	// reconcilePrune return path carries the real superseded ids without that timing
	// assumption.
	var prunedAll []searchengine.SegmentID
	const n = 8
	for i := range n {
		require.NoError(t, prodEng.Add([]searchengine.Document{
			doc(fmt.Sprintf("d%d", i), fmt.Sprintf("body %d", i)),
		}))
		pruned, err := mgr.ship(ctx, mgr.locallyShipped)
		require.NoError(t, err)
		prunedAll = append(prunedAll, pruned...)
	}

	// Wait for the background merge to consolidate the accumulated small segments.
	require.Eventually(t, func() bool { return prodEng.MergeCount() > 0 },
		2*time.Second, 2*time.Millisecond, "background merge must fire once >SegmentCountTarget segments accumulate")

	// One more ship() after the merge guarantees the final consolidated set is
	// reconciled onto the server (re-ship consolidated FIRST, then prune merged-away).
	postMergePruned, err := mgr.ship(ctx, mgr.locallyShipped)
	require.NoError(t, err)
	prunedAll = append(prunedAll, postMergePruned...)

	// At least one Prune RPC must have fired across the merge+reconcile — the
	// merged-away small segments were deleted from the server.
	require.GreaterOrEqual(t, cc.pruneCalls.Load(), int64(1),
		"reconcile-on-ship prunes the merged-away ids (at least one Prune RPC fires)")

	// The new ship()→reconcilePrune return path must carry the REAL superseded ids,
	// not nil. Asserted on the CUMULATIVE union of every ship()'s returned set
	// (Reviewer T3): the merge that produces a non-empty pruneSet may land on any
	// ship() in the test, so a pinned per-ship NotEmpty would flake — the union is
	// the timing-independent proof.
	require.NotEmpty(t, prunedAll,
		"some ship() across the merge returns the superseded segment ids it pruned (cumulative — return path carries real ids, not nil)")

	// The engine's post-merge Export is the bounded consolidated set; every exported
	// id is present on the server, and the server segment count MATCHES the engine's
	// live set — BOUNDED (~1), NOT the ~8 pre-merge accumulation.
	exported := prodEng.Export()
	serverIDs := map[string]bool{}
	for _, m := range svc.listMetas(target, 0) {
		serverIDs[m.GetId()] = true
	}
	for _, b := range exported {
		require.True(t, serverIDs[b.ID], "every live (post-merge) segment is shipped to the server")
	}
	require.Equal(t, len(exported), serverSegCount(t, svc, target),
		"server segment count is BOUNDED — it matches the engine's post-merge live set, not the pre-merge accumulation")
	require.Less(t, serverSegCount(t, svc, target), n,
		"server holds far fewer than the 8 pre-merge segments — accumulation is bounded")

	// Steady state: a final ship() with no intervening Add or merge is fully
	// zero-RPC on BOTH legs (empty diff AND empty pruneSet).
	beforePrune := cc.pruneCalls.Load()
	beforeShip := cc.shipCalls.Load()
	_, err = mgr.ship(ctx, mgr.locallyShipped)
	require.NoError(t, err)
	require.Equal(t, beforePrune, cc.pruneCalls.Load(), "steady-state ship() issues ZERO Prune RPCs")
	require.Equal(t, beforeShip, cc.shipCalls.Load(), "steady-state ship() issues ZERO Ship RPCs")
}
