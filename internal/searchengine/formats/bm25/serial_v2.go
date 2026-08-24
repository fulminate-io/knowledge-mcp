// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

// serialVersion is the BM25 segment blob format version. A single version byte
// prefixes every encoded segment (mirrors hnsw serial.go's version-byte gate) so
// a format change is detectable rather than silently mis-decoded. Version 2 is
// the offset-addressed layout: every section is addressed by an absolute u32
// offset from the blob start, so a reader resolves postings, terms and document
// lengths as views into the bytes rather than hydrating them into Go maps.
const serialVersion byte = 2

// Dictionary encodings. The kind is a header byte, so all three are readable by
// one decoder and the choice is a writer-side default rather than a format fork.
//
//   - dictFlat: sorted 16-byte entry rows, located by binary search.
//   - dictBlocked: front-coded blocks of blockedBlockTerms terms with a
//     contiguous index of each block's first term. Smallest on disk and fewest
//     pages faulted, at the cost of prefix reconstruction on every probe.
//   - dictHash: the flat rows plus an open-addressed {fingerprint, entry index}
//     accelerator beside them. Fastest warm, largest on disk.
const (
	dictFlat byte = iota
	dictBlocked
	dictHash
)

// defaultDictKind is the encoding Build writes, chosen by measuring all three
// against a real query trace over a real corpus — see BenchmarkDictionaryChoice.
//
// The front-coded encoding wins on both MEMORY axes and loses on warm CPU, and
// this is a memory change: it is the smallest on disk and it faults the fewest
// pages per query, at the cost of reconstructing each term's prefix during a
// scan. The same trade was already made one level up when read-ahead
// suppression was turned on by default — more cold wall time for a materially
// smaller physical footprint — so choosing the fastest-warm encoding here would
// have spent, on a sub-millisecond difference, exactly what that lever bought.
const defaultDictKind = dictBlocked

// Fixed layout sizes.
const (
	// v2HeaderSize is the fixed header at offset 0.
	v2HeaderSize = 32
	// v2FieldEntrySize is one row of the field table.
	v2FieldEntrySize = 48
	// v2FlatEntrySize is one {termOff, termLen, postOff, postCount} row.
	v2FlatEntrySize = 16
	// v2HashSlotSize is one {fingerprint, entryIdx} accelerator slot.
	v2HashSlotSize = 8
	// v2DocFreqRowSize is one {termOff, termLen, df} row of the docFreq dictionary.
	v2DocFreqRowSize = 16
	// blockedBlockTerms is how many terms share one front-coded block.
	blockedBlockTerms = 32
	// hashEmptySlot marks an unoccupied accelerator slot. Entry indices are
	// bounded by the term count, which the size guard keeps well under this.
	hashEmptySlot = math.MaxUint32
)

// Header field offsets.
const (
	v2HdrVersion       = 0
	v2HdrDictKind      = 1
	v2HdrFieldCount    = 2
	v2HdrDocCount      = 4
	v2HdrMemberOffsets = 8
	v2HdrMemberBytes   = 12
	v2HdrFieldTable    = 16
	v2HdrDocFreqDict   = 20
	v2HdrBlobLen       = 24
	v2HdrReserved      = 28
)

// Field-table entry offsets, relative to the entry's start.
const (
	v2FldNameOff     = 0
	v2FldNameLen     = 4
	v2FldBoost       = 8
	v2FldB           = 16
	v2FldTotalTokens = 24
	v2FldDocLengths  = 32
	v2FldDict        = 36
)

// v2MaxBlobBytes is the largest blob a u32 offset can address. It is a variable
// rather than a constant for ONE reason, stated plainly because it is a test
// seam: a blob that actually crosses this ceiling is not constructible in a test
// (the size is dominated by four bytes per field per document, so reaching it
// needs on the order of 215 million documents), and a guard no fixture can drive
// is a guard nobody knows is wired. Lowering this in a test drives the real check
// through the real encode path. The shipped value is asserted by that same test
// before it lowers it, so a permanently-lowered ceiling cannot hide here.
var v2MaxBlobBytes = int64(math.MaxUint32)

// align rounds off up to the next multiple of a (a must be a power of two).
func align(off, a int) int { return (off + a - 1) &^ (a - 1) }

