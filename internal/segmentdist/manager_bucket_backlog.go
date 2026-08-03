// SPDX-License-Identifier: Apache-2.0

// manager_bucket_backlog.go — the WRITE ENTRY POINTS and the reconcile tick that
// drains the backlog: the calls that make documents searchable immediately and record
// them for a later partitioned re-emit, and the tick that rebuilds those partitions
// and ships once per engine. The backlog itself — what an entry is, and how it is
// recorded, purged, snapshotted and consumed — lives in
// manager_bucket_backlog_state.go.
//
// Deferral is the whole design, not an optimisation: ids are hash distributed, so a
// batch of about a hundred documents touches over half the partitions of a large
// corpus, and re-emitting those on every write costs more CPU than the interval
// between batches provides. Everything here exists to collapse any number of writes
// in a window into at most one rebuild per dirtied partition.

package segmentdist

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// AddAndMarkDirty makes freshly written documents searchable immediately and
// records them for a later partitioned re-emit. It does NOT merge and does NOT
// ship — that is the whole point.
//
// Re-emitting on every write would be unaffordable rather than merely slow: ids are
// hash distributed, so a batch of roughly one hundred documents touches over half
// the partitions of a large corpus, and rebuilding those on every batch costs more
// CPU than the interval between batches provides. Deferring to the reconcile tick
// collapses any number of writes in a window into at most one rebuild per dirtied
// partition.
//
// VISIBILITY: a document written here is searchable IN THIS PROCESS as soon as the
// call returns, which is sooner than the previous path made it. It becomes
// DURABLE — shipped, published, and therefore visible to another process or after
// a restart — at the next reconcile tick, bounded by segmentReconcileInterval, or
// sooner if the backlog crosses pendingReEmitByteCap.
func (m *Manager) AddAndMarkDirty(_ context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
	if len(docs) == 0 {
		return nil
	}
	// THE SEQUENCE IS ALLOCATED BEFORE THE SEAL, and the order is load-bearing. It
	// must upper-bound WHEN THIS WRITE BEGAN, not when it finished being recorded: the
	// seal below runs outside mu, so a delete can land between it and the record, and
	// a sequence issued after that delete's stamp would make this pre-delete document
	// compare as later than the delete — resurrecting it.
	seq := m.nextWriteSeq()
	dm := m.managerFor(gt, name)
	sealed, err := dm.engine.AddSealAndSupersede(docs)
	if err != nil {
		return err
	}
	// ONLY A SEGMENT THIS WRITE CREATED is recorded as a tail. A batch whose bytes
	// reproduce a resident segment mints that segment's id without appending
	// anything, and that segment was resident before this call: it is not this
	// window's to retire, and recording it would put a foreign partition in front of
	// the drain's Unload. The DOCUMENTS are recorded either way — they still owe a
	// partitioned re-emit.
	var tails []searchengine.SegmentID
	if sealed.Created {
		tails = []searchengine.SegmentID{sealed.ID}
	}
	m.recordDirty(gt, name, false, docs, tails, seq)
	return nil
}

// AddAndMarkDirtyFields is the field-engine counterpart of AddAndMarkDirty, with
// the same no-merge, no-ship contract and the same visibility window.
func (m *Manager) AddAndMarkDirtyFields(_ context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
	if len(docs) == 0 {
		return nil
	}
	// Before the seal, for the reason AddAndMarkDirty states.
	seq := m.nextWriteSeq()
	dm := m.bm25ManagerFor(gt, name)
	sealed, err := dm.engine.AddSealAndSupersede(docs)
	if err != nil {
		return err
	}
	// Created-only, for the reason AddAndMarkDirty states.
	var tails []searchengine.SegmentID
	if sealed.Created {
		tails = []searchengine.SegmentID{sealed.ID}
	}
	m.recordDirty(gt, name, true, docs, tails, seq)
	return nil
}

