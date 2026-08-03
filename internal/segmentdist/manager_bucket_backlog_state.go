// SPDX-License-Identifier: Apache-2.0

// manager_bucket_backlog_state.go — the per-graph write backlog itself: what an entry
// IS, and how it is recorded, purged, snapshotted and consumed. The write entry points
// that feed it and the reconcile tick that drains it live in manager_bucket_backlog.go.
//
// Split out of that file when the entry grew a sequence: the two concerns read
// differently — one is the queue's own accounting, the other is the deferral policy
// built on top of it — and the combined file was approaching the size where neither
// could be read without the other.

package segmentdist

import (
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// pendingReEmitByteCap bounds the re-emit backlog by the SIZE of the accumulated
// document text, and it is an emergency valve rather than a batching knob.
//
// An early re-emit is expensive in a way a doc count hides. Ids are hash
// distributed, so a backlog of N documents over B buckets touches
// B*(1-(1-1/B)^N) distinct buckets, which reaches essentially ALL of them once N
// approaches B: a backlog the size of one segment already dirties every bucket, so
// every cap trigger is a FULL corpus re-emit. Sizing the cap by bytes keeps that
// rare — tens of MB is many windows' worth of accumulation — while still bounding
// memory if a reconcile tick stops arriving.
const pendingReEmitByteCap = 64 << 20

// pendingDoc is one queued write plus the SEQUENCE the backlog stamped it with, and
// the sequence is what makes a queued write IDENTIFIABLE.
//
// Consuming by id cannot tell two entries for one id apart, and the two are not
// interchangeable. A delete purges an entry out of the MIDDLE of the queue while a
// tick is in flight, and a write that arrives afterwards carrying the SAME id — a
// node deleted and immediately re-created — is then eaten by the budget the tick's
// snapshot allocated for the copy the purge already removed. Its document is dropped
// while its tail id survives, so the next drain meets a tail holding a live member it
// was never handed. The retire is gated on member coverage, so that tail is KEPT and
// the document stays searchable — but it is kept resident until some later drain
// absorbs it, and the write it carried is missing from every rebuilt partition.
// Sequencing is what stops the state arising at all.
//
// TAILS ARE NOT SEQUENCED, deliberately. A tail id is a content hash, so two entries
// sharing one are byte-identical and interchangeable; the multiset budget over tails
// is sound as it stands and stays.
type pendingDoc struct {
	doc searchengine.Document
	seq uint64
}

// graphDirtyState is one graph's re-emit backlog, split by format because the two
// engines seal and publish independently.
type graphDirtyState struct {
	hnsw formatDirtyState
	bm25 formatDirtyState
}

// formatDirtyState is the backlog for a single engine: the documents awaiting a
// bucket re-emit, the ids of the tail segments the drains sealed them into, and
// the accumulated size used against pendingReEmitByteCap.
//
// The dirty BUCKETS are deliberately not stored. They are derived from the pending
// documents when the tick runs, under the bucket count in force AT THAT MOMENT —
// a count recorded at drain time could be stale by the tick if the corpus crossed
// a power-of-two boundary in between, and stale bucket numbers would re-emit the
// wrong partitions.
type formatDirtyState struct {
	pending []pendingDoc
	tails   []searchengine.SegmentID
	bytes   int
}

// recordDirty appends one drain's documents and sealed tail ids to the graph's
// backlog, stamping every document with the sequence the CALLER already allocated.
// When the backlog crosses the byte cap it flags the graph for an earlier reconcile
// rather than re-emitting inline — the caller is a write path and must not block on
// a rebuild.
//
// IT DOES NOT ALLOCATE THE SEQUENCE, and that is a correctness requirement rather
// than a division of labor. The write entry points seal OUTSIDE mu and call this
// afterwards, so a delete can land in between; a sequence issued here would be above
// that delete's stamp and would make a document written BEFORE the delete compare as
// written after it. The entry points allocate before the seal for exactly that
// reason.
//
// ONE SEQUENCE PER BATCH, shared by every entry this call records. That is what the
// consume needs: a batch is appended atomically under mu, so a snapshot holds all of
// its entries or none, and a sequence-keyed budget drops exactly the ones it built.
func (m *Manager) recordDirty(
	gt kgtypes.GraphType, name string, fields bool,
	docs []searchengine.Document, sealed []searchengine.SegmentID, seq uint64,
) {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	st, ok := m.dirty[k]
	if !ok {
		st = &graphDirtyState{}
		m.dirty[k] = st
	}
	fs := &st.hnsw
	if fields {
		fs = &st.bm25
	}
	for _, d := range docs {
		fs.pending = append(fs.pending, pendingDoc{doc: d, seq: seq})
		fs.bytes += documentBytes(d)
	}
	fs.tails = append(fs.tails, sealed...)
	over := fs.bytes >= pendingReEmitByteCap
	m.mu.Unlock()

	if over {
		m.flagReconcileNudge(gt, name)
	}
}

// documentBytes is what one backlog entry charges against pendingReEmitByteCap: its
// vector plus every field value. It has ONE definition because three sites charge
// and discharge against the same total — the write path adding, the purge dropping,
// and the tick consuming — and a total that drifts moves the cap trigger.
func documentBytes(d searchengine.Document) int {
	n := len(d.Vector)
	for _, v := range d.Fields {
		n += len(v)
	}
	return n
}

// pendingDocuments strips the sequences back off, for the drain that wants the
// documents alone. The sequence is the backlog's own accounting and means nothing to
// the bucket rebuild below it.
func pendingDocuments(entries []pendingDoc) []searchengine.Document {
	if len(entries) == 0 {
		return nil
	}
	out := make([]searchengine.Document, len(entries))
	for i, e := range entries {
		out[i] = e.doc
	}
	return out
}

// purgeDirty removes the named ids from the graph's pending backlog on BOTH formats,
// so a delete's documents cannot be rebuilt out of a queue the delete never touched.
//
// It deliberately does NOT touch the tails. A tail segment carries live documents
// alongside the deleted one, so dropping it by name would lose them; the drain
// retires a tail only once it has proved every live member it still holds was
// carried by the rebuild.
func (m *Manager) purgeDirty(gt kgtypes.GraphType, name string, ids []searchengine.ExternalID) {
	if len(ids) == 0 {
		return
	}
	dead := make(map[searchengine.ExternalID]bool, len(ids))
	for _, id := range ids {
		dead[id] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.dirty[graphKey{graphType: gt, graphName: name}]
	if !ok {
		return
	}
	st.hnsw.purge(dead)
	st.bm25.purge(dead)
}

// purge drops every pending document whose id is dead, discharging its bytes from
// the running total.
func (f *formatDirtyState) purge(dead map[searchengine.ExternalID]bool) {
	if len(dead) == 0 || len(f.pending) == 0 {
		return
	}
	out := make([]pendingDoc, 0, len(f.pending))
	for _, e := range f.pending {
		if dead[e.doc.ID] {
			f.bytes -= documentBytes(e.doc)
			continue
		}
		out = append(out, e)
	}
	f.pending = out
	if f.bytes < 0 {
		f.bytes = 0
	}
}

// TombstonedPendingWriteIDs reports the ids that are BOTH listed in this graph's
// tombstone set AND sitting in its write backlog under a sequence issued AFTER that
// id's own delete was last reported — the deleted-then-re-created ids, and nothing
// else. IT REPORTS AND MUTATES NOTHING; the caller decides what to do with the answer.
//
// THE PER-ID SEQUENCE COMPARISON IS WHAT MAKES THE ANSWER SOUND, not a census of which
// routes purge the backlog and not a per-graph watermark. The routes do not agree:
// SetGraphTombstones has three production call sites and purgeDirty has one, so the
// delta consumer's already-known branch and the rebuild driver both tombstone an id
// while leaving the backlog untouched. Those routes are deliberately NOT patched to
// purge — purging at the rebuild driver's seeding would drop legitimate re-creations
// queued for merely-carried ids, data loss in the opposite direction. The fact is
// CHECKED here instead. And a per-graph watermark would fail the other way: a window
// that deleted an unrelated id would suppress every queued re-creation in the graph.
//
// BOTH FORMAT BACKLOGS ARE WALKED. A write can land on one engine and fail on the
// other, because the field-engine call is best-effort at its call site.
func (m *Manager) TombstonedPendingWriteIDs(gt kgtypes.GraphType, name string) []searchengine.ExternalID {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()

	tombstoned := m.tombstoned[k]
	if len(tombstoned) == 0 {
		return nil
	}
	st, ok := m.dirty[k]
	if !ok {
		return nil
	}
	dead := make(map[searchengine.ExternalID]struct{}, len(tombstoned))
	for _, id := range tombstoned {
		dead[id] = struct{}{}
	}
	stamps := m.tombstoneSeq[k]

	var out []searchengine.ExternalID
	reported := make(map[searchengine.ExternalID]struct{})
	collect := func(entries []pendingDoc) {
		for _, e := range entries {
			if _, isDead := dead[e.doc.ID]; !isDead {
				continue
			}
			// STRICTLY GREATER: an entry issued at exactly the stamped value began
			// before the stamp. A missing stamp yields the zero value, so a first-ever
			// delete never suppresses a write.
			if e.seq <= stamps[e.doc.ID] {
				continue
			}
			if _, dup := reported[e.doc.ID]; dup {
				continue
			}
			reported[e.doc.ID] = struct{}{}
			out = append(out, e.doc.ID)
		}
	}
	collect(st.hnsw.pending)
	collect(st.bm25.pending)
	return out
}

// snapshotDirty copies the current backlog without clearing it, so a tick that
// fails leaves the work queued.
func (m *Manager) snapshotDirty(gt kgtypes.GraphType, name string) (hnswSnap, bm25Snap formatDirtyState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.dirty[graphKey{graphType: gt, graphName: name}]
	if !ok {
		return formatDirtyState{}, formatDirtyState{}
	}
	return st.hnsw.snapshot(), st.bm25.snapshot()
}

// withoutTombstoned returns the snapshot with every pending document whose id the
// graph has tombstoned removed, so a drain cannot rebuild a document that is already
// deleted. The receiver is returned unchanged when there is nothing to filter — the
// common case by a wide margin, since the reconcile tick walks every segment-bearing
// graph and most carry no tombstones at all — and again when nothing was dropped.
//
// TAILS ARE CARRIED THROUGH UNFILTERED, deliberately. A tail segment holds live
// documents alongside the tombstoned one, and the drain's existing retiring/Unload
// pair already retires spent tails correctly.
//
// A tombstoned id is DROPPED rather than re-offered as a superseded id. Re-offering
// would dirty one partition per tombstoned id on every tick, and by the arithmetic at
// the head of the drain file a set of ids the size of one segment already touches
// essentially every partition — every tick would become a full-corpus re-emit.
// Dropping suffices: the delete already killed those partition copies, and imported
// blobs are seeded dead from this same set.
//
// THE STALENESS BOUND, and what narrows it. The tombstone set remains
// authoritative-but-stale for ids WITH NO LIVE DOCUMENT: it only grows as the delta
// feed reports deletions, and is only pruned when a rebuild re-emits an id's
// partition. A node deleted and LATER RE-CREATED under the same external id used to
// inherit that staleness and lose its fresh document here, which was unrepairable —
// import seeding inherits the same staleness but a seeded-dead import is corrected by
// the next re-emit of its partition, where a dropped backlog entry is simply gone.
// That case is now closed OUTSIDE this filter: the caller clears the record's
// tombstone for every id a write has re-created BEFORE this filter is built, so a
// re-created id is no longer in the set this reads and its fresh document survives.
//
// FILTERING HERE CANNOT STRAND AN ENTRY, and that no longer rests on anything
// non-local. Pending CAN now be non-empty while tails is empty: the write path
// records a tail only for a segment it actually CREATED, so a batch that merely
// aliased a resident segment queues its documents with no tail at all. The caller
// still derives what to REBUILD from this filtered snapshot, but derives whether
// there is anything to CONSUME from the UNFILTERED one, so a window whose pending
// filters down to nothing reaches the clear either way and its bytes stop charging
// pendingReEmitByteCap. The consume no longer depends on the two staying coupled.
func (f formatDirtyState) withoutTombstoned(tombstoned []searchengine.ExternalID) formatDirtyState {
	if len(tombstoned) == 0 || len(f.pending) == 0 {
		return f
	}
	dead := make(map[searchengine.ExternalID]bool, len(tombstoned))
	for _, id := range tombstoned {
		dead[id] = true
	}
	kept := make([]pendingDoc, 0, len(f.pending))
	for _, e := range f.pending {
		if !dead[e.doc.ID] {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(f.pending) {
		return f
	}
	return formatDirtyState{pending: kept, tails: f.tails, bytes: f.bytes}
}

func (f formatDirtyState) snapshot() formatDirtyState {
	return formatDirtyState{
		pending: append([]pendingDoc(nil), f.pending...),
		tails:   append([]searchengine.SegmentID(nil), f.tails...),
		bytes:   f.bytes,
	}
}

// clearDirty drops exactly the entries a successful tick consumed, identified by THE
// SNAPSHOT ITSELF rather than by a leading count, so writes that arrived DURING the
// tick survive it.
//
// The distinction is not academic: a delete purges entries out of the MIDDLE of the
// backlog, on a different goroutine from the tick, so a leading count consumed after
// a purge eats one late-arriving write per purged entry — dropping its documents
// while its tail id survives, which leaves the next drain holding a tail whose member
// it never rebuilt and must therefore keep resident instead of retiring.
//
// AND THE SNAPSHOT IS MATCHED BY SEQUENCE, which is what extends that guarantee to a
// late write carrying an id the concurrent purge removed. Matching by id cannot tell
// such a write apart from the snapshot's own copy of that id, so it was consumed with
// it — the deleted-then-re-created case, and the one hole the guarantee above used to
// be read past.
func (m *Manager) clearDirty(gt kgtypes.GraphType, name string, hnswSnap, bm25Snap formatDirtyState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.dirty[graphKey{graphType: gt, graphName: name}]
	if !ok {
		return
	}
	st.hnsw.consume(hnswSnap)
	st.bm25.consume(bm25Snap)
}

// consume drops exactly the entries the snapshot named and rescales the byte total to
// what remains, so the cap tracks the surviving backlog.
//
// Documents are matched by SEQUENCE, which identifies a queued write rather than
// merely describing it. One window can legitimately hold the same id twice — two
// writebacks of one document inside a single tick interval, or a delete and an
// immediate re-creation — and only the sequence tells those entries apart. A whole
// batch shares one sequence, which is exactly right: a batch is appended atomically,
// so a snapshot holds all of its entries or none.
//
// Tails keep a MULTISET budget. A tail id is a content hash, so two entries sharing
// one are byte-identical and interchangeable, and there is nothing for a sequence to
// disambiguate.
func (f *formatDirtyState) consume(snap formatDirtyState) {
	consumed := make(map[uint64]struct{}, len(snap.pending))
	for _, e := range snap.pending {
		consumed[e.seq] = struct{}{}
	}
	tailBudget := make(map[searchengine.SegmentID]int, len(snap.tails))
	for _, id := range snap.tails {
		tailBudget[id]++
	}

	pending := make([]pendingDoc, 0, len(f.pending))
	for _, e := range f.pending {
		if _, ok := consumed[e.seq]; ok {
			f.bytes -= documentBytes(e.doc)
			continue
		}
		pending = append(pending, e)
	}
	f.pending = pending

	tails := make([]searchengine.SegmentID, 0, len(f.tails))
	for _, id := range f.tails {
		if tailBudget[id] > 0 {
			tailBudget[id]--
			continue
		}
		tails = append(tails, id)
	}
	f.tails = tails

	if f.bytes < 0 {
		f.bytes = 0
	}
}
