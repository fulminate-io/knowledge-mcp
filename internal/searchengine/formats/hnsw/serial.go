// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"fmt"
	"hash/crc32"
	"io"
	"slices"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// serial.go carries the serialVersion-3 EMITTER: the sizing pass, the one fill,
// the footer checksum and the sink adapter the Build path encodes through. The
// offset-addressed writer those stores go through, and the coalescing windows
// that keep the encode's syscall count proportional to flushes rather than to
// encoded values, are in serial_writer.go. The layout the bytes conform to — the
// version integers, the header field offsets and the dtype tags — is in
// serial_layout.go.

// v3Layout is every section offset a serialVersion-3 blob's emission depends on,
// derived once by sizeGraphV3 and consumed by encodeGraphV3To.
//
// Every offset here is ABSOLUTE FROM BLOB START, which is what lets the fill
// address a WriterAt exactly as it addressed a slice.
type v3Layout struct {
	nodeCount       int
	totalRuns       int
	nodeDirOff      int
	idBytesOff      int
	layerOffsetsOff int
	neighborsOff    int
	idDirOff        int
	vectorsOff      int
	crcOff          int
	blobLen         int
}

// sizeGraphV3 derives the blob's section layout from the graph, and carries
// GUARD (b) with it.
//
// IT IS A SEPARATE FUNCTION BECAUSE TWO CALLERS NEED THE SAME ARITHMETIC.
// encodeGraphV3 must know blobLen to allocate the slice it hands the sink before
// encodeGraphV3To can write a byte, and encodeGraphV3To needs every offset to
// place its stores. Deriving it twice in two places is the failure this shape
// exists to prevent: the two copies must agree byte-for-byte, in a function
// whose output is content-hashed into every segment id.
//
// THE GUARD TRAVELS WITH THE ARITHMETIC and must not be hoisted into one caller,
// or the other loses it.
func sizeGraphV3(h *binaryGraph) (v3Layout, error) {
	var l v3Layout
	l.nodeCount = len(h.nodes)

	idBytesTotal, arenaBytes := 0, 0
	for i := range h.nodes {
		idBytesTotal += len(h.nodes[i].externalID)
		l.totalRuns += len(h.nodes[i].neighbors)
		for lv := range h.nodes[i].neighbors {
			arenaBytes += len(h.nodes[i].neighbors[lv]) * 4
		}
	}

	l.nodeDirOff = align(v3HeaderSize, 4)
	l.idBytesOff = l.nodeDirOff + l.nodeCount*v3NodeEntrySize
	l.layerOffsetsOff = align(l.idBytesOff+idBytesTotal, 4)
	l.neighborsOff = align(l.layerOffsetsOff+(l.totalRuns+1)*4, 4)
	l.idDirOff = align(l.neighborsOff+arenaBytes, 4)
	l.vectorsOff = l.idDirOff + l.nodeCount*4
	l.crcOff = l.vectorsOff + l.nodeCount*h.vecBytes
	l.blobLen = l.crcOff + 4

	// GUARD (b): a u32 offset cannot address past this ceiling. Erroring is the
	// only correct move — a silently wrapped offset yields a corrupt blob that
	// still passes its own self-check.
	if int64(l.blobLen) > maxBlobBytes {
		return v3Layout{}, fmt.Errorf(
			"hnsw encode: blob would be %d bytes, past the %d-byte ceiling a u32 offset can address",
			l.blobLen, maxBlobBytes)
	}
	return l, nil
}

// v3CRCChunk is the fixed window encodeGraphV3To reads back through to checksum
// the finished blob. It is a NAMED CONSTANT and never derived from blobLen: a
// buffer sized from the output would reintroduce precisely the output-sized
// allocation this emitter exists to remove.
const v3CRCChunk = 64 << 10

// encodeGraphV3 serializes the binary HNSW graph to a serialVersion-3 blob and
// returns it as heap bytes.
//
// It is a thin wrapper over the one emitter. The Build path has no file behind
// it — publishGraph reopens these bytes as the segment's payload — so it sizes a
// slice, wraps it as a sink, and delegates.
func encodeGraphV3(h *binaryGraph) ([]byte, error) {
	layout, err := sizeGraphV3(h)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, layout.blobLen)
	if _, err := encodeGraphV3To(sliceSink(buf), h); err != nil {
		return nil, err
	}
	return buf, nil
}

