// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"fmt"
	"hash/crc32"
	"sort"
	"unsafe"
)

// mapped.go is the IN-PLACE READER over a serialVersion-3 blob. It holds no
// per-node Go structure: the node directory is read field-by-field on demand,
// the layer-offset array and id directory are typed views over the bytes, and
// the vector block is the embedded vectorBlock both graph forms share.
//
// Its analog is formats/bm25/mapped.go — checkSection, u32sAt, termView (whose
// doc explains why the use-site guard is NOT redundant with checkSection),
// openSegmentV2 and segmentDocFreq's binary search over a sorted on-disk
// dictionary. The SHAPE is copied; no code is, because the two layouts share no
// fields and the architecture invariant denies a shared hand-written package.

// mappedGraph reads a v3 blob without hydrating it.
type mappedGraph struct {
	// vectorBlock is EMBEDDED so nodeVector stays a direct, inlinable call —
	// the same reason binaryGraph embeds it, and what lets one traversal read
	// both forms.
	vectorBlock

	blob []byte

	m, mMax0, efConstruction int
	efSearch                 int
	maxLevel                 int
	entryPoint               uint32
	nodes                    int

	nodeDirOff int
	// layerOffsets and idDir are TYPED VIEWS over the blob, taken once at open
	// after their sections are bounds-checked. The node directory is
	// deliberately NOT a view: its 16-byte entries are read field-by-field with
	// binary.LittleEndian, which needs no cast and keeps the unsafe surface at
	// exactly two sites.
	layerOffsets []uint32
	idDir        []uint32
}

// Compile-time contract assertion — a signature drift is a build failure.
var _ neighborSource = (*mappedGraph)(nil)

// checkSection validates one section's alignment and extent against the blob.
// Mirrors bm25's helper of the same name.
func checkSection(name string, blobLen, off, size, alignTo int) error {
	if alignTo > 1 && off%alignTo != 0 {
		return fmt.Errorf("hnsw open: %s at offset %d is not %d-aligned", name, off, alignTo)
	}
	if off < 0 || size < 0 || off+size > blobLen {
		return fmt.Errorf("hnsw open: %s spans [%d,%d) past the %d-byte blob", name, off, off+size, blobLen)
	}
	return nil
}

// u32sAt returns n uint32s at off as a zero-copy view.
func u32sAt(b []byte, off, n int) []uint32 {
	if n == 0 {
		return nil
	}
	//nolint:gosec // the zero-copy read this format exists for; off is 4-aligned and bounded by checkSection
	return unsafe.Slice((*uint32)(unsafe.Pointer(&b[off])), n)
}

// idView returns the id at [off, off+length) as a string sharing the blob's
// bytes.
//
// THE PANIC IS NOT REDUNDANT WITH checkSection, and that is the same reasoning
// bm25's termView records. Open validates the id-bytes SECTION's extent; it does
// NOT walk every node entry's idOff, because doing so is the eager pass a lazy
// open exists to avoid. So a per-node offset is DATA INSIDE a validated section,
// and a corrupt one is caught here, at use, naming the span and the blob length.
// With the footer CRC verified in front of this, a panic here now means a WRITER
// defect rather than a bit flip.
func idView(b []byte, off, length int) string {
	if length == 0 {
		return ""
	}
	if off < 0 || length < 0 || off+length > len(b) {
		panic(fmt.Sprintf("hnsw: id spans [%d,%d) in a %d-byte blob", off, off+length, len(b)))
	}
	//nolint:gosec // bounds checked immediately above; every id that leaves the segment is copied
	return unsafe.String(&b[off], length)
}

