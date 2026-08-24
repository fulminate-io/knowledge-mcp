// SPDX-License-Identifier: Apache-2.0

package bm25

import "encoding/binary"

// mergeEmitter turns the k-way merge's callbacks into bytes. Posting runs, term
// bytes and front-coded block payloads are APPENDED to the tail as they are
// produced, and the dictionary rows that point at them are patched back into the
// fixed prefix — so no tail offset is ever predicted, only observed.
//
// What it holds is bounded by the widest posting run and, for the front-coded
// encoding, by one 32-term block per field. Nothing it holds grows with the
// corpus.
type mergeEmitter struct {
	w *mergeWriter
	p *mergePlan
	// next is the index of the next entry row to write, per field.
	next []int
	// dfNext is the index of the next docFreq row to write.
	dfNext int
	// scratch is reused to serialize each posting run.
	scratch []byte

	// Front-coded state, per field: the block being assembled, the previous
	// term it front-codes against, and how many terms it holds.
	payload [][]byte
	prev    [][]byte
	inBlock []int

	// hashes holds each term's FULL hash, per field, for the accelerator. The
	// full value is kept rather than just the stored fingerprint because probe
	// placement masks the hash's LOW bits while the fingerprint is its high
	// word — keeping only one of the two would not let placement agree with the
	// reader. Eight bytes per term, against a whole slot table per field.
	hashes [][]uint64
}

// newMergeEmitter prepares per-field state for a plan.
func newMergeEmitter(w *mergeWriter, p *mergePlan) *mergeEmitter {
	nf := len(defaultFieldConfigs)
	e := &mergeEmitter{w: w, p: p, next: make([]int, nf)}
	switch p.kind {
	case dictBlocked:
		e.payload = make([][]byte, nf)
		e.prev = make([][]byte, nf)
		e.inBlock = make([]int, nf)
	case dictHash:
		e.hashes = make([][]uint64, nf)
		for i := range nf {
			e.hashes[i] = make([]uint64, 0, p.termCount[i])
		}
	}
	return e
}

// field emits one (term, field) group: the posting run first, then the
// dictionary row that points at it.
func (e *mergeEmitter) field(term string, field int, docIDs []uint32, tfs []uint16) {
	postOff := e.appendPostings(docIDs, tfs)
	if e.p.kind == dictBlocked {
		e.appendBlocked(term, field, postOff, len(docIDs))
		return
	}
	termOff := e.w.appendStr(term, 1)
	i := e.next[field]
	row := e.p.entriesOff[field] + v2FlatEntrySize*i
	e.w.patchU32(row, uint32(termOff))
	e.w.patchU32(row+4, uint32(len(term)))
	e.w.patchU32(row+8, uint32(postOff))
	e.w.patchU32(row+12, uint32(len(docIDs)))
	if e.p.kind == dictHash {
		e.hashes[field] = append(e.hashes[field], termHash(term))
	}
	e.next[field] = i + 1
}

// appendPostings writes a run of ascending document ids followed by their term
// frequencies. The two runs are adjacent so a short list is one page, and the
// run starts 4-aligned so the reader can view the ids without copying.
func (e *mergeEmitter) appendPostings(docIDs []uint32, tfs []uint16) int {
	n := len(docIDs)
	need := 6 * n
	if cap(e.scratch) < need {
		e.scratch = make([]byte, need)
	}
	buf := e.scratch[:need]
	for i, d := range docIDs {
		binary.LittleEndian.PutUint32(buf[4*i:], d)
		binary.LittleEndian.PutUint16(buf[4*n+2*i:], tfs[i])
	}
	return e.w.appendAligned(buf, 4)
}

// appendBlocked adds one term to its field's pending front-coded block, opening
// a block when the previous one filled and closing it when this one does.
func (e *mergeEmitter) appendBlocked(term string, field, postOff, count int) {
	if e.inBlock[field] == 0 {
		block := e.next[field] / blockedBlockTerms
		termOff := e.w.appendStr(term, 1)
		row := e.p.firstTermsOff[field] + 8*block
		e.w.patchU32(row, uint32(termOff))
		e.w.patchU32(row+4, uint32(len(term)))
		e.payload[field] = e.payload[field][:0]
		e.payload[field] = appendU32(e.payload[field], uint32(postOff))
		e.payload[field] = appendU32(e.payload[field], uint32(count))
	} else {
		shared := sharedPrefixLen(string(e.prev[field]), term)
		e.payload[field] = binary.LittleEndian.AppendUint16(e.payload[field], uint16(shared))
		e.payload[field] = binary.LittleEndian.AppendUint16(e.payload[field], uint16(len(term)-shared))
		e.payload[field] = append(e.payload[field], term[shared:]...)
		e.payload[field] = appendU32(e.payload[field], uint32(postOff))
		e.payload[field] = appendU32(e.payload[field], uint32(count))
	}
	e.prev[field] = append(e.prev[field][:0], term...)
	e.next[field]++
	e.inBlock[field]++
	if e.inBlock[field] == blockedBlockTerms {
		e.closeBlock(field)
	}
}

// closeBlock appends a finished block payload and records where it landed.
func (e *mergeEmitter) closeBlock(field int) {
	block := (e.next[field] - 1) / blockedBlockTerms
	at := e.w.appendAligned(e.payload[field], 1)
	e.w.patchU32(e.p.blockIdxOff[field]+4*block, uint32(at))
	e.inBlock[field] = 0
}

// appendU32 appends a little-endian word.
func appendU32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

// term emits one docFreq row: how many DISTINCT surviving documents carry the
// term across every field.
func (e *mergeEmitter) term(term string, df int64) {
	termOff := e.w.appendStr(term, 1)
	row := e.p.dfEntriesOff + v2DocFreqRowSize*e.dfNext
	e.w.patchU32(row, uint32(termOff))
	e.w.patchU32(row+4, uint32(len(term)))
	e.w.patchU64(row+8, uint64(df))
	e.dfNext++
}

// flushBlocks closes any partly-filled front-coded block and builds the hash
// accelerator. Both are end-of-walk work: a block is only known to be the last
// one once the merge is exhausted, and the accelerator's placement depends on
// every term's index.
func (e *mergeEmitter) flushBlocks() {
	if e.p.kind == dictBlocked {
		for field := range e.inBlock {
			if e.inBlock[field] > 0 {
				e.closeBlock(field)
			}
		}
		return
	}
	if e.p.kind != dictHash {
		return
	}
	// One field's slot table at a time, released before the next is built, so
	// the accelerator never costs more than the widest single field.
	for field, hs := range e.hashes {
		slots := e.p.slotCount[field]
		if slots == 0 {
			continue
		}
		table := make([]byte, v2HashSlotSize*slots)
		for s := range slots {
			binary.LittleEndian.PutUint32(table[v2HashSlotSize*s+4:], hashEmptySlot)
		}
		mask := uint64(slots - 1)
		for i, h := range hs {
			// Seeded and probed exactly as the reader does — masked low bits of
			// the full hash for the slot, high word as the stored fingerprint —
			// and filled in ascending term order, so the same terms always
			// produce the same table.
			slot := h & mask
			for {
				at := v2HashSlotSize * int(slot)
				if binary.LittleEndian.Uint32(table[at+4:]) == hashEmptySlot {
					binary.LittleEndian.PutUint32(table[at:], uint32(h>>32))
					binary.LittleEndian.PutUint32(table[at+4:], uint32(i))
					break
				}
				slot = (slot + 1) & mask
			}
		}
		e.w.patch(e.p.slotsOff[field], table)
	}
}
