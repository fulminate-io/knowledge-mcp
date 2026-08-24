// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// mergeSegmentsV2 consolidates several offset-addressed segments into one,
// streaming the output to a temp file instead of assembling it on the heap.
//
// The map-shaped merge it replaces built the whole merged segment in Go maps
// before encoding it, so its peak was the accumulator PLUS the output. This one
// holds a cursor per input dictionary, a reused posting buffer, and nothing that
// grows with the corpus; the output exists only as bytes in a file until it is
// read back once at the end.
//
// The returned segment is HEAP-backed: the temp file is read back after the last
// patch and UNLINKED before this returns, on the success path and on every error
// path, so nothing outside this call may depend on the file's name. Publishing
// that payload as a mapping of a cache file — which is what makes merge
// retention actually zero — is the distribution layer's job, not the format's;
// this package creates no mappings and contains no platform code.
func mergeSegmentsV2(ins []*mappedSegment, accept []func(searchengine.ExternalID) bool, kind byte) (*mappedSegment, error) {
	if kind > dictHash {
		return nil, fmt.Errorf("bm25 merge: unknown dictionary kind %d", kind)
	}
	blob, err := streamMergeToBlob(ins, accept, kind)
	if err != nil {
		return nil, err
	}
	return openSegmentV2(blob)
}

// streamMergeToBlob runs the streamed merge and reads the finished file back.
// The read-back is the ONE whole-output allocation in the merge, and it is the
// segment's own payload rather than writer scratch.
func streamMergeToBlob(ins []*mappedSegment, accept []func(searchengine.ExternalID) bool, kind byte) ([]byte, error) {
	f, err := os.CreateTemp("", "bm25-merge-*.seg")
	if err != nil {
		return nil, fmt.Errorf("bm25 merge: create temp: %w", err)
	}
	// Unlink and close on EVERY path. The file is an implementation detail of
	// this call and must not outlive it, including when the merge fails.
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()

	if err := streamMergeToFile(f, ins, accept, kind); err != nil {
		return nil, err
	}
	size, err := f.Seek(0, 2)
	if err != nil {
		return nil, fmt.Errorf("bm25 merge: size temp: %w", err)
	}
	blob := make([]byte, size)
	if _, err := f.ReadAt(blob, 0); err != nil {
		return nil, fmt.Errorf("bm25 merge: read back: %w", err)
	}
	return blob, nil
}

// mergePlan is the merged segment's fixed prefix: every section whose size is
// known once the surviving members and the surviving per-field term counts are,
// which is what the counting pass produces. Everything else — term bytes,
// posting runs, front-coded block payloads — is appended to the tail as it is
// produced, so no offset in the tail has to be predicted.
type mergePlan struct {
	kind             byte
	members          []string
	memberOffsetsOff int
	memberOff        []int
	fieldTableOff    int
	nameOff          []int
	docLengthsOff    []int
	dictOff          []int
	entriesOff       []int
	slotsOff         []int
	slotCount        []int
	firstTermsOff    []int
	blockIdxOff      []int
	termCount        []int
	dfDictOff        int
	dfEntriesOff     int
	dfCount          int
	// prefixEnd is where the streamed tail begins.
	prefixEnd int
}

// planMerge lays out the fixed prefix from the counting pass's results.
func planMerge(kind byte, members []string, termCount []int, dfCount int) *mergePlan {
	p := &mergePlan{kind: kind, members: members, termCount: termCount, dfCount: dfCount}
	nf := len(defaultFieldConfigs)
	off := v2HeaderSize

	p.memberOffsetsOff = align(off, 4)
	off = p.memberOffsetsOff + 4*(len(members)+1)
	p.memberOff = make([]int, 0, len(members)+1)
	for _, id := range members {
		p.memberOff = append(p.memberOff, off)
		off += len(id)
	}
	p.memberOff = append(p.memberOff, off)

	p.fieldTableOff = align(off, 8)
	off = p.fieldTableOff + v2FieldEntrySize*nf

	p.nameOff = make([]int, nf)
	for i, cfg := range defaultFieldConfigs {
		p.nameOff[i] = off
		off += len(cfg.Name)
	}
	p.docLengthsOff = make([]int, nf)
	for i := range nf {
		p.docLengthsOff[i] = align(off, 4)
		off = p.docLengthsOff[i] + 4*len(members)
	}

	p.dictOff = make([]int, nf)
	p.entriesOff = make([]int, nf)
	p.slotsOff = make([]int, nf)
	p.slotCount = make([]int, nf)
	p.firstTermsOff = make([]int, nf)
	p.blockIdxOff = make([]int, nf)
	for i := range nf {
		off = p.planFieldDict(i, off)
	}

	p.dfDictOff = align(off, 8)
	off = p.dfDictOff + 8
	p.dfEntriesOff = align(off, 8)
	off = p.dfEntriesOff + v2DocFreqRowSize*dfCount

	p.prefixEnd = align(off, 8)
	return p
}