// ReEmitDirtyBuckets drains one graph's backlog: it rebuilds every partition the
// window's writes touched, retires the tail segments those writes produced, and
// ships once per engine.
//
// The order is load-bearing. The re-emit supersedes the tail copies FIRST, so they
// are already dead when the rebuild reads live data, and the freshly rebuilt
// partition carries the surviving content. Only then are the spent tails dropped —
// and a tail counts as spent only when every live member it still holds was carried
// by this rebuild, because a tail is excluded from the rebuild's constituency and is
// therefore the last searchable home of anything the rebuild did not receive.
//
// THE BACKLOG AND THE OUTSTANDING PUBLISH ARE TWO SEPARATE OBLIGATIONS, and only
// the first is what this backlog represents. It is cleared once the rebuild and
// ship complete without error, WHATEVER the publish did. A skipped publish leaves
// the manifest swap outstanding, and that is carried by the publish-retry bit
// alone, which the head of this method already consults. Keeping the documents
// around for it would be a category error: the manifest is a full snapshot of the
// live set rather than a delta, so a later publish republishes whatever is
// resident — which already contains these buckets — and needs no record of which
// documents happened to arrive in this window. An ERROR is different and does
// retain, which is safe rather than wasteful because the consolidation converges:
// repeating it produces the same bytes, ships an empty diff, and lands the same
// live set.
//
// Across a full cycle exactly one live copy of a rewritten document exists at
// every instant, and WHICH copy dies differs by moment: at write time the
// partition copy dies and the fresh tail copy lives; here the tail copy dies and
// the rebuilt partition copy is published.
//
// A WINDOW WITH NOTHING TO BUILD STILL REACHES THE CONSUME. The drain decides what
// to REBUILD from the tombstone-filtered snapshots and what to CONSUME from the
// unfiltered ones, so a window whose every pending document was filtered out — and
// which recorded no tail — finishes with its entries discharged rather than queued
// forever against the byte cap.
func (m *Manager) ReEmitDirtyBuckets(ctx context.Context, gt kgtypes.GraphType, name string) error {
	hnswSnap, bm25Snap := m.snapshotDirty(gt, name)

	// Read the tombstone set ONCE for both formats — one lock acquisition, and one
	// source of truth shared with import seeding. A document deleted since it was
	// queued must not be rebuilt out of the backlog, but it must still be CONSUMED
	// from it, which is why only the drain sees the filtered snapshots and the clear
	// below sees the originals.
	tombstoned := m.graphTombstones(gt, name)
	hnswDrain := hnswSnap.withoutTombstoned(tombstoned)
	bm25Drain := bm25Snap.withoutTombstoned(tombstoned)
	hnswWork := len(hnswDrain.pending) > 0 || len(hnswDrain.tails) > 0
	bm25Work := len(bm25Drain.pending) > 0 || len(bm25Drain.tails) > 0

	// WHAT THERE IS TO BUILD AND WHAT THERE IS TO CONSUME ARE DIFFERENT QUESTIONS,
	// and the work flags above answer only the first. A window whose pending filters
	// down to nothing and that recorded no tail has nothing to rebuild but still
	// holds queued entries; returning here would leave them queued forever, their
	// bytes charging pendingReEmitByteCap and re-triggering this tick on every pass.
	// The UNFILTERED snapshots are what says whether anything is queued at all.
	queued := len(hnswSnap.pending) > 0 || len(hnswSnap.tails) > 0 ||
		len(bm25Snap.pending) > 0 || len(bm25Snap.tails) > 0

	// A tick with nothing new to re-emit still has a job when a previous publish
	// did not land: the content is already shipped, only the manifest swap is
	// outstanding, and nothing else will retry it. Without this, a graph that goes
	// quiet after a skipped publish keeps its new segments unreferenced while the
	// manifest still names the retired ones.
	hnswDM := m.managerFor(gt, name)
	bm25DM := m.bm25ManagerFor(gt, name)
	hnswRetry := hnswDM.publishRetryPending()
	bm25Retry := bm25DM.publishRetryPending()
	if !hnswWork && !bm25Work && !hnswRetry && !bm25Retry && !queued {
		return nil
	}

	if hnswWork || hnswRetry {
		if err := drainFormat(ctx, hnswDM, hnswDrain, tombstoned, hnswWork); err != nil {
			return err
		}
	}
	if bm25Work || bm25Retry {
		if err := drainFormat(ctx, bm25DM, bm25Drain, tombstoned, bm25Work); err != nil {
			return err
		}
	}

	// The rebuild and ship both landed, so the re-emit obligation is discharged and
	// the backlog goes regardless of what the publish did — see the method doc. The
	// UNFILTERED snapshots are what is consumed: a document the filter dropped from
	// the build is still finished with, and leaving it queued would re-trigger a
	// drain on every tick forever.
	m.clearDirty(gt, name, hnswSnap, bm25Snap)
	return nil
}

