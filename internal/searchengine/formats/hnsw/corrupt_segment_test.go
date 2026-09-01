// SPDX-License-Identifier: Apache-2.0

package hnsw

// corrupt_segment_test.go — the hnsw half of the segment-corruption containment,
// and the read-path audit behind it.
//
// WHY THE THREAT MODEL DIFFERS FROM bm25's, which is the first thing to state
// because it changes what is worth defending. openGraphV3 verifies a footer
// CRC32 over the whole blob prefix before any typed view is taken, so ordinary
// bit rot in an hnsw segment is REFUSED AT OPEN with a rebuild remedy. bm25 has
// no such check, which is why one damaged byte there reached a query at all.
//
// WHAT THE CRC DOES NOT COVER, and why this file exists: a WRITER that emits
// internally inconsistent bytes and then checksums exactly what it emitted. The
// CRC is computed over the damage, so it matches, and the segment opens clean —
// which is precisely the shape the bm25 incident turned out to have (the id was
// the sha256 of the very bytes that were wrong). Every fixture here therefore
// re-seals the footer CRC after damaging the payload: a fixture that left the
// CRC stale would be refused at open and would prove nothing about the read
// path below it.
//
// THE DAMAGE IS MINIMAL AND ASSERTED. Each case changes the smallest field that
// expresses the defect and asserts how many bytes differ, so a raise is
// attributable to that field rather than to collateral damage.