// planFieldDict places one field's dictionary structures, whose shapes differ by
// encoding but whose SIZES are all fixed by the surviving term count.
func (p *mergePlan) planFieldDict(i, off int) int {
	n := p.termCount[i]
	p.dictOff[i] = align(off, 8)
	switch p.kind {
	case dictBlocked:
		blocks := (n + blockedBlockTerms - 1) / blockedBlockTerms
		p.firstTermsOff[i] = p.dictOff[i] + 16
		off = p.firstTermsOff[i] + 8*blocks
		p.blockIdxOff[i] = align(off, 4)
		return p.blockIdxOff[i] + 4*blocks
	case dictHash:
		p.entriesOff[i] = p.dictOff[i] + 16
		off = p.entriesOff[i] + v2FlatEntrySize*n
		p.slotCount[i] = hashSlotCount(n)
		p.slotsOff[i] = align(off, 8)
		return p.slotsOff[i] + v2HashSlotSize*p.slotCount[i]
	default:
		p.entriesOff[i] = p.dictOff[i] + 8
		return p.entriesOff[i] + v2FlatEntrySize*n
	}
}

// mergeWriter appends to the tail of an output file and patches earlier offsets
// in place. Errors are held and reported once at the end rather than checked at
// every store: a partial file is discarded whole, so the first failure is the
// only one that carries information.
type mergeWriter struct {
	f    *os.File
	tail int64
	err  error
	// strBuf carries string payloads to WriteAt without a per-call conversion.
	// Terms and member ids are written one at a time and in huge numbers, so a
	// []byte(s) at each of them would allocate proportionally to the corpus —
	// which is exactly the cost this writer exists to avoid.
	strBuf []byte
}

// patch writes b at an already-planned offset in the fixed prefix.
func (w *mergeWriter) patch(off int, b []byte) {
	if w.err != nil {
		return
	}
	if _, err := w.f.WriteAt(b, int64(off)); err != nil {
		w.err = fmt.Errorf("bm25 merge: patch at %d: %w", off, err)
	}
}

func (w *mergeWriter) patchU16(off int, v uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	w.patch(off, b[:])
}

func (w *mergeWriter) patchU32(off int, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	w.patch(off, b[:])
}

func (w *mergeWriter) patchU64(off int, v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	w.patch(off, b[:])
}

// appendAligned writes b at the next tail offset that satisfies alignTo and
// returns where it landed. Gaps left by alignment read back as zeros, which is
// what keeps the output byte-identical between runs.
func (w *mergeWriter) appendAligned(b []byte, alignTo int) int {
	if w.err != nil {
		return 0
	}
	at := int64(align(int(w.tail), alignTo))
	if _, err := w.f.WriteAt(b, at); err != nil {
		w.err = fmt.Errorf("bm25 merge: append at %d: %w", at, err)
		return 0
	}
	w.tail = at + int64(len(b))
	return int(at)
}

// appendStr appends a string's bytes through the writer's reusable buffer.
func (w *mergeWriter) appendStr(s string, alignTo int) int {
	w.strBuf = append(w.strBuf[:0], s...)
	return w.appendAligned(w.strBuf, alignTo)
}

// patchStr writes a string's bytes at a planned offset.
func (w *mergeWriter) patchStr(off int, s string) {
	w.strBuf = append(w.strBuf[:0], s...)
	w.patch(off, w.strBuf)
}

