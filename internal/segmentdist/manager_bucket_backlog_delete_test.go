// SPDX-License-Identifier: Apache-2.0

// manager_bucket_backlog_delete_test.go — the write backlog against deletes: a
// document removed by DeleteFromBuckets must not come back when the same reconcile
// pass drains the backlog that still holds it, and a tombstoned document re-queued
// AFTER the delete must not be rebuilt either. Both legs are asserted on the vector
// AND the field corpus, and against a FRESH engine loaded from the shipped segments
// rather than the writer's own memory.
//
// It also covers the accounting hazard on the other side of the same window: a write
// re-queued DURING a tick carrying the SAME id a concurrent purge removed must survive
// the clear, which id-keyed consumption cannot deliver.

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// reEmbeddedDocs returns the SAME ids carrying DIFFERENT payload bytes, modeling a
// re-embedding writeback: every vector is copied with byte 0 and the last byte
// perturbed, and every field value gets the tag appended.
//
// THE PERTURBATION IS LOAD-BEARING — do not simplify it into an identical re-add.
// Re-adding the identical documents produces a batch whose content hash ALIASES the
// resident partition: the seal is dropped at publish, no new tail exists, and the
// DeleteFromBuckets that follows does not even reduce the corpus. A test built that
// way fails at its own precondition, for a reason that has nothing to do with the
// write backlog these tests exist to exercise. That aliasing is a separate defect,
// tracked and owned elsewhere; distinct payload bytes route around it.
func reEmbeddedDocs(docs []searchengine.Document, tag string) []searchengine.Document {
	out := make([]searchengine.Document, len(docs))
	for i, d := range docs {
		out[i] = searchengine.Document{ID: d.ID}
		if len(d.Vector) > 0 {
			v := append([]byte(nil), d.Vector...)
			v[0] ^= 0x5A
			v[len(v)-1] ^= 0x3C
			out[i].Vector = v
		}
		if d.Fields != nil {
			fields := make(map[string]string, len(d.Fields))
			for k, val := range d.Fields {
				fields[k] = val + " " + tag
			}
			out[i].Fields = fields
		}
	}
	return out
}

// TestDrainCannotResurrectDeletedNode is the REPRODUCTION for the backlog
// resurrection: the documents a delete removes are still sitting in the write
// backlog, and the reconcile pass that performs the delete drains that backlog
// immediately afterwards — rebuilding the deleted document straight back into both
// corpora and into the shipped blob.
//
// The re-add is deliberately NOT drained before the delete: an undrained backlog is
// exactly the state a delete arrives into in production, where writes accumulate
// between five-minute reconcile ticks.
func TestDrainCannotResurrectDeletedNode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const name = "backlogResurrect"
	gt := kgtypes.GraphCode

	dir := t.TempDir()
	_, gc := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc))

	docs := bothFormatDocs(deleteFixtureN, "resurrect-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	victim := docs[0]
	require.True(t, residentInFreshEngine(t, ctx, gc, dir, gt, name, victim),
		"PRECONDITION: the node must be in the shipped corpus before the delete, or this test proves nothing")

	// The backlog at delete time: a re-embedding writeback re-queued every document
	// and no tick has drained it yet.
	rewritten := reEmbeddedDocs(docs, "rewrite")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, rewritten))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, rewritten))

	hnswDM := mgr.managerFor(gt, name)
	bm25DM := mgr.bm25ManagerFor(gt, name)

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))

	require.Equal(t, deleteFixtureN-1, hnswDM.engine.DistinctResidentDocCount(),
		"PRECONDITION: the delete must land on the vector corpus before the drain runs")
	require.Equal(t, deleteFixtureN-1, bm25DM.engine.DistinctResidentDocCount(),
		"PRECONDITION: the delete must land on the field corpus before the drain runs")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	require.Equal(t, deleteFixtureN-1, hnswDM.engine.DistinctResidentDocCount(),
		"the drain rebuilt the deleted node back into the vector corpus")
	require.Equal(t, deleteFixtureN-1, bm25DM.engine.DistinctResidentDocCount(),
		"the drain rebuilt the deleted node back into the field corpus")
	require.False(t, residentInFreshEngine(t, ctx, gc, dir, gt, name, victim),
		"the drain shipped the deleted node back into the blob")
	require.True(t, residentInFreshEngine(t, ctx, gc, dir, gt, name, docs[1]),
		"CONTROL: an undeleted neighbor is still shipped")
}

