// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"encoding/binary"
	"unsafe"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// openDict decodes one field's dictionary header and validates every sub-section
// it names. This is O(1) per field — the entry rows, blocks and term bytes are
// read only when a query probes for a term.
func (mf *mappedField) openDict(off int) error {
	b := mf.blob
	header := 8
	if mf.kind != dictFlat {
		header = 16
	}
	if err := checkSection("dictionary", len(b), off, header, 8); err != nil {
		return err
	}
	mf.termCount = int(binary.LittleEndian.Uint32(b[off:]))
	switch mf.kind {
	case dictBlocked:
		mf.blockCount = int(binary.LittleEndian.Uint32(b[off+4:]))
		mf.firstTermsOff = int(binary.LittleEndian.Uint32(b[off+8:]))
		mf.blockIdxOff = int(binary.LittleEndian.Uint32(b[off+12:]))
		if err := checkSection("dictionary first terms", len(b), mf.firstTermsOff, 8*mf.blockCount, 4); err != nil {
			return err
		}
		return checkSection("dictionary block index", len(b), mf.blockIdxOff, 4*mf.blockCount, 4)
	case dictHash:
		mf.entriesOff = int(binary.LittleEndian.Uint32(b[off+4:]))
		mf.slotsOff = int(binary.LittleEndian.Uint32(b[off+8:]))
		mf.slotCount = int(binary.LittleEndian.Uint32(b[off+12:]))
		if err := checkSection("dictionary entries", len(b), mf.entriesOff, v2FlatEntrySize*mf.termCount, 8); err != nil {
			return err
		}
		return checkSection("dictionary hash slots", len(b), mf.slotsOff, v2HashSlotSize*mf.slotCount, 8)
	default:
		mf.entriesOff = int(binary.LittleEndian.Uint32(b[off+4:]))
		return checkSection("dictionary entries", len(b), mf.entriesOff, v2FlatEntrySize*mf.termCount, 8)
	}
}

// entryAt reads flat entry row i: the term view and its posting run's location.
func (mf *mappedField) entryAt(i int) (term string, postOff, postCount int) {
	at := mf.entriesOff + v2FlatEntrySize*i
	termOff := int(binary.LittleEndian.Uint32(mf.blob[at:]))
	termLen := int(binary.LittleEndian.Uint32(mf.blob[at+4:]))
	postOff = int(binary.LittleEndian.Uint32(mf.blob[at+8:]))
	postCount = int(binary.LittleEndian.Uint32(mf.blob[at+12:]))
	term = termView(mf.segmentID(), mf.blob, termOff, termLen, "dictionary term")
	return term, postOff, postCount
}

// postings returns zero-copy views of a term's posting run: the ascending
// document ids and the parallel term frequencies. The two runs are adjacent, so
// a short list is one page rather than two.
//
// A misaligned or out-of-bounds run is an invariant violation, not a search
// miss: segment blobs are content-addressed by sha256 of their own bytes, so the
// only way to reach it is a writer defect. Reporting it as "term absent" would
// turn that defect into silently wrong search results, so it fails loudly here.
func (mf *mappedField) postings(off, count int) ([]uint32, []uint16) {
	if count == 0 {
		return nil, nil
	}
	if off%4 != 0 || off+6*count > len(mf.blob) {
		// THE INCIDENT'S OWN CLASS, and the one the merge path hits. Attributed,
		// because a merge drains several inputs under one boundary that cannot
		// say which of them raised — unattributed, this withdraws nothing and the
		// merge re-selects the same constituents every 50ms forever.
		searchengine.RaiseCorruptIn(mf.segmentID(),
			"bm25: posting run of %d at offset %d is misaligned or past the %d-byte blob", count, off, len(mf.blob))
	}
	return u32sAt(mf.blob, off, count), u16sAt(mf.blob, off+4*count, count)
}

