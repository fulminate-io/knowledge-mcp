// SPDX-License-Identifier: Apache-2.0

package searchengine

import (
	"fmt"
	"runtime"
)

// attachBlobCleanup arranges for release to run once entry is unreachable.
//
// REACHABILITY IS THE CONDITION THAT MAKES A RELEASE SAFE, and that is why this
// is a cleanup rather than an explicit free after the swap. Search loads the
// entry set through an atomic pointer and then reads entries with NO lock held,
// so a reader can still be walking a pre-swap snapshot at the moment Unload or a
// merge CASes an entry out. Freeing there would either unmap memory a live
// reader is inside, or — behind an acquire-if-nonzero refcount — silently drop
// that segment's documents from a result the snapshot already promised. Neither
// is acceptable on a search path.
//
// Two facts make the delayed unmap harmless rather than a leak. Mapped pages are
// page-cache pages the OS can reclaim whether or not this process has unmapped
// them, so a late unmap holds address space rather than RAM. And the descriptor
// is released at MAP time, so a pending mapping holds no file handle either.
//
// release must not capture entry: a cleanup argument that references the object
// it is attached to keeps that object alive forever, and the mapping with it.
func attachBlobCleanup[Q, S any](entry *segmentEntry[Q, S], release func()) {
	if entry == nil || release == nil {
		return
	}
	runtime.AddCleanup(entry, func(rel func()) { rel() }, release)
}

// releaseUnattached frees a mapping that never became an entry's. It is the
// counterpart to attachBlobCleanup: exactly one of the two must run for every
// blob handed to RemapResident, and calling it is safe when release is nil.
//
// Freeing here is immediate and safe precisely BECAUSE the mapping was never
// attached — it was never published into a segment set, so no reader can hold a
// snapshot referring to it, and the reachability argument that forces the
// attached case to wait for a cleanup does not apply.
func releaseUnattached(release func()) {
	if release != nil {
		release()
	}
}

// RemapResident republishes a resident segment's payload from blob, which is
// normally the same bytes mapped from the disk cache rather than held on the
// heap. It is how a segment stops being heap-resident, and there are two
// producers of one: a MERGE, whose output is published through newEntry and which
// would otherwise strand its consolidated blob on the heap for the life of the
// process, and a SEAL, whose encoder output is retained by the payload until the
// durability path has written it and this republishes it over the stored copy.
//
// It is a NO-OP when id is not resident, so a segment superseded before this ran
// does not resurrect it.
//
// It is the ONE-SEGMENT case of RemapResidentBatch, which carries the whole
// contract; the batch exists because the statistics fold below is per SNAPSHOT
// rather than per segment. It reports only the ERROR half of that outcome,
// because a caller of the single form already knows which id it handed in and a
// decline is not an error.
func (e *SegmentedIndex[Q, S]) RemapResident(id SegmentID, blob SegmentBlob) error {
	blob.ID = id
	return e.RemapResidentBatch([]SegmentBlob{blob})[id].Err
}

// RemapOutcome says which of the three things happened to one blob in a batch.
//
// THE THIRD VALUE EXISTS BECAUSE TWO OF THEM USED TO SHARE A nil ERROR. A decline
// and a republication are both ordinary and neither is a failure, so reporting
// them as one "no error" left a caller unable to say what it had done: an
// operator-facing line reading "released N" could be printed for a batch in which
// nothing was swapped at all, because every segment had left the set between the
// caller's list and the CAS. Counting is the whole reason this seam reports
// anything, so the two are now distinguishable.
type RemapOutcome int

const (
	// RemapRepublished: the payload was swapped for the one this blob carries,
	// and the blob's release is now the new entry's cleanup.
	RemapRepublished RemapOutcome = iota
	// RemapDeclined: the segment was not resident — superseded, evicted or
	// pruned before the remap reached it. The mapping was released here; there
	// was no degraded entry left to repair, so this is a terminus rather than a
	// failure.
	RemapDeclined
	// RemapFailed: the blob's bytes could not be decoded. Err carries why.
	RemapFailed
)

// RemapResult is one blob's outcome. Err is non-nil only for RemapFailed.
type RemapResult struct {
	Outcome RemapOutcome
	Err     error
}

