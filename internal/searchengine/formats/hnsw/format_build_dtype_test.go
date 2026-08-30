// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// float32Doc builds one document from explicit float32 values, encoded
// little-endian and tagged as float32 — the exact byte shape the voyage arm
// emits.
func float32Doc(id string, values []float32) searchengine.Document {
	vec := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(vec[i*4:], math.Float32bits(v))
	}
	return searchengine.Document{ID: id, Vector: vec, Dtype: searchengine.DtypeFloat32}
}

// axisVector returns a dim-wide vector that is `mag` on one axis and zero
// elsewhere. Two such vectors on DIFFERENT axes have a dot product of exactly
// zero, which is what lets the float32 leg below state its expected scores as
// literals instead of recomputing them.
func axisVector(dim, axis int, mag float32) []float32 {
	v := make([]float32, dim)
	v[axis] = mag
	return v
}

// ubinaryDoc builds one document whose Vector is width packed bytes, tagged as
// ubinary.
func ubinaryDoc(id string, width int, seed byte) searchengine.Document {
	vec := make([]byte, width)
	for i := range vec {
		vec[i] = seed + byte(i)
	}
	return searchengine.Document{ID: id, Vector: vec, Dtype: searchengine.DtypeUbinary}
}

// TestFormatBuild_DtypeFromBatch pins that a sealed segment's dtype tracks the
// DOCUMENTS it was built from, which is what Build used to ignore: it derived
// the width from the batch and then passed a fixed ubinary tag, so float32 bytes
// sealed at the correct width were ranked by Hamming distance over IEEE bit
// patterns — correct bytes, correct lengths, wrong order, no error.
func TestFormatBuild_DtypeFromBatch(t *testing.T) {
	t.Run("float32 batch seals a float32 segment", func(t *testing.T) {
		// Each document sits on its OWN axis, so every cross pair has a dot
		// product of exactly zero and the self pair is mag*mag. That makes the
		// expected score below a stated number rather than a recomputation.
		const dim = 8
		docs := []searchengine.Document{
			float32Doc("a", axisVector(dim, 0, 2)),
			float32Doc("b", axisVector(dim, 1, 1)),
			float32Doc("c", axisVector(dim, 2, 1)),
		}

		seg, err := Format{}.Build(docs)
		require.NoError(t, err)
		hs, ok := seg.(*hnswSegment)
		require.True(t, ok)

		require.Equal(t, dtypeFloat32, hs.graph.dtype,
			"the sealed segment must carry the batch's dtype, not a constant the format chose")
		require.Equal(t, dim*4, hs.graph.vecBytes,
			"and its width must be the four-bytes-per-dimension one the documents carry")

		// THE TAG IS NOT DECORATIVE — IT SELECTS THE METRIC, and this is the
		// assertion that proves it rather than trusting the tag. Querying "a"
		// with its own vector scores dot(a,a) = 2*2 = 4.0 under the float32
		// metric. A ubinary segment's score is a Hamming similarity normalized
		// into [0,1] and CANNOT be 4.0 for any input, so this number is
		// unreachable by the metric the hard-coded tag used to select — which is
		// exactly the silent failure this step removes.
		q, found := hs.VectorByID("a")
		require.True(t, found)
		hits := hs.Search(q, struct{}{}, 3, nil)
		require.NotEmpty(t, hits, "a float32 segment must be searchable")
		require.Equal(t, "a", hits[0].ID, "a vector queried against itself ranks first among orthogonal peers")
		require.InDelta(t, 4.0, hits[0].Score, 1e-6,
			"the score must be the DOT PRODUCT — a Hamming-scored segment cannot exceed 1.0, "+
				"so this value is only reachable if the batch's dtype selected the metric")
	})

	t.Run("ubinary batch still seals ubinary", func(t *testing.T) {
		// THE SAME-RUN KNOWN-POSITIVE for every corpus that exists today. Without
		// it, a change that sealed EVERYTHING as float32 would satisfy the leg
		// above and silently reinterpret every shipped segment.
		docs := []searchengine.Document{
			ubinaryDoc("a", defaultVecBytes, 1),
			ubinaryDoc("b", defaultVecBytes, 40),
			ubinaryDoc("c", defaultVecBytes, 90),
		}

		seg, err := Format{}.Build(docs)
		require.NoError(t, err)
		hs, ok := seg.(*hnswSegment)
		require.True(t, ok)
		require.Equal(t, dtypeUbinary, hs.graph.dtype)
		require.Equal(t, defaultVecBytes, hs.graph.vecBytes)

		// AN UNSET DTYPE IS THE SAME ANSWER, which is the on-disk tag-0
		// convention every pre-tag segment relies on. Documents built before the
		// field existed must keep sealing exactly as they did.
		untagged := []searchengine.Document{
			{ID: "a", Vector: make([]byte, defaultVecBytes)},
			{ID: "b", Vector: make([]byte, defaultVecBytes)},
		}
		useg, uerr := Format{}.Build(untagged)
		require.NoError(t, uerr)
		uhs, ok := useg.(*hnswSegment)
		require.True(t, ok)
		require.Equal(t, dtypeUbinary, uhs.graph.dtype,
			"an unset Dtype reads as ubinary, matching the zero tag byte on every historical segment")
	})

	t.Run("mixed dtype batch refused", func(t *testing.T) {
		// SAME WIDTH, DIFFERENT DTYPES — deliberately. If the two documents also
		// differed in width, batchVecBytes would refuse first and this leg would
		// pass without the dtype derivation existing at all. 32 ubinary bytes and
		// 8 float32 dimensions are both 32 bytes, so ONLY the dtype disagrees.
		mixed := []searchengine.Document{
			ubinaryDoc("a", 32, 1),
			float32Doc("b", axisVector(8, 0, 2)),
		}
		require.Len(t, mixed[0].Vector, len(mixed[1].Vector),
			"the fixture must be width-identical, or the width refusal masks the dtype one")

		_, err := Format{}.Build(mixed)
		require.Error(t, err,
			"a batch mixing representations must be REFUSED — sealing one tag over it silently "+
				"reinterprets the other document's vectors")
		require.ErrorIs(t, err, ErrMixedVectorDtype,
			"callers match the refusal by sentinel")
		require.Contains(t, err.Error(), `"b"`, "the refusal names the offending document")

		// AND AN UNKNOWN REPRESENTATION IS REFUSED RATHER THAN READ AS UBINARY.
		// Reading it as ubinary would rank its vectors by Hamming distance and
		// return confident wrong neighbors instead of failing.
		unknown := []searchengine.Document{
			{ID: "a", Vector: make([]byte, 32), Dtype: "float16"},
		}
		_, uerr := Format{}.Build(unknown)
		require.Error(t, uerr, "an unrecognized dtype must be refused, never coerced to ubinary")
		require.Contains(t, uerr.Error(), "float16", "the refusal names the value it did not recognize")
	})
}