// lookup resolves a term to its posting run. Sorted order is what every
// encoding's search depends on, and it is the same order the merge streams in.
func (mf *mappedField) lookup(term string) (docIDs []uint32, tfs []uint16, ok bool) {
	if mf.termCount == 0 {
		return nil, nil, false
	}
	var off, count int
	switch mf.kind {
	case dictBlocked:
		off, count, ok = mf.lookupBlocked(term)
	case dictHash:
		off, count, ok = mf.lookupHash(term)
	default:
		off, count, ok = mf.lookupFlat(term)
	}
	if !ok {
		return nil, nil, false
	}
	docIDs, tfs = mf.postings(off, count)
	return docIDs, tfs, true
}

// lookupFlat binary-searches the sorted entry rows.
func (mf *mappedField) lookupFlat(term string) (off, count int, ok bool) {
	lo, hi := 0, mf.termCount
	for lo < hi {
		mid := (lo + hi) / 2
		got, o, c := mf.entryAt(mid)
		switch {
		case got == term:
			return o, c, true
		case got < term:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return 0, 0, false
}

// lookupHash probes the open-addressed accelerator beside the sorted rows,
// confirming every fingerprint hit against the real term. The table is at most
// half full and an empty slot terminates the probe, so a miss is bounded.
func (mf *mappedField) lookupHash(term string) (off, count int, ok bool) {
	if mf.slotCount == 0 {
		return 0, 0, false
	}
	h := termHash(term)
	fingerprint := uint32(h >> 32)
	mask := uint64(mf.slotCount - 1)
	slot := h & mask
	for range mf.slotCount {
		at := mf.slotsOff + v2HashSlotSize*int(slot)
		idx := binary.LittleEndian.Uint32(mf.blob[at+4:])
		if idx == hashEmptySlot {
			return 0, 0, false
		}
		if binary.LittleEndian.Uint32(mf.blob[at:]) == fingerprint && int(idx) < mf.termCount {
			if got, o, c := mf.entryAt(int(idx)); got == term {
				return o, c, true
			}
		}
		slot = (slot + 1) & mask
	}
	return 0, 0, false
}

// firstTermAt returns block b's first term as a view into the blob.
func (mf *mappedField) firstTermAt(b int) string {
	at := mf.firstTermsOff + 8*b
	termOff := int(binary.LittleEndian.Uint32(mf.blob[at:]))
	termLen := int(binary.LittleEndian.Uint32(mf.blob[at+4:]))
	return termView(mf.segmentID(), mf.blob, termOff, termLen, "block first term")
}

// lookupBlocked binary-searches the contiguous first-term index for the block
// that could hold term, then scans that block reconstructing front-coded terms.
// The first-term index is small and every probe in a query reuses it, which is
// why this encoding faults the fewest pages despite doing the most CPU work.
func (mf *mappedField) lookupBlocked(term string) (off, count int, ok bool) {
	lo, hi := 0, mf.blockCount
	for lo < hi {
		mid := (lo + hi) / 2
		if mf.firstTermAt(mid) <= term {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return 0, 0, false
	}
	return mf.scanBlock(lo-1, term)
}

// scanBlock walks one front-coded block looking for term. The scratch buffer
// carries the previous term forward, since each entry stores only the bytes on
// which it diverges from its predecessor.
//
// EVERY SLICE HERE IS BOUNDS-CHECKED, for the reason dictIter.nextBlocked
// records: this is that function's SEARCH-path twin, walking the same layout
// with the same arithmetic, and an unchecked slice raises a RUNTIME bounds panic
// that the engine's boundary deliberately does not contain — so it kills the
// process exactly as the original defect did.
//
// OF THE TWO TWINS THIS IS THE ONE AN ORDINARY QUERY REACHES. nextBlocked serves
// the merge and the dictionary walk; this serves lookupBlocked, which is what a
// search calls on a blocked-dictionary segment — and the incident's own file is
// blocked.
func (mf *mappedField) scanBlock(b int, term string) (off, count int, ok bool) {
	first := mf.firstTermAt(b)
	idxAt := mf.blockIdxOff + 4*b
	if idxAt < 0 || idxAt+4 > len(mf.blob) {
		searchengine.RaiseCorruptIn(mf.segmentID(),
			"bm25: block index %d is past the %d-byte blob", b, len(mf.blob))
	}
	at := int(binary.LittleEndian.Uint32(mf.blob[idxAt:]))
	if first == term {
		return mf.blockEntry(at)
	}
	at += 8
	start := b * blockedBlockTerms
	end := min(start+blockedBlockTerms, mf.termCount)
	scratch := append(make([]byte, 0, len(first)+16), first...)
	for range end - start - 1 {
		scratch, at = stepFrontCoded(mf.segmentID(), mf.blob, at, scratch)
		// Compared as a view rather than string(scratch), which would allocate a
		// copy of every term the scan walks past. The view is used only inside
		// this iteration; the next one overwrites the buffer behind it.
		//nolint:gosec // view over the local reconstruction buffer, used only in this iteration
		got := unsafe.String(unsafe.SliceData(scratch), len(scratch))
		// Terms ascend within a block, so a scratch past term means term is absent.
		if got == term {
			return mf.blockEntry(at)
		} else if got > term {
			return 0, 0, false
		}
		at += 8
	}
	return 0, 0, false
}

// blockEntry reads the posting-run location a front-coded block entry carries at
// offset at. Shared by scanBlock's two hit paths so the bounds check cannot be
// present on one and missing on the other.
func (mf *mappedField) blockEntry(at int) (off, count int, ok bool) {
	if at < 0 || at+8 > len(mf.blob) {
		searchengine.RaiseCorruptIn(mf.segmentID(),
			"bm25: block entry at offset %d is past the %d-byte blob", at, len(mf.blob))
	}
	return int(binary.LittleEndian.Uint32(mf.blob[at:])), int(binary.LittleEndian.Uint32(mf.blob[at+4:])), true
}

// stepFrontCoded decodes ONE front-coded dictionary entry at off, extending prev
// with the suffix it carries, and returns the reconstructed term buffer and the
// offset just past that suffix.
//
// IT EXISTS BECAUSE THERE WERE TWO COPIES OF IT. The search path (scanBlock) and
// the merge cursor (extendPreviousTerm) decode the same bytes by the same rule,
// and both were maintained by hand down to byte-identical raise strings. That is
// the shape this package has already been bitten by: the incident's front-coding
// guard was added to one twin while the other kept the unchecked reslice, and an
// ordinary search on the real corrupt artifact was still fatal after the "fix".
// With one implementation, a change to the rule cannot reach one reader and miss
// the other.
//
// THE TWO BOUNDS ARE DIFFERENT KINDS OF BOUND, which is the subtlety worth
// keeping in one place. The header must fit the BLOB; the shared-prefix count
// indexes the PREVIOUS TERM and is bounded by the reconstruction buffer, not by
// the blob — and it is that second one a corrupt value turns into a reslice
// panic rather than an out-of-range read.
func stepFrontCoded(id searchengine.SegmentID, blob []byte, off int, prev []byte) ([]byte, int) {
	if off < 0 || off+4 > len(blob) {
		searchengine.RaiseCorruptIn(id, "bm25: front-coded term header at offset %d is past the %d-byte blob", off, len(blob))
	}
	shared := int(binary.LittleEndian.Uint16(blob[off:]))
	suffixLen := int(binary.LittleEndian.Uint16(blob[off+2:]))
	off += 4
	if shared > len(prev) || off+suffixLen > len(blob) {
		searchengine.RaiseCorruptIn(id,
			"bm25: front-coded term shares %d bytes of a %d-byte prefix and adds %d at offset %d in a %d-byte blob",
			shared, len(prev), suffixLen, off, len(blob))
	}
	return append(prev[:shared], blob[off:off+suffixLen]...), off + suffixLen
}
