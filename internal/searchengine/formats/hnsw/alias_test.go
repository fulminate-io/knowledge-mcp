// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
)

// THE HAZARD THESE TWO CATCH IS A USE-AFTER-UNMAP, NOT A STYLE QUESTION.
//
// Every value the mapped reader produces internally — a vector from nodeVector,
// an id from idView — is a VIEW over the segment mapping. A value that escapes
// the segment API outlives the call that produced it: the engine holds the route
// map for the life of the entry, and a "similar" query carries a vector returned
// by VectorByID across OTHER segments. If the mapping is released while such a
// value is still reachable, the read does not crash — it returns whatever the OS
// has since put at that address. Plausible garbage, silently.
//
// So the assertion is deliberately about IDENTITY OF BACKING MEMORY, not about
// equality of contents: a copy and a view compare equal, which is exactly why a
// contents assertion would pass while the bug shipped. unsafe.SliceData and
// unsafe.StringData give the backing pointer, and the test requires it to differ
// from the blob's.

// blobContains reports whether p points inside the blob's byte range.
//
//nolint:gosec // G103: reading a backing pointer IS the assertion; test-only
func blobContains(blob []byte, p uintptr) bool {
	base := uintptr(unsafe.Pointer(unsafe.SliceData(blob)))
	return p >= base && p < base+uintptr(len(blob))
}

// TestVectorByIDDoesNotAliasBlob pins the copy at the VectorByID boundary.
func TestVectorByIDDoesNotAliasBlob(t *testing.T) {
	seg, blob := aliasFixture(t)

	vec, ok := seg.VectorByID(idForOrdinal(3))
	require.True(t, ok, "fixture id must resolve")
	require.NotEmpty(t, vec)

	// CONTROL: the reader's INTERNAL accessor really does hand back a view, so
	// this test is discriminating rather than asserting something that could not
	// have been false.
	hs, isHNSW := seg.(*hnswSegment)
	require.True(t, isHNSW)
	internal, ok := hs.graph.vectorByID(idForOrdinal(3))
	require.True(t, ok)
	//nolint:gosec // G103: reading a backing pointer IS the assertion; test-only
	require.True(t, blobContains(blob, uintptr(unsafe.Pointer(unsafe.SliceData(internal)))),
		"control: the internal accessor must alias the blob, or this test proves nothing")

	//nolint:gosec // G103: reading a backing pointer IS the assertion; test-only
	require.False(t, blobContains(blob, uintptr(unsafe.Pointer(unsafe.SliceData(vec)))),
		"VectorByID returned a view into the segment mapping; it must return a copy")
}

// TestSegmentIDsDoNotAliasBlob pins the copy at the IDs boundary.
func TestSegmentIDsDoNotAliasBlob(t *testing.T) {
	seg, blob := aliasFixture(t)

	ids := seg.IDs()
	require.NotEmpty(t, ids)

	hs, isHNSW := seg.(*hnswSegment)
	require.True(t, isHNSW)
	// CONTROL: the internal view aliases.
	internal := hs.graph.ids()
	//nolint:gosec // G103: reading a backing pointer IS the assertion; test-only
	require.True(t, blobContains(blob, uintptr(unsafe.Pointer(unsafe.StringData(internal[0])))),
		"control: the internal accessor must alias the blob, or this test proves nothing")

	for i, id := range ids {
		//nolint:gosec // G103: reading a backing pointer IS the assertion; test-only
		require.False(t, blobContains(blob, uintptr(unsafe.Pointer(unsafe.StringData(id)))),
			"IDs()[%d] is a view into the segment mapping; every id must be copied", i)
	}
}

// aliasFixture seals a real segment and returns it with its blob.
func aliasFixture(t *testing.T) (interface {
	IDs() []string
	VectorByID(string) ([]byte, bool)
}, []byte) {
	t.Helper()
	g := buildBinaryHNSWSerialDeterministic(randomBuildItems(t, 16), defaultVecBytes, defaultM, defaultEfConstruction)
	blob, err := encodeGraphV3(g)
	require.NoError(t, err)
	seg, err := Format{}.Decode(blob)
	require.NoError(t, err)
	hs, ok := seg.(*hnswSegment)
	require.True(t, ok)
	return hs, hs.graph.blob
}
