// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// toString renders a recovered panic value for assertion.
func toString(r any) string { return fmt.Sprint(r) }

// fixCRC recomputes the footer checksum in place, so a test that edits a header
// byte is exercising the check it means to and not the CRC guard in front of it.
func fixCRC(b []byte) {
	crcOff := int(le32(b, v3HdrCRC))
	binary.LittleEndian.PutUint32(b[crcOff:], crc32.Checksum(b[:crcOff], crcTable))
}

// dtype_pairing_test.go covers the ONE systemic defect the phase-1 review found,
// in each of the places it surfaced: (vecBytes, dtype) is a PAIR, and every seam
// that derives, checks or records one half must do the same for the other. The
// width half was threaded correctly; the dtype half was hardcoded, unchecked, or
// unrepresented in the artifact.

// float32Segment seals a real float32 segment through the same publish path
// production uses, so these tests exercise the shipped Decode/Merge surface
// rather than internals.
func float32Segment(t *testing.T, ids []string, dim int) *hnswSegment {
	t.Helper()
	items := float32Items(len(ids), dim)
	for i, id := range ids {
		items[i].id = id
	}
	blob, err := encodeGraphV3(buildFloat32Graph(items, dim))
	require.NoError(t, err)
	seg, err := Format{}.Decode(blob)
	require.NoError(t, err)
	hs, ok := seg.(*hnswSegment)
	require.True(t, ok)
	require.Equal(t, dtypeFloat32, hs.graph.dtype)
	return hs
}

// TestMergeDerivesDtypeFromSurvivors is T2-1: Merge derived the width from its
// survivors but passed a hardcoded ubinary dtype, so consolidating two float32
// segments silently produced a ubinary one — the vectors' bytes preserved, the
// metric that ranks them replaced.
func TestMergeDerivesDtypeFromSurvivors(t *testing.T) {
	t.Parallel()

	const dim = 16
	accept := []func(searchengine.ExternalID) bool{nil, nil}

	t.Run("two float32 constituents merge to a float32 segment", func(t *testing.T) {
		t.Parallel()

		a := float32Segment(t, []string{"a1", "a2", "a3", "a4"}, dim)
		b := float32Segment(t, []string{"b1", "b2", "b3", "b4"}, dim)

		merged, err := mergeSegments(t, []searchengine.Segment[[]byte, struct{}]{a, b}, accept)
		require.NoError(t, err)

		blob, err := merged.Encode()
		require.NoError(t, err)
		require.Equal(t, dtypeFloat32, blob[v3HdrDtype],
			"the merged blob must carry the constituents' dtype, not a hardcoded ubinary tag")

		// The merged segment must also RANK as float32 — the tag alone could be
		// right while the resolved metric was wrong, which is the drift setDtype
		// exists to prevent.
		mhs, ok := merged.(*hnswSegment)
		require.True(t, ok)
		require.Equal(t, dtypeFloat32, mhs.graph.dtype)
		require.Len(t, mhs.IDs(), 8)
	})

	t.Run("a merge mixing dtypes is refused", func(t *testing.T) {
		t.Parallel()

		f := float32Segment(t, []string{"f1", "f2", "f3", "f4"}, dim)

		// A ubinary segment at the SAME width, so the refusal is attributable to
		// the dtype and not to the width check that already exists.
		uSeg, err := Format{}.Build([]searchengine.Document{
			{ID: "u1", Vector: make([]byte, dim*4)},
			{ID: "u2", Vector: make([]byte, dim*4)},
		})
		require.NoError(t, err)
		uhs, ok := uSeg.(*hnswSegment)
		require.True(t, ok)
		require.Equal(t, dtypeUbinary, uhs.graph.dtype)
		require.Equal(t, f.graph.vecBytes, uhs.graph.vecBytes,
			"control: both constituents are the same WIDTH, so only the dtype differs")

		_, err = mergeSegments(t, []searchengine.Segment[[]byte, struct{}]{f, uhs}, accept)
		require.Error(t, err, "merging a float32 segment with a ubinary one must be refused")
		require.ErrorIs(t, err, ErrMixedVectorDtype)
		msg := err.Error()
		require.Contains(t, msg, "1", "the error names the offending index")
		require.Contains(t, msg, "dtype", "the error says what kind of mismatch it is")
	})
}

