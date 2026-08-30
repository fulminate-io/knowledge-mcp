// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBatchScoreAgreesWithScalarDistance gates the seam the batched neighbor
// scoring will call: for every dtype, scoring a run of ids in ONE call must
// agree with scoring them one at a time.
//
// THIS IS WHAT MAKES THE BATCH SEAM SAFE TO ADOPT. Both arms are resolved by the
// same setDtype from the same tag, so they cannot disagree about WHICH metric
// applies — but they can still disagree about the metric's RESULT, because the
// batched float32 arm accumulates through a different kernel than the scalar one
// and negates in a different place. A batched path that ranked differently from
// the scalar path would change results depending on which one a caller took.
//
// AGREEMENT IS SCALE-RELATIVE PLUS RANKING, not literal equality. Float32 dot
// products accumulated in different orders are not bit-identical, and this
// repo's kernel work already established that a literal relative tolerance is
// unmeetable for them; the properties that actually matter are that the values
// agree to within floating-point scale and that they induce the SAME ORDER,
// since order is what a nearest-neighbor search consumes.
func TestBatchScoreAgreesWithScalarDistance(t *testing.T) {
	t.Parallel()

	const dim = 64
	items := float32Items(40, dim)

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
			require.NotNil(t, g.batchScore, "openGraphV3 must resolve the batch scorer at open")

			vb := &g.vectorBlock
			q := vb.prepareQuery(items[0].vec)

			// RUN LENGTHS INCLUDE NON-MULTIPLES OF FOUR, because the fused kernel
			// scores four rows at a time and its tail is exactly where a batched
			// path silently drops or duplicates candidates.
			for _, n := range []int{1, 2, 3, 4, 5, 7, 8, 13, 32, 33} {
				t.Run(fmt.Sprintf("run%d", n), func(t *testing.T) {
					ids := make([]uint32, n)
					for i := range ids {
						ids[i] = uint32((i * 7) % g.nodeCount())
					}

					batched := make([]float32, n)
					vb.batchScore(batched, &q, vb, ids)

					scalar := make([]float32, n)
					for i, id := range ids {
						scalar[i] = vb.distance(&q, vb.nodeVector(id))
					}

					for i := range ids {
						// Scale-relative agreement: the absolute gap is judged
						// against the magnitude of the values being compared, so a
						// large dot product is not held to the same absolute
						// tolerance as one near zero.
						scale := math.Max(1, math.Max(
							math.Abs(float64(batched[i])), math.Abs(float64(scalar[i]))))
						require.InDelta(t, float64(scalar[i]), float64(batched[i]), 1e-5*scale,
							"batched and scalar distances must agree for id %d in a run of %d", ids[i], n)
					}

					// RANKING AGREEMENT, the property the search actually consumes.
					// Two distance vectors could sit inside the tolerance above and
					// still order two near-equal candidates differently; sorting
					// both by (distance, id) and comparing the id sequences is what
					// catches that.
					require.Equal(t, orderOf(ids, scalar), orderOf(ids, batched),
						"batched and scalar scoring must induce the same order for a run of %d", n)
				})
			}
		})
	}
}

// orderOf returns ids sorted by their score ascending, ties broken by id — the
// same total order the neighbor selection uses.
func orderOf(ids []uint32, scores []float32) []uint32 {
	out := make([]uint32, len(ids))
	copy(out, ids)
	idx := make(map[uint32]float32, len(ids))
	for i, id := range ids {
		idx[id] = scores[i]
	}
	sort.Slice(out, func(i, j int) bool {
		if idx[out[i]] != idx[out[j]] {
			return idx[out[i]] < idx[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
