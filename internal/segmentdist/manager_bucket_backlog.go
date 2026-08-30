// SPDX-License-Identifier: Apache-2.0

// manager_bucket_backlog.go — the WRITE ENTRY POINTS and the reconcile tick that
// drains the backlog: the calls that make documents searchable immediately and record
// them for a later partitioned re-emit, and the tick that rebuilds those partitions
// and writes L2 once per engine. The backlog itself — what an entry is, and how it is
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
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// sealPerPartition seals a write batch as ONE SEGMENT PER PARTITION it touches,
// rather than as one segment spanning every partition, and returns the ids of the
// segments this call created.
//
// THE INVARIANT IT ESTABLISHES, stated with its count: every segment a write path
// seals holds members of exactly one partition AT THE COUNT IT WAS SEALED UNDER. The
// unqualified form — no resident segment spans more than one partition — is FALSE, and
// searchengine/bucket.go:61-64 says so in its own words: a segment aligned to an older
// count "can therefore sit SEVERAL counts behind, spanning one partition per doubling
// it has missed". A later doubling therefore bounds one of these tails by DOUBLINGS
// CROSSED, which is the whole difference from a batch-wide seal, whose single segment
// spans every partition of the corpus regardless of any count change.
//
// WHY THAT MATTERS ENOUGH TO SPLIT THE SEAL: a delete landing while such a tail is
// still resident closes its constituency over every partition the tail spans, so
// removing one document rebuilds the whole corpus.
//
// THE "+ len(docs)" IS THE EXISTING IDIOM FOR THIS EXACT SITUATION rather than a new
// judgement — replaceBucket states it at manager_bucket.go:59-61, and
// replaceBucketFields repeats it: the incoming documents are not yet resident on this
// path, so the corpus they will form is the resident set plus them.
//
// IT OVER-COUNTS WHEN A BATCH CARRIES UPDATES TO ALREADY-RESIDENT IDS, AND OVER-COUNTING
// IS THE SAFE DIRECTION. This is the non-obvious half. Counts are powers of two and
// BucketOf is a modulo, so a partition under count 2N is a strict SUBSET of a partition
// under count N: a segment sealed at a count at or above the drain's count still
// occupies exactly one partition at the drain's count, while sealing BELOW it costs a
// span of drainCount/sealCount.
func sealPerPartition[Q, S any](
	dm *distManager[Q, S], docs []searchengine.Document,
) ([]searchengine.SegmentID, error) {
	bucketCount := searchengine.BucketCountFor(dm.engine.DistinctResidentDocCount() + len(docs))
	if bucketCount <= 1 {
		// The whole corpus is one partition, so the split has nothing to split and a
		// small graph pays none of the per-seal fixed cost.
		return sealOne(dm, docs)
	}

	byBucket := make(map[int][]searchengine.Document)
	for _, d := range docs {
		b := searchengine.BucketOf(d.ID, bucketCount)
		byBucket[b] = append(byBucket[b], d)
	}

	// SEAL THE PARTITIONS IN ASCENDING ORDER, because Go randomizes map range and the
	// append order reaches durable state. Export (searchengine/distribution.go:11-14)
	// walks the resident entries in order and emits the blob list in that order, and
	// that list is what persistResident makes durable — so an unsorted loop would write
	// a different L2 blob order on every run of the same batch.
	//
	// It is NOT the merge union that consumes this order: groupWorkInputs sorts each
	// partition's constituents by id and replaceBucketGroups walks partitions ascending,
	// so union order derives from sorted ids and partition number. Nor can
	// last-append-wins fire within one batch, because BucketOf partitions the batch's
	// ids DISJOINTLY and no id appears in two of these seals.
	buckets := make([]int, 0, len(byBucket))
	for b := range byBucket {
		buckets = append(buckets, b)
	}
	slices.Sort(buckets)

	// THE SEALS RUN SERIALLY AND DELIBERATELY SO. AddSealAndSupersede mutates shared
	// engine state under activeMu and ends in a CAS publish, so concurrent seals would
	// contend on that CAS and destroy the deterministic ordering established above.
	//
	// A MID-LOOP ERROR RETURNS IMMEDIATELY, leaving the earlier partitions' segments
	// resident and searchable but absent from the backlog, because recordDirty has not
	// run. Nothing is lost: those documents are live in their segments, and the caller
	// returns the error so the delta consumer retries the whole window next tick, where
	// the re-seal aliases the same content-hashed ids and records the documents. The
	// unrecorded segments are absorbed by the next drain that touches their partitions
	// as ORDINARY CONSTITUENTS — the disposition the drain already gives a retained
	// tail.
	var tails []searchengine.SegmentID
	for _, b := range buckets {
		sealed, err := sealOne(dm, byBucket[b])
		if err != nil {
			return nil, err
		}
		tails = append(tails, sealed...)
	}
	return tails, nil
}