// TestSearchRefusesWrongWidthQuery is T2-2. The float arm built a dim-length
// float32 VIEW over the CALLER's query buffer, so a query narrower than the
// segment's width read past the caller's allocation and returned confident
// garbage instead of failing; the ubinary arm silently truncated. Both are the
// width half of the pair checked nowhere.
func TestSearchRefusesWrongWidthQuery(t *testing.T) {
	t.Parallel()

	const dim = 64
	items := float32Items(16, dim)

	for _, tc := range []struct {
		name  string
		dtype byte
	}{
		{"float32", dtypeFloat32},
		{"ubinary", dtypeUbinary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			blob, err := encodeGraphV3(
				buildBinaryHNSWSerialDeterministic(items, dim*4, tc.dtype, defaultM, defaultEfConstruction))
			require.NoError(t, err)
			g, err := openGraphV3(blob)
			require.NoError(t, err)

			// KNOWN-POSITIVE FIRST: a correct-width query works, so the refusals
			// below are about the WIDTH and not about the segment being unusable.
			hits := g.search(items[0].vec, 3, nil)
			require.NotEmpty(t, hits, "control: a correct-width query must return hits")

			// SHORT. This is the one that read out of bounds: a 16-byte query
			// against a 256-byte segment yielded a 64-element float32 view over a
			// 16-byte allocation — 240 bytes past the end, with no panic.
			short := make([]byte, 16)
			require.Panics(t, func() { g.search(short, 3, nil) },
				"a query narrower than the segment must be REFUSED, not read past its allocation")

			// LONG, which the ubinary arm silently truncated rather than refusing.
			long := make([]byte, dim*4+8)
			require.Panics(t, func() { g.search(long, 3, nil) },
				"a query wider than the segment must be REFUSED, not silently truncated")

			// The refusal names BOTH widths, so an operator can tell which side is
			// wrong rather than only that something mismatched.
			var msg string
			func() {
				defer func() {
					if r := recover(); r != nil {
						msg = toString(r)
					}
				}()
				g.search(short, 3, nil)
			}()
			require.Contains(t, msg, "16", "the refusal names the QUERY's width")
			require.Contains(t, msg, "256", "the refusal names the SEGMENT's width")
		})
	}

	t.Run("a correct-width float32 query still ranks by dot", func(t *testing.T) {
		t.Parallel()

		blob, err := encodeGraphV3(buildFloat32Graph(items, dim))
		require.NoError(t, err)
		g, err := openGraphV3(blob)
		require.NoError(t, err)
		g.setEfSearch(len(items) * 4)

		got := hitIDs(g.search(items[0].vec, len(items), nil))
		require.Equal(t, bruteDot(items, items[0].vec, dim), got,
			"the width check must not disturb ranking — still dot, largest first")
	})
}

// TestEmptySegmentAcceptsAnyQueryWidth pins the one case where the width guard
// must NOT fire.
//
// AN EMPTY SEGMENT'S WIDTH IS INVENTED, NOT OBSERVED. batchVecBytes has nothing
// to derive a width from when every document's vector is absent, so it returns
// the package default and the sealed segment reports vecBytes=32 — a number that
// describes no vector, because the segment holds none. Refusing a query against
// that number rejects correctly-sized queries for disagreeing with a placeholder.
//
// THE PATH IS REACHABLE, which is what makes this a defect rather than a
// curiosity: the engine hands unfiltered batches to Build while embedding is
// still draining, so all-empty batches are ordinary, and a deployment whose real
// width is not 32 would then panic on every query that touched one.
//
// The known-positive is in the same run and is the whole point: a segment that
// DOES hold vectors must still refuse a wrong-width query. Without it, deleting
// the guard entirely would satisfy the first half.
func TestEmptySegmentAcceptsAnyQueryWidth(t *testing.T) {
	t.Parallel()

	const otherWidth = 128 // deliberately NOT defaultVecBytes
	require.NotEqual(t, defaultVecBytes, otherWidth, "the test's premise is that this width differs from the default")

	empty, err := Format{}.Build([]searchengine.Document{
		{ID: "x", Vector: nil},
		{ID: "y", Vector: []byte{}},
	})
	require.NoError(t, err)
	require.Empty(t, empty.IDs(), "the batch really is all-empty")

	require.NotPanics(t, func() {
		hits := empty.Search(make([]byte, otherWidth), struct{}{}, 5, nil)
		require.Empty(t, hits, "an empty segment answers no hits at any width")
	}, "a segment holding no vectors cannot misread a query, so it must not refuse one")

	// KNOWN-POSITIVE, same run: a segment that holds vectors still refuses.
	nonEmpty, err := Format{}.Build([]searchengine.Document{
		{ID: "a", Vector: make([]byte, defaultVecBytes)},
		{ID: "b", Vector: make([]byte, defaultVecBytes)},
	})
	require.NoError(t, err)
	require.Panics(t, func() {
		nonEmpty.Search(make([]byte, otherWidth), struct{}{}, 5, nil)
	}, "a vector-holding segment must still refuse a wrong-width query — the guard is narrowed, not deleted")
}

