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
// heap. It is how a MERGE's output stops being heap-resident: merges publish
// through newEntry, which no import-path cleanup ever covers, so without this
// every merge would strand its consolidated blob on the heap for the life of the
// process — and merges replace their constituents, so a progressively merged
// corpus would climb back to whole-corpus residency.
//
// It is a NO-OP when id is not resident, so a merge whose output was superseded
// before this ran does not resurrect it.
//
// THE COPY CARRIES LIVENESS FORWARD IN PLACE. The replacement entry keeps the
// SAME *liveDocs pointer and the SAME members map, and its meta is untouched;
// only payload changes. That is not an optimization, it is the correctness
// requirement: an entry's liveDocs are mutated IN PLACE and without any CAS —
// the documented exception to the snapshot's immutability — and the merged entry
// is published and searchable BEFORE this runs. Rebuilding liveness from a
// tombstone slice would discard every delete that landed in that window and the
// document would silently come back from the dead. Rebuilding members is
// pointless besides: the bytes are byte-identical to the published ones, because
// a segment id IS their content hash, so members, ordinals and routes are the
// same by construction.
//
// OWNERSHIP: this TAKES the blob. From the moment it is called, blob.Release is
// its responsibility on every path — it either hands the release to the new
// entry's cleanup, or calls it. A caller must never release a blob it passed
// here, and never has to remember to.
//
// That contract exists because the alternative leaks silently. This function
// DECLINES in two cases that are both ordinary rather than exceptional — the id
// is not resident, and the id vanished between a lost CAS and the retry — and it
// returns a nil error for both, because neither is an error. A decline that
// returned nil while holding an unattached mapping would leak it with no error,
// no log and no test failure; the mapping would simply never be freed. Signaling
// the decline to the caller instead would work, but it puts the obligation on
// every call site and the leak returns the first time one forgets.
func (e *SegmentedIndex[Q, S]) RemapResident(id SegmentID, blob SegmentBlob) error {
	// THE PAYLOAD IS Bytes, ALREADY SPLIT. An envelope, if this blob carries one,
	// is in Envelope and is not consulted here at all: the record is taken from the
	// entry being replaced rather than from the blob — see the copy below.
	seg, err := e.format.Decode(blob.Bytes)
	if err != nil {
		releaseUnattached(blob.Release)
		return fmt.Errorf("remap segment %s: %w", id, err)
	}
	for {
		old := e.set.Load()
		idx := -1
		for i, entry := range old.entries {
			if entry.meta.ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			// Not resident: superseded before the remap reached it, or gone
			// between a lost CAS and this retry. Nothing will ever attach this
			// mapping, so it is freed here rather than stranded.
			releaseUnattached(blob.Release)
			return nil
		}
		src := old.entries[idx]
		next := &segmentEntry[Q, S]{
			payload: seg,
			live:    src.live,
			members: src.members,
			meta:    src.meta,
			// THE RECORD COMES FROM THE ENTRY, not from the blob, for the same reason
			// its liveness does: the bytes are byte-identical to the published ones (a
			// segment id is their payload's content hash), so the two agree — and
			// taking it from the entry keeps ONE source for what this segment replaced.
			record: src.record,
			// The same borrowed-memory pin entryFromDecoded carries, for the same
			// reason: a blob's bytes can be a view into memory another entry owns,
			// and the unmap is keyed on that owner's reachability rather than on
			// holding the bytes.
			pin: blob.keepAlive,
		}
		entries := make([]*segmentEntry[Q, S], len(old.entries))
		copy(entries, old.entries)
		entries[idx] = next
		if e.set.CompareAndSwap(old, newSegmentSet(e.format, entries)) {
			// Attached only AFTER the swap wins. A losing attempt's entry is
			// garbage immediately, and a cleanup on it would free the mapping
			// the winning entry is still using.
			attachBlobCleanup(next, blob.Release)
			return nil
		}
	}
}
