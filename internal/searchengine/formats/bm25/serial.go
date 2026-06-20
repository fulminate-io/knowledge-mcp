// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// serialVersion is the BM25 segment blob format version. A single version byte
// prefixes every encoded segment (mirrors hnsw serial.go's version-byte gate) so
// a future format change is detectable rather than silently mis-decoded.
const serialVersion byte = 1

// Encode serializes the sealed segment to a self-describing blob for
// shipping/persistence. Layout (all integers little-endian):
//
//	version:byte
//	docCount:uint32  then docCount × (idLen:uint16, externalID bytes)
//	fieldCount:uint32 then per field:
//	    nameLen:uint16, name bytes, boost:float64bits(uint64), b:float64bits, totalTokens:uint64
//	    docLengths: uint32 count, then count × uint32
//	    postings:   uint32 termCount, then per term:
//	                  termLen:uint16, term bytes, postingCount:uint32, then count × (docID:uint32, tf:uint16)
//	docFreqCount:uint32 then count × (termLen:uint16, term bytes, df:uint64)
//
// A decoded segment is indistinguishable from a freshly built one (postings +
// stats survive the round trip), so it is fully merge-eligible — the contract's
// Decode-reconstructs-concrete requirement.
func (s *bm25Segment) Encode() ([]byte, error) {
	// Precompute the EXACT encoded size and allocate the buffer to it up front, so
	// the append sequence below never triggers a regrowth/copy. Encode is the
	// build path's dominant byte allocator (the 1KB-seed buffer otherwise doubles
	// repeatedly to the final size, allocating ~2× the payload across the regrowth
	// chain). The bound is exact (every field below contributes a fixed or
	// length-prefixed byte count), so the emitted bytes are unchanged — only the
	// backing array's capacity differs.
	buf := make([]byte, 0, s.encodedSize())
	buf = append(buf, serialVersion)

	// members.
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(s.members)))
	for _, id := range s.members {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(id)))
		buf = append(buf, id...)
	}

	// fields.
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(s.fields)))
	for _, fd := range s.fields {
		buf = appendField(buf, fd)
	}

	// docFreq. Emit in sorted-term order: Go map iteration is randomized, so
	// ranging s.docFreq directly would produce a different byte layout every run,
	// breaking content-hash convergence (the segment id is sha256(Encode()), so two
	// writers — or one writer across runs — must serialize byte-identically to dedup
	// to a single blob). Decode reconstructs a map, so emit order is semantically
	// irrelevant; sorting is purely for byte-determinism.
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(s.docFreq)))
	dfTerms := make([]string, 0, len(s.docFreq))
	for term := range s.docFreq {
		dfTerms = append(dfTerms, term)
	}
	sort.Strings(dfTerms)
	for _, term := range dfTerms {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(term)))
		buf = append(buf, term...)
		buf = binary.LittleEndian.AppendUint64(buf, uint64(s.docFreq[term]))
	}

	return buf, nil
}

// encodedSize returns the EXACT number of bytes Encode will emit for this
// segment. It mirrors Encode's append sequence field-for-field so the value can
// seed Encode's buffer to its final capacity (no regrowth). Each clause matches a
// fixed-width or length-prefixed write in Encode/appendField; keep the two in
// lockstep — a divergence only over- or under-sizes the buffer (correctness is
// unaffected, since append still grows on a short bound), but an exact match is
// what eliminates the regrowth allocation.
func (s *bm25Segment) encodedSize() int {
	n := 1 // version byte

	// members: u32 count, then per member u16 len-prefix + id bytes.
	n += 4
	for _, id := range s.members {
		n += 2 + len(id)
	}

	// fields: u32 count, then per field.
	n += 4
	for _, fd := range s.fields {
		// name (u16 len + bytes) + boost(u64) + b(u64) + totalTokens(u64).
		n += 2 + len(fd.config.Name) + 8 + 8 + 8
		// docLengths: u32 count + count × u32.
		n += 4 + 4*len(fd.docLengths)
		// postings: u32 termCount, then per term u16 len + term bytes + u32
		// postingCount + count × (u32 docID + u16 tf).
		n += 4
		for term, posts := range fd.postings {
			n += 2 + len(term) + 4 + len(posts)*(4+2)
		}
	}

	// docFreq: u32 count, then per term u16 len + term bytes + u64 df.
	n += 4
	for term := range s.docFreq {
		n += 2 + len(term) + 8
	}

	return n
}

