// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// float32Items produces n deterministic float32 vectors of dim dimensions each,
// laid out as little-endian bytes exactly as the vector block stores them.
func float32Items(n, dim int) []binaryBuildItem {
	rng := rand.New(rand.NewPCG(0xf10a7, 0x32b17))
	items := make([]binaryBuildItem, n)
	for i := range items {
		buf := make([]byte, dim*4)
		for j := range dim {
			// Spread across negatives and positives so a sign-dropping or
			// truncating round trip cannot survive by accident.
			f := float32(rng.Float64()*2 - 1)
			binary.LittleEndian.PutUint32(buf[j*4:], math.Float32bits(f))
		}
		items[i] = binaryBuildItem{id: fmt.Sprintf("f%03d", i), vec: buf}
	}
	return items
}

// buildFloat32Graph builds a graph over float32 vectors as a float32 graph — the
// dtype goes in through the builder, so the neighbor lists are selected under
// the dot metric rather than retagged afterwards.
func buildFloat32Graph(items []binaryBuildItem, dim int) *binaryGraph {
	return buildBinaryHNSWSerialDeterministic(items, dim*4, dtypeFloat32, defaultM, defaultEfConstruction)
}

// TestV3DtypeTagZeroLoadsAsUbinary is the tag-0 compatibility contract: every v3
// segment written before the dtype tag existed must still load, and must load as
// UBINARY.
//
// THE INPUT IS A CHECKED-IN FIXTURE AND THAT IS LOAD-BEARING — the same reasoning
// migration_test.go records for the v2 fixture. A test that wrote a blob with the
// CURRENT encoder and set the tag to 0 would be exercising the new writer twice
// and would still pass if the historical claim were false. testdata/hnsw_v3_ubinary_segment.seg
// was captured from the encoder BEFORE the tag was introduced, so decoding it is
// the only way to test a claim about bytes already on disk. See testdata/README.md.
func TestV3DtypeTagZeroLoadsAsUbinary(t *testing.T) {
	t.Parallel()

	blob, err := os.ReadFile(filepath.Join("testdata", "hnsw_v3_ubinary_segment.seg"))
	require.NoError(t, err, "the v3 ubinary fixture must be checked in — it cannot be regenerated once the encoder writes a tag")
	require.NotEmpty(t, blob)
	require.Equal(t, serialVersionOffsets, blob[0],
		"the fixture must really be v3; a fixture that drifted to another version tests nothing")
	require.Equal(t, byte(0), blob[v3HdrDtype],
		"the fixture must carry a ZERO tag byte — that zero is the historical fact this test rests on")

	seg, err := Format{}.Decode(blob)
	require.NoError(t, err, "a pre-tag v3 blob must still decode — tag 0 needs no converter")
	hs, ok := seg.(*hnswSegment)
	require.True(t, ok)
	require.Equal(t, dtypeUbinary, hs.graph.dtype, "a zero tag reads as ubinary")
	require.Equal(t, defaultVecBytes, hs.graph.vecBytes)

	// The segment is not merely openable — its contents survived.
	ids := hs.IDs()
	require.Len(t, ids, 8, "the fixture holds eight nodes")
	vec, found := hs.VectorByID("v0")
	require.True(t, found, "a stored id resolves through the id directory")
	require.Len(t, vec, defaultVecBytes)

	// KNOWN-POSITIVE IN THE SAME RUN. Without it, a reader that hardcoded ubinary
	// — or never read the tag byte at all — would satisfy every assertion above.
	// The identical Decode path must report float32 for a float32-tagged blob.
	f32Blob, err := encodeGraphV3(buildFloat32Graph(float32Items(8, 16), 16))
	require.NoError(t, err)
	require.Equal(t, dtypeFloat32, f32Blob[v3HdrDtype])
	f32Seg, err := Format{}.Decode(f32Blob)
	require.NoError(t, err)
	f32hs, ok := f32Seg.(*hnswSegment)
	require.True(t, ok)
	require.Equal(t, dtypeFloat32, f32hs.graph.dtype,
		"the same reader reports float32 for a float32 blob — so the ubinary reading above is the TAG, not a constant")
}

