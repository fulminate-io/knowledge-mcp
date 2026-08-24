// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/stretchr/testify/require"
)

// mappedFixture seals a real graph and returns the blob plus its reader.
func mappedFixture(t *testing.T, n int) ([]byte, *mappedGraph, *binaryGraph) {
	t.Helper()
	g := buildBinaryHNSWSerialDeterministic(randomBuildItems(t, n), defaultVecBytes, defaultM, defaultEfConstruction)
	blob, err := encodeGraphV3(g)
	require.NoError(t, err)
	mg, err := openGraphV3(blob)
	require.NoError(t, err)
	return blob, mg, g
}

// TestNeighborsAtMatchesBuiltTopology is the reader's topology proof: for EVERY
// node and EVERY layer, the run the mapped reader derives from the offset array
// must equal the run the built graph holds in Go memory.
//
// It is the assertion that would catch an off-by-one in the layerIdx + layer
// derivation, an arena-relative reading of an absolute offset, or a missing
// sentinel — none of which crash. They return a WRONG NEIGHBOR SET, and a
// wrong neighbor set degrades recall silently while every other test passes.
func TestNeighborsAtMatchesBuiltTopology(t *testing.T) {
	// THE CORPUS IS SIZED SO UPPER LAYERS EXIST. Level assignment is exponential
	// with ml = 1/ln(M) at M=32, so roughly 1 node in 32 rises above layer 0 — a
	// 64-node fixture drew ALL of them at layer 0 and left the layerIdx + layer
	// derivation, the single most bug-prone line in the reader, completely
	// unexercised while the test still passed every comparison it made. The
	// multiLayer assertion below is what keeps that from silently recurring.
	_, mg, g := mappedFixture(t, 512)

	require.Equal(t, g.nodeCount(), mg.nodeCount())
	compared, multiLayer := 0, 0
	for ord := range g.nodes {
		if g.nodes[ord].maxLevel > 0 {
			multiLayer++
		}
		require.Equal(t, g.nodes[ord].maxLevel, mg.nodeMaxLevel(uint32(ord)),
			"node %d maxLevel", ord)
		for layer := 0; layer <= g.nodes[ord].maxLevel; layer++ {
			want := g.neighborsAt(uint32(ord), layer)
			got := mg.neighborsAt(uint32(ord), layer)
			require.Equal(t, want, got, "node %d layer %d neighbor run", ord, layer)
			compared++
		}
		// Past the node's own top layer both forms yield nil.
		require.Nil(t, mg.neighborsAt(uint32(ord), g.nodes[ord].maxLevel+1),
			"node %d above maxLevel must yield nil", ord)
	}
	// KNOWN-POSITIVE FLOORS, and the second is the one that matters. Comparing
	// only layer-0 runs would exercise a base + 0 derivation and prove nothing
	// about base + layer, so the fixture must contain nodes that actually live
	// above layer 0 — and this asserts it rather than hoping.
	require.Greater(t, compared, g.nodeCount(), "fixture must exercise more runs than nodes")
	require.Positive(t, multiLayer,
		"fixture must contain nodes ABOVE layer 0, or the layerIdx+layer derivation is untested")
}

// TestVectorByIDResolvesWhenOrdinalOrderIsNotIDOrder is the DISCRIMINATOR for
// the explicit id directory.
//
// Today's builder inserts sorted by id, so ordinal order already IS ascending-by-id
// and a binary search keyed on the NODE DIRECTORY would work by accident. That
// makes every ordinary fixture unable to tell a correct id-keyed lookup from an
// ordinal-keyed one. This test breaks the coincidence: it rewrites the ids so
// ordinal order and id order DISAGREE, re-encodes, and requires every id to still
// resolve to its own vector. An ordinal-keyed directory fails here and nowhere else.
func TestVectorByIDResolvesWhenOrdinalOrderIsNotIDOrder(t *testing.T) {
	g := buildBinaryHNSWSerialDeterministic(randomBuildItems(t, 24), defaultVecBytes, defaultM, defaultEfConstruction)

	// Reverse the id ordering relative to ordinals: ordinal 0 gets the LAST name.
	n := len(g.nodes)
	for i := range g.nodes {
		g.nodes[i].externalID = idForOrdinal(n - 1 - i)
	}

	// CONTROL: ordinal order and id order now genuinely disagree.
	require.Greater(t, g.nodes[0].externalID, g.nodes[n-1].externalID,
		"control: the fixture must have ordinal order DISAGREE with id order")

	blob, err := encodeGraphV3(g)
	require.NoError(t, err)
	mg, err := openGraphV3(blob)
	require.NoError(t, err)

	for ord := range g.nodes {
		id := g.nodes[ord].externalID
		got, ok := mg.vectorByID(id)
		require.True(t, ok, "id %q must resolve", id)
		require.Equal(t, g.nodeVector(uint32(ord)), got, "id %q resolved to the wrong vector", id)
	}
	_, ok := mg.vectorByID("no-such-id")
	require.False(t, ok, "an absent id must not resolve")
}