// streamMergeToFile writes the whole merged segment into f. It runs the k-way
// merge TWICE over the same mapped inputs: once to count surviving terms, which
// is all the fixed prefix's sizes depend on, and once to write. Both passes
// apply the SAME accept filter, so a term the filter empties is absent from both
// — the omission is true by construction rather than repaired afterwards.
func streamMergeToFile(f *os.File, ins []*mappedSegment, accept []func(searchengine.ExternalID) bool, kind byte) error {
	members, remap := resolveMergeLayout(ins, accept)

	termCount := make([]int, len(defaultFieldConfigs))
	dfCount := 0
	mergeWalk(ins, remap,
		func(_ string, field int, _ []uint32, _ []uint16) { termCount[field]++ },
		func(string, int64) { dfCount++ })

	p := planMerge(kind, members, termCount, dfCount)
	w := &mergeWriter{f: f, tail: int64(p.prefixEnd)}
	writeMergePrefix(w, p, ins, remap)

	e := newMergeEmitter(w, p)
	mergeWalk(ins, remap, e.field, e.term)
	e.flushBlocks()

	if w.err != nil {
		return w.err
	}
	// The merged blob is addressed by the same u32 offsets a built one is, so it
	// carries the same ceiling. A merge is the one operation that can grow a
	// segment without bound — it consolidates every constituent — so this is
	// where a silently truncated offset would actually be reachable.
	if w.tail > v2MaxBlobBytes {
		return fmt.Errorf(
			"bm25 merge: merged segment would be %d bytes, past the %d-byte ceiling the format's u32 offsets can address; merge fewer segments at a time",
			w.tail, v2MaxBlobBytes)
	}
	if err := f.Truncate(w.tail); err != nil {
		return fmt.Errorf("bm25 merge: truncate: %w", err)
	}
	w.patchU32(v2HdrBlobLen, uint32(w.tail))
	return w.err
}

// resolveMergeLayout assigns the merged segment's document ids. It walks the
// inputs' members exactly as the merge will and keeps ONE slot per external id —
// the last one, matching the route map's own last-append-wins semantics — then
// numbers the survivors contiguously in (input, document) order. Numbering in
// that order is what makes every merged posting run ascending without a sort.
//
// Member ids are VIEWS into the inputs' blobs, which stay alive for the whole
// merge; they are copied into the output file rather than retained.
func resolveMergeLayout(ins []*mappedSegment, accept []func(searchengine.ExternalID) bool) ([]string, [][]int32) {
	winner := resolveMergeWinners(ins, accept)
	var members []string
	remap := make([][]int32, len(ins))
	for i, ms := range ins {
		remap[i] = make([]int32, ms.docCount)
		for oldID := range ms.docCount {
			remap[i][oldID] = -1
			extID := ms.member(oldID)
			if keep := acceptFor(accept, i); keep != nil && !keep(extID) {
				continue
			}
			if !winsSlot(winner, extID, i, oldID) {
				continue
			}
			remap[i][oldID] = int32(len(members))
			members = append(members, extID)
		}
	}
	return members, remap
}

// acceptFor returns the filter gating input i, or nil when it has none.
func acceptFor(accept []func(searchengine.ExternalID) bool, i int) func(searchengine.ExternalID) bool {
	if i < len(accept) {
		return accept[i]
	}
	return nil
}

