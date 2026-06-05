// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// hitIDs projects a ranked Hit slice to its ID order for order assertions.
func hitIDs(hits []searchengine.Hit) []string {
	if len(hits) == 0 {
		return nil
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	return ids
}

// TestReciprocalRankFusion is Phase 1 Step 1's criterion: RRF fuses ranked Hit
// lists by sum of 1/(60+rank0+1), keyed by Hit.ID, sorted desc, truncated to k,
// and a hit ranked high in BOTH lists outranks one ranked high in only one.
func TestReciprocalRankFusion(t *testing.T) {
	hit := func(id string) searchengine.Hit { return searchengine.Hit{ID: id, Score: 0} }

	cases := []struct {
		name string
		// per-list scores are irrelevant to RRF (rank-only) — only order matters.
		lists [][]searchengine.Hit
		k     int
		want  []string
	}{
		{
			name: "both-lists-wins: a hit high in both lists outranks single-list highs",
			// "b" is rank0 in list2 and rank1 in list1 → present in both, so it
			// accrues 1/61 + 1/62. "a" is rank0 in list1 only (1/61). "c" is rank1
			// in list2 only (1/62). b's two-list sum beats either single-list hit.
			lists: [][]searchengine.Hit{
				{hit("a"), hit("b")},
				{hit("b"), hit("c")},
			},
			k:    10,
			want: []string{"b", "a", "c"},
		},
		{
			name: "single list: ranking is preserved unchanged (no fusion)",
			lists: [][]searchengine.Hit{
				{hit("x"), hit("y"), hit("z")},
			},
			k:    10,
			want: []string{"x", "y", "z"},
		},
		{
			name: "truncation to k",
			lists: [][]searchengine.Hit{
				{hit("a"), hit("b"), hit("c"), hit("d")},
			},
			k:    2,
			want: []string{"a", "b"},
		},
		{
			name:  "empty input yields nil",
			lists: nil,
			k:     10,
			want:  nil,
		},
		{
			name: "k<=0 yields nil",
			lists: [][]searchengine.Hit{
				{hit("a")},
			},
			k:    0,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reciprocalRankFusion(tc.lists, tc.k)
			require.Equal(t, tc.want, hitIDs(got))
		})
	}
}

// TestReciprocalRankFusionScoreFormula pins the exact 1/(60+rank+1) contribution
// so a regression in the damping constant or rank offset is caught.
func TestReciprocalRankFusionScoreFormula(t *testing.T) {
	lists := [][]searchengine.Hit{
		{{ID: "a"}, {ID: "b"}},
		{{ID: "b"}, {ID: "a"}},
	}
	got := reciprocalRankFusion(lists, 10)
	require.Len(t, got, 2)

	// Both "a" and "b" appear once at rank0 and once at rank1, so both score
	// 1/61 + 1/62 and the tiebreak orders them by ID ascending.
	wantScore := 1.0/61.0 + 1.0/62.0
	require.Equal(t, []string{"a", "b"}, hitIDs(got))
	require.InDelta(t, wantScore, got[0].Score, 1e-12)
	require.InDelta(t, wantScore, got[1].Score, 1e-12)
}