// drainFormat re-emits one engine's dirty partitions and publishes the result.
//
// The publish runs even when there is nothing to re-emit: a previous publish may
// have been skipped, leaving the content shipped but the manifest still naming the
// segments the re-emit retired, and nothing else retries it.
//
// THE RETIRE IS GATED ON MEMBER COVERAGE. The window's tails are excluded from the
// rebuild's constituency, so a tail's member survives only when this drain carried
// its document; a tail still holding a live member outside that set is KEPT resident
// instead of unloaded, and the retention is logged. Retaining costs a duplicate copy
// at worst; unloading such a tail destroys the member outright.
//
// The tombstoned ids are passed in because they belong on the covered side of that
// question: the drain drops them from the build deliberately, and a tail held
// resident for one of them would put a deleted document back into search.
func drainFormat[Q, S any](
	ctx context.Context, dm *distManager[Q, S], snap formatDirtyState,
	tombstoned []searchengine.ExternalID, work bool,
) error {
	if work {
		// The window's documents are ALREADY resident — the write path sealed them
		// before this drain — so the corpus is the resident count alone. Adding the
		// window again would derive a count for twice the corpus that exists, which
		// is wrong on the very first tick of a graph's life, not merely near a
		// boundary.
		//
		// DISTINCT, NOT SUMMED. A document resident in more than one segment is what
		// two rebuilds landing without the first being retired leave behind, and a
		// summed count counts it once per segment. Deriving the partition count from
		// that inflated reading manufactures a crossing the real corpus never made,
		// which is precisely what puts segments spanning several partitions in front
		// of the swap.
		docs := pendingDocuments(snap.pending)
		ids := docIDs(docs)
		published, err := replaceBucketGroups(
			dm, ids, docs, snap.tails, dm.engine.DistinctResidentDocCount())
		if err != nil {
			return err
		}

		// A tail member that is neither carried by this rebuild nor tombstoned has no
		// other searchable home, so the tail holding it must stay.
		uncovered := dm.engine.LiveMembersOutside(snap.tails, coveredSet(ids, tombstoned))
		retire, retained := retiring(snap.tails, published, uncovered)
		dm.engine.Unload(retire)
		if len(retained) > 0 {
			// THIS LINE IS PART OF THE FIX, not decoration. Which write route leaves a
			// live member in a tail this drain does not rebuild is not yet established,
			// so the next live occurrence has to arrive as evidence rather than as a
			// silent drop in the resident count.
			members := 0
			for _, id := range retained {
				members += uncovered[id]
			}
			slog.Warn("segmentdist: drain KEPT tail segments it could not prove spent — nothing was lost",
				"graph", dm.target.GetGraph(), "name", dm.target.GetName(), "repo", dm.target.GetRepo(),
				"format", dm.format, "retained_segments", len(retained),
				"uncovered_live_members", members)
		}
	}
	_, err := dm.shipAndPublish(ctx, dm.locallyShipped)
	return err
}