// encodeGraphV3To is THE serialVersion-3 emitter: header, fixed-stride node
// directory, id bytes, layer-offset array, neighbor arena, sorted id directory,
// flat vector block, footer CRC. It writes into dst and returns the blob's
// length. It does not truncate, close or otherwise own dst.
//
// SIZE, THEN FILL, THEN BACKPATCH, THEN CRC LAST. Every section offset is
// computed before a byte is written, so the header is filled in place and every
// offset it carries is ABSOLUTE FROM BLOB START — the layer offsets included.
// Because the arena begins 4-aligned and every run is a whole number of uint32s,
// EVERY run offset is 4-aligned by construction; that is what makes the reader's
// typed view legal, and it is a property of this emission order rather than an
// accident.
//
// DETERMINISM IS NON-NEGOTIABLE. Segment ids are the sha256 of these bytes, so a
// writer that varied on identical input would break content-addressed dedup.
// Nothing here iterates a Go map: nodes are emitted in ordinal order (h.nodes is
// a slice), neighbors in stored order, and the id directory is produced by
// sorting a slice of ordinals under a TOTAL order — ascending by id text with
// the ordinal as tie-break, so even an impossible duplicate sorts stably rather
// than landing in an unstable sort's input-dependent order.
//
// THE SIZING PASS IS SPLIT OUT AND THE REST IS NOT, and the asymmetry is the
// point. sizeGraphV3 is separated deliberately so this function and its
// slice-allocating wrapper share ONE copy of the arithmetic. The fill, the
// header backpatch and the footer CRC stay a single unit here for the reason the
// original one-function form gave: the CRC must cover the finished bytes, and
// splitting it out would open a window for a backpatch to land between the
// checksum and what it covers. The CRC is still computed LAST.
//
// THE CHECKSUM RE-READS RATHER THAN ACCUMULATES, and that is forced by the
// format. The fill advances idCursor, runCursor, arenaCursor and the node
// directory's entry offset in four independently-ascending but jointly
// out-of-order streams, so there is no point at which a running checksum could
// be fed the bytes in file order. It is computed instead by reading the written
// range back through a FIXED v3CRCChunk buffer. The Build path pays for this
// too: it now checksums by reading its just-written slice back in bounded chunks
// rather than by one crc32.Checksum over memory it already holds — copies within
// this process, no syscall, and no allocation beyond the fixed buffer.
//
// THE SIZING PASS RUNS TWICE ON THE BUILD PATH, stated rather than hidden: the
// wrapper runs it to size its slice and this function runs it again, because the
// signature takes only (dst, h) and carries no layout parameter. That pass is
// pure arithmetic over h.nodes and their neighbor slices, O(nodes + runs), and
// allocates nothing. It is a small constant on an operation dominated by the
// fill and the vector block, and it buys one emitter with a signature neither
// caller has to special-case.
//
// WHAT THIS FUNCTION DOES NOT BUY. It removes the encoder's output-sized buffer;
// it does not make an hnsw MERGE allocate nothing output-sized. hnsw's merge
// re-inserts every survivor into a fresh binaryGraph, and that graph's vector
// block alone is output-sized and must be fully resident before a byte can be
// emitted. The zero-heap merge property is bm25's.
//
//nolint:funlen // the fill, the backpatch and the CRC are one unit by construction; see above
func encodeGraphV3To(dst searchengine.MergeSink, h *binaryGraph) (int64, error) {
	layout, err := sizeGraphV3(h)
	if err != nil {
		return 0, err
	}
	w := newV3Writer(dst)

	// --- header ------------------------------------------------------------
	// THE VERSION IS SELECTED FROM THE DTYPE, never written as a constant: a
	// float32 blob must announce a version already-released readers refuse
	// outright, rather than one they accept and then misread. See
	// serialVersionFloat32.
	w.putByte(v3HdrVersion, versionForDtype(h.dtype))
	w.putByte(v3HdrDtype, h.dtype)
	w.putU32(v3HdrVecBytes, uint32(h.vecBytes))
	w.putU32(v3HdrM, uint32(h.m))
	w.putU32(v3HdrMMax0, uint32(h.mMax0))
	w.putU32(v3HdrEfConstruction, uint32(h.efConstruction))
	w.putU32(v3HdrMaxLevel, uint32(int32(h.maxLevel)))
	w.putU32(v3HdrEntryPoint, h.entryPoint)
	w.putU32(v3HdrNodeCount, uint32(layout.nodeCount))
	w.putU32(v3HdrNodeDir, uint32(layout.nodeDirOff))
	w.putU32(v3HdrIDBytes, uint32(layout.idBytesOff))
	w.putU32(v3HdrLayerOffsets, uint32(layout.layerOffsetsOff))
	w.putU32(v3HdrNeighbors, uint32(layout.neighborsOff))
	w.putU32(v3HdrIDDir, uint32(layout.idDirOff))
	w.putU32(v3HdrVectors, uint32(layout.vectorsOff))
	w.putU32(v3HdrBlobLen, uint32(layout.blobLen))
	w.putU32(v3HdrCRC, uint32(layout.crcOff))

	// --- node directory, id bytes, layer offsets, neighbor arena ----------
	idCursor, runCursor, arenaCursor := layout.idBytesOff, 0, layout.neighborsOff
	for ord := range h.nodes {
		node := &h.nodes[ord]
		ent := layout.nodeDirOff + ord*v3NodeEntrySize
		w.putU32(ent+v3EntIDOff, uint32(idCursor))
		w.putU16(ent+v3EntIDLen, uint16(len(node.externalID)))
		w.putU16(ent+v3EntMaxLevel, uint16(node.maxLevel))
		w.putU32(ent+v3EntLayerIdx, uint32(runCursor))

		w.putString(idCursor, node.externalID)
		idCursor += len(node.externalID)

		for l := range node.neighbors {
			w.putU32(layout.layerOffsetsOff+runCursor*4, uint32(arenaCursor))
			runCursor++
			for _, nb := range node.neighbors[l] {
				w.putU32(arenaCursor, nb)
				arenaCursor += 4
			}
		}
	}
	// The single global sentinel that closes the last run.
	w.putU32(layout.layerOffsetsOff+layout.totalRuns*4, uint32(arenaCursor))

	// --- id directory ------------------------------------------------------
	dir := make([]uint32, layout.nodeCount)
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
			return 0, fmt.Errorf(
				"hnsw encode: id directory is not strictly ascending at ordinals %d and %d (%q >= %q)",
				dir[i-1], dir[i], prev, cur)
		}
	}
	for i, ord := range dir {
		w.putU32(layout.idDirOff+i*4, ord)
	}

	// --- vectors -----------------------------------------------------------
	//
	// The length is min'd against the block the layout reserved, mirroring what
	// the slice form did implicitly: `copy(buf[vectorsOff:crcOff], h.vectors)`
	// copied whichever was shorter. Slicing h.vectors to the block size directly
	// would panic instead of truncating on a graph whose vector store is short.
	if layout.nodeCount > 0 && h.vecBytes > 0 {
		w.put(layout.vectorsOff, h.vectors[:min(len(h.vectors), layout.crcOff-layout.vectorsOff)])
	}

	// EVERY WINDOW IS FLUSHED BEFORE THE HELD ERROR IS READ, and both halves of
	// that sentence are load-bearing. checksumRange reads the SINK back rather
	// than any buffer, so bytes still held in a window here would be invisible to
	// the CRC and the footer would describe a blob nobody wrote. And the error is
	// checked AFTER the flush so a failing flush lands in w.err and returns here,
	// rather than being discovered after the checksum has already read a partial
	// blob.
	//
	// The held error is checked ONCE here, before the checksum reads anything
	// back: a partial encode is discarded whole, so the first failure is the only
	// one that carries information, and checksumming a half-written blob would
	// turn a write failure into a corrupt-looking success.
	w.flushAll()
	if w.err != nil {
		return 0, w.err
	}

	// --- footer CRC, computed LAST so it covers every backpatched byte ------
	sum, err := checksumRange(dst, layout.crcOff)
	if err != nil {
		return 0, err
	}
	w.putU32(layout.crcOff, sum)
	w.flushAll()
	if w.err != nil {
		return 0, w.err
	}
	return int64(layout.blobLen), nil
}

// checksumRange computes the Castagnoli CRC over dst's first n bytes, reading
// them back through one fixed-size buffer.
func checksumRange(dst searchengine.MergeSink, n int) (uint32, error) {
	buf := make([]byte, v3CRCChunk)
	var sum uint32
	for off := 0; off < n; {
		end := min(off+v3CRCChunk, n)
		chunk := buf[:end-off]
		if _, err := dst.ReadAt(chunk, int64(off)); err != nil {
			return 0, fmt.Errorf("hnsw encode: reading back at %d to checksum: %w", off, err)
		}
		sum = crc32.Update(sum, crcTable, chunk)
		off = end
	}
	return sum, nil
}

// sliceSink adapts a byte slice to a MergeSink so the Build path shares the one
// emitter without a file. It is bounds-checked at both edges and is deliberately
// not exported: it is an adapter for this package's wrapper, not a utility.
type sliceSink []byte

func (s sliceSink) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(s)) || int64(len(p)) > int64(len(s))-off {
		return 0, io.ErrShortWrite
	}
	return copy(s[off:], p), nil
}

func (s sliceSink) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(s)) {
		return 0, io.EOF
	}
	n := copy(p, s[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