// TestUbinaryEncodingIsByteIdenticalToThePreDtypeEncoder is the KGV-class guard
// on everything this phase touched.
//
// A SEGMENT'S ID IS THE SHA256 OF ITS BYTES. So any change to the ubinary
// encoding — the dtype tag, the version selection, the width threading — would
// re-key every segment already stored, turning an in-place upgrade into a global
// rebuild that expresses nothing. The checked-in fixture was produced by the
// encoder as it stood BEFORE any of this phase's edits, which makes it the only
// available independent witness: comparing against a freshly-computed expectation
// would be comparing this encoder to itself.
func TestUbinaryEncodingIsByteIdenticalToThePreDtypeEncoder(t *testing.T) {
	t.Parallel()

	want, err := os.ReadFile(filepath.Join("testdata", "hnsw_v3_ubinary_segment.seg"))
	require.NoError(t, err)

	// The fixture was captured from exactly this input through exactly this path.
	seg, err := Format{}.Build(vecDocs(8))
	require.NoError(t, err)
	got, err := seg.Encode()
	require.NoError(t, err)

	require.Equal(t, want, got,
		"the current encoder must reproduce the pre-dtype ubinary bytes EXACTLY — a single differing byte re-keys every stored ubinary segment")
	require.Equal(t, sha256.Sum256(want), sha256.Sum256(got),
		"and therefore the segment id is unchanged")
}

// TestFloat32BlobsCarryADistinctSerialVersion is T2-3, per the orchestrator's
// recorded disposition. A float32-tagged blob at serialVersion 3 is accepted by
// every already-released client, which reads its float bytes as bit patterns and
// ranks them by Hamming distance — silently wrong results from a segment that
// looks entirely valid. Writing a DISTINCT version routes those clients into the
// unsupported-version refusal they already implement, remedy included.
func TestFloat32BlobsCarryADistinctSerialVersion(t *testing.T) {
	t.Parallel()

	const dim = 16
	items := float32Items(8, dim)

	fBlob, err := encodeGraphV3(buildFloat32Graph(items, dim))
	require.NoError(t, err)
	require.Equal(t, serialVersionFloat32, fBlob[v3HdrVersion],
		"a float32 blob must announce a version an old reader refuses")
	require.NotEqual(t, serialVersionOffsets, fBlob[v3HdrVersion])

	uBlob, err := encodeGraphV3(
		buildBinaryHNSWSerialDeterministic(items, dim*4, dtypeUbinary, defaultM, defaultEfConstruction))
	require.NoError(t, err)
	require.Equal(t, serialVersionOffsets, uBlob[v3HdrVersion],
		"a ubinary blob's version byte is UNCHANGED — existing segments and their ids must not move")

	// The new reader reads everything.
	fg, err := openGraphV3(fBlob)
	require.NoError(t, err)
	require.Equal(t, dtypeFloat32, fg.dtype)
	ug, err := openGraphV3(uBlob)
	require.NoError(t, err)
	require.Equal(t, dtypeUbinary, ug.dtype)

	// VERSION AND DTYPE ARE THE SAME PAIR, so a blob whose two halves disagree is
	// refused rather than believed. Without this the version byte would be
	// decorative and the whole protection optional.
	torn := make([]byte, len(fBlob))
	copy(torn, fBlob)
	torn[v3HdrVersion] = serialVersionOffsets
	fixCRC(torn)
	_, err = openGraphV3(torn)
	require.Error(t, err, "a blob whose version and dtype disagree must be refused")
}
