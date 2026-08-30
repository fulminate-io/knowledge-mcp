// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"encoding/binary"
	"math"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// bruteHamming ranks every item against query by Hamming distance, nearest first.
func bruteHamming(items []binaryBuildItem, query []byte) []string {
	type row struct {
		id string
		d  float32
	}
	rows := make([]row, len(items))
	for i, it := range items {
		rows[i] = row{it.id, hammingDistance(query, it.vec)}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].d != rows[j].d {
			return rows[i].d < rows[j].d
		}
		return rows[i].id < rows[j].id
	})
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.id
	}
	return out
}

// bruteDot ranks every item against query by dot product, LARGEST first — the
// float32 ordering, computed independently of the kernel under test.
func bruteDot(items []binaryBuildItem, query []byte, dim int) []string {
	asF32 := func(b []byte) []float32 {
		out := make([]float32, dim)
		for j := range dim {
			out[j] = math.Float32frombits(binary.LittleEndian.Uint32(b[j*4:]))
		}
		return out
	}
	q := asF32(query)
	type row struct {
		id string
		d  float64
	}
	rows := make([]row, len(items))
	for i, it := range items {
		v := asF32(it.vec)
		var sum float64
		for j := range dim {
			sum += float64(q[j]) * float64(v[j])
		}
		rows[i] = row{it.id, sum}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].d != rows[j].d {
			return rows[i].d > rows[j].d // dot is higher-is-better
		}
		return rows[i].id < rows[j].id
	})
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.id
	}
	return out
}

func hitIDs(hits []graphHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.externalID
	}
	return out
}

// TestDtypeDispatchIsPerSegment proves both halves of the dispatch contract: the
// metric is resolved ONCE PER SEGMENT and read from storage on every distance,
// and a float32 segment genuinely ranks by a different metric than a ubinary one.
func TestDtypeDispatchIsPerSegment(t *testing.T) {
	t.Parallel()

	const dim = 64
	items := float32Items(24, dim)
	query := items[0].vec

	// --- half 1: the metric is STORED, not re-derived per distance -----------
	//
	// The proof is a substitution. After open, the resolved function is replaced
	// with a sentinel that ignores its arguments and returns a constant. If the
	// traversal read the stored field, every distance it computes is that
	// constant and every hit scores identically. If it instead branched on the
	// tag per call — or called the resolver again — the substitution would have
	// no observable effect and the scores would spread.
	//
	// This is what "resolved once per segment" MEANS operationally: the hot path
	// consults the field, never the selection logic.
	t.Run("metric is resolved once and read from the block", func(t *testing.T) {
		t.Parallel()

		blob, err := encodeGraphV3(buildFloat32Graph(items, dim))
		require.NoError(t, err)
		g, err := openGraphV3(blob)
		require.NoError(t, err)
		require.NotNil(t, g.distance, "openGraphV3 must resolve the metric at open")

		// Known-positive FIRST: unsubstituted, the scores genuinely vary. Without
		// this, a search that returned a single hit — or identical scores for an
		// unrelated reason — would satisfy the substitution assertion vacuously.
		before := g.search(query, 10, nil)
		require.Greater(t, len(before), 1, "need several hits for a spread to be meaningful")
		spread := false
		for _, h := range before[1:] {
			if h.score != before[0].score {
				spread = true
			}
		}
		require.True(t, spread, "control: real scores must differ before substitution")

		// BOTH RESOLVED SEAMS ARE SUBSTITUTED, because the traversal now reads
		// both: the scalar metric for entry points and the greedy descent's
		// current node, and the batched scorer for every neighbor run. Leaving
		// batchScore un-substituted would let real distances leak into the
		// result and is exactly what caught the batching restructure — the
		// property under test is that the hot path consults STORED FIELDS, and
		// that claim has to cover every field it consults.
		const sentinel float32 = 0.25
		g.distance = func(_ *preparedQuery, _ []byte) float32 { return sentinel }
		g.batchScore = func(dst []float32, _ *preparedQuery, _ *vectorBlock, ids []uint32) {
			for i := range ids {
				dst[i] = sentinel
			}
		}

		after := g.search(query, 10, nil)
		require.NotEmpty(t, after)
		// Bit-exact: both sides are the same computation over the same constant,
		// so anything other than identity here means the substitution did not
		// reach the traversal. A tolerance would blur precisely that signal.
		want := math.Float64bits(scoreForDtype(dtypeFloat32, sentinel, g.vecBytes))
		for _, h := range after {
			require.Equal(t, want, math.Float64bits(h.score),
				"every distance must come from the STORED function — a per-call branch would ignore the substitution")
		}
	})

	// --- half 2: the two dtypes rank by genuinely different metrics ----------
	t.Run("float32 and ubinary rank the same bytes differently", func(t *testing.T) {
		t.Parallel()

		// THE SAME BYTES, TAGGED TWO WAYS. Identical vector payloads and identical
		// query, so any difference in the returned order is attributable to the
		// dtype tag alone and to nothing about the data.
		fBlob, err := encodeGraphV3(buildFloat32Graph(items, dim))
		require.NoError(t, err)
		uGraph := buildBinaryHNSWSerialDeterministic(items, dim*4, dtypeUbinary, defaultM, defaultEfConstruction)
		uBlob, err := encodeGraphV3(uGraph)
		require.NoError(t, err)

		fg, err := openGraphV3(fBlob)
		require.NoError(t, err)
		require.Equal(t, dtypeFloat32, fg.dtype)
		ug, err := openGraphV3(uBlob)
		require.NoError(t, err)
		require.Equal(t, dtypeUbinary, ug.dtype)

		// ef wide enough to visit the whole 24-node corpus, so the comparison is
		// against the metric rather than against beam luck.
		fg.setEfSearch(len(items) * 4)
		ug.setEfSearch(len(items) * 4)

		fOrder := hitIDs(fg.search(query, len(items), nil))
		uOrder := hitIDs(ug.search(query, len(items), nil))
		require.Len(t, fOrder, len(items))
		require.Len(t, uOrder, len(items))

		// Each side matches ITS OWN independently-computed brute-force ordering.
		// This is the assertion that makes the difference meaningful: two orders
		// could differ merely because one of them is wrong.
		require.Equal(t, bruteDot(items, query, dim), fOrder,
			"the float32 segment must rank by dot product, largest first")
		require.Equal(t, bruteHamming(items, query), uOrder,
			"the ubinary segment must rank by Hamming distance, nearest first")

		// And the two orders are actually different, so the test would fail if a
		// single metric were serving both tags.
		require.NotEqual(t, uOrder, fOrder,
			"the same bytes under two tags must not produce one ordering")

		// The scores follow their own metric's convention too: a dot-product score
		// is the raw similarity, a Hamming score is the [0,1] normalization.
		fTop := fg.search(query, 1, nil)
		require.Len(t, fTop, 1)
		require.Equal(t, items[0].id, fTop[0].externalID, "a vector is its own nearest neighbor under dot")
		require.Greater(t, fTop[0].score, 0.0, "self-dot of a non-zero vector is positive")

		uTop := ug.search(query, 1, nil)
		require.Len(t, uTop, 1)
		require.Equal(t, items[0].id, uTop[0].externalID)
		require.Equal(t, math.Float64bits(1.0), math.Float64bits(uTop[0].score),
			"a zero Hamming distance normalizes to exactly 1.0 — exactly, not approximately")
	})
}
