// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func liveNode(id string, updatedAt int64) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: "thought", UpdatedAt: updatedAt}
}

func deadNode(id string, updatedAt int64) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: "thought", UpdatedAt: updatedAt, TombstonedAt: updatedAt}
}

func cursor(updatedAt int64, id string) *knowledgev1.LayerCursor {
	return &knowledgev1.LayerCursor{LayerKey: "default", AfterUpdatedAt: updatedAt, AfterId: id}
}

func probe(live, maxUpdatedAt int64) *knowledgev1.LayerProbe {
	return &knowledgev1.LayerProbe{LayerKey: "default", LiveCount: live, MaxUpdatedAt: maxUpdatedAt}
}

// TestCorpusCache_MergeDelta_UpsertRemoveAdvance covers the core merge mechanics:
// live upsert, tombstone removal, and tombstone-INCLUSIVE cursor advance.
func TestCorpusCache_MergeDelta_UpsertRemoveAdvance(t *testing.T) {
	c := newCorpusCache()
	c.MergeDelta(&knowledgev1.CorpusDeltaResponse{
		Items:       []*knowledgev1.Node{liveNode("t1", 1000), liveNode("t2", 2000)},
		NextCursors: []*knowledgev1.LayerCursor{cursor(2000, "t2")},
	})
	require.Len(t, c.Snapshot(), 2, "two live rows after the first merge")

	// A delete-tick: t2 tombstoned with a bumped updated_at.
	c.MergeDelta(&knowledgev1.CorpusDeltaResponse{
		Items:       []*knowledgev1.Node{deadNode("t2", 3000)},
		NextCursors: []*knowledgev1.LayerCursor{cursor(3000, "t2")},
	})
	snap := c.Snapshot()
	require.Len(t, snap, 1, "tombstoned row removed from the live set")
	assert.Equal(t, "t1", snap[0].GetId())
	// Cursor advanced tombstone-inclusively past the delete.
	assert.Equal(t, int64(3000), c.cursors["default"].afterUpdatedAt)
}

// TestCorpusCache_Len asserts Len() is an exact equivalent of len(Snapshot())
// across the four states in order — fresh, populated, post-tombstone and post-Reset.
// The tombstone state is the one that matters: it is where a naive counter drifts
// from the live map, because the delete path REMOVES a row rather than adding one.
//
// Each state pins Len() against a fixture-derived constant AS WELL AS against the
// snapshot. The equivalence alone would be satisfied by both sides losing the same
// rows — two empties agree just as well as two twos — so the constant is what makes
// the agreement mean the counts are RIGHT rather than merely equal.
func TestCorpusCache_Len(t *testing.T) {
	c := newCorpusCache()

	assert.Equal(t, 0, c.Len(), "a fresh cache holds nothing")
	assert.Len(t, c.Snapshot(), c.Len(), "fresh: Len agrees with the snapshot")

	c.MergeDelta(&knowledgev1.CorpusDeltaResponse{
		Items:       []*knowledgev1.Node{liveNode("t1", 1000), liveNode("t2", 2000)},
		NextCursors: []*knowledgev1.LayerCursor{cursor(2000, "t2")},
	})
	assert.Equal(t, 2, c.Len(), "both merged live rows are counted")
	assert.Len(t, c.Snapshot(), c.Len(), "populated: Len agrees with the snapshot")

	c.MergeDelta(&knowledgev1.CorpusDeltaResponse{
		Items:       []*knowledgev1.Node{deadNode("t2", 3000)},
		NextCursors: []*knowledgev1.LayerCursor{cursor(3000, "t2")},
	})
	assert.Equal(t, 1, c.Len(), "the tombstoned row leaves the live set")
	assert.Len(t, c.Snapshot(), c.Len(), "post-tombstone: Len agrees with the snapshot")

	c.Reset()
	assert.Equal(t, 0, c.Len(), "Reset empties the cache")
	assert.Len(t, c.Snapshot(), c.Len(), "post-Reset: Len agrees with the snapshot")
}

// TestCorpusCache_Snapshot_DedupesOverlayOverBase asserts a same-id base row
// followed by an overlay row (server emits base-first) resolves overlay-wins.
func TestCorpusCache_Snapshot_DedupesOverlayOverBase(t *testing.T) {
	c := newCorpusCache()
	base := &knowledgev1.Node{Id: "t1", Type: "thought", UpdatedAt: 1000, Summary: "base"}
	overlay := &knowledgev1.Node{Id: "t1", Type: "thought", UpdatedAt: 1000, Summary: "overlay"}
	c.MergeDelta(&knowledgev1.CorpusDeltaResponse{Items: []*knowledgev1.Node{base, overlay}})
	snap := c.Snapshot()
	require.Len(t, snap, 1, "same id collapses to one entry")
	assert.Equal(t, "overlay", snap[0].GetSummary(), "overlay (merged later) wins")
}

// TestCorpusCache_Reconcile_DeleteTick asserts a delete-tick reconciles CLEAN:
// cache-live-max (t1) < probe.max (the tombstone) but the cursor high-water ==
// probe.max, so no spurious resync.
func TestCorpusCache_Reconcile_DeleteTick(t *testing.T) {
	c := newCorpusCache()
	c.MergeDelta(&knowledgev1.CorpusDeltaResponse{
		Items:       []*knowledgev1.Node{liveNode("t1", 1000), liveNode("t2", 2000)},
		NextCursors: []*knowledgev1.LayerCursor{cursor(2000, "t2")},
	})
	del := &knowledgev1.CorpusDeltaResponse{
		Items:       []*knowledgev1.Node{deadNode("t2", 3000)},
		SafeHorizon: 10000,
		LayerProbes: []*knowledgev1.LayerProbe{probe(1, 3000)}, // 1 live (t1), max tombstone-incl 3000
		NextCursors: []*knowledgev1.LayerCursor{cursor(3000, "t2")},
	}
	c.MergeDelta(del)
	assert.True(t, c.Reconcile(del), "delete-tick reconciles clean (cursor high-water == probe.max)")
}