// TestV3Float32VectorsRoundTripAtEveryWidth proves the v3 layout carries float32
// vectors losslessly across the whole width range the index is meant to serve,
// not just at the 32-byte width it shipped with.
//
// EVERY VALUE IS COMPARED, not a checksum or a length. A layout bug that dropped,
// reordered or misaligned the tail of a wide vector would leave the byte count
// intact, so length equality is exactly the assertion that would miss it.
func TestV3Float32VectorsRoundTripAtEveryWidth(t *testing.T) {
	t.Parallel()

	// 8 through 2048 dimensions — 32 to 8192 vector bytes, spanning the widths the
	// research measured, and including a non-power-of-two dim so the width is not
	// assumed to be a tidy multiple of any block size.
	for _, dim := range []int{8, 16, 48, 64, 128, 256, 512, 1024, 2048} {
		t.Run(fmt.Sprintf("dim%d", dim), func(t *testing.T) {
			t.Parallel()

			items := float32Items(12, dim)
			blob, err := encodeGraphV3(buildFloat32Graph(items, dim))
			require.NoError(t, err)
			require.Equal(t, dtypeFloat32, blob[v3HdrDtype])

			g, err := openGraphV3(blob)
			require.NoError(t, err, "a float32 blob at dim %d must open", dim)
			require.Equal(t, dtypeFloat32, g.dtype)
			require.Equal(t, dim*4, g.vecBytes)
			require.Equal(t, len(items), g.nodeCount())

			for _, it := range items {
				got, found := g.vectorByID(it.id)
				require.True(t, found, "id %s must resolve at dim %d", it.id, dim)
				require.Equal(t, it.vec, got, "raw bytes must round-trip for %s at dim %d", it.id, dim)

				// Read the SAME bytes back through the float32 typed view, which is
				// how the float distance path will consume them. This is what makes
				// the test cover f32sAt's alignment contract rather than just the
				// byte copy.
				view := f32sAt(got, 0, dim)
				require.Len(t, view, dim)
				for j := range dim {
					// COMPARED AS BIT PATTERNS, and deliberately not with a
					// tolerance. The claim is losslessness — the same 32 bits come
					// back — so any epsilon would admit exactly the truncation
					// this test exists to catch.
					want := binary.LittleEndian.Uint32(it.vec[j*4:])
					require.Equal(t, want, math.Float32bits(view[j]),
						"float %d of %s must survive the typed view at dim %d", j, it.id, dim)
				}
			}
		})
	}
}

// TestV3UnknownDtypeTagIsRefused pins the validation half: an unrecognized tag is
// an ERROR, never coerced to a default.
//
// WHY COERCION WOULD BE THE WORSE FAILURE, and why this test exists rather than a
// silent fallback: reading a float32 block as ubinary does not fail — it ranks
// bit patterns by Hamming distance and returns confident, wrong neighbors. A
// refusal is the only outcome that surfaces the mismatch.
func TestV3UnknownDtypeTagIsRefused(t *testing.T) {
	t.Parallel()

	g := buildBinaryHNSWSerialDeterministic(randomVectors(8), defaultVecBytes, dtypeUbinary, defaultM, defaultEfConstruction)
	blob, err := encodeGraphV3(g)
	require.NoError(t, err)

	// KNOWN-POSITIVE FIRST: unmodified, this blob opens. So the refusal below is
	// attributable to the tag rather than to anything else about the bytes.
	_, err = openGraphV3(blob)
	require.NoError(t, err, "the unmodified blob must open — otherwise the refusal below proves nothing")

	blob[v3HdrDtype] = 7
	// The CRC covers the header, so a flipped tag byte would be caught as
	// corruption before the dtype check ever ran. Recompute it, which is what a
	// genuine newer writer would have produced.
	crcOff := int(le32(blob, v3HdrCRC))
	binary.LittleEndian.PutUint32(blob[crcOff:], crc32.Checksum(blob[:crcOff], crcTable))

	_, err = openGraphV3(blob)
	require.Error(t, err, "an unknown dtype tag must be REFUSED, not defaulted")
	require.Contains(t, err.Error(), "unsupported vector dtype tag 7",
		"the error names the tag it FOUND")
	require.Contains(t, err.Error(), "rebuild it from source",
		"the error carries the REMEDY, like the version and CRC refusals")
}