// termHash is the fingerprint function of the hash-accelerated dictionary:
// FNV-1a 64. It is FIXED BY THE FORMAT — the accelerator table on disk is built
// with it, so changing it changes what a written blob means and is a version bump.
func termHash(s string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := range len(s) {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}

// mathFloatBits / mathFloatFromBits bridge float64 ↔ its IEEE-754 bit pattern for
// lossless serialization of the field boost/B parameters.
func mathFloatBits(f float64) uint64     { return math.Float64bits(f) }
func mathFloatFromBits(b uint64) float64 { return math.Float64frombits(b) }

// v2FieldPlan is one field's computed section offsets.
type v2FieldPlan struct {
	nameOff       int
	docLengthsOff int
	// terms is this field's posting terms in ascending order. Sorting is
	// load-bearing twice over: it makes the emit byte-deterministic (the segment
	// id is sha256 of the blob, so two writers converge only if the layout is
	// stable), and it is what lets a dictionary be searched without hydrating it.
	terms []string
	// postOff is the absolute offset of each term's posting block.
	postOff []int
	dict    v2DictPlan
}

// v2Plan is the complete computed layout of one blob. Every offset is absolute
// and final, so writing is a set of stores at known positions and the total size
// is exact before a single byte is allocated.
type v2Plan struct {
	kind             byte
	docCount         int
	memberOffsetsOff int
	// memberOff holds docCount+1 absolute offsets: member i occupies
	// [memberOff[i], memberOff[i+1]).
	memberOff     []int
	fieldTableOff int
	fields        []v2FieldPlan
	dfTerms       []string
	dfTermOff     []int
	dfDictOff     int
	dfEntriesOff  int
	size          int
}

// sortedKeys drains a map's keys into an ascending slice. Every emit order in
// this encoder runs through it, for the byte-determinism reason serial.go
// documented on the v1 writer: Go randomizes map iteration per range, so ranging
// a posting or docFreq map directly would lay the blob out differently every run
// and break content-hash convergence.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// planSegmentV2 computes every section offset for the segment in one forward
// pass, returning a plan whose size field is the exact encoded length.
func planSegmentV2(s *bm25Segment, kind byte) (*v2Plan, error) {
	p := &v2Plan{kind: kind, docCount: len(s.members)}
	off := v2HeaderSize

	p.memberOffsetsOff = align(off, 4)
	off = p.memberOffsetsOff + 4*(p.docCount+1)
	p.memberOff = make([]int, 0, p.docCount+1)
	for _, id := range s.members {
		p.memberOff = append(p.memberOff, off)
		off += len(id)
	}
	p.memberOff = append(p.memberOff, off)

	p.fieldTableOff = align(off, 8)
	off = p.fieldTableOff + v2FieldEntrySize*len(s.fields)

	p.fields = make([]v2FieldPlan, len(s.fields))
	for i, fd := range s.fields {
		p.fields[i].nameOff = off
		off += len(fd.config.Name)
	}
	for i := range s.fields {
		p.fields[i].docLengthsOff = align(off, 4)
		off = p.fields[i].docLengthsOff + 4*p.docCount
	}
	for i, fd := range s.fields {
		fp := &p.fields[i]
		fp.terms = sortedKeys(fd.postings)
		fp.dict, off = planDict(kind, off, fp.terms)
		fp.postOff = make([]int, len(fp.terms))
		for t, term := range fp.terms {
			n := len(fd.postings[term])
			fp.postOff[t] = align(off, 4)
			off = fp.postOff[t] + 4*n + 2*n
		}
	}

	p.dfTerms = sortedKeys(s.docFreq)
	p.dfDictOff = align(off, 8)
	off = p.dfDictOff + 8
	p.dfEntriesOff = align(off, 8)
	off = p.dfEntriesOff + v2DocFreqRowSize*len(p.dfTerms)
	p.dfTermOff = make([]int, len(p.dfTerms))
	for i, term := range p.dfTerms {
		p.dfTermOff[i] = off
		off += len(term)
	}

	p.size = off
	if int64(p.size) > v2MaxBlobBytes {
		return nil, fmt.Errorf(
			"bm25 encode: segment would be %d bytes, past the %d-byte ceiling the format's u32 offsets can address; split the segment into smaller ones",
			p.size, v2MaxBlobBytes)
	}
	return p, nil
}

// encodeSegmentV2 serializes a built segment into the offset-addressed layout.
// It plans the whole blob first — so the buffer is allocated exactly once at the
// final size, the property v1's encodedSize precompute existed to give — then
// writes each section at its planned offset.
func encodeSegmentV2(s *bm25Segment, kind byte) ([]byte, error) {
	if kind > dictHash {
		return nil, fmt.Errorf("bm25 encode: unknown dictionary kind %d", kind)
	}
	p, err := planSegmentV2(s, kind)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, p.size)

	buf[v2HdrVersion] = serialVersion
	buf[v2HdrDictKind] = kind
	binary.LittleEndian.PutUint16(buf[v2HdrFieldCount:], uint16(len(s.fields)))
	binary.LittleEndian.PutUint32(buf[v2HdrDocCount:], uint32(p.docCount))
	binary.LittleEndian.PutUint32(buf[v2HdrMemberOffsets:], uint32(p.memberOffsetsOff))
	binary.LittleEndian.PutUint32(buf[v2HdrMemberBytes:], uint32(p.memberOff[0]))
	binary.LittleEndian.PutUint32(buf[v2HdrFieldTable:], uint32(p.fieldTableOff))
	binary.LittleEndian.PutUint32(buf[v2HdrDocFreqDict:], uint32(p.dfDictOff))
	binary.LittleEndian.PutUint32(buf[v2HdrBlobLen:], uint32(p.size))
	binary.LittleEndian.PutUint32(buf[v2HdrReserved:], 0)

	for i, o := range p.memberOff {
		binary.LittleEndian.PutUint32(buf[p.memberOffsetsOff+4*i:], uint32(o))
	}
	for i, id := range s.members {
		copy(buf[p.memberOff[i]:], id)
	}

	for i, fd := range s.fields {
		writeFieldV2(buf, p, i, fd)
	}
	writeDocFreqDict(buf, p, s.docFreq)
	return buf, nil
}