// RemapResidentBatch republishes SEVERAL resident payloads in ONE swap, reporting
// each blob's outcome by segment id: republished, declined because the segment is
// no longer resident, or failed because its bytes could not be decoded.
//
// IT IS A BATCH BECAUSE THE SNAPSHOT WORK IS PER SWAP, NOT PER SEGMENT. Each swap
// re-folds the format's corpus statistics over the resident payloads (see
// withReplacedPayloads for why they cannot be carried), so N one-segment swaps cost
// N x resident where one N-segment swap costs resident once. A drain releases every
// segment it sealed, so that difference is the difference between a release that
// scales and one that does not.
//
// THE COPY CARRIES LIVENESS FORWARD IN PLACE. Each replacement entry keeps the SAME
// *liveDocs pointer and the SAME members map, and its meta is untouched; only
// payload changes. That is not an optimization, it is the correctness requirement:
// an entry's liveDocs are mutated IN PLACE and without any CAS — the documented
// exception to the snapshot's immutability — and a published entry is searchable
// BEFORE this runs. Rebuilding liveness from a tombstone slice would discard every
// delete that landed in that window and the document would silently come back from
// the dead. Rebuilding members is pointless besides: the bytes are byte-identical to
// the published ones, because a segment id IS their content hash, so members,
// ordinals and routes are the same by construction.
//
// OWNERSHIP: this TAKES every blob. From the moment it is called, each blob's
// Release is its responsibility on every path — it either hands the release to that
// segment's new entry cleanup, or calls it. A caller must never release a blob it
// passed here, and never has to remember to.
//
// That contract exists because the alternative leaks silently. This function
// DECLINES for two reasons that are both ordinary rather than exceptional — the id
// is not resident, and the id vanished between a lost CAS and the retry — and it
// reports RemapDeclined rather than an error for both, because neither is one. A
// decline that reported success while holding an unattached mapping would leak it
// with no error, no log and no test failure; the mapping would simply never be
// freed. Signaling the decline to
// the caller instead would work, but it puts the obligation on every call site and
// the leak returns the first time one forgets.
func (e *SegmentedIndex[Q, S]) RemapResidentBatch(blobs []SegmentBlob) map[SegmentID]RemapResult {
	outcomes := make(map[SegmentID]RemapResult, len(blobs))
	if len(blobs) == 0 {
		return outcomes
	}

	// DECODE FIRST, OUTSIDE THE CAS LOOP. A decode is a header-and-field-table parse
	// over bytes that are already durable; repeating it per lost race would be the
	// same mistake the publish path's lock exists to prevent.
	type decoded struct {
		blob SegmentBlob
		seg  Segment[Q, S]
	}
	pending := make([]decoded, 0, len(blobs))
	for _, blob := range blobs {
		// THE PAYLOAD IS Bytes, ALREADY SPLIT. An envelope, if this blob carries one,
		// is in Envelope and is not consulted here at all: the record is taken from the
		// entry being replaced rather than from the blob — see the copy below.
		seg, err := e.format.Decode(blob.Bytes)
		if err != nil {
			releaseUnattached(blob.Release)
			outcomes[blob.ID] = RemapResult{
				Outcome: RemapFailed,
				Err:     fmt.Errorf("remap segment %s: %w", blob.ID, err),
			}
			continue
		}
		// Provisionally declined: a blob that reaches no resident entry below keeps
		// this value, and a winning swap overwrites it.
		outcomes[blob.ID] = RemapResult{Outcome: RemapDeclined}
		pending = append(pending, decoded{blob: blob, seg: seg})
	}
	if len(pending) == 0 {
		return outcomes
	}

	for {
		old := e.set.Load()
		indexByID := make(map[SegmentID]int, len(old.entries))
		for i, entry := range old.entries {
			indexByID[entry.meta.ID] = i
		}

		replacements := make(map[int]*segmentEntry[Q, S], len(pending))
		winners := make([]*segmentEntry[Q, S], 0, len(pending))
		releases := make([]func(), 0, len(pending))
		var declined []func()
		for _, p := range pending {
			idx, resident := indexByID[p.blob.ID]
			if !resident {
				// Not resident: superseded before the remap reached it, or gone between
				// a lost CAS and this retry. Nothing will ever attach this mapping, so
				// it is freed once this attempt settles rather than stranded.
				declined = append(declined, p.blob.Release)
				continue
			}
			src := old.entries[idx]
			next := &segmentEntry[Q, S]{
				payload: p.seg,
				live:    src.live,
				members: src.members,
				meta:    src.meta,
				// heapPayload IS DELIBERATELY ZERO, and that is the point of the swap: the
				// replacement payload reads the mapping handed in here, so the encoder's
				// output the old entry retained is no longer reachable from this set once
				// the CAS wins. Carrying the old value forward would leave the residency
				// budget metering bytes this call released, and the budget would then evict
				// whole pools to reclaim memory that is already page cache.
				heapPayload: 0,
				// THE RECORD COMES FROM THE ENTRY, not from the blob, for the same reason
				// its liveness does: the bytes are byte-identical to the published ones (a
				// segment id is their payload's content hash), so the two agree — and
				// taking it from the entry keeps ONE source for what this segment replaced.
				record: src.record,
				// The same borrowed-memory pin entryFromDecoded carries, for the same
				// reason: a blob's bytes can be a view into memory another entry owns,
				// and the unmap is keyed on that owner's reachability rather than on
				// holding the bytes.
				pin: p.blob.keepAlive,
			}
			replacements[idx] = next
			winners = append(winners, next)
			releases = append(releases, p.blob.Release)
		}
		if len(replacements) == 0 {
			for _, release := range declined {
				releaseUnattached(release)
			}
			return outcomes
		}
		if e.set.CompareAndSwap(old, old.withReplacedPayloads(e.format, replacements)) {
			// Attached only AFTER the swap wins. A losing attempt's entries are
			// garbage immediately, and a cleanup on one would free the mapping the
			// winning entry is still using.
			for i, entry := range winners {
				attachBlobCleanup(entry, releases[i])
				outcomes[entry.meta.ID] = RemapResult{Outcome: RemapRepublished}
			}
			for _, release := range declined {
				releaseUnattached(release)
			}
			return outcomes
		}
	}
}