// TestDrainSkipsTombstonedBacklogDoc is the SECOND reproduction, and it is not a
// restatement of the first: it covers the ids that arrive in the backlog AFTER the
// delete has already run, which a purge at delete time cannot reach.
//
// The tombstone set is established BEFORE the delete, matching the order the delta
// consumer uses, and the re-embedding writeback lands afterwards — the shape a
// retained tombstoned node takes when something re-queues it.
func TestDrainSkipsTombstonedBacklogDoc(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const name = "backlogTombstoned"
	gt := kgtypes.GraphKnowledge

	dir := t.TempDir()
	_, gc := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc))

	docs := bothFormatDocs(deleteFixtureN, "tombdrain-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	victim := docs[0]
	require.True(t, residentInFreshEngine(t, ctx, gc, dir, gt, name, victim),
		"PRECONDITION: the node must be in the shipped corpus before the delete, or this test proves nothing")

	hnswDM := mgr.managerFor(gt, name)
	bm25DM := mgr.bm25ManagerFor(gt, name)

	mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})
	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))

	require.Equal(t, deleteFixtureN-1, hnswDM.engine.DistinctResidentDocCount(),
		"PRECONDITION: the delete must land on the vector corpus before the drain runs")
	require.Equal(t, deleteFixtureN-1, bm25DM.engine.DistinctResidentDocCount(),
		"PRECONDITION: the delete must land on the field corpus before the drain runs")

	// The writeback re-queues the retained tombstoned node AFTER the delete.
	requeued := reEmbeddedDocs([]searchengine.Document{victim}, "post-delete")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, requeued))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, requeued))

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	require.Equal(t, deleteFixtureN-1, hnswDM.engine.DistinctResidentDocCount(),
		"a tombstoned backlog document was built back into the vector corpus")
	require.Equal(t, deleteFixtureN-1, bm25DM.engine.DistinctResidentDocCount(),
		"a tombstoned backlog document was built back into the field corpus")
	require.False(t, residentInFreshEngine(t, ctx, gc, dir, gt, name, victim),
		"the drain shipped the tombstoned node back into the blob")
	require.True(t, residentInFreshEngine(t, ctx, gc, dir, gt, name, docs[1]),
		"CONTROL: an undeleted neighbor is still shipped")

	// DROPPED FROM THE BUILD IS NOT ENOUGH — the entries must also be CONSUMED from
	// the backlog. A filter that skips them at build time but leaves them queued
	// re-triggers a drain on every tick forever, with their bytes still charging the
	// backlog's byte cap.
	hnswLeft, bm25Left := mgr.snapshotDirty(gt, name)
	require.Empty(t, hnswLeft.pending, "a filtered vector document must still be consumed from the backlog")
	require.Empty(t, bm25Left.pending, "a filtered field document must still be consumed from the backlog")
	require.Empty(t, hnswLeft.tails, "the vector tail the filtered document sealed into must still be consumed")
	require.Empty(t, bm25Left.tails, "the field tail the filtered document sealed into must still be consumed")
}

