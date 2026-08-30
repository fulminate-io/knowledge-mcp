// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"hash/crc32"
	"math"
)

// serial_layout.go is the serialVersion-3 format's DECLARATION: the version
// integers and why each exists, the fixed header and node-directory field
// offsets, the vector dtype tags, and the size ceilings. It describes bytes on
// disk and is read by the emitter in serial.go and by the reader in mapped.go.

// serialVersionOffsets is the v3 binary HNSW format: an OFFSET-ADDRESSED layout
// in which every section is located by an absolute u32 offset from the blob
// start, so a reader resolves the node directory, neighbor runs and vectors as
// views into the bytes rather than hydrating them into Go structures.
//
// It is the version encodeGraphV3 writes for UBINARY vectors, and one of the two
// openGraphV3 accepts — the float32 flavor of the same layout is
// serialVersionFloat32, which see for why the dtype gets its own version number
// rather than riding the header tag alone.
//
// The v2 layout it replaces was a cursor-walked variable-length stream with NO
// directory — node i was reachable only by walking every prior node — which is
// precisely why it could not be read in place. There is no converter: a v2 blob
// is rejected with a rebuild-from-source remedy, exactly as bm25 did at its own
// v1-to-v2 bump.
const serialVersionOffsets byte = 3

// serialVersionFloat32 is the SAME offset-addressed layout as
// serialVersionOffsets, carrying float32 vectors instead of ubinary ones. The
// two versions differ in exactly one respect — how the vector block is to be
// read — and that is the whole reason the float32 flavor gets its own number.
//
// WHY A DISTINCT VERSION RATHER THAN THE DTYPE TAG ALONE. The tag is enough for
// THIS build, which reads it and dispatches. It is not enough for the builds
// already deployed: every released client accepts any blob whose version byte is
// 3, ignores the byte at v3HdrDtype entirely, and would read a float32 vector
// block as bit patterns — ranking it by Hamming distance and returning
// confident, wrong neighbors from a segment that passes every structural check
// and its own CRC. Segment distribution filters on the format NAME, which is
// unchanged, so an old client genuinely can be handed one of these.
//
// A version an old reader does not recognize converts that silent mis-ranking
// into the loud unsupported-version refusal it ALREADY implements, remedy
// included. That is the bad-input-always-errors rule applied to a reader that
// has not been written yet, and it is the same disjoint-families reasoning
// formatName records for the distribution layer, applied one level down.
//
// UBINARY BLOBS STAY AT 3, BYTE-IDENTICALLY. A segment's id is the sha256 of its
// bytes, so moving the version byte on the existing corpus would re-key every
// stored ubinary segment and force a global rebuild to express nothing.
const serialVersionFloat32 byte = 4

// versionForDtype is the single place the dtype-to-version mapping lives, so the
// encoder and the reader's cross-check cannot disagree about it.
func versionForDtype(dtype byte) byte {
	if dtype == dtypeFloat32 {
		return serialVersionFloat32
	}
	return serialVersionOffsets
}

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
	v3HdrDtype          = 1
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

// Vector dtype tags, carried in the header byte at v3HdrDtype (formerly named
// v3HdrReserved1). The tag says how to READ the vector block and therefore which
// metric ranks it: ubinary vectors are compared by Hamming distance, float32
// vectors by dot product.
//
// THIS IS NOT A FORMAT VERSION BUMP, AND THE REASON IS A FACT ABOUT BYTES ALREADY
// ON DISK. serialVersionOffsets stays 3. The two bytes at offsets 1 and 2 have
// been present-and-unused since v3 shipped — encodeGraphV3 never wrote them, and
// Go zeroes the buffer it allocates, so EVERY v3 blob any release has written
// carries 0 in this position. Tag 0 is dtypeUbinary, so those historical segments
// already say "ubinary" under the new reading, and a tag-dispatching reader needs
// no converter and no migration. A version bump would be the opposite trade: it
// would reject every existing segment and force a rebuild to express a property
// those segments already encode correctly.
//
// The claim in the paragraph above is a claim about HISTORY, so it is tested
// against history rather than against this writer: testdata/hnsw_v3_ubinary_segment.seg
// was captured from the encoder BEFORE this tag existed, and
// TestV3DtypeTagZeroLoadsAsUbinary decodes that file.
const (
	dtypeUbinary byte = 0
	dtypeFloat32 byte = 1
)