import (
	"encoding/binary"
	"hash/crc32"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// resealCRC recomputes the footer checksum so a damaged payload still opens.
// Without it every case below would be rejected by openGraphV3's CRC check and
// would exercise nothing past open.
func resealCRC(t *testing.T, b []byte) {
	t.Helper()
	crcOff := int(binary.LittleEndian.Uint32(b[v3HdrCRC:]))
	// The checksum is the blob's LAST four bytes, so crcOff+4 lands exactly on
	// its length; anything beyond that is a header pointing outside itself.
	require.LessOrEqual(t, crcOff+4, len(b), "the footer offset must lie inside the blob")
	binary.LittleEndian.PutUint32(b[crcOff:], crc32.Checksum(b[:crcOff], crcTable))
}

// damagedNeighborOrdinal returns a payload whose FIRST neighbor entry names a
// node ordinal that does not exist, with the CRC re-sealed.
//
// An out-of-range ordinal is the shape with the widest blast radius in this
// format: neighbor ids are the currency of the whole traversal, and every one of
// them is used to index the node directory and the vector block.
func damagedNeighborOrdinal(t *testing.T, clean []byte, nodes int) []byte {
	t.Helper()
	dmg := slices.Clone(clean)
	// The neighbor arena begins at the header's neighbors offset; its first
	// uint32 is a neighbor ordinal belonging to some node's layer run.
	arena := int(binary.LittleEndian.Uint32(dmg[v3HdrNeighbors:]))
	binary.LittleEndian.PutUint32(dmg[arena:], uint32(nodes)+7)
	resealCRC(t, dmg)
	return dmg
}

// TestHNSWReadPath_CorruptNeighborOrdinal_RaisesTypedNotBarePanic is the audit's
// core case: an ordinal past the node count must arrive as a value the engine
// can contain, not as a runtime panic indistinguishable from a nil dereference.
func TestHNSWReadPath_CorruptNeighborOrdinal_RaisesTypedNotBarePanic(t *testing.T) {
	clean, _, _ := mappedFixture(t, 512)
	mg, err := openGraphV3(clean)
	require.NoError(t, err)
	nodes := mg.nodeCount()

	dmg := damagedNeighborOrdinal(t, clean, nodes)
	require.Len(t, dmg, len(clean))
	require.LessOrEqual(t, byteDiffCount(clean, dmg), 8,
		"the recipe must change one neighbor ordinal and the footer CRC, nothing else")

	dmgGraph, err := openGraphV3(dmg)
	require.NoError(t, err,
		"the damaged payload must still OPEN — its CRC was re-sealed, which is what makes it the writer-defect shape rather than bit rot")

	var raised *searchengine.CorruptSegmentError
	func() {
		defer searchengine.CatchCorrupt("dmg-neighbor", &raised)
		// The real query path, not an accessor poked by hand.
		dmgGraph.search(queryOfWidth(t, dmgGraph.vecBytes), 10, nil)
	}()
	require.NotNil(t, raised,
		"a search over a segment whose neighbor run names a node that does not exist must raise a typed corruption; "+
			"a bare runtime panic here is indistinguishable from a genuine bug and so cannot be contained")
	t.Logf("search raised: %v", raised)
}

// TestHNSWReadPath_CorruptIDOffset_RaisesTypedNotBarePanic covers the other
// per-node datum a validated section does not constrain: the id offset stored
// INSIDE a node-directory entry, which open deliberately does not walk.
func TestHNSWReadPath_CorruptIDOffset_RaisesTypedNotBarePanic(t *testing.T) {
	clean, _, _ := mappedFixture(t, 128)
	dmg := slices.Clone(clean)

	nodeDir := int(binary.LittleEndian.Uint32(dmg[v3HdrNodeDir:]))
	binary.LittleEndian.PutUint32(dmg[nodeDir+v3EntIDOff:], uint32(len(dmg))+1024)
	resealCRC(t, dmg)
	require.LessOrEqual(t, byteDiffCount(clean, dmg), 8, "one id offset plus the CRC")

	mg, err := openGraphV3(dmg)
	require.NoError(t, err, "the damage is below the header and the CRC was re-sealed")

	var raised *searchengine.CorruptSegmentError
	func() {
		defer searchengine.CatchCorrupt("dmg-idoff", &raised)
		_ = mg.externalIDAt(0)
	}()
	require.NotNil(t, raised, "an id offset past the blob must raise a typed corruption")
	t.Logf("id view raised: %v", raised)
}

// TestHNSWReadPath_CorruptLayerIndex_RaisesTypedNotBarePanic drives the layer
// derivation — base := layerIdx + layer — which the reader's own comment calls
// the thing the whole reader rests on.
func TestHNSWReadPath_CorruptLayerIndex_RaisesTypedNotBarePanic(t *testing.T) {
	clean, _, _ := mappedFixture(t, 128)
	dmg := slices.Clone(clean)

	nodeDir := int(binary.LittleEndian.Uint32(dmg[v3HdrNodeDir:]))
	binary.LittleEndian.PutUint32(dmg[nodeDir+v3EntLayerIdx:], 1<<30)
	resealCRC(t, dmg)
	require.LessOrEqual(t, byteDiffCount(clean, dmg), 8, "one layer index plus the CRC")

	mg, err := openGraphV3(dmg)
	require.NoError(t, err)

	var raised *searchengine.CorruptSegmentError
	func() {
		defer searchengine.CatchCorrupt("dmg-layeridx", &raised)
		_ = mg.neighborsAt(0, 0)
	}()
	require.NotNil(t, raised, "a layer row outside the offset array must raise a typed corruption")
	t.Logf("neighborsAt raised: %v", raised)
}

// TestValidateSegment_CleanPassesAndDamagedTopologyIsFlagged is the validator's
// own control set, and the arm the store census delegates its deep hnsw
// discrimination to — the census cannot stage this damage without duplicating
// this format's header offsets and checksum polynomial into a package that does
// not own them.
func TestValidateSegment_CleanPassesAndDamagedTopologyIsFlagged(t *testing.T) {
	clean, _, _ := mappedFixture(t, 256)

	// CONTROL. A validator that rejected everything would satisfy the damaged arm.
	require.NoError(t, ValidateSegment("clean", clean),
		"a graph this package just sealed must pass its own reader-rule validator")

	mg, err := openGraphV3(clean)
	require.NoError(t, err)
	dmg := damagedNeighborOrdinal(t, clean, mg.nodeCount())
	require.LessOrEqual(t, byteDiffCount(clean, dmg), 8,
		"one neighbor ordinal plus the re-sealed CRC, nothing else")

	err = ValidateSegment("dmg", dmg)
	var ce *searchengine.CorruptSegmentError
	require.ErrorAs(t, err, &ce,
		"a stored neighbor run naming a node the segment does not have must be reported as corrupt; "+
			"unreported, the census reads a store clean that an ordinary search would die on")
	require.Contains(t, ce.Detail, "ordinal")
	require.Equal(t, searchengine.SegmentID("dmg"), ce.ID,
		"the validator stamps the id so a census of thousands can name the file")
}

// TestValidateSegment_RefusesBytesThatAreNotASegment pins the OTHER verdict the
// census distinguishes: bytes that cannot be opened are a plain error, never a
// CorruptSegmentError. Merging the two would let "this file is not a segment"
// read as "this segment's stored references are wrong".
func TestValidateSegment_RefusesBytesThatAreNotASegment(t *testing.T) {
	err := ValidateSegment("junk", []byte("not an hnsw segment"))
	require.Error(t, err)
	require.ErrorContains(t, err, "does not open")
	var ce *searchengine.CorruptSegmentError
	require.NotErrorIs(t, err, ce, "unopenable bytes are not a structural corruption verdict")
}

// byteDiffCount reports how many bytes differ between two equal-length buffers,
// so each recipe above can assert it damaged only what it meant to.
func byteDiffCount(a, b []byte) int {
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}

// queryOfWidth returns a query vector this segment will accept, so a search
// reaches the traversal rather than being refused for width.
func queryOfWidth(t *testing.T, vecBytes int) []byte {
	t.Helper()
	require.Positive(t, vecBytes, "the fixture segment must declare a vector width")
	return make([]byte, vecBytes)
}

// TestOpenAcceptsAnEmptySegmentsMaxLevel pins the FLOOR of the max-level bound
// against the case that broke when the bound was first written.
//
// AN EMPTY GRAPH HAS NO LEVELS and this encoder writes -1 to say so. A bound
// that floored at 0 therefore refused every empty segment the engine seals —
// which the engine does routinely, whenever a batch is sealed while embedding is
// still draining. The observed consequence was not a refused read but a merge
// that could never succeed: two empty hnsw segments failed to consolidate, and
// the merge loop retried them every 50ms forever.
//
// The ceiling is asserted in the same test so the two halves cannot drift apart.
func TestOpenAcceptsAnEmptySegmentsMaxLevel(t *testing.T) {
	seg, err := Format{}.Build(nil)
	require.NoError(t, err, "the engine seals empty batches; the format must build one")
	payload, err := seg.Encode()
	require.NoError(t, err)

	g, err := openGraphV3(payload)
	require.NoError(t, err, "an empty segment must open: its -1 max level is the encoder saying it has no levels")
	require.Zero(t, g.nodeCount())
	require.Negative(t, g.maxLevel, "the empty-graph sentinel is what the floor has to admit")

	require.NoError(t, ValidateSegment("empty", payload),
		"the census must not report every empty segment in the store as corrupt")

	// THE CEILING still refuses a header no node's level field could express.
	overCeiling := slices.Clone(payload)
	binary.LittleEndian.PutUint32(overCeiling[v3HdrMaxLevel:], uint32(math.MaxUint16)+1)
	resealCRC(t, overCeiling)
	_, err = openGraphV3(overCeiling)
	require.ErrorContains(t, err, "max level",
		"a max level past the uint16 a node's own field holds must still be refused, or the descent loop is unbounded again")
}
