// SPDX-License-Identifier: Apache-2.0

package bm25

import "encoding/binary"

// v2DictPlan is one field's computed dictionary region. Which members are
// populated depends on the kind; the shared ones are off (the dictionary header,
// which the field table points at) and termsOff (the term-bytes region).
type v2DictPlan struct {
	// off is the 8-aligned dictionary header, the fieldEntry's dictOff.
	off int
	// termsOff starts the term-bytes region. Flat and hash store every term
	// there; blocked stores only each block's first term, since the rest are
	// front-coded suffixes living inside their block.
	termsOff int
	// termOff is the absolute offset of each STORED term: one per term for flat
	// and hash, one per block for blocked.
	termOff []int
	// entriesOff starts the {termOff, termLen, postOff, postCount} rows (flat, hash).
	entriesOff int
	// slotsOff and slotCount describe the open-addressed accelerator (hash only).
	// slotCount is a power of two so a probe masks rather than divides.
	slotsOff  int
	slotCount int
	// blockCount, blockIdxOff and blockOff describe the front-coded blocks
	// (blocked only). blockIdxOff holds blockCount u32 payload offsets.
	blockCount  int
	blockIdxOff int
	blockOff    []int
}

// sharedPrefixLen returns how many leading bytes a and b have in common. Front
// coding stores only the divergent tail, which is what makes the blocked
// dictionary the only encoding smaller than the map form it replaces.
func sharedPrefixLen(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// planDict computes one field's dictionary region starting at off, returning the
// plan and the offset just past it. Sizes depend only on the terms, never on
// where the posting blocks land, which is what lets the caller lay the postings
// out afterwards in the same single forward pass.
func planDict(kind byte, off int, terms []string) (v2DictPlan, int) {
	switch kind {
	case dictBlocked:
		return planBlockedDict(off, terms)
	case dictHash:
		return planHashDict(off, terms)
	default:
		return planFlatDict(off, terms)
	}
}

// planFlatDict lays out the term bytes followed by the sorted entry rows.
func planFlatDict(off int, terms []string) (v2DictPlan, int) {
	var d v2DictPlan
	d.termsOff = off
	d.termOff = make([]int, len(terms))
	for i, term := range terms {
		d.termOff[i] = off
		off += len(term)
	}
	d.off = align(off, 8)
	// Header: termCount, entriesOff.
	d.entriesOff = d.off + 8
	off = d.entriesOff + v2FlatEntrySize*len(terms)
	return d, off
}

// planHashDict is the flat layout plus an accelerator table sized to at least
// twice the term count so linear probing stays short.
func planHashDict(off int, terms []string) (v2DictPlan, int) {
	var d v2DictPlan
	d.termsOff = off
	d.termOff = make([]int, len(terms))
	for i, term := range terms {
		d.termOff[i] = off
		off += len(term)
	}
	d.off = align(off, 8)
	// Header: termCount, entriesOff, slotsOff, slotCount.
	d.entriesOff = d.off + 16
	off = d.entriesOff + v2FlatEntrySize*len(terms)
	d.slotCount = hashSlotCount(len(terms))
	d.slotsOff = align(off, 8)
	off = d.slotsOff + v2HashSlotSize*d.slotCount
	return d, off
}

// hashSlotCount returns the accelerator size: a power of two at least twice the
// term count, so the table stays at most half full and probes stay short. A
// field with no terms still gets one slot, keeping the mask arithmetic total.
func hashSlotCount(terms int) int {
	n := 1
	for n < 2*terms {
		n <<= 1
	}
	return n
}

// planBlockedDict lays out each block's first term, then a block index, then the
// front-coded block payloads.
func planBlockedDict(off int, terms []string) (v2DictPlan, int) {
	var d v2DictPlan
	d.blockCount = (len(terms) + blockedBlockTerms - 1) / blockedBlockTerms
	d.termsOff = off
	d.termOff = make([]int, d.blockCount)
	for b := range d.blockCount {
		first := terms[b*blockedBlockTerms]
		d.termOff[b] = off
		off += len(first)
	}
	d.off = align(off, 8)
	// Header: termCount, blockCount, firstTermsOff, blockIdxOff.
	firstTermRowsOff := d.off + 16
	off = firstTermRowsOff + 8*d.blockCount
	d.blockIdxOff = align(off, 4)
	off = d.blockIdxOff + 4*d.blockCount
	d.blockOff = make([]int, d.blockCount)
	for b := range d.blockCount {
		d.blockOff[b] = off
		off += blockedPayloadSize(terms, b)
	}
	return d, off
}

// blockedPayloadSize is the byte length of block b's payload. The first term of
// a block is addressed by the first-term index, so it contributes only its
// posting pointer; every later term contributes a front-coded header, its
// divergent suffix bytes, and its posting pointer.
func blockedPayloadSize(terms []string, b int) int {
	start := b * blockedBlockTerms
	end := min(start+blockedBlockTerms, len(terms))
	size := 8
	for i := start + 1; i < end; i++ {
		shared := sharedPrefixLen(terms[i-1], terms[i])
		size += 2 + 2 + (len(terms[i]) - shared) + 8
	}
	return size
}

// writeDict emits one field's dictionary. postOff and postCount are parallel to
// terms and were computed by the same planning pass.
func writeDict(buf []byte, kind byte, d v2DictPlan, terms []string, postOff, postCount []int) {
	switch kind {
	case dictBlocked:
		writeBlockedDict(buf, d, terms, postOff, postCount)
	case dictHash:
		writeHashDict(buf, d, terms, postOff, postCount)
	default:
		writeFlatDict(buf, d, terms, postOff, postCount)
	}
}

// writeFlatEntries emits the term bytes and the sorted entry rows shared by the
// flat and hash encodings.
func writeFlatEntries(buf []byte, d v2DictPlan, terms []string, postOff, postCount []int) {
	for i, term := range terms {
		copy(buf[d.termOff[i]:], term)
		row := d.entriesOff + v2FlatEntrySize*i
		binary.LittleEndian.PutUint32(buf[row:], uint32(d.termOff[i]))
		binary.LittleEndian.PutUint32(buf[row+4:], uint32(len(term)))
		binary.LittleEndian.PutUint32(buf[row+8:], uint32(postOff[i]))
		binary.LittleEndian.PutUint32(buf[row+12:], uint32(postCount[i]))
	}
}

// writeFlatDict emits the header, term bytes and entry rows.
func writeFlatDict(buf []byte, d v2DictPlan, terms []string, postOff, postCount []int) {
	binary.LittleEndian.PutUint32(buf[d.off:], uint32(len(terms)))
	binary.LittleEndian.PutUint32(buf[d.off+4:], uint32(d.entriesOff))
	writeFlatEntries(buf, d, terms, postOff, postCount)
}

// writeHashDict emits the flat rows plus the accelerator. Slots are filled in
// ascending term order, so an identical term set always produces an identical
// table — the probe sequence is a function of the fingerprints alone and no map
// iteration reaches the bytes.
func writeHashDict(buf []byte, d v2DictPlan, terms []string, postOff, postCount []int) {
	binary.LittleEndian.PutUint32(buf[d.off:], uint32(len(terms)))
	binary.LittleEndian.PutUint32(buf[d.off+4:], uint32(d.entriesOff))
	binary.LittleEndian.PutUint32(buf[d.off+8:], uint32(d.slotsOff))
	binary.LittleEndian.PutUint32(buf[d.off+12:], uint32(d.slotCount))
	writeFlatEntries(buf, d, terms, postOff, postCount)

	for s := range d.slotCount {
		binary.LittleEndian.PutUint32(buf[d.slotsOff+v2HashSlotSize*s+4:], hashEmptySlot)
	}
	mask := uint64(d.slotCount - 1)
	for i, term := range terms {
		h := termHash(term)
		slot := h & mask
		for {
			at := d.slotsOff + v2HashSlotSize*int(slot)
			if binary.LittleEndian.Uint32(buf[at+4:]) == hashEmptySlot {
				binary.LittleEndian.PutUint32(buf[at:], uint32(h>>32))
				binary.LittleEndian.PutUint32(buf[at+4:], uint32(i))
				break
			}
			slot = (slot + 1) & mask
		}
	}
}

// writeBlockedDict emits the header, the per-block first terms, the block index
// and the front-coded payloads.
func writeBlockedDict(buf []byte, d v2DictPlan, terms []string, postOff, postCount []int) {
	binary.LittleEndian.PutUint32(buf[d.off:], uint32(len(terms)))
	binary.LittleEndian.PutUint32(buf[d.off+4:], uint32(d.blockCount))
	firstTermRowsOff := d.off + 16
	binary.LittleEndian.PutUint32(buf[d.off+8:], uint32(firstTermRowsOff))
	binary.LittleEndian.PutUint32(buf[d.off+12:], uint32(d.blockIdxOff))

	for b := range d.blockCount {
		first := terms[b*blockedBlockTerms]
		copy(buf[d.termOff[b]:], first)
		row := firstTermRowsOff + 8*b
		binary.LittleEndian.PutUint32(buf[row:], uint32(d.termOff[b]))
		binary.LittleEndian.PutUint32(buf[row+4:], uint32(len(first)))
		binary.LittleEndian.PutUint32(buf[d.blockIdxOff+4*b:], uint32(d.blockOff[b]))
		writeBlockedPayload(buf, d.blockOff[b], b, terms, postOff, postCount)
	}
}

// writeBlockedPayload emits one block: the first term's posting pointer, then
// each later term as {sharedLen, suffixLen, suffix bytes, postOff, postCount}.
func writeBlockedPayload(buf []byte, at, b int, terms []string, postOff, postCount []int) {
	start := b * blockedBlockTerms
	end := min(start+blockedBlockTerms, len(terms))
	binary.LittleEndian.PutUint32(buf[at:], uint32(postOff[start]))
	binary.LittleEndian.PutUint32(buf[at+4:], uint32(postCount[start]))
	at += 8
	for i := start + 1; i < end; i++ {
		shared := sharedPrefixLen(terms[i-1], terms[i])
		suffix := terms[i][shared:]
		binary.LittleEndian.PutUint16(buf[at:], uint16(shared))
		binary.LittleEndian.PutUint16(buf[at+2:], uint16(len(suffix)))
		at += 4
		copy(buf[at:], suffix)
		at += len(suffix)
		binary.LittleEndian.PutUint32(buf[at:], uint32(postOff[i]))
		binary.LittleEndian.PutUint32(buf[at+4:], uint32(postCount[i]))
		at += 8
	}
}