// sealOne seals one group and reports its id only when the seal CREATED a segment.
//
// ONLY A SEGMENT THIS WRITE CREATED is recorded as a tail. A batch whose bytes
// reproduce a resident segment mints that segment's id without appending anything, and
// that segment was resident before this call: it is not this window's to retire, and
// recording it would put a foreign partition in front of the drain's Unload. The
// DOCUMENTS are recorded either way — they still owe a partitioned re-emit.
func sealOne[Q, S any](
	dm *distManager[Q, S], docs []searchengine.Document,
) ([]searchengine.SegmentID, error) {
	sealed, err := dm.engine.AddSealAndSupersede(docs)
	if err != nil {
		return nil, err
	}
	if !sealed.Created {
		return nil, nil
	}
	return []searchengine.SegmentID{sealed.ID}, nil
}

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
// THE SEAL IS PER PARTITION, through sealPerPartition rather than one seal over the
// whole batch, so every segment this call produces holds members of exactly one
// partition AT THE COUNT IT WAS SEALED UNDER. Hash distribution is why: a batch-wide
// seal produces ONE segment spanning every partition the batch touched, and a delete
// arriving before the drain closes its constituency over all of them — so removing a
// single document rebuilds the whole corpus. See sealPerPartition for why the
// qualified form is the true one and the unqualified one is not.
//
// VISIBILITY: a document written here is searchable IN THIS PROCESS as soon as the
// call returns, which is sooner than the previous path made it. It becomes
// DURABLE — written to the L2 cache, and therefore visible to another process or
// after a restart — at the next reconcile tick, bounded by
// segmentReconcileInterval, or sooner if the backlog crosses pendingReEmitByteCap.
// A WRITE RE-MATERIALIZES AN EVICTED POOL BEFORE SEALING INTO IT. Sealing a handful
// of documents into an emptied engine would leave the pool holding those documents
// ALONE: the drain writes the engine's resident export, so an emptied engine makes
// the resident set the whole of what is written, and the heal decider then reads
// that collapsed resident count against the graph's embedded corpus and drives a
// from-scratch rebuild. A written-to evicted pool would reach that state by a path
// nothing else in this ticket looks for.
//
// This is the SECOND of two belts and they close different windows: the budget pass
// skips a graph with a non-empty write backlog, which stops an eviction landing
// while writes are queued; this stops the damage from writes arriving AFTER one.
func (m *Manager) AddAndMarkDirty(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
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
	if dm.isEvicted() {
		if err := dm.load(ctx); err != nil {
			return err
		}
		slog.Info("segmentdist: a write re-materialized an evicted pool before sealing",
			"graph_type", gt, "name", name, "format", dm.armFormat(), "docs", len(docs))
	}
	tails, err := sealPerPartition(dm, docs)
	if err != nil {
		return err
	}
	// ONE SEQUENCE AND ONE recordDirty CALL PER BATCH, however many segments the seal
	// produced. recordDirty states "ONE SEQUENCE PER BATCH, shared by every entry this
	// call records", and consume matches its snapshot by sequence set — so a batch must
	// be appended atomically under mu or a snapshot can hold some of its entries and not
	// others.
	m.recordDirty(gt, name, false, docs, tails, seq)
	return nil
}

// AddAndMarkDirtyFields is the field-engine counterpart of AddAndMarkDirty, with
// the same no-merge, no-ship contract, the same visibility window, the same
// re-materialization of an evicted pool before the seal, and the same PER-PARTITION
// seal — over the BM25 pool, which is this entry point's own and is the larger of the
// two. The per-partition seal matters most here, and the reason is now stronger than
// the cost share it used to rest on: the field leg is the ONLY leg a delete still
// re-emits inline, so it is this pool's tail whose span decides what a delete pays. A
// tail spanning many partitions closes a delete's constituency over all of them, and
// there is no longer a second inline leg for that cost to be measured against.
func (m *Manager) AddAndMarkDirtyFields(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
	if len(docs) == 0 {
		return nil
	}
	// Before the seal, for the reason AddAndMarkDirty states.
	seq := m.nextWriteSeq()
	dm := m.bm25ManagerFor(gt, name)
	if dm.isEvicted() {
		if err := dm.load(ctx); err != nil {
			return err
		}
		slog.Info("segmentdist: a write re-materialized an evicted pool before sealing",
			"graph_type", gt, "name", name, "format", dm.armFormat(), "docs", len(docs))
	}
	tails, err := sealPerPartition(dm, docs)
	if err != nil {
		return err
	}
	// One sequence and one recordDirty call per batch, for the reason AddAndMarkDirty
	// states.
	m.recordDirty(gt, name, true, docs, tails, seq)
	return nil
}