// TestCorpusCache_Reconcile_RegressedH asserts a regressed horizon (below the
// cursor high-water) reconciles CLEAN via the <=H-filtered count, skipping the
// high-water equality the lower H cannot satisfy.
func TestCorpusCache_Reconcile_RegressedH(t *testing.T) {
	c := newCorpusCache()
	c.MergeDelta(&knowledgev1.CorpusDeltaResponse{
		Items:       []*knowledgev1.Node{liveNode("t1", 1000), liveNode("t2", 2000), liveNode("t3", 5000)},
		NextCursors: []*knowledgev1.LayerCursor{cursor(5000, "t3")},
	})
	// H regresses to 3000 (below the cursor high-water 5000). Probe at H=3000 sees
	// t1,t2 live (2), max <=H = 2000; the empty (5000,3000] range leaves the cursor
	// at 5000.
	regressed := &knowledgev1.CorpusDeltaResponse{
		SafeHorizon: 3000,
		LayerProbes: []*knowledgev1.LayerProbe{probe(2, 2000)},
		NextCursors: []*knowledgev1.LayerCursor{cursor(5000, "t3")}, // unchanged (empty page)
	}
	c.MergeDelta(regressed)
	assert.True(t, c.Reconcile(regressed), "regressed-H reconciles clean via <=H-filtered count")
}

// TestCorpusCache_Reconcile_ResurrectTick asserts a tombstone-then-recreate of the
// same id reconciles clean.
func TestCorpusCache_Reconcile_ResurrectTick(t *testing.T) {
	c := newCorpusCache()
	c.MergeDelta(&knowledgev1.CorpusDeltaResponse{
		Items:       []*knowledgev1.Node{liveNode("t1", 1000), liveNode("t9", 2000)},
		NextCursors: []*knowledgev1.LayerCursor{cursor(2000, "t9")},
	})
	// Tick: t9 tombstoned at 3000 then re-created LIVE at 4000 (same tick, later
	// updated_at). The live re-add restores t9; cursor advances to 4000.
	resurrect := &knowledgev1.CorpusDeltaResponse{
		Items:       []*knowledgev1.Node{deadNode("t9", 3000), liveNode("t9", 4000)},
		SafeHorizon: 10000,
		LayerProbes: []*knowledgev1.LayerProbe{probe(2, 4000)}, // t1,t9 live; max 4000
		NextCursors: []*knowledgev1.LayerCursor{cursor(4000, "t9")},
	}
	c.MergeDelta(resurrect)
	require.Len(t, c.Snapshot(), 2, "resurrected id is live again")
	assert.True(t, c.Reconcile(resurrect), "resurrect-tick reconciles clean")
}

// TestCorpusCache_Reconcile_MixedTick asserts a tick with BOTH deletes and live
// changes reconciles clean.
func TestCorpusCache_Reconcile_MixedTick(t *testing.T) {
	c := newCorpusCache()
	c.MergeDelta(&knowledgev1.CorpusDeltaResponse{
		Items:       []*knowledgev1.Node{liveNode("t1", 1000), liveNode("t2", 2000), liveNode("t3", 3000)},
		NextCursors: []*knowledgev1.LayerCursor{cursor(3000, "t3")},
	})
	// Mixed: t2 deleted (tomb 5000), t4 added live (4000), t1 updated (6000).
	mixed := &knowledgev1.CorpusDeltaResponse{
		Items: []*knowledgev1.Node{
			liveNode("t4", 4000), deadNode("t2", 5000), liveNode("t1", 6000),
		},
		SafeHorizon: 20000,
		LayerProbes: []*knowledgev1.LayerProbe{probe(3, 6000)}, // live: t1,t3,t4 = 3; max tombstone-incl 6000
		NextCursors: []*knowledgev1.LayerCursor{cursor(6000, "t1")},
	}
	c.MergeDelta(mixed)
	assert.True(t, c.Reconcile(mixed), "mixed-tick reconciles clean")
	require.Len(t, c.Snapshot(), 3, "live set: t1, t3, t4 (t2 removed)")
}

// TestCorpusCache_Reconcile_GenuineDivergence asserts a real count mismatch forces
// a resync (returns false).
func TestCorpusCache_Reconcile_GenuineDivergence(t *testing.T) {
	c := newCorpusCache()
	c.MergeDelta(&knowledgev1.CorpusDeltaResponse{
		Items:       []*knowledgev1.Node{liveNode("t1", 1000), liveNode("t2", 2000)},
		NextCursors: []*knowledgev1.LayerCursor{cursor(2000, "t2")},
	})
	// The server reports 5 live rows but the cache holds 2 — a genuine divergence.
	diverged := &knowledgev1.CorpusDeltaResponse{
		SafeHorizon: 10000,
		LayerProbes: []*knowledgev1.LayerProbe{probe(5, 2000)},
		NextCursors: []*knowledgev1.LayerCursor{cursor(2000, "t2")},
	}
	c.MergeDelta(diverged)
	assert.False(t, c.Reconcile(diverged), "a genuine count divergence forces a resync")
}