// openGraphV3 validates a v3 blob and returns a reader over it.
//
// THE ORDER IS LOAD-BEARING. Structural checks run before any typed view is
// taken, and the footer CRC runs before all of them, so a corrupted blob is
// rejected rather than cast. A CRC mismatch and a bad version byte carry the
// SAME no-converter rebuild remedy, which routes both into the heal path.
func openGraphV3(b []byte) (*mappedGraph, error) {
	if len(b) < v3HeaderSize {
		return nil, fmt.Errorf("hnsw open: blob is %d bytes, shorter than the %d-byte header", len(b), v3HeaderSize)
	}
	if v := b[v3HdrVersion]; v != serialVersionOffsets {
		return nil, fmt.Errorf(
			"hnsw open: unsupported serial version %d (want %d); this segment predates the offset-addressed layout and has no converter — rebuild it from source",
			v, serialVersionOffsets)
	}
	if declared := int(le32(b, v3HdrBlobLen)); declared != len(b) {
		return nil, fmt.Errorf("hnsw open: header declares %d bytes but the blob is %d", declared, len(b))
	}

	crcOff := int(le32(b, v3HdrCRC))
	if err := checkSection("footer crc", len(b), crcOff, 4, 4); err != nil {
		return nil, err
	}
	if got, want := le32(b, crcOff), crc32.Checksum(b[:crcOff], crcTable); got != want {
		return nil, fmt.Errorf(
			"hnsw open: footer CRC is %#08x but the bytes hash to %#08x; this segment is corrupt and has no converter — rebuild it from source",
			got, want)
	}

	g := &mappedGraph{
		blob:           b,
		m:              int(le32(b, v3HdrM)),
		mMax0:          int(le32(b, v3HdrMMax0)),
		efConstruction: int(le32(b, v3HdrEfConstruction)),
		efSearch:       defaultEfSearch,
		maxLevel:       int(int32(le32(b, v3HdrMaxLevel))),
		entryPoint:     le32(b, v3HdrEntryPoint),
		nodes:          int(le32(b, v3HdrNodeCount)),
	}
	g.vecBytes = int(le32(b, v3HdrVecBytes))

	g.nodeDirOff = int(le32(b, v3HdrNodeDir))
	idBytesOff := int(le32(b, v3HdrIDBytes))
	layerOffsetsOff := int(le32(b, v3HdrLayerOffsets))
	neighborsOff := int(le32(b, v3HdrNeighbors))
	idDirOff := int(le32(b, v3HdrIDDir))
	vectorsOff := int(le32(b, v3HdrVectors))

	if err := checkSection("node directory", len(b), g.nodeDirOff, g.nodes*v3NodeEntrySize, 4); err != nil {
		return nil, err
	}
	if err := checkSection("id bytes", len(b), idBytesOff, layerOffsetsOff-idBytesOff, 1); err != nil {
		return nil, err
	}
	// THE HEADER CARRIES NO RUN COUNT, so the layer-offsets extent is DERIVED
	// from the gap to the arena — a derivation the writer's emission order
	// guarantees. It must be a positive multiple of 4, or the array is not a
	// whole number of uint32s and the sentinel that closes the last run is
	// missing.
	layerOffsetsBytes := neighborsOff - layerOffsetsOff
	if layerOffsetsBytes <= 0 || layerOffsetsBytes%4 != 0 {
		return nil, fmt.Errorf(
			"hnsw open: layer-offset array spans %d bytes, which is not a positive multiple of 4", layerOffsetsBytes)
	}
	if err := checkSection("layer offsets", len(b), layerOffsetsOff, layerOffsetsBytes, 4); err != nil {
		return nil, err
	}
	if err := checkSection("neighbor arena", len(b), neighborsOff, idDirOff-neighborsOff, 4); err != nil {
		return nil, err
	}
	if err := checkSection("id directory", len(b), idDirOff, g.nodes*4, 4); err != nil {
		return nil, err
	}
	if err := checkSection("vectors", len(b), vectorsOff, g.nodes*g.vecBytes, 1); err != nil {
		return nil, err
	}

	g.layerOffsets = u32sAt(b, layerOffsetsOff, layerOffsetsBytes/4)
	g.idDir = u32sAt(b, idDirOff, g.nodes)
	g.vectors = b[vectorsOff : vectorsOff+g.nodes*g.vecBytes]
	return g, nil
}

// decodeGraph opens a v3 blob. THE NAME IS KEPT ON PURPOSE: Format.Decode calls
// it and graph_test.go's round trip gates it, so the real reader installs here
// with no call-site rewrite.
func decodeGraph(b []byte) (*mappedGraph, error) { return openGraphV3(b) }

// le32 reads a little-endian uint32 at off. Not a typed view — a plain read, so
// the header costs no cast.
func le32(b []byte, off int) uint32 {
	_ = b[off+3]
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}

// entryAt returns the byte offset of node ord's directory entry.
func (g *mappedGraph) entryAt(ord uint32) int { return g.nodeDirOff + int(ord)*v3NodeEntrySize }

func (g *mappedGraph) nodeCount() int { return g.nodes }

