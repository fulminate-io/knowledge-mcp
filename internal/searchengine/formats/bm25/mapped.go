// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unsafe"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// mappedField is one field of an offset-addressed segment. It EMBEDS a
// fieldData so the shared BM25F math (fieldData.scoreField) is called rather
// than duplicated; only the embedded config and totalTokens are populated, and
// the embedded postings/docLengths stay nil because those are the build-time
// accumulator's shape and this field reads its equivalents out of the blob.
type mappedField struct {
	fieldData
	blob []byte
	// lengths is a zero-copy view of this field's per-document token counts.
	lengths []uint32
	// Dictionary header, decoded once at open. Which members are meaningful
	// depends on the segment's dictKind.
	termCount     int
	entriesOff    int
	slotsOff      int
	slotCount     int
	blockCount    int
	firstTermsOff int
	blockIdxOff   int
	kind          byte
}

// mappedSegment is a sealed BM25 segment read IN PLACE out of its encoded bytes.
// Opening one parses the header and field table — a few hundred bytes — and
// nothing else; postings, terms, member ids and document lengths are resolved as
// views into the blob when a query asks for them. There is no hydrated map form,
// which is what moves a segment's cost out of the Go heap.
type mappedSegment struct {
	blob          []byte
	kind          byte
	docCount      int
	memberOffsets []uint32
	fields        []*mappedField
	fieldByName   map[string]*mappedField
}

// u32sAt returns a zero-copy view of n uint32 words at off. The cast is
// host-endian and the format is little-endian, which is a declared constraint of
// this package (see doc.go); off must be 4-aligned, which open verifies for
// every section it hands here.
func u32sAt(b []byte, off, n int) []uint32 {
	if n == 0 {
		return nil
	}
	//nolint:gosec // the zero-copy read this format exists for; off is 4-aligned and bounded by checkSection
	return unsafe.Slice((*uint32)(unsafe.Pointer(&b[off])), n)
}

// u16sAt returns a zero-copy view of n uint16 words at off. off must be 2-aligned.
func u16sAt(b []byte, off, n int) []uint16 {
	if n == 0 {
		return nil
	}
	//nolint:gosec // the zero-copy read this format exists for; off is 2-aligned and bounded by its posting run
	return unsafe.Slice((*uint16)(unsafe.Pointer(&b[off])), n)
}

// termView returns a bounds-checked view of termLen bytes at termOff.
//
// This guard is NOT redundant with the section checks open performs, and the
// difference is the whole reason it exists. checkSection validates where a
// SECTION lives — the extent of the entry rows, of the docFreq rows. A term
// offset and length are DATA STORED INSIDE those rows, and nothing at open
// constrains where a row points; validating every row there would be the eager
// per-row walk that lazy open exists to avoid.
//
// Go bounds-checks the indexing expression, so termOff alone cannot escape the
// blob — but unsafe.String then reads termLen bytes onward with no check at all,
// so a corrupt row reads past the end silently on heap bytes and faults once
// those bytes are a mapping. This mirrors the posting reader's guard, for the
// same reason and with the same loudness: content-addressing makes it
// unreachable for an honestly-named blob, so reaching it means a writer defect,
// and reporting a defect as "empty term" would turn it into quietly wrong
// search results.
func termView(b []byte, termOff, termLen int, what string) string {
	if termLen == 0 {
		return ""
	}
	if termOff < 0 || termLen < 0 || termOff+termLen > len(b) {
		panic(fmt.Sprintf("bm25: %s spans [%d,%d) in a %d-byte blob", what, termOff, termOff+termLen, len(b)))
	}
	//nolint:gosec // bounds checked immediately above; the view stays internal
	return unsafe.String(&b[termOff], termLen)
}

// checkSection rejects a section whose offset is misaligned or out of bounds. A
// misaligned typed view is undefined behaviour rather than a wrong answer, so
// this runs before any cast and refuses the blob outright.
func checkSection(name string, blobLen, off, size, alignTo int) error {
	if off%alignTo != 0 {
		return fmt.Errorf("bm25 open: %s at offset %d is not %d-aligned", name, off, alignTo)
	}
	if off < 0 || size < 0 || off+size > blobLen {
		return fmt.Errorf("bm25 open: %s spans [%d,%d) past the %d-byte blob", name, off, off+size, blobLen)
	}
	return nil
}

