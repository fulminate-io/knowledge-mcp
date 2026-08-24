// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"slices"
	"strings"
)

// serialVersionOffsets is the v3 binary HNSW format: an OFFSET-ADDRESSED layout
// in which every section is located by an absolute u32 offset from the blob
// start, so a reader resolves the node directory, neighbor runs and vectors as
// views into the bytes rather than hydrating them into Go structures.
//
// It is the ONLY version encodeGraphV3 writes and the only version openGraphV3
// accepts. The v2 layout it replaces was a cursor-walked variable-length stream
// with NO directory — node i was reachable only by walking every prior node —
// which is precisely why it could not be read in place. There is no converter: a
// v2 blob is rejected with a rebuild-from-source remedy, exactly as bm25 did at
// its own v1-to-v2 bump.
const serialVersionOffsets byte = 3

// Fixed layout sizes.
const (
	// v3HeaderSize is the fixed header at offset 0.
	v3HeaderSize = 64
	// v3NodeEntrySize is one fixed-stride row of the node directory. The stride
	// is what makes node i addressable by arithmetic instead of by walking.
	v3NodeEntrySize = 16
)

// Header field offsets. Every offset field holds an ABSOLUTE position from the
// blob start — including the layerOffsets entries, which are absolute rather
// than arena-relative so no reader re-derives them.
const (
	v3HdrVersion        = 0
	v3HdrReserved1      = 1
	v3HdrReserved2      = 2
	v3HdrVecBytes       = 4
	v3HdrM              = 8
	v3HdrMMax0          = 12
	v3HdrEfConstruction = 16
	v3HdrMaxLevel       = 20
	v3HdrEntryPoint     = 24
	v3HdrNodeCount      = 28
	v3HdrNodeDir        = 32
	v3HdrIDBytes        = 36
	v3HdrLayerOffsets   = 40
	v3HdrNeighbors      = 44
	v3HdrIDDir          = 48
	v3HdrVectors        = 52
	v3HdrBlobLen        = 56
	v3HdrCRC            = 60
)

// Node-directory entry offsets, relative to the entry's start.
const (
	v3EntIDOff    = 0
	v3EntIDLen    = 4
	v3EntMaxLevel = 6
	v3EntLayerIdx = 8
	v3EntReserved = 12
)

// maxBlobBytes is the largest blob a u32 offset can address. It is a VARIABLE
// rather than a constant for ONE reason, stated plainly because it is a test
// seam: a blob that actually crosses this ceiling is not constructible in a test
// — at roughly 367 bytes per node it needs on the order of 11 million nodes —
// and a guard no fixture can drive is a guard nobody knows is wired. Lowering it
// in a test drives the real check through the real encoder. The shipped value is
// asserted by that same test BEFORE it lowers it, so a permanently-lowered
// ceiling cannot hide. bm25's v2MaxBlobBytes is the same seam for the same
// reason.
var maxBlobBytes = int64(math.MaxUint32)

// align rounds off up to the next multiple of a (a must be a power of two).
//
// THE PARAMETER IS UNIFORM HERE AND NOT DEAD IN THE IDIOM. This is a deliberate
// shape mirror of formats/bm25's align, which is genuinely polymorphic — 8, 4,
// and a variable across its 21 call sites. hnsw's v3 layout happens to align
// every section to 4, so `a` receives one value in THIS package only; the mirror
// is what keeps the two formats' layout arithmetic readable as the same idiom.
//
// WHAT RETIRES THE DIRECTIVE BELOW: a v3 section that aligns to anything other
// than 4. At that point the parameter is genuinely exercised here too, and the
// nolint must be DELETED rather than kept.
//
//nolint:unparam // uniform here, load-bearing as the bm25 mirror — see above
func align(off, a int) int { return (off + a - 1) &^ (a - 1) }

// crcTable is the Castagnoli polynomial table backing the footer checksum. It is
// FIXED BY THE FORMAT — the checksum on disk is computed with it, so changing it
// changes what a written blob means and is a version bump.
var crcTable = crc32.MakeTable(crc32.Castagnoli)