// writeMergePrefix emits every fixed-prefix section: header, member table, field
// table, field names, document lengths and the dictionary headers.
func writeMergePrefix(w *mergeWriter, p *mergePlan, ins []*mappedSegment, remap [][]int32) {
	var hdr [v2HeaderSize]byte
	hdr[v2HdrVersion] = serialVersion
	hdr[v2HdrDictKind] = p.kind
	binary.LittleEndian.PutUint16(hdr[v2HdrFieldCount:], uint16(len(defaultFieldConfigs)))
	binary.LittleEndian.PutUint32(hdr[v2HdrDocCount:], uint32(len(p.members)))
	binary.LittleEndian.PutUint32(hdr[v2HdrMemberOffsets:], uint32(p.memberOffsetsOff))
	binary.LittleEndian.PutUint32(hdr[v2HdrMemberBytes:], uint32(p.memberOff[0]))
	binary.LittleEndian.PutUint32(hdr[v2HdrFieldTable:], uint32(p.fieldTableOff))
	binary.LittleEndian.PutUint32(hdr[v2HdrDocFreqDict:], uint32(p.dfDictOff))
	w.patch(0, hdr[:])

	for i, o := range p.memberOff {
		w.patchU32(p.memberOffsetsOff+4*i, uint32(o))
	}
	for i, id := range p.members {
		w.patchStr(p.memberOff[i], id)
	}

	lengths := mergeDocLengths(p, ins, remap)
	// One serialization buffer for all five fields' document lengths: the array
	// is the same size for each, and a fresh one per field would allocate five
	// times the corpus's document count for no reason.
	raw := make([]byte, 4*len(p.members))
	for i, cfg := range defaultFieldConfigs {
		e := p.fieldTableOff + v2FieldEntrySize*i
		w.patchU32(e+v2FldNameOff, uint32(p.nameOff[i]))
		w.patchU16(e+v2FldNameLen, uint16(len(cfg.Name)))
		w.patchU64(e+v2FldBoost, mathFloatBits(cfg.Boost))
		w.patchU64(e+v2FldB, mathFloatBits(cfg.B))
		w.patchU32(e+v2FldDocLengths, uint32(p.docLengthsOff[i]))
		w.patchU32(e+v2FldDict, uint32(p.dictOff[i]))
		w.patchStr(p.nameOff[i], cfg.Name)

		var total int64
		for d, dl := range lengths[i] {
			binary.LittleEndian.PutUint32(raw[4*d:], dl)
			total += int64(dl)
		}
		w.patch(p.docLengthsOff[i], raw)
		w.patchU64(e+v2FldTotalTokens, uint64(total))
		writeMergeDictHeader(w, p, i)
	}

	w.patchU32(p.dfDictOff, uint32(p.dfCount))
	w.patchU32(p.dfDictOff+4, uint32(p.dfEntriesOff))
}

// mergeDocLengths carries each surviving document's per-field token count over
// from the input it came from, so length normalization is preserved exactly.
func mergeDocLengths(p *mergePlan, ins []*mappedSegment, remap [][]int32) [][]uint32 {
	out := make([][]uint32, len(defaultFieldConfigs))
	for i := range out {
		out[i] = make([]uint32, len(p.members))
	}
	for i, ms := range ins {
		for _, mf := range ms.fields {
			slot, ok := fieldSlot(mf.config.Name)
			if !ok {
				continue
			}
			for oldID, newID := range remap[i] {
				if newID >= 0 && oldID < len(mf.lengths) {
					out[slot][newID] = mf.lengths[oldID]
				}
			}
		}
	}
	return out
}

// fieldSlot maps a field name to its index in defaultFieldConfigs.
func fieldSlot(name string) (int, bool) {
	for i, cfg := range defaultFieldConfigs {
		if cfg.Name == name {
			return i, true
		}
	}
	return 0, false
}

// writeMergeDictHeader emits one field's dictionary header, whose shape depends
// on the encoding.
func writeMergeDictHeader(w *mergeWriter, p *mergePlan, i int) {
	n := p.termCount[i]
	w.patchU32(p.dictOff[i], uint32(n))
	switch p.kind {
	case dictBlocked:
		blocks := (n + blockedBlockTerms - 1) / blockedBlockTerms
		w.patchU32(p.dictOff[i]+4, uint32(blocks))
		w.patchU32(p.dictOff[i]+8, uint32(p.firstTermsOff[i]))
		w.patchU32(p.dictOff[i]+12, uint32(p.blockIdxOff[i]))
	case dictHash:
		w.patchU32(p.dictOff[i]+4, uint32(p.entriesOff[i]))
		w.patchU32(p.dictOff[i]+8, uint32(p.slotsOff[i]))
		w.patchU32(p.dictOff[i]+12, uint32(p.slotCount[i]))
		empty := make([]byte, v2HashSlotSize*p.slotCount[i])
		for s := range p.slotCount[i] {
			binary.LittleEndian.PutUint32(empty[v2HashSlotSize*s+4:], hashEmptySlot)
		}
		w.patch(p.slotsOff[i], empty)
	default:
		w.patchU32(p.dictOff[i]+4, uint32(p.entriesOff[i]))
	}
}