// ReEmitDirtyBuckets drains one graph's backlog: it rebuilds every partition the
// window's writes touched, retires the tail segments those writes produced, and
// writes L2 once per engine.
//
// The order is load-bearing. The re-emit supersedes the tail copies FIRST, so they
// are already dead when the rebuild reads live data, and the freshly rebuilt
// partition carries the surviving content. Only then are the spent tails dropped —
// and a tail counts as spent only when every live member it still holds was carried
// by this rebuild, because a tail is excluded from the rebuild's constituency and is
// therefore the last searchable home of anything the rebuild did not receive.
//
// THE BACKLOG AND THE OUTSTANDING L2 WRITE ARE TWO SEPARATE OBLIGATIONS, and only
// the first is what this backlog represents. It is cleared once the rebuild and the
// L2 write both complete without error. The second obligation is carried by the
// unwritten-export question the head of this method asks the cache directly — is
// anything resident missing from L2 — and never by the backlog. Keeping the
// documents around for it would be a category error: the write is a DIFF OVER THE
// WHOLE RESIDENT SET rather than a delta of one window, so a later write persists
// whatever is resident — which already contains these buckets — and needs no record
// of which documents happened to arrive in this window. An ERROR is different and
// does retain, which is safe rather than wasteful because the consolidation
// converges: repeating it produces the same bytes, so the write adds only what did
// not already land, and the same live set stands.
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
	// Fail closed on an in-session account switch, for the same reason Flush
	// does: this manager's sources belong to the account it was built under.
	if err := m.checkAccountBinding(ctx); err != nil {
		return err
	}
	hnswSnap, bm25Snap := m.snapshotDirty(gt, name)

	// Read the tombstone set ONCE for both formats — one lock acquisition, and one
	// source of truth shared with import seeding. A document deleted since it was
	// queued must not be rebuilt out of the backlog, but it must still be CONSUMED
	// from it, which is why only the drain sees the filtered snapshots and the clear
	// below sees the originals.
	tombstoned := m.graphTombstones(gt, name)
	// THE DEFERRED RE-EMIT'S OFFER, read from the mask the line above just hydrated. A
	// delete kills its live bits and seals the mask synchronously; the partitions those
	// ids route to are rebuilt here, a bounded number per tick. The selector declines
	// entirely unless both pools hold a corpus — see deferredReEmitIDs.
	deferred := m.deferredReEmitIDs(gt, name)
	hnswDrain := hnswSnap.withoutTombstoned(tombstoned)
	bm25Drain := bm25Snap.withoutTombstoned(tombstoned)
	// THE DEFERRED SET FIRES THE FLAGS TOO, and without that the backstop never runs on
	// the graph it exists for: one whose writes have stopped and whose only outstanding
	// work is a mask nothing else will discharge.
	hnswWork := len(hnswDrain.pending) > 0 || len(hnswDrain.tails) > 0 || len(deferred) > 0
	bm25Work := len(bm25Drain.pending) > 0 || len(bm25Drain.tails) > 0 || len(deferred) > 0

	// WHAT THERE IS TO BUILD AND WHAT THERE IS TO CONSUME ARE DIFFERENT QUESTIONS,
	// and the work flags above answer only the first. A window whose pending filters
	// down to nothing and that recorded no tail has nothing to rebuild but still
	// holds queued entries; returning here would leave them queued forever, their
	// bytes charging pendingReEmitByteCap and re-triggering this tick on every pass.
	// The UNFILTERED snapshots are what says whether anything is queued at all.
	queued := len(hnswSnap.pending) > 0 || len(hnswSnap.tails) > 0 ||
		len(bm25Snap.pending) > 0 || len(bm25Snap.tails) > 0

	// A tick with nothing new to re-emit still has a job when a previous pass left
	// resident segments unwritten: nothing else will retry the L2 write, so a graph
	// that goes quiet after a failed write would keep its new segments in memory only.
	//
	// THIS USED TO BE A PUBLISH-RETRY BIT, latched when a manifest swap did not land.
	// The bit is gone with the publish; the surviving question is directly observable
	// from the cache — is anything resident missing from L2 — so it is asked rather
	// than remembered.
	hnswDM := m.managerFor(gt, name)
	bm25DM := m.bm25ManagerFor(gt, name)
	hnswRetry := hnswDM.hasUnwrittenExport()
	bm25Retry := bm25DM.hasUnwrittenExport()
	if !hnswWork && !bm25Work && !hnswRetry && !bm25Retry && !queued {
		return nil
	}

	var (
		hnswPublished, bm25Published map[int]bool
		hnswCount, bm25Count         int
	)
	if hnswWork || hnswRetry {
		var err error
		if hnswPublished, hnswCount, err = drainFormat(hnswDM, hnswDrain, tombstoned, deferred, hnswWork); err != nil {
			return err
		}
	}
	if bm25Work || bm25Retry {
		var err error
		if bm25Published, bm25Count, err = drainFormat(bm25DM, bm25Drain, tombstoned, deferred, bm25Work); err != nil {
			return err
		}
	}

	// THE TRIM RUNS ONLY BEHIND BOTH PERSISTS, and only on what was PUBLISHED. Each
	// drainFormat ends in persistResident and returns its error, so reaching this line
	// means both formats' blobs are durable; trimming earlier would advance a durable
	// position ahead of the persist that position describes. A crash between the
	// persists and the trim leaves the ids masked, which costs one redundant re-emit
	// next tick and never a lost mask entry.
	if err := m.trimReEmittedTombstones(
		gt, name, deferred, hnswPublished, hnswCount, bm25Published, bm25Count); err != nil {
		return err
	}

	// The rebuild and the L2 write both landed, so the re-emit obligation is
	// discharged and the backlog goes — see the method doc. The UNFILTERED snapshots
	// are what is consumed: a document the filter dropped from the build is still
	// finished with, and leaving it queued would re-trigger a drain on every tick
	// forever.
	m.clearDirty(gt, name, hnswSnap, bm25Snap)
	return nil
}

