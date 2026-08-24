// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"encoding/binary"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// The distinct-page census the dictionary decision's COLD column rests on.
// It lives beside the benchmark rather than inside it because it is a separate
// instrument with its own failure mode: it MIRRORS the readers' search loops,
// and a reader change not mirrored here would silently make it describe the
// wrong access pattern.

// pageBytes is the unit the distinct-page census counts in. The design's
// measurements were taken on a machine with 16 KiB pages, so the census reports
// in the same unit the baseline table uses.
const pageBytes = 16 << 10

// pagesTouchedIn16KiB counts the distinct pages one query touches in one
// segment. It is the COLD half of the dictionary decision, and the half the
// encodings actually differ on — warm latency separates them by tens of percent,
// pages faulted by more than 2x.
//
// It counts the two things a probe reads: the dictionary bytes walked to locate
// each term, and the posting run the term resolves to. The dictionary walk is
// replayed by recordProbe rather than modeled, so the count is the real access
// pattern and not an estimate.
//
// THE DIVERGENCE RISK, stated because it is the honest weakness: recordProbe
// mirrors the reader's search loops, so a change to a reader that is not made
// here silently makes the census describe the wrong access pattern. That is
// tolerable ONLY because the census informs a one-time encoding choice rather
// than gating correctness — the readers themselves are held by the equality
// tests, which compare results and not access patterns.
func pagesTouchedIn16KiB(seg searchengine.Segment[Query, *CorpusStats], q Query) int {
	ms, ok := seg.(*mappedSegment)
	if !ok {
		return -1
	}
	pages := make(map[int]struct{})
	touch := func(off, n int) {
		if n <= 0 {
			return
		}
		for p := off / pageBytes; p <= (off+n-1)/pageBytes; p++ {
			pages[p] = struct{}{}
		}
	}
	for _, term := range sortedQueryTerms(q) {
		for _, mf := range ms.fields {
			recordProbe(mf, term, touch)
		}
	}
	return len(pages)
}

// recordProbe replays one field's dictionary probe for term, reporting every
// byte range the real lookup reads, then the posting run on a hit.
//
// COMPARING A TERM READS THAT TERM'S BYTES, and those bytes live in a different
// region from the row that points at them — so every comparison touches TWO
// places, not one. An earlier version of this census counted only the rows, and
// the tell was the result: the three encodings came out within 16% of each other
// on pages, where the design's measurement separated them by 2.4x. A census that
// collapses the very difference the encodings exist to create is measuring its
// own shape rather than theirs. The row-plus-term pairing below is what makes
// the front-coded encoding's advantage visible: its suffixes are INLINE in the
// block it already read, so it does not pay that second region at all.
func recordProbe(mf *mappedField, term string, touch func(off, n int)) {
	// entryRow records the row AND the term bytes that row points at.
	entryRow := func(i int) string {
		at := mf.entriesOff + v2FlatEntrySize*i
		touch(at, v2FlatEntrySize)
		termOff := int(binary.LittleEndian.Uint32(mf.blob[at:]))
		termLen := int(binary.LittleEndian.Uint32(mf.blob[at+4:]))
		touch(termOff, termLen)
		got, _, _ := mf.entryAt(i)
		return got
	}
	firstTermRow := func(b int) string {
		at := mf.firstTermsOff + 8*b
		touch(at, 8)
		touch(int(binary.LittleEndian.Uint32(mf.blob[at:])), int(binary.LittleEndian.Uint32(mf.blob[at+4:])))
		return mf.firstTermAt(b)
	}

	switch mf.kind {
	case dictBlocked:
		lo, hi := 0, mf.blockCount
		for lo < hi {
			mid := (lo + hi) / 2
			if firstTermRow(mid) <= term {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo > 0 {
			b := lo - 1
			touch(mf.blockIdxOff+4*b, 4)
			touchBlockScan(mf, b, term, touch)
		}
	case dictHash:
		if mf.slotCount > 0 {
			h := termHash(term)
			mask := uint64(mf.slotCount - 1)
			slot := h & mask
			fingerprint := uint32(h >> 32)
			for range mf.slotCount {
				at := mf.slotsOff + v2HashSlotSize*int(slot)
				touch(at, v2HashSlotSize)
				idx := binary.LittleEndian.Uint32(mf.blob[at+4:])
				if idx == hashEmptySlot {
					break
				}
				// A fingerprint hit confirms against the real entry, which
				// reads the row and its term bytes.
				if binary.LittleEndian.Uint32(mf.blob[at:]) == fingerprint && int(idx) < mf.termCount {
					if entryRow(int(idx)) == term {
						break
					}
				}
				slot = (slot + 1) & mask
			}
		}
	default:
		lo, hi := 0, mf.termCount
		for lo < hi {
			mid := (lo + hi) / 2
			switch got := entryRow(mid); {
			case got == term:
				lo, hi = mid, mid
			case got < term:
				lo = mid + 1
			default:
				hi = mid
			}
		}
	}
	if off, count, ok := termExtent(mf, term); ok {
		touch(off, 6*count)
	}
}

// touchBlockScan replays the front-coded scan of block b, recording only the
// bytes the scan actually CONSUMES — it stops on a match or once the terms have
// ascended past the one sought, exactly as the reader does. Counting the whole
// block payload instead would charge the encoding for bytes it never reads, and
// on a block of long identifiers that is roughly a factor of two.
func touchBlockScan(mf *mappedField, b int, term string, touch func(off, n int)) {
	at := int(binary.LittleEndian.Uint32(mf.blob[mf.blockIdxOff+4*b:]))
	touch(at, 8) // the first term's posting pointer
	if mf.firstTermAt(b) == term {
		return
	}
	at += 8
	scratch := append(make([]byte, 0, 64), mf.firstTermAt(b)...)
	start := b * blockedBlockTerms
	end := min(start+blockedBlockTerms, mf.termCount)
	for range end - start - 1 {
		shared := int(binary.LittleEndian.Uint16(mf.blob[at:]))
		suffixLen := int(binary.LittleEndian.Uint16(mf.blob[at+2:]))
		touch(at, 4+suffixLen+8)
		at += 4
		scratch = append(scratch[:shared], mf.blob[at:at+suffixLen]...)
		at += suffixLen + 8
		if got := string(scratch); got == term || got > term {
			return
		}
	}
}

// termExtent returns where a term's posting run lives, dispatching exactly as
// lookup does. Test-only: the shipped reader hands back views, not offsets.
func termExtent(mf *mappedField, term string) (off, count int, ok bool) {
	switch mf.kind {
	case dictBlocked:
		return mf.lookupBlocked(term)
	case dictHash:
		return mf.lookupHash(term)
	default:
		return mf.lookupFlat(term)
	}
}

// BenchmarkDictionaryChoice replays the real query trace against a real corpus
// once per dictionary encoding and reports what the encoding decision is made
// on: latency distribution, allocations, whole-corpus blob bytes, and the mean
// distinct pages a query touches per segment.
//
// It is INERT unless both BM25_TRACE and BM25_CORPUS point at real inputs, so
