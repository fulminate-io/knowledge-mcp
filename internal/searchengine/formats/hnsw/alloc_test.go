// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// maxAllocsPerSearch is the pinned per-search allocation bound for a mapped
// segment.
//
// THE NUMBER IS MEASURED, NOT CHOSEN, and the headroom is sized to a REGRESSION
// rather than to noise. Measured steady state is 8.0 (float32) and 7.0
// (ubinary), and the observed run-to-run variance is ZERO — testing.AllocsPerRun
// returns an exact count, not a timing sample, so there is no spread to absorb.
//
// SO WHAT DOES THE HEADROOM BUY? It is the gap to the smallest regression worth
// catching, not a tolerance for jitter. The nearest real failure shape — a
// searchState that stops being pooled, so the heaps and bitset allocate per
// search — was MEASURED by installing exactly that regression: 27.0 on the
// float32 arm and 26.0 on the ubinary one. A bound of 16 therefore sits 10 below
// the closer of the two and 8 above the steady state, which is the window that
// separates "a refactor added an incidental allocation" from "the pooling
// broke". A bound at the measured value would fail on the former; one above 26
// would miss the latter.
//
// It is a CEILING rather than an equality for that reason: what this defends is
// the batched scoring's claim to add NOTHING — both scratch slices ride the
// existing searchStatePool — and a break in that claim is a step change of many
// allocations, never one or two.
//
// WHY THIS GATE EXISTS SEPARATELY FROM THE FLOOR GATE. The wired-traverse floor
// measures NANOSECONDS on a simulation that has no collection pass of its own,
// so it cannot observe an allocation regression in the production traversal at
// all — an 8-to-209 allocs probe leaves it entirely green. Timing and allocation
// are different properties and need different instruments; conflating them is
// what left this one unguarded.
const maxAllocsPerSearch = 16

// TestSearchAllocationsAreBounded pins the per-search allocation count of the
// production search path.
func TestSearchAllocationsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dtype byte
	}{
		{"float32", dtypeFloat32},
		{"ubinary", dtypeUbinary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const dim = 64
			items := float32Items(256, dim)
			blob, err := encodeGraphV3(
				buildBinaryHNSWSerialDeterministic(items, dim*4, tc.dtype, defaultM, defaultEfConstruction))
			require.NoError(t, err)
			g, err := openGraphV3(blob)
			require.NoError(t, err)
			g.setEfSearch(defaultEfSearch)

			query := items[0].vec
			// Warm the pools once so the first search's pool fill is not counted
			// as steady-state cost.
			_ = g.search(query, 10, nil)

			got := testing.AllocsPerRun(50, func() {
				hits := g.search(query, 10, nil)
				if len(hits) == 0 {
					t.Fatal("control: the measured search must actually return hits, " +
						"or this is counting the allocations of doing nothing")
				}
			})

			t.Logf("%s: %.1f allocs/search (bound %d)", tc.name, got, maxAllocsPerSearch)
			require.LessOrEqualf(t, got, float64(maxAllocsPerSearch),
				"%s search allocates %.1f times per query, above the pinned bound of %d. The batched "+
					"neighbor scoring is supposed to add NO allocations — both scratch slices ride "+
					"searchStatePool — so a jump here means a scratch stopped being reused or a run "+
					"is being re-sliced per candidate.", tc.name, got, maxAllocsPerSearch)
		})
	}
}
