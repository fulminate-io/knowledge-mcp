package searchengine

import (
	"fmt"
	"runtime"
	"sync"
)

// Export serializes every current segment to a shippable SegmentBlob. The
// client marshals these and ships them; the server stores them opaquely.
func (e *SegmentedIndex[Q, S]) Export() []SegmentBlob {
	set := e.set.Load()
	blobs := make([]SegmentBlob, 0, len(set.entries))
	for _, entry := range set.entries {
		// blobParts, never payload.Encode: an exported blob is what the owner makes
		// durable, so it must carry the entry's supersession record.
		envelope, payload, err := entry.blobParts()
		if err != nil {
			continue
		}
		blobs = append(blobs, SegmentBlob{
			ID:         entry.meta.ID,
			Format:     e.format.Name(),
			Generation: entry.meta.Generation,
			DocCount:   entry.meta.DocCount,
			Bytes:      payload,
			Envelope:   envelope,
			// The exported blob OUTLIVES this call, and on a mapped segment
			// Encode returns the mapping itself rather than a copy. Pinning the
			// entry keeps the mapping's cleanup from running while a caller
			// still holds these bytes.
			keepAlive: entry,
		})
	}
	return blobs
}

// ResidentSegmentIDs lists the id of every sealed segment currently resident in
// the searchable set. It is the CHEAP id-only counterpart of Export: one atomic
// snapshot load and a walk of the entry metas, with NO per-segment Encode.
//
// Export is the wrong call for a caller that only needs to know WHICH segments are
// resident — it re-serializes every payload, which on a full corpus is tens of
// megabytes of encoding to answer a set-membership question. The completeness
// reconcile asks exactly that question, per graph, per tick.
//
// Same snapshot semantics as Search/Export/ResidentDocCount: the SEALED set only,
// read through the lock-free atomic pointer. The sub-threshold active buffer holds
// no segment and is intentionally absent.
func (e *SegmentedIndex[Q, S]) ResidentSegmentIDs() []SegmentID {
	set := e.set.Load()
	ids := make([]SegmentID, 0, len(set.entries))
	for _, entry := range set.entries {
		ids = append(ids, entry.meta.ID)
	}
	return ids
}

// Import decodes a batch of blobs into segments and publishes them in ONE CAS.
// Each decoded segment seeds its liveDocs from the tombstones (contract: liveDocs
// seeded from tombstones at Import), so already-deleted documents start dead.
//
// Import is IDEMPOTENT BY SEGMENT ID: publishImport skips any incoming entry whose
// content-hash meta.ID is already resident in the set, so re-importing a blob the
// engine already holds is a no-op (it does NOT add a second copy). This matters
// because mergeTopK (topk.go) does NOT dedup docIDs across segments — a doc
// resident in two segments would surface the SAME docID in two result slots,
// inflating/crowding the top-k. Genuinely-new segments still ADD; only resident
// ids are dropped.
//
// Imported and locally-built segments are MERGE-EQUIVALENT: background merge
// calls format.Merge, which reads live INDEXED data directly from a decoded
// Segment — no source Documents required. This is the whole point of the amended
// Merge contract.
//
// IT HONORS EACH BLOB'S SUPERSESSION RECORD (supersession.go): a consolidated blob
// names the constituents it replaced, and any of those present in the SAME batch are
// declined rather than published beside it. That is what makes a cold load of a stored
// corpus correct without external state — an L2 index holds both across an un-reclaimed
// merge window, and publishing both duplicates every document across two segments while
// resurrecting anything the merge dropped.
//
// THE SCOPE IS THIS BATCH, and that is the whole reachable surface rather than a
// convenient subset: every load path in the distribution layer imports the pool's whole
// stored index in ONE call, and a constituent that is already RESIDENT was superseded by
// the merge's own CAS at the moment it ran.
func (e *SegmentedIndex[Q, S]) Import(blobs []SegmentBlob, tombstones []ExternalID) error {
	if len(blobs) == 0 {
		return nil
	}

	entries := make([]*segmentEntry[Q, S], len(blobs))
	errs := make([]error, len(blobs))
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for i, blob := range blobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, blob SegmentBlob) {
			defer wg.Done()
			defer func() { <-sem }()
			// THE RECORD COMES FROM Envelope AND THE PAYLOAD FROM Bytes, with no
			// parse of the stored bytes here: the blob arrives already split, from
			// an engine producer or from the load path, and Bytes is the payload by
			// the invariant on SegmentBlob.
			rec, err := decodeSupersessionEnvelope(blob.Envelope)
			if err != nil {
				errs[i] = fmt.Errorf("decode segment %s: %w", blob.ID, err)
				return
			}
			seg, err := e.format.Decode(blob.Bytes)
			if err != nil {
				errs[i] = fmt.Errorf("decode segment %s: %w", blob.ID, err)
				return
			}
			entries[i] = e.entryFromDecoded(seg, blob, tombstones)
			// THE RECORD IS CARRIED ONTO THE ENTRY, so a segment that is imported and
			// later re-exported still says what it replaced. An entry that forgot it
			// would write the record away on the next persist, and the corpus would
			// quietly return to saying nothing about supersession.
			entries[i].record = rec
		}(i, blob)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	e.publishImport(declineSuperseded(entries))
	return nil
}