// drainFormat re-emits one engine's dirty partitions and makes the result durable.
//
// The L2 write runs even when there is nothing to re-emit: a previous pass may have
// failed its write, leaving resident segments with no durable copy, and nothing else
// retries it.
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
// IT REPORTS WHAT IT PUBLISHED AND THE COUNT IT PUBLISHED UNDER, and the pair travels
// together because neither is meaningful without the other: a partition number is only
// interpretable under the count it was derived at, and the two formats derive their
// counts independently. A caller comparing a raw partition int from one format against
// one from the other would be making exactly the mistake this ticket exists to fix.
//
// THE DEFERRED IDS ARE OFFERED TO BOTH FORMATS, and the BM25 half is not redundant even
// though a delete re-emits that format inline. The mask is per-GRAPH and has routes that
// re-emit NEITHER format — the rebuild driver seeds it from its own scan, and the delta
// consumer's already-known branch returns before any re-emit — so an id can enter the
// mask with the field blob still carrying it. Beyond that, the trim is only sound when
// BOTH formats' blobs have dropped the id, so the drain must hold PUBLISHED evidence for
// both; offering only the vector format would leave half the predicate unwitnessed while
// the trim asserted it anyway.
func drainFormat[Q, S any](
	dm *distManager[Q, S], snap formatDirtyState,
	tombstoned, deferred []searchengine.ExternalID, work bool,
) (map[int]bool, int, error) {
	publishedBuckets := map[int]bool{}
	bucketCount := 0
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
		// docIDs allocates a fresh slice, so the deferred ids are appended onto it
		// rather than aliasing anything the caller holds. They ride the SUPERSEDED
		// argument because that is what dirties a partition: the ids the mask still
		// owes a re-emit are exactly the ids whose partitions must be rebuilt without
		// them.
		ids := append(docIDs(docs), deferred...)
		// One read of the corpus count, used for BOTH the call and the count reported
		// back: replaceBucketGroups derives its partition count from this same value by
		// the same pure function, so a caller told this count is told the count the
		// publish below actually ran under.
		corpusDocs := dm.engine.DistinctResidentDocCount()
		bucketCount = searchengine.BucketCountFor(corpusDocs)
		published, publishedBy, err := replaceBucketGroups(
			dm, ids, docs, snap.tails, corpusDocs, nil)
		if err != nil {
			return nil, 0, err
		}
		for b := range publishedBy {
			publishedBuckets[b] = true
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
	_, err := dm.persistResident()
	return publishedBuckets, bucketCount, err
}