// writeFieldV2 emits one field's table entry, name, document lengths, dictionary
// and posting blocks.
func writeFieldV2(buf []byte, p *v2Plan, i int, fd *fieldData) {
	fp := &p.fields[i]
	e := p.fieldTableOff + v2FieldEntrySize*i
	binary.LittleEndian.PutUint32(buf[e+v2FldNameOff:], uint32(fp.nameOff))
	binary.LittleEndian.PutUint16(buf[e+v2FldNameLen:], uint16(len(fd.config.Name)))
	binary.LittleEndian.PutUint64(buf[e+v2FldBoost:], mathFloatBits(fd.config.Boost))
	binary.LittleEndian.PutUint64(buf[e+v2FldB:], mathFloatBits(fd.config.B))
	binary.LittleEndian.PutUint64(buf[e+v2FldTotalTokens:], uint64(fd.totalTokens))
	binary.LittleEndian.PutUint32(buf[e+v2FldDocLengths:], uint32(fp.docLengthsOff))
	binary.LittleEndian.PutUint32(buf[e+v2FldDict:], uint32(fp.dict.off))
	copy(buf[fp.nameOff:], fd.config.Name)

	for d := range p.docCount {
		dl := 0
		if d < len(fd.docLengths) {
			dl = fd.docLengths[d]
		}
		binary.LittleEndian.PutUint32(buf[fp.docLengthsOff+4*d:], uint32(dl))
	}

	counts := make([]int, len(fp.terms))
	for t, term := range fp.terms {
		counts[t] = len(fd.postings[term])
	}
	writeDict(buf, p.kind, fp.dict, fp.terms, fp.postOff, counts)

	// Posting block: n docIDs then n term frequencies, so each run is one
	// contiguous typed view and a short list sits on a single page.
	for t, term := range fp.terms {
		posts := fd.postings[term]
		base := fp.postOff[t]
		tfBase := base + 4*len(posts)
		for j, post := range posts {
			binary.LittleEndian.PutUint32(buf[base+4*j:], post.docID)
			binary.LittleEndian.PutUint16(buf[tfBase+2*j:], post.tf)
		}
	}
}

// writeDocFreqDict emits the per-segment document-frequency dictionary: a flat
// sorted run of {termOff, termLen, df} rows that a reader binary-searches, and
// that a merge reads as a sorted stream without hydrating a map.
func writeDocFreqDict(buf []byte, p *v2Plan, docFreq map[string]int64) {
	binary.LittleEndian.PutUint32(buf[p.dfDictOff:], uint32(len(p.dfTerms)))
	binary.LittleEndian.PutUint32(buf[p.dfDictOff+4:], uint32(p.dfEntriesOff))
	for i, term := range p.dfTerms {
		row := p.dfEntriesOff + v2DocFreqRowSize*i
		binary.LittleEndian.PutUint32(buf[row:], uint32(p.dfTermOff[i]))
		binary.LittleEndian.PutUint32(buf[row+4:], uint32(len(term)))
		binary.LittleEndian.PutUint64(buf[row+8:], uint64(docFreq[term]))
		copy(buf[p.dfTermOff[i]:], term)
	}
}
