// SPDX-License-Identifier: Apache-2.0

// manager_bucket_backlog_recreate_test.go — the DELETED-THEN-RE-CREATED id: what the
// backlog reports about it, and what the drain does with it once the caller has
// cleared the record's tombstone. It is the constructive twin of the delete tests
// next door, which prove the drain drops a genuinely-deleted document; these prove it
// keeps one that has been written again.
//
// It lives beside them rather than in the same file only because of the repo's
// line cap; every fixture it uses is package-level and shared with them.

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// TestTombstonedPendingWriteIDs gates the reporter that tells the reconcile pass which
// tombstoned ids have live documents queued again.
//
// The design is FAIL-OPEN — an id with no stamp reads as zero and is therefore
// reportable, which is right for a first-ever delete — so the subtests that matter
// most are the three that prove a stamp actually suppresses: the re-delete, the
// unrelated delete, and the seal race.
func TestTombstonedPendingWriteIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt := kgtypes.GraphCode

	t.Run("reports_write_after_delete", func(t *testing.T) {
		const name = "reportAfterDelete"
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

		docs := bothFormatDocs(2, "recreate-")
		victim := docs[0]
		mgr.NoteDeletedIDs(gt, name, []searchengine.ExternalID{victim.ID})
		mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})

		mgr.recordDirty(gt, name, false,
			reEmbeddedDocs([]searchengine.Document{victim}, "again"),
			[]searchengine.SegmentID{"tail-again"}, mgr.nextWriteSeq())

		require.Equal(t, []searchengine.ExternalID{victim.ID},
			mgr.TombstonedPendingWriteIDs(gt, name))
	})

	t.Run("walks_both_format_backlogs", func(t *testing.T) {
		// A write can land on one engine and fail on the other, because the field-engine
		// call is best-effort at its production call site.
		const name = "reportFieldOnly"
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

		docs := bothFormatDocs(2, "fieldonly-")
		victim := docs[0]
		mgr.NoteDeletedIDs(gt, name, []searchengine.ExternalID{victim.ID})
		mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})

		mgr.recordDirty(gt, name, true,
			reEmbeddedDocs([]searchengine.Document{victim}, "fields"),
			[]searchengine.SegmentID{"tail-fields"}, mgr.nextWriteSeq())

		require.Equal(t, []searchengine.ExternalID{victim.ID},
			mgr.TombstonedPendingWriteIDs(gt, name))
	})

	t.Run("stale_pre_delete_write_is_purged_first", func(t *testing.T) {
		// HONEST LABEL: this covers only the ONE route that purges the backlog. It
		// cannot fail for the three routes that tombstone without purging, which is
		// exactly why the next two subtests exist.
		const name = "reportPurged"
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

		docs := bothFormatDocs(2, "purged-")
		victim := docs[0]
		mgr.recordDirty(gt, name, false, docs, []searchengine.SegmentID{"tail-stale"}, mgr.nextWriteSeq())

		mgr.NoteDeletedIDs(gt, name, []searchengine.ExternalID{victim.ID})
		mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})
		require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))

		require.Empty(t, mgr.TombstonedPendingWriteIDs(gt, name),
			"the delete purged the queued entry, so there is nothing to re-create")
	})

	t.Run("redelete_after_a_queued_write_suppresses_the_report", func(t *testing.T) {
		// The already-known re-delete purges NOTHING — the delta consumer returns before
		// DeleteFromBuckets when the id was already in the record — so only the stamp can
		// tell the reporter that the queued write is now stale.
		const name = "reportRedelete"
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

		docs := bothFormatDocs(2, "redelete-")
		victim := docs[0]

		mgr.NoteDeletedIDs(gt, name, []searchengine.ExternalID{victim.ID})
		mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})
		mgr.recordDirty(gt, name, false,
			reEmbeddedDocs([]searchengine.Document{victim}, "mid"),
			[]searchengine.SegmentID{"tail-mid"}, mgr.nextWriteSeq())
		mgr.NoteDeletedIDs(gt, name, []searchengine.ExternalID{victim.ID})

		require.Empty(t, mgr.TombstonedPendingWriteIDs(gt, name),
			"a delete reported AFTER the queued write makes that write stale")
	})

	t.Run("unrelated_delete_does_not_suppress_a_queued_write", func(t *testing.T) {
		// THE PER-ID CATCHER. Under a per-graph watermark this returns empty, the fresh
		// document is dropped by the drain and consumed from the backlog — the ticket's
		// headline scenario, defeated by its own fix.
		const name = "reportUnrelated"
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

		docs := bothFormatDocs(2, "unrelated-")
		victim, bystander := docs[0], docs[1]

		mgr.NoteDeletedIDs(gt, name, []searchengine.ExternalID{victim.ID})
		mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})
		mgr.recordDirty(gt, name, false,
			reEmbeddedDocs([]searchengine.Document{victim}, "kept"),
			[]searchengine.SegmentID{"tail-kept"}, mgr.nextWriteSeq())

		// A LATER window deletes something else entirely, with the victim still
		// tombstoned.
		mgr.NoteDeletedIDs(gt, name, []searchengine.ExternalID{bystander.ID})
		mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID, bystander.ID})

		require.Equal(t, []searchengine.ExternalID{victim.ID},
			mgr.TombstonedPendingWriteIDs(gt, name),
			"an unrelated id's delete must not suppress this id's re-creation")
	})

	t.Run("write_begun_before_the_delete_is_not_reported", func(t *testing.T) {
		// THE SEAL-RACE CATCHER for the sequence allocation site. The write entry points
		// allocate BEFORE they seal and record afterwards, so a delete can land in the
		// middle. Driven directly here rather than with goroutines, so the interleaving
		// is exact rather than hoped for.
		const name = "reportSealRace"
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

		docs := bothFormatDocs(2, "sealrace-")
		victim := docs[0]

		seq := mgr.nextWriteSeq() // the write BEGINS
		mgr.NoteDeletedIDs(gt, name, []searchengine.ExternalID{victim.ID})
		mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})
		// ...and only now does the seal finish and the entry get recorded, under the
		// sequence the write began with.
		mgr.recordDirty(gt, name, false,
			[]searchengine.Document{victim}, []searchengine.SegmentID{"tail-race"}, seq)

		require.Empty(t, mgr.TombstonedPendingWriteIDs(gt, name),
			"a document written BEFORE the delete must not be reported as a re-creation")
	})
}