// openSegmentV2 parses a version-2 blob's header and field table and returns a
// segment that reads everything else in place. The blob is retained, not copied:
// the caller owns its lifetime, and once Phase 4 hands in a mapping the bytes are
// page cache rather than heap.
func openSegmentV2(b []byte) (*mappedSegment, error) {
	if len(b) < v2HeaderSize {
		return nil, fmt.Errorf("bm25 open: blob is %d bytes, shorter than the %d-byte header", len(b), v2HeaderSize)
	}
	if v := b[v2HdrVersion]; v != serialVersion {
		return nil, fmt.Errorf(
			"bm25 open: unsupported serial version %d (want %d); this segment predates the offset-addressed layout and has no converter — rebuild it from source",
			v, serialVersion)
	}
	if declared := int(binary.LittleEndian.Uint32(b[v2HdrBlobLen:])); declared != len(b) {
		return nil, fmt.Errorf("bm25 open: header declares %d bytes but the blob is %d", declared, len(b))
	}
	s := &mappedSegment{
		blob:     b,
		kind:     b[v2HdrDictKind],
		docCount: int(binary.LittleEndian.Uint32(b[v2HdrDocCount:])),
	}
	if s.kind > dictHash {
		return nil, fmt.Errorf("bm25 open: unknown dictionary kind %d", s.kind)
	}
	fieldCount := int(binary.LittleEndian.Uint16(b[v2HdrFieldCount:]))
	memberOffsetsOff := int(binary.LittleEndian.Uint32(b[v2HdrMemberOffsets:]))
	fieldTableOff := int(binary.LittleEndian.Uint32(b[v2HdrFieldTable:]))

	if err := checkSection("member offsets", len(b), memberOffsetsOff, 4*(s.docCount+1), 4); err != nil {
		return nil, err
	}
	s.memberOffsets = u32sAt(b, memberOffsetsOff, s.docCount+1)
	if err := checkSection("field table", len(b), fieldTableOff, v2FieldEntrySize*fieldCount, 8); err != nil {
		return nil, err
	}
	if err := s.openDocFreqDict(); err != nil {
		return nil, err
	}
	s.fields = make([]*mappedField, 0, fieldCount)
	s.fieldByName = make(map[string]*mappedField, fieldCount)
	for i := range fieldCount {
		mf, err := s.openField(fieldTableOff + v2FieldEntrySize*i)
		if err != nil {
			return nil, fmt.Errorf("bm25 open: field %d: %w", i, err)
		}
		s.fields = append(s.fields, mf)
		s.fieldByName[mf.config.Name] = mf
	}
	return s, nil
}

// openField parses one field-table entry and its dictionary header.
func (s *mappedSegment) openField(e int) (*mappedField, error) {
	b := s.blob
	nameOff := int(binary.LittleEndian.Uint32(b[e+v2FldNameOff:]))
	nameLen := int(binary.LittleEndian.Uint16(b[e+v2FldNameLen:]))
	if err := checkSection("field name", len(b), nameOff, nameLen, 1); err != nil {
		return nil, err
	}
	docLengthsOff := int(binary.LittleEndian.Uint32(b[e+v2FldDocLengths:]))
	if err := checkSection("doc lengths", len(b), docLengthsOff, 4*s.docCount, 4); err != nil {
		return nil, err
	}
	mf := &mappedField{
		fieldData: fieldData{
			config: FieldConfig{
				Name:  string(b[nameOff : nameOff+nameLen]),
				Boost: mathFloatFromBits(binary.LittleEndian.Uint64(b[e+v2FldBoost:])),
				B:     mathFloatFromBits(binary.LittleEndian.Uint64(b[e+v2FldB:])),
			},
			totalTokens: int64(binary.LittleEndian.Uint64(b[e+v2FldTotalTokens:])),
		},
		blob:    b,
		lengths: u32sAt(b, docLengthsOff, s.docCount),
		kind:    s.kind,
	}
	if err := mf.openDict(int(binary.LittleEndian.Uint32(b[e+v2FldDict:]))); err != nil {
		return nil, err
	}
	return mf, nil
}

// member returns a VIEW of member i's external id. The view aliases the blob and
// must never cross the segment's API boundary — see IDs and collectTopK, which
// copy. Keeping views internal is what makes releasing the blob safe.
func (s *mappedSegment) member(i int) string {
	lo, hi := int(s.memberOffsets[i]), int(s.memberOffsets[i+1])
	if lo > hi || hi > len(s.blob) {
		panic(fmt.Sprintf("bm25: member %d spans [%d,%d) in a %d-byte blob", i, lo, hi, len(s.blob)))
	}
	//nolint:gosec // internal view, bounds-checked immediately above; every id that leaves the segment is copied
	return unsafe.String(&s.blob[lo], hi-lo)
}