// declineSuperseded drops the entries this batch's own records name as superseded.
//
// IT COLLECTS THE WHOLE BATCH FIRST, because the order blobs arrive in says nothing
// about which superseded which — a constituent routinely sits ahead of the blob that
// replaced it, which is exactly how a cache index enumerates them.
//
// A RECORD IS HONORED ONLY WHEN ITS WHOLE COHORT IS PRESENT, and that gate is what
// keeps this from being a data-loss instrument. A consolidation publishes its outputs as
// a SET, and it is the set that carries the superseded members forward: a group swap
// harvests several partitions at once and a layer swap replaces the whole corpus. If
// only part of that set reached disk — a crash between two writes, an aborted L2 write —
// then declining the constituents would retire documents whose only other copy is a
// sibling output that is not here. Requiring the cohort makes "the replacement landed
// whole" a precondition rather than an assumption.
func declineSuperseded[Q, S any](entries []*segmentEntry[Q, S]) []*segmentEntry[Q, S] {
	present := make(map[SegmentID]struct{}, len(entries))
	for _, entry := range entries {
		present[entry.meta.ID] = struct{}{}
	}
	var superseded map[SegmentID]struct{}
	for _, entry := range entries {
		if entry.record.empty() || !subsetPresent(entry.record.Cohort, present) {
			continue
		}
		if superseded == nil {
			superseded = make(map[SegmentID]struct{}, len(entry.record.Superseded))
		}
		for _, id := range entry.record.Superseded {
			superseded[id] = struct{}{}
		}
	}
	if superseded == nil {
		return entries // the ordinary case: nothing in this batch supersedes anything
	}
	kept := make([]*segmentEntry[Q, S], 0, len(entries))
	for _, entry := range entries {
		if _, dead := superseded[entry.meta.ID]; dead {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// subsetPresent reports whether every id is in present. An EMPTY cohort reports false:
// a record that names no publisher cannot prove its replacement landed, so it is not
// acted on.
func subsetPresent(ids []SegmentID, present map[SegmentID]struct{}) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if _, ok := present[id]; !ok {
			return false
		}
	}
	return true
}

// entryFromDecoded wraps a decoded segment into an entry: members from IDs(),
// liveDocs seeded from tombstones, meta carrying the blob's content-addressed ID
// and Generation.
func (e *SegmentedIndex[Q, S]) entryFromDecoded(seg Segment[Q, S], blob SegmentBlob, tombstones []ExternalID) *segmentEntry[Q, S] {
	ids := seg.IDs()
	members := make(idSet, len(ids))
	for ord, extID := range ids {
		members[extID] = ord
	}
	live := newLiveDocsFromTombstones(len(ids), tombstones, members)
	// DISTINCT, exactly as newEntry counts it — this is the IMPORT path, and a
	// blob built before the count was corrected carries duplicate ids, so it is
	// precisely the path that would otherwise re-inflate the corpus size on load.
	entry := &segmentEntry[Q, S]{
		payload: seg,
		live:    live,
		members: members,
		meta: SegmentMeta{
			ID:         blob.ID,
			Format:     blob.Format,
			Generation: blob.Generation,
			DocCount:   len(members),
			DeadCount:  distinctDeadCount(members, live),
		},
	}
	// When the blob arrived as a mapping this entry OWNS, the mapping's lifetime is
	// now this entry's reachability.
	attachBlobCleanup(entry, blob.Release)
	// And when it arrived as a view into memory SOMEONE ELSE owns — which is what an
	// exported blob is, since Encode on a mapped segment returns the mapping itself
	// — the owner is pinned here. Dropping it is how an imported segment comes to
	// read unmapped memory after its exporter is collected.
	entry.pin = blob.keepAlive
	return entry
}

// publishImport CAS-appends the imported entries to the current set in one swap,
// retrying on a lost CAS. It is IDEMPOTENT BY SEGMENT ID: the `present` skip set
// is rebuilt from old.entries EACH iteration (a stale snapshot computed once
// would double-add a segment that another writer published between iterations),
// and any incoming entry whose content-hash meta.ID is already resident is
// dropped. Same identity Unload keys on (entry.meta.ID). Genuinely-new entries
// still append; if every incoming entry is already resident the CAS is skipped
// entirely (nothing to publish).
func (e *SegmentedIndex[Q, S]) publishImport(entries []*segmentEntry[Q, S]) {
	for {
		old := e.set.Load()
		present := make(map[SegmentID]struct{}, len(old.entries))
		for _, entry := range old.entries {
			present[entry.meta.ID] = struct{}{}
		}
		fresh := make([]*segmentEntry[Q, S], 0, len(entries))
		for _, entry := range entries {
			if _, resident := present[entry.meta.ID]; resident {
				continue // idempotent: already resident, do not double-add
			}
			present[entry.meta.ID] = struct{}{} // guard against dup ids within this batch
			fresh = append(fresh, entry)
		}
		if len(fresh) == 0 {
			return // every incoming entry already resident — nothing to publish
		}
		merged := make([]*segmentEntry[Q, S], 0, len(old.entries)+len(fresh))
		merged = append(merged, old.entries...)
		merged = append(merged, fresh...)
		next := newSegmentSet[Q, S](e.format, merged)
		if e.set.CompareAndSwap(old, next) {
			e.signalMerge()
			return
		}
	}
}

// Unload drops the named segments from the searchable set via one CAS swap. The
// reload path that puts them back reads the client's L2 disk cache directly and
// lives in segmentdist/manager_load.go (reload).
func (e *SegmentedIndex[Q, S]) Unload(ids []SegmentID) {
	if len(ids) == 0 {
		return
	}
	remove := make(map[SegmentID]bool, len(ids))
	for _, id := range ids {
		remove[id] = true
	}
	for {
		old := e.set.Load()
		kept := make([]*segmentEntry[Q, S], 0, len(old.entries))
		for _, entry := range old.entries {
			if !remove[entry.meta.ID] {
				kept = append(kept, entry)
			}
		}
		next := newSegmentSet[Q, S](e.format, kept)
		if e.set.CompareAndSwap(old, next) {
			return
		}
	}
}