// nodeMaxLevel is node ord's own top layer.
func (g *mappedGraph) nodeMaxLevel(ord uint32) int {
	e := g.entryAt(ord)
	return int(uint16(g.blob[e+v3EntMaxLevel]) | uint16(g.blob[e+v3EntMaxLevel+1])<<8)
}

// neighborsAt returns node ord's neighbor run at layer, or nil when layer
// exceeds that node's own maxLevel.
//
// THE DERIVATION the whole reader rests on: base := layerIdx + layer, and the
// run occupies the ABSOLUTE blob range [layerOffsets[base], layerOffsets[base+1]).
// A single global sentinel at the end of layerOffsets closes the last run.
func (g *mappedGraph) neighborsAt(ord uint32, layer int) []uint32 {
	if layer < 0 || layer > g.nodeMaxLevel(ord) {
		return nil
	}
	e := g.entryAt(ord)
	base := int(le32(g.blob, e+v3EntLayerIdx)) + layer
	if base < 0 || base+1 >= len(g.layerOffsets) {
		panic(fmt.Sprintf("hnsw: layer row %d for node %d is outside the %d-row offset array",
			base, ord, len(g.layerOffsets)))
	}
	lo, hi := int(g.layerOffsets[base]), int(g.layerOffsets[base+1])
	if lo < 0 || hi < lo || hi > len(g.blob) || (hi-lo)%4 != 0 {
		panic(fmt.Sprintf("hnsw: neighbor run for node %d layer %d spans [%d,%d) in a %d-byte blob",
			ord, layer, lo, hi, len(g.blob)))
	}
	return u32sAt(g.blob, lo, (hi-lo)/4)
}

// externalIDAt returns node ord's id as an INTERNAL VIEW sharing the blob. Any
// caller handing it across the segment API boundary must copy.
func (g *mappedGraph) externalIDAt(ord uint32) string {
	e := g.entryAt(ord)
	off := int(le32(g.blob, e+v3EntIDOff))
	length := int(uint16(g.blob[e+v3EntIDLen]) | uint16(g.blob[e+v3EntIDLen+1])<<8)
	return idView(g.blob, off, length)
}

// ids lists every externalID in ordinal order as INTERNAL VIEWS over the blob.
// The copy is the BOUNDARY's job, not this accessor's — hnswSegment.IDs clones,
// which keeps every copy at the one place a value escapes the segment.
func (g *mappedGraph) ids() []string {
	out := make([]string, g.nodes)
	for i := range out {
		out[i] = g.externalIDAt(uint32(i))
	}
	return out
}

// rangeVectors yields every (externalID, vector) pair in ordinal order. Both
// yielded values alias the mapping; Merge copies on Insert.
func (g *mappedGraph) rangeVectors(yield func(externalID string, vec []byte)) {
	for i := range g.nodes {
		yield(g.externalIDAt(uint32(i)), g.nodeVector(uint32(i)))
	}
}

// vectorByID resolves a stored vector by external id through a BINARY SEARCH
// over the id directory.
//
// THE DIRECTORY IS EXPLICIT RATHER THAN IMPLIED. Today's builder inserts in
// sorted-by-id order, so ordinal order already IS ascending-by-id and a search
// over the node directory alone would work for free. The format does not rely on
// that: sorted insertion is a BUILD-path determinism choice, not a format
// guarantee, and a read path that silently answers "not indexed" when it lapses
// is the exact silent degradation this ticket exists to prevent.
func (g *mappedGraph) vectorByID(externalID string) ([]byte, bool) {
	i := sort.Search(len(g.idDir), func(i int) bool {
		return g.externalIDAt(g.idDir[i]) >= externalID
	})
	if i >= len(g.idDir) || g.externalIDAt(g.idDir[i]) != externalID {
		return nil, false
	}
	return g.nodeVector(g.idDir[i]), true
}

func (g *mappedGraph) setEfSearch(ef int) { g.efSearch = ef }

// search delegates to the ONE traversal both graph forms share — see
// traverse.go. Re-implementing it here would put the score formula in two
// places.
func (g *mappedGraph) search(query []byte, k int, accept func(externalID string) bool) []graphHit {
	return searchTopK(&g.vectorBlock, g, g.maxLevel, g.entryPoint, query, g.efSearch, k, accept, g.externalIDAt)
}
