// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"container/heap"
	"encoding/binary"
	"slices"
	"unsafe"
)

// dictIter walks one field's dictionary in ascending term order, holding four
// words of state and hydrating nothing. It is the unit the k-way merge is built
// from: a merge over N inputs is N iterators in a heap, never N hydrated maps.
//
// term is valid until the NEXT call to next() ON THIS ITERATOR. The flat and
// hash encodings hand back a view into the blob, which would outlive the call,
// but the blocked encoding reconstructs each term into a buffer the following
// term overwrites — so the shorter guarantee is the one every caller must obey.
type dictIter struct {
	mf *mappedField
	// idx is the index of the term most recently returned, plus one.
	idx int
	// at is the blocked encoding's cursor into the current block payload.
	at int
	// scratch carries the blocked encoding's reconstructed term forward.
	scratch []byte
	term    string
	postOff int
	count   int
}

// iter returns a fresh iterator positioned before the first term.
func (mf *mappedField) iter() *dictIter { return &dictIter{mf: mf} }

// next advances to the next term, reporting false once the dictionary is
// exhausted.
func (it *dictIter) next() bool {
	if it.idx >= it.mf.termCount {
		return false
	}
	if it.mf.kind == dictBlocked {
		it.nextBlocked()
	} else {
		it.term, it.postOff, it.count = it.mf.entryAt(it.idx)
	}
	it.idx++
	return true
}

// nextBlocked reconstructs the next front-coded term. The first term of a block
// is stored whole in the first-term index; every later one stores only the bytes
// on which it diverges from its predecessor.
func (it *dictIter) nextBlocked() {
	b := it.mf.blob
	if it.idx%blockedBlockTerms == 0 {
		block := it.idx / blockedBlockTerms
		it.at = int(binary.LittleEndian.Uint32(b[it.mf.blockIdxOff+4*block:]))
		it.scratch = append(it.scratch[:0], it.mf.firstTermAt(block)...)
	} else {
		shared := int(binary.LittleEndian.Uint16(b[it.at:]))
		suffixLen := int(binary.LittleEndian.Uint16(b[it.at+2:]))
		it.at += 4
		it.scratch = append(it.scratch[:shared], b[it.at:it.at+suffixLen]...)
		it.at += suffixLen
	}
	it.postOff = int(binary.LittleEndian.Uint32(b[it.at:]))
	it.count = int(binary.LittleEndian.Uint32(b[it.at+4:]))
	it.at += 8
	//nolint:gosec // view over the local reconstruction buffer, documented as valid until the next next()
	it.term = unsafe.String(unsafe.SliceData(it.scratch), len(it.scratch))
}

// eachTerm walks every term in the field's dictionary in ASCENDING term order,
// with that term's posting run. Sorted order is a property of all three
// encodings and is what lets a merge stream several dictionaries together.
//
// The term handed to fn is valid ONLY for the duration of the call, for the
// reason dictIter documents. A caller that retains a term must copy it.
func (mf *mappedField) eachTerm(fn func(term string, docIDs []uint32, tfs []uint16)) {
	it := mf.iter()
	for it.next() {
		docIDs, tfs := mf.postings(it.postOff, it.count)
		fn(it.term, docIDs, tfs)
	}
}

// termCursor is one (input, field) position in the k-way merge.
type termCursor struct {
	input int
	field int
	it    *dictIter
}

// cursorHeap orders cursors by (term, field, input). That total order is what
// makes the merged output deterministic and ascending BY CONSTRUCTION rather
// than by a sort: for one term the fields arrive in field order, within a field
// the inputs arrive in input order, and new document ids were assigned by
// walking the inputs in that same order — so each merged posting run comes out
// ascending without ever being sorted.
type cursorHeap []*termCursor

func (h cursorHeap) Len() int { return len(h) }

func (h cursorHeap) Less(i, j int) bool {
	if h[i].it.term != h[j].it.term {
		return h[i].it.term < h[j].it.term
	}
	if h[i].field != h[j].field {
		return h[i].field < h[j].field
	}
	return h[i].input < h[j].input
}

func (h cursorHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *cursorHeap) Push(x any) {
	c, _ := x.(*termCursor)
	*h = append(*h, c)
}

func (h *cursorHeap) Pop() any {
	old := *h
	n := len(old)
	c := old[n-1]
	*h = old[:n-1]
	return c
}

// mergeWalk drives the k-way merge over every input's every field dictionary.
//
// onField is called once per (term, field) that has at least one SURVIVING
// posting, with the remapped document ids and their term frequencies; onTerm is
// called once per term that survives in any field, with the number of DISTINCT
// documents carrying it — the merged segment's document frequency for that term.
//
// A term whose every posting the filter drops reaches NEITHER callback. That is
// the behaviour a fresh build of the same surviving documents produces, and
// getting it wrong is what a naive port does: the terms are still present in the
// inputs' dictionaries, so a merge that emits them puts entries in the output
// that no document justifies.
//
// term is a view into a reused buffer, valid only for the duration of each call.
// The slices handed to onField are reused between calls for the same reason.
func mergeWalk(
	ins []*mappedSegment, remap [][]int32,
	onField func(term string, field int, docIDs []uint32, tfs []uint16),
	onTerm func(term string, df int64),
) {
	h := make(cursorHeap, 0, len(ins)*len(defaultFieldConfigs))
	for i, ms := range ins {
		for f, mf := range ms.fields {
			it := mf.iter()
			if it.next() {
				h = append(h, &termCursor{input: i, field: f, it: it})
			}
		}
	}
	heap.Init(&h)

	// Reused across every term, so the walk allocates nothing per term. Sizes
	// converge on the widest posting run and the most-carried term.
	var termBuf []byte
	var docBuf []uint32
	var tfBuf []uint16
	var dfBuf []uint32

	for len(h) > 0 {
		termBuf = append(termBuf[:0], h[0].it.term...)
		//nolint:gosec // view over the reused term buffer; valid for this term's group only
		term := unsafe.String(unsafe.SliceData(termBuf), len(termBuf))
		dfBuf = dfBuf[:0]
		for len(h) > 0 && h[0].it.term == term {
			field := h[0].field
			docBuf, tfBuf = docBuf[:0], tfBuf[:0]
			for len(h) > 0 && h[0].it.term == term && h[0].field == field {
				docBuf, tfBuf = drainCursor(&h, remap, docBuf, tfBuf)
			}
			if len(docBuf) > 0 {
				onField(term, field, docBuf, tfBuf)
				dfBuf = append(dfBuf, docBuf...)
			}
		}
		if len(dfBuf) > 0 {
			slices.Sort(dfBuf)
			dfBuf = slices.Compact(dfBuf)
			onTerm(term, int64(len(dfBuf)))
		}
	}
}

// drainCursor appends the surviving, remapped postings of the heap's current
// cursor and advances it, popping the cursor once its dictionary is exhausted.
func drainCursor(h *cursorHeap, remap [][]int32, docBuf []uint32, tfBuf []uint16) ([]uint32, []uint16) {
	c := (*h)[0]
	docIDs, tfs := c.it.mf.postings(c.it.postOff, c.it.count)
	rm := remap[c.input]
	for k, d := range docIDs {
		if int(d) < len(rm) && rm[d] >= 0 {
			docBuf = append(docBuf, uint32(rm[d]))
			tfBuf = append(tfBuf, tfs[k])
		}
	}
	if c.it.next() {
		heap.Fix(h, 0)
	} else {
		heap.Pop(h)
	}
	return docBuf, tfBuf
}