// encodeGraphV3 serializes the binary HNSW graph to a serialVersion-3 blob:
// header, fixed-stride node directory, id bytes, layer-offset array, neighbor
// arena, sorted id directory, flat vector block, footer CRC.
//
// It is a FREE FUNCTION rather than a method because it now returns an error and
// its reader counterpart lives elsewhere — bm25's encodeSegmentV2 for the same
// reasons.
//
// ONE SIZING PASS, THEN FILL, THEN BACKPATCH, THEN CRC LAST. Every section
// offset is computed before a byte is written, so the header can be filled in
// place and every offset it carries is ABSOLUTE FROM BLOB START — the layer
// offsets included. Because the arena begins 4-aligned and every run is a whole
// number of uint32s, EVERY run offset is 4-aligned by construction; that is what
// makes the reader's typed view legal, and it is a property of this emission
// order rather than an accident.
//
// DETERMINISM IS NON-NEGOTIABLE. Segment ids are the sha256 of these bytes, so a
// writer that varied on identical input would break content-addressed dedup.
// Nothing here iterates a Go map: nodes are emitted in ordinal order (h.nodes is
// a slice), neighbors in stored order, and the id directory is produced by
// sorting a slice of ordinals under a TOTAL order — ascending by id text with
// the ordinal as tie-break, so even an impossible duplicate sorts stably rather
// than landing in an unstable sort's input-dependent order.
//
// IT IS ONE FUNCTION ON PURPOSE, past the statement limit. The sizing pass, the
// fill, the header backpatch and the footer CRC are a single unit: every offset
// the fill writes is computed by the sizing pass immediately above it, and the
// CRC must cover the finished bytes. Splitting them would separate the
// arithmetic from its only use and open a window for a backpatch to land
// between the checksum and what it covers.
//
//nolint:funlen // see the paragraph above: the four passes are one unit by construction
func encodeGraphV3(h *binaryGraph) ([]byte, error) {
	nodeCount := len(h.nodes)

	// --- sizing pass -------------------------------------------------------
	idBytesTotal, totalRuns, arenaBytes := 0, 0, 0
	for i := range h.nodes {
		idBytesTotal += len(h.nodes[i].externalID)
		runs := len(h.nodes[i].neighbors)
		totalRuns += runs
		for l := range h.nodes[i].neighbors {
			arenaBytes += len(h.nodes[i].neighbors[l]) * 4
		}
	}

	nodeDirOff := align(v3HeaderSize, 4)
	idBytesOff := nodeDirOff + nodeCount*v3NodeEntrySize
	layerOffsetsOff := align(idBytesOff+idBytesTotal, 4)
	neighborsOff := align(layerOffsetsOff+(totalRuns+1)*4, 4)
	idDirOff := align(neighborsOff+arenaBytes, 4)
	vectorsOff := idDirOff + nodeCount*4
	crcOff := vectorsOff + nodeCount*h.vecBytes
	blobLen := crcOff + 4

	// GUARD (b): a u32 offset cannot address past this ceiling. Erroring is the
	// only correct move — a silently wrapped offset yields a corrupt blob that
	// still passes its own self-check.
	if int64(blobLen) > maxBlobBytes {
		return nil, fmt.Errorf(
			"hnsw encode: blob would be %d bytes, past the %d-byte ceiling a u32 offset can address",
			blobLen, maxBlobBytes)
	}

	buf := make([]byte, blobLen)

	// --- header ------------------------------------------------------------
	buf[v3HdrVersion] = serialVersionOffsets
	binary.LittleEndian.PutUint32(buf[v3HdrVecBytes:], uint32(h.vecBytes))
	binary.LittleEndian.PutUint32(buf[v3HdrM:], uint32(h.m))
	binary.LittleEndian.PutUint32(buf[v3HdrMMax0:], uint32(h.mMax0))
	binary.LittleEndian.PutUint32(buf[v3HdrEfConstruction:], uint32(h.efConstruction))
	binary.LittleEndian.PutUint32(buf[v3HdrMaxLevel:], uint32(int32(h.maxLevel)))
	binary.LittleEndian.PutUint32(buf[v3HdrEntryPoint:], h.entryPoint)
	binary.LittleEndian.PutUint32(buf[v3HdrNodeCount:], uint32(nodeCount))
	binary.LittleEndian.PutUint32(buf[v3HdrNodeDir:], uint32(nodeDirOff))
	binary.LittleEndian.PutUint32(buf[v3HdrIDBytes:], uint32(idBytesOff))
	binary.LittleEndian.PutUint32(buf[v3HdrLayerOffsets:], uint32(layerOffsetsOff))
	binary.LittleEndian.PutUint32(buf[v3HdrNeighbors:], uint32(neighborsOff))
	binary.LittleEndian.PutUint32(buf[v3HdrIDDir:], uint32(idDirOff))
	binary.LittleEndian.PutUint32(buf[v3HdrVectors:], uint32(vectorsOff))
	binary.LittleEndian.PutUint32(buf[v3HdrBlobLen:], uint32(blobLen))
	binary.LittleEndian.PutUint32(buf[v3HdrCRC:], uint32(crcOff))

	// --- node directory, id bytes, layer offsets, neighbor arena ----------
	idCursor, runCursor, arenaCursor := idBytesOff, 0, neighborsOff
	for ord := range h.nodes {
		node := &h.nodes[ord]
		ent := nodeDirOff + ord*v3NodeEntrySize
		binary.LittleEndian.PutUint32(buf[ent+v3EntIDOff:], uint32(idCursor))
		binary.LittleEndian.PutUint16(buf[ent+v3EntIDLen:], uint16(len(node.externalID)))
		binary.LittleEndian.PutUint16(buf[ent+v3EntMaxLevel:], uint16(node.maxLevel))
		binary.LittleEndian.PutUint32(buf[ent+v3EntLayerIdx:], uint32(runCursor))

		copy(buf[idCursor:], node.externalID)
		idCursor += len(node.externalID)

		for l := range node.neighbors {
			binary.LittleEndian.PutUint32(buf[layerOffsetsOff+runCursor*4:], uint32(arenaCursor))
			runCursor++
			for _, nb := range node.neighbors[l] {
				binary.LittleEndian.PutUint32(buf[arenaCursor:], nb)
				arenaCursor += 4
			}
		}
	}
	// The single global sentinel that closes the last run.
	binary.LittleEndian.PutUint32(buf[layerOffsetsOff+totalRuns*4:], uint32(arenaCursor))

	// --- id directory ------------------------------------------------------
	dir := make([]uint32, nodeCount)
	for i := range dir {
		dir[i] = uint32(i)
	}
	slices.SortFunc(dir, func(a, b uint32) int {
		if c := strings.Compare(h.nodes[a].externalID, h.nodes[b].externalID); c != 0 {
			return c
		}
		return int(a) - int(b) // total order: ties break on ordinal, never unstable
	})

	// GUARD (a): the directory must be STRICTLY ASCENDING by id text. A non-strict
	// pair means duplicate ids, which Insert makes impossible today — so reaching
	// it is a builder defect, and emitting it anyway would hand the reader a
	// binary search that silently answers "not indexed".
	for i := 1; i < len(dir); i++ {
		prev, cur := h.nodes[dir[i-1]].externalID, h.nodes[dir[i]].externalID
		if prev >= cur {
			return nil, fmt.Errorf(
				"hnsw encode: id directory is not strictly ascending at ordinals %d and %d (%q >= %q)",
				dir[i-1], dir[i], prev, cur)
		}
	}
	for i, ord := range dir {
		binary.LittleEndian.PutUint32(buf[idDirOff+i*4:], ord)
	}

	// --- vectors -----------------------------------------------------------
	if nodeCount > 0 && h.vecBytes > 0 {
		copy(buf[vectorsOff:crcOff], h.vectors)
	}

	// --- footer CRC, computed LAST so it covers every backpatched byte ------
	binary.LittleEndian.PutUint32(buf[crcOff:], crc32.Checksum(buf[:crcOff], crcTable))
	return buf, nil
}