// TestOpenGraphV3RejectsV2Blob pins the migration contract: a v2 blob is
// REJECTED with the rebuild remedy, never mis-parsed as v3.
func TestOpenGraphV3RejectsV2Blob(t *testing.T) {
	blob, _, _ := mappedFixture(t, 8)

	// CONTROL: unmodified, it opens.
	_, err := openGraphV3(blob)
	require.NoError(t, err, "control: the fixture must open before the version byte is changed")

	v2 := make([]byte, len(blob))
	copy(v2, blob)
	v2[v3HdrVersion] = 2

	_, err = openGraphV3(v2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rebuild it from source",
		"a v2 blob must carry the no-converter rebuild remedy that routes it into the heal path")
}

// TestOpenGraphV3RejectsCorruptSections pins the structural gate: a section
// offset pointing past the blob is rejected at open, before any typed view is
// taken over it.
func TestOpenGraphV3RejectsCorruptSections(t *testing.T) {
	blob, _, _ := mappedFixture(t, 8)

	for _, tc := range []struct {
		name string
		off  int
	}{
		{"node directory", v3HdrNodeDir},
		{"layer offsets", v3HdrLayerOffsets},
		{"id directory", v3HdrIDDir},
		{"vectors", v3HdrVectors},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := make([]byte, len(blob))
			copy(bad, blob)
			// Push the section's offset past the end of the blob.
			binary.LittleEndian.PutUint32(bad[tc.off:], uint32(len(blob)+1<<20))
			// Re-checksum so the CRC gate does not fire first: this test is
			// about the STRUCTURAL checks, and a blob that failed the CRC would
			// never reach them.
			crcOff := int(binary.LittleEndian.Uint32(bad[v3HdrCRC:]))
			binary.LittleEndian.PutUint32(bad[crcOff:], crc32Checksum(bad[:crcOff]))

			_, err := openGraphV3(bad)
			require.Error(t, err, "a section pointing past the blob must be rejected at open")
		})
	}
}

// TestOpenGraphV3RejectsCorruptedFooterCRC is the READER half of the corruption
// guard. A bit flip INSIDE an already-validated section passes every structural
// check — the offsets are still in range — and would reach a typed view and
// answer with wrong neighbors. The CRC is what turns that into a rejection.
func TestOpenGraphV3RejectsCorruptedFooterCRC(t *testing.T) {
	blob, _, _ := mappedFixture(t, 16)

	bad := make([]byte, len(blob))
	copy(bad, blob)
	// Flip a byte INSIDE a validated section (the vector block), not in the
	// header: the point is that structure alone cannot see this.
	vectorsOff := int(binary.LittleEndian.Uint32(bad[v3HdrVectors:]))
	bad[vectorsOff]++

	// CONTROL: the structural checks really do pass on this blob — it is ONLY
	// the CRC that rejects it. Without this the test could be passing because of
	// some unrelated bounds failure.
	crcOff := int(binary.LittleEndian.Uint32(bad[v3HdrCRC:]))
	repaired := make([]byte, len(bad))
	copy(repaired, bad)
	binary.LittleEndian.PutUint32(repaired[crcOff:], crc32Checksum(repaired[:crcOff]))
	_, err := openGraphV3(repaired)
	require.NoError(t, err, "control: with the CRC repaired the flipped blob passes every structural check")

	_, err = openGraphV3(bad)
	require.Error(t, err, "a flipped byte inside a validated section must be REJECTED, not read")
	require.Contains(t, err.Error(), "rebuild it from source",
		"a CRC mismatch carries the same rebuild remedy as a bad version byte")
}

// crc32Checksum re-computes the footer checksum the way the writer does, so a
// test that deliberately edits a blob can restore a VALID checksum and isolate
// the structural checks from the CRC gate.
func crc32Checksum(b []byte) uint32 { return crc32.Checksum(b, crcTable) }