// dtypeVecAlign is the alignment the vector block requires for the given dtype: a
// ubinary block is a plain byte array and needs none, while a float32 block is
// read through a typed view and must be 4-aligned or the cast is illegal.
//
// The v3 emission order already satisfies the float32 case by construction —
// vectorsOff = idDirOff + nodeCount*4 and idDirOff is 4-aligned by align() — so
// this function states the requirement the reader CHECKS rather than one it has
// to arrange.
func dtypeVecAlign(dtype byte) int {
	if dtype == dtypeFloat32 {
		return 4
	}
	return 1
}

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

// v3PerNodeOverheadBytes is the MEASURED non-vector cost of one node in a v3
// blob. It is not a guess and not a round number: the width-scaling research fitted
// the per-node cost by executing real builds at widths from 32 to 8192 bytes and
// found it to be exactly 292.2 + vecBytes at every width — the node-directory
// entry, the id bytes, the layer-offset rows and the neighbor arena, none of
// which scale with the vector.
//
// A reader can re-derive the published ceilings from it rather than trust them:
// maxBlobBytes / (292.2 + vecBytes) gives 13.25M nodes at w=32, 979k at w=4096
// (1024-dim float32) and 506k at w=8192 (2048-dim float32), which are the three
// figures that research records.
const v3PerNodeOverheadBytes = 292.2

// v3SealSafetyDivisor is how far BELOW the hard u32 ceiling a segment seals, and
// the number has a specific meaning rather than being a comfort margin: it is the
// number of FULL segments one merge may consolidate and still produce a legal
// blob. At 2, any two full segments merge cleanly and land exactly at the hard
// ceiling; three do not, which is precisely why Merge CHECKS the bound instead of
// assuming it (see maxNodesPerSegment's callers).
const v3SealSafetyDivisor = 2

// maxNodesPerSegment is the largest node count a v3 segment should hold at a
// given vector width, derived from the measured per-node model above.
//
// WHY THIS EXISTS AT ALL: every v3 section offset is a u32, so encodeGraphV3
// ERRORS rather than wrapping once a blob would cross maxBlobBytes. Because Merge
// re-inserts every survivor through the builder — an HNSW graph cannot be spliced,
// its neighbor links are internal-id-relative — that ceiling surfaces as a FAILED
// CONSOLIDATION rather than as gradual degradation. Wide float32 vectors reach it
// roughly 26x sooner than the 32-byte ubinary corpus this format shipped with.
//
// THE COST OF SEALING SMALLER IS REAL AND IS NOT A FREE WIN: more segments means
// wider query-time fan-out. That trade is forced by the u32 ceiling, not chosen,
// and owners are expected to keep the resulting count within the engine's
// SegmentCountTarget model rather than let it drift.
func maxNodesPerSegment(vecBytes int) int {
	perNode := v3PerNodeOverheadBytes + float64(vecBytes)
	return int(float64(maxBlobBytes)/perNode) / v3SealSafetyDivisor
}

// MaxSegmentDocsForWidth is the exported mirror of maxNodesPerSegment, for owners
// that choose an engine's seal threshold.
//
// It is exported for the same reason searchengine.DefaultMinSegmentDocs is: the
// value has to cross a package boundary to be used, and the dependency only runs
// one way — this package imports searchengine, so searchengine cannot import back
// to ask. An owner indexing wide vectors clamps its Options.MinSegmentDocs with
// this rather than taking the engine default, which is sized for 32-byte ubinary
// vectors and is ~13000x below the ceiling there but would exceed it at width.
func MaxSegmentDocsForWidth(vecBytes int) int { return maxNodesPerSegment(vecBytes) }

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