// TestClearKeepsWritesDuringTick is THE CATCHER for the corpus-loss hazard the purge
// would otherwise introduce: a delete removes entries from the MIDDLE of the backlog
// while a tick is in flight, so a clear that consumed a LEADING COUNT would eat one
// late-arriving write per purged entry — dropping its documents while its tail id
// survived, leaving the next drain to retire a tail whose partition was never built.
//
// It runs on the backlog accounting alone, with no engine and no segment source: the
// hazard is entirely in what clearDirty consumes.
//
// The counts are deliberately UNEQUAL — four early against one late — because that is
// what makes the assertion discriminating. Equal counts would let an off-by-one
// accounting error pass unnoticed.
func TestClearKeepsWritesDuringTick(t *testing.T) {
	t.Parallel()

	const name = "clearDuringTick"
	gt := kgtypes.GraphCode

	mgr := NewManager(loginStateStub{}, t.TempDir(), 0)

	early := bothFormatDocs(4, "tick-")
	mgr.recordDirty(gt, name, false, early, []searchengine.SegmentID{"tail-early"}, mgr.nextWriteSeq())

	// The tick takes its snapshot, then a concurrent delete purges one entry out of
	// the middle of the backlog and a writeback appends a new one behind it.
	snap, _ := mgr.snapshotDirty(gt, name)
	mgr.purgeDirty(gt, name, []searchengine.ExternalID{early[1].ID})
	late := bothFormatDocs(1, "late-")
	mgr.recordDirty(gt, name, false, late, []searchengine.SegmentID{"tail-late"}, mgr.nextWriteSeq())

	mgr.clearDirty(gt, name, snap, formatDirtyState{})

	after, _ := mgr.snapshotDirty(gt, name)
	require.Len(t, after.pending, 1,
		"the write that arrived DURING the tick must survive a mid-flight purge")
	require.Equal(t, late[0].ID, after.pending[0].doc.ID,
		"the surviving entry must be the late write, not a snapshot entry the clear missed")
	require.Equal(t, []searchengine.SegmentID{"tail-late"}, after.tails,
		"the late write's tail must survive too — a dropped document with a surviving tail is the corpus-loss shape")
}

// TestClearKeepsRequeuedSameIDDuringTick is the sibling of TestClearKeepsWritesDuringTick
// for the case that test cannot reach: the late write carries the SAME id the concurrent
// purge removed, which is a node deleted and immediately re-created.
//
// Matching the snapshot by id cannot tell the two apart, so the budget the tick allocated
// for the copy the purge already dropped eats the re-creation instead — its document gone
// while its tail id survives, which is the corpus-loss shape, not a missed optimisation.
// The sibling above catches this only for DIFFERENT ids.
//
// The counts are deliberately UNEQUAL — four early against one late — for the reason the
// sibling states: equal counts let an off-by-one accounting error pass unnoticed.
func TestClearKeepsRequeuedSameIDDuringTick(t *testing.T) {
	t.Parallel()

	const name = "clearRequeuedSameID"
	gt := kgtypes.GraphCode

	t.Run("recreated_write_survives_the_tick", func(t *testing.T) {
		mgr := NewManager(loginStateStub{}, t.TempDir(), 0)

		early := bothFormatDocs(4, "tick-")
		mgr.recordDirty(gt, name, false, early, []searchengine.SegmentID{"tail-early"}, mgr.nextWriteSeq())

		// The tick snapshots, a concurrent delete purges one entry out of the middle,
		// and the re-creation of THAT SAME id lands behind it.
		snap, _ := mgr.snapshotDirty(gt, name)
		mgr.purgeDirty(gt, name, []searchengine.ExternalID{early[1].ID})
		late := reEmbeddedDocs([]searchengine.Document{early[1]}, "recreated")
		mgr.recordDirty(gt, name, false, late, []searchengine.SegmentID{"tail-late"}, mgr.nextWriteSeq())

		mgr.clearDirty(gt, name, snap, formatDirtyState{})

		after, _ := mgr.snapshotDirty(gt, name)
		require.Len(t, after.pending, 1,
			"the re-created write must survive a mid-flight purge of its own id")
		require.Equal(t, early[1].ID, after.pending[0].doc.ID,
			"the surviving entry must be the re-created write, not a snapshot entry the clear missed")
		require.Equal(t, []searchengine.SegmentID{"tail-late"}, after.tails,
			"the re-created write's tail must survive too — a dropped document with a surviving tail is the corpus-loss shape")
	})

	t.Run("unpurged_snapshot_entry_is_still_consumed", func(t *testing.T) {
		// THE CONTROL that keeps the fix from being "stop consuming": with no purge and
		// no late write, everything the snapshot named must go.
		mgr := NewManager(loginStateStub{}, t.TempDir(), 0)

		docs := bothFormatDocs(4, "consume-")
		mgr.recordDirty(gt, name, false, docs, []searchengine.SegmentID{"tail-only"}, mgr.nextWriteSeq())

		snap, _ := mgr.snapshotDirty(gt, name)
		mgr.clearDirty(gt, name, snap, formatDirtyState{})

		after, _ := mgr.snapshotDirty(gt, name)
		require.Empty(t, after.pending, "a snapshot entry no purge touched must still be consumed")
		require.Empty(t, after.tails, "the tail the snapshot named must still be consumed")
	})
}