// IDs lists every ExternalID the segment indexes (live or dead), in stable
// segment-local-docID order. Every id is COPIED off the blob: the engine holds
// these for the life of its route map, so a view would pin — or outlive — the
// bytes they were read from.
func (s *mappedSegment) IDs() []searchengine.ExternalID {
	out := make([]searchengine.ExternalID, s.docCount)
	for i := range s.docCount {
		out[i] = strings.Clone(s.member(i))
	}
	return out
}

// Encode returns the segment's own bytes. The blob IS the encoded form, so this
// is an identity rather than a re-serialization, and the segment id (sha256 of
// the bytes) is trivially stable across a decode/encode round trip.
func (s *mappedSegment) Encode() ([]byte, error) { return s.blob, nil }

// mappedFieldEntryOverheadBytes models the per-field cost beyond the
// mappedField struct itself: one slice element holding the pointer and one
// fieldByName map entry (bucket slot, key string header and value pointer).
const mappedFieldEntryOverheadBytes = 64

// HeapBytes models the Go heap this sealed mapped segment holds — see
// searchengine.Segment.HeapBytes, which documents that the number is an
// estimate rather than a measurement.
//
// IT DOES NOT SCALE WITH DOCUMENT COUNT, and that is the whole point of the
// mapped format. memberOffsets, every posting list and every term dictionary
// are zero-copy views taken over the blob by u32sAt, and the blob is page
// cache rather than Go heap. What remains on the heap is this struct plus one
// mappedField per INDEXED FIELD, and the field set is fixed
// (defaultFieldConfigs), so the total is bounded by a small constant no matter
// how many documents the segment indexes.
//
// The struct terms are taken with unsafe.Sizeof rather than written as a
// literal so the model cannot silently rot as either struct gains a field.
func (s *mappedSegment) HeapBytes() int64 {
	return int64(unsafe.Sizeof(*s)) +
		int64(len(s.fields))*(int64(unsafe.Sizeof(mappedField{}))+mappedFieldEntryOverheadBytes)
}

// openDocFreqDict validates the per-segment document-frequency dictionary's
// header. The rows themselves are read on demand.
func (s *mappedSegment) openDocFreqDict() error {
	off := int(binary.LittleEndian.Uint32(s.blob[v2HdrDocFreqDict:]))
	if err := checkSection("docFreq dictionary", len(s.blob), off, 8, 8); err != nil {
		return err
	}
	count := int(binary.LittleEndian.Uint32(s.blob[off:]))
	rows := int(binary.LittleEndian.Uint32(s.blob[off+4:]))
	return checkSection("docFreq rows", len(s.blob), rows, v2DocFreqRowSize*count, 8)
}

// docFreqHeader returns the row count and row-array offset of the docFreq dictionary.
func (s *mappedSegment) docFreqHeader() (count, rows int) {
	off := int(binary.LittleEndian.Uint32(s.blob[v2HdrDocFreqDict:]))
	return int(binary.LittleEndian.Uint32(s.blob[off:])), int(binary.LittleEndian.Uint32(s.blob[off+4:]))
}

// docFreqRow returns row i's term view and document frequency.
func (s *mappedSegment) docFreqRow(rows, i int) (string, int64) {
	at := rows + v2DocFreqRowSize*i
	termOff := int(binary.LittleEndian.Uint32(s.blob[at:]))
	termLen := int(binary.LittleEndian.Uint32(s.blob[at+4:]))
	df := int64(binary.LittleEndian.Uint64(s.blob[at+8:]))
	return termView(s.blob, termOff, termLen, "docFreq term"), df
}

// docFreqEach walks the segment's document frequencies in ascending term order.
// The term handed to fn is a VIEW into the blob and must not be retained.
func (s *mappedSegment) docFreqEach(fn func(term string, df int64)) {
	count, rows := s.docFreqHeader()
	for i := range count {
		fn(s.docFreqRow(rows, i))
	}
}

// segmentDocFreq returns how many of THIS segment's documents contain term, by
// binary-searching the sorted docFreq dictionary. Zero means the term is absent,
// which is the same answer the eager fold gave for a term it never recorded.
func (s *mappedSegment) segmentDocFreq(term string) int64 {
	count, rows := s.docFreqHeader()
	lo, hi := 0, count
	for lo < hi {
		mid := (lo + hi) / 2
		got, df := s.docFreqRow(rows, mid)
		switch {
		case got == term:
			return df
		case got < term:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return 0
}