// TestDrainKeepsRecreatedNodeAfterUntombstone is the behavioral twin of
// TestDrainSkipsTombstonedBacklogDoc: that one proves the drain's filter DROPS a
// genuinely-deleted document, this proves it KEEPS one whose tombstone the caller
// cleared first. A fix that simply stopped filtering would pass this and fail its twin.
func TestDrainKeepsRecreatedNodeAfterUntombstone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt := kgtypes.GraphKnowledge

	t.Run("record_and_engine_agree", func(t *testing.T) {
		const name = "recreateAgree"
		dir := t.TempDir()

		mgr := closeOnCleanup(t, NewManager(dir, 0))

		docs := bothFormatDocs(deleteFixtureN, "recreate-")
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
		require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

		victim := docs[0]
		require.True(t, residentInFreshEngine(t, ctx, dir, gt, name, victim),
			"PRECONDITION: the node must be in the shipped corpus before the delete")

		const seededWatermark = int64(555000111)
		require.NoError(t, mgr.SaveRebuildState(gt, name, seededWatermark,
			[]searchengine.ExternalID{victim.ID}))
		mgr.NoteDeletedIDs(gt, name, []searchengine.ExternalID{victim.ID})
		mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})
		require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))
		require.False(t, residentInFreshEngine(t, ctx, dir, gt, name, victim),
			"PRECONDITION: the delete must have removed it from the shipped corpus")

		recreated := reEmbeddedDocs([]searchengine.Document{victim}, "recreated")
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, recreated))
		require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, recreated))

		ids := mgr.TombstonedPendingWriteIDs(gt, name)
		require.Equal(t, []searchengine.ExternalID{victim.ID}, ids)

		cleared, err := tools.UntombstoneWrittenIDs(toolsShipperAdapter{mgr}, gt, name, ids)
		require.NoError(t, err)
		require.Equal(t, 1, cleared)

		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

		require.True(t, residentInFreshEngine(t, ctx, dir, gt, name, recreated[0]),
			"the re-created document must survive the drain and reach the shipped corpus")
		require.True(t, residentInFreshEngine(t, ctx, dir, gt, name, docs[1]),
			"CONTROL: an untouched neighbor is still shipped")
	})

	t.Run("engine_superset_of_record_is_converged", func(t *testing.T) {
		// THE CATCHER for the divergence a rebuild leaves: the engines are seeded with
		// the full carried union while the finalize persists only the retained subset, so
		// ENGINE can strictly exceed RECORD. An untombstone that returned early on "no
		// record intersection" would leave the engines still filtering the victim, and
		// this fails.
		const name = "recreateSuperset"
		dir := t.TempDir()

		mgr := closeOnCleanup(t, NewManager(dir, 0))

		docs := bothFormatDocs(deleteFixtureN, "superset-")
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
		require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

		victim, extra := docs[0], docs[1]

		require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))

		// ENGINE holds both; RECORD holds only `extra`. That asymmetry is exactly what a
		// rebuild leaves behind — the driver seeds the engines with the full carried
		// union while the finalize persists only the retained subset — so it is
		// established HERE, after the delete, rather than before it. A delete now seals
		// its own ids into the record (manager_bucket_delete_seal.go), so building the
		// divergence first and then deleting through it would have the delete repair the
		// very asymmetry this leg exists to drive.
		require.NoError(t, mgr.SaveRebuildState(gt, name, 42, []searchengine.ExternalID{extra.ID}))
		mgr.NoteDeletedIDs(gt, name, []searchengine.ExternalID{victim.ID, extra.ID})
		mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID, extra.ID})

		recreated := reEmbeddedDocs([]searchengine.Document{victim}, "superset-again")
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, recreated))
		require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, recreated))

		ids := mgr.TombstonedPendingWriteIDs(gt, name)
		require.Equal(t, []searchengine.ExternalID{victim.ID}, ids)

		cleared, err := tools.UntombstoneWrittenIDs(toolsShipperAdapter{mgr}, gt, name, ids)
		require.NoError(t, err)
		require.Equal(t, 0, cleared,
			"the record never held the victim, so nothing leaves it — the engines are what must converge")

		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

		require.True(t, residentInFreshEngine(t, ctx, dir, gt, name, recreated[0]),
			"the re-created document must reach the shipped corpus even with no record intersection")
	})
}