// appendField serializes one field's config + docLengths + postings.
func appendField(buf []byte, fd *fieldData) []byte {
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(fd.config.Name)))
	buf = append(buf, fd.config.Name...)
	buf = binary.LittleEndian.AppendUint64(buf, mathFloatBits(fd.config.Boost))
	buf = binary.LittleEndian.AppendUint64(buf, mathFloatBits(fd.config.B))
	buf = binary.LittleEndian.AppendUint64(buf, uint64(fd.totalTokens))

	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(fd.docLengths)))
	for _, dl := range fd.docLengths {
		buf = binary.LittleEndian.AppendUint32(buf, uint32(dl))
	}

	// Emit postings in sorted-term order for the same byte-determinism reason as
	// docFreq above: ranging the map directly randomizes the layout per run. Within
	// a term, posts is already a slice (stable order), so only the term keys need
	// sorting. Decode rebuilds the map, so emit order carries no semantics.
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(fd.postings)))
	terms := make([]string, 0, len(fd.postings))
	for term := range fd.postings {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	for _, term := range terms {
		posts := fd.postings[term]
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(term)))
		buf = append(buf, term...)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(posts)))
		for _, p := range posts {
			buf = binary.LittleEndian.AppendUint32(buf, p.docID)
			buf = binary.LittleEndian.AppendUint16(buf, p.tf)
		}
	}
	return buf
}

// decodeSegment reconstructs a bm25Segment from an Encode() blob. Only the
// current version byte is accepted.
func decodeSegment(data []byte) (*bm25Segment, error) {
	d := &decoder{data: data}
	version, err := d.byte()
	if err != nil {
		return nil, err
	}
	if version != serialVersion {
		return nil, fmt.Errorf("bm25 decode: unsupported serial version %d (want %d)", version, serialVersion)
	}

	memberCount, err := d.u32()
	if err != nil {
		return nil, err
	}
	members := make([]string, 0, memberCount)
	for i := range int(memberCount) {
		id, err := d.lenPrefixedString()
		if err != nil {
			return nil, fmt.Errorf("bm25 decode member %d: %w", i, err)
		}
		members = append(members, id)
	}

	fieldCount, err := d.u32()
	if err != nil {
		return nil, err
	}
	fields := make([]*fieldData, 0, fieldCount)
	byName := make(map[string]*fieldData, fieldCount)
	for i := range int(fieldCount) {
		fd, err := d.field()
		if err != nil {
			return nil, fmt.Errorf("bm25 decode field %d: %w", i, err)
		}
		fields = append(fields, fd)
		byName[fd.config.Name] = fd
	}

	dfCount, err := d.u32()
	if err != nil {
		return nil, err
	}
	docFreq := make(map[string]int64, dfCount)
	for i := range int(dfCount) {
		term, err := d.lenPrefixedString()
		if err != nil {
			return nil, fmt.Errorf("bm25 decode docFreq term %d: %w", i, err)
		}
		df, err := d.u64()
		if err != nil {
			return nil, fmt.Errorf("bm25 decode docFreq val %d: %w", i, err)
		}
		docFreq[term] = int64(df)
	}

	return &bm25Segment{
		fields:      fields,
		fieldByName: byName,
		members:     members,
		docFreq:     docFreq,
	}, nil
}

// field decodes one field's config + docLengths + postings.
func (d *decoder) field() (*fieldData, error) {
	name, err := d.lenPrefixedString()
	if err != nil {
		return nil, err
	}
	boostBits, err := d.u64()
	if err != nil {
		return nil, err
	}
	bBits, err := d.u64()
	if err != nil {
		return nil, err
	}
	totalTokens, err := d.u64()
	if err != nil {
		return nil, err
	}

	dlCount, err := d.u32()
	if err != nil {
		return nil, err
	}
	docLengths := make([]int, 0, dlCount)
	for range int(dlCount) {
		dl, err := d.u32()
		if err != nil {
			return nil, err
		}
		docLengths = append(docLengths, int(dl))
	}

	termCount, err := d.u32()
	if err != nil {
		return nil, err
	}
	postings := make(map[string][]posting, termCount)
	for range int(termCount) {
		term, err := d.lenPrefixedString()
		if err != nil {
			return nil, err
		}
		pcount, err := d.u32()
		if err != nil {
			return nil, err
		}
		posts := make([]posting, 0, pcount)
		for range int(pcount) {
			docID, err := d.u32()
			if err != nil {
				return nil, err
			}
			tf, err := d.u16()
			if err != nil {
				return nil, err
			}
			posts = append(posts, posting{docID: docID, tf: tf})
		}
		postings[term] = posts
	}

	return &fieldData{
		config:      FieldConfig{Name: name, Boost: mathFloatFromBits(boostBits), B: mathFloatFromBits(bBits)},
		postings:    postings,
		docLengths:  docLengths,
		totalTokens: int64(totalTokens),
	}, nil
}