// TestDeleteRecreateLifecycleStaysCoherent crosses every mechanism the delete/re-create
// lifecycle depends on in ONE run: the retained tombstone reaching the client, the
// drain's filter, the record clear, the engine re-seed, the identity-keyed backlog
// consume, and the record write itself. The individual tests above each gate one of
// those; this is what proves they compose.
//
// It is NOT also the duplicate-seal catcher, and should not be read as broader than it
// is. Forcing content-hash aliasing at the manager level is not reliably reachable —
// after the drain retires the tail and rebuilds the partition, a re-added batch does
// not reproduce a resident segment's bytes. That arm is covered at the engine level;
// this covers the record-and-backlog arm.
func TestDeleteRecreateLifecycleStaysCoherent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const name = "lifecycleCoherent"
	gt := kgtypes.GraphKnowledge

	dir := t.TempDir()

	// A real cache root, so the REAL persisted record file is exercised rather than an
	// in-memory stand-in.
	mgr := closeOnCleanup(t, NewManager(dir, 0))

	// 1. Write and drain a corpus on both formats.
	docs := bothFormatDocs(deleteFixtureN, "lifecycle-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	victim, neighbor := docs[0], docs[1]
	require.True(t, residentInFreshEngine(t, ctx, dir, gt, name, victim),
		"PRECONDITION: the node must be in the shipped corpus before the delete")

	// 2. Persist the delete the way the delta consumer does.
	const seededWatermark = int64(987654321000)
	require.NoError(t, mgr.SaveRebuildState(gt, name, seededWatermark,
		[]searchengine.ExternalID{victim.ID}))
	mgr.NoteDeletedIDs(gt, name, []searchengine.ExternalID{victim.ID})
	mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})
	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))
	require.False(t, residentInFreshEngine(t, ctx, dir, gt, name, victim),
		"PRECONDITION: the delete must have removed it from the shipped corpus")

	// 3. Re-create it. THE ORDER MATTERS AND MUST NOT BE REARRANGED: these writes are
	// issued AFTER step 2's stamp, so their backlog sequences exceed it and the reporter
	// can see them. A fixture that wrote first would report nothing and pass vacuously.
	recreated := reEmbeddedDocs([]searchengine.Document{victim}, "lifecycle-again")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, recreated))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, recreated))

	// 4. Run what the reconcile pass runs.
	ids := mgr.TombstonedPendingWriteIDs(gt, name)
	require.Equal(t, []searchengine.ExternalID{victim.ID}, ids)
	cleared, err := tools.UntombstoneWrittenIDs(toolsShipperAdapter{mgr}, gt, name, ids)
	require.NoError(t, err)
	require.Equal(t, 1, cleared)

	// 5. Drain.
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	// 6. The five claims the grouping exists to prove.
	require.True(t, residentInFreshEngine(t, ctx, dir, gt, name, recreated[0]),
		"the re-created document must be searchable and durable again")
	require.True(t, residentInFreshEngine(t, ctx, dir, gt, name, neighbor),
		"CONTROL: an untouched neighbor is still shipped")

	watermark, retained, lerr := mgr.LoadRebuildState(gt, name)
	require.NoError(t, lerr)
	require.NotContains(t, retained, victim.ID, "the record must no longer list the re-created id")
	require.Equal(t, seededWatermark, watermark,
		"no step of this lifecycle may advance the rebuild's durability watermark")

	hnswLeft, bm25Left := mgr.snapshotDirty(gt, name)
	require.Empty(t, hnswLeft.pending, "nothing may be stranded in the vector backlog")
	require.Empty(t, bm25Left.pending, "nothing may be stranded in the field backlog")
	require.Empty(t, hnswLeft.tails, "no vector tail may be stranded")
	require.Empty(t, bm25Left.tails, "no field tail may be stranded")
}
