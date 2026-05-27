// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// recommendCase is the client-side scenario shape pinning the relocated
// RecommendAction against the documented hysteresis bands. It mirrors the
// store-side applyCase table (node_value_stats_recommend_test.go) so the client
// renderer's recommendation and the server executor's Apply() share the same
// threshold geometry over their respective KeyStats/OverrideConfig types.
//
// Each KeyStats/OverrideConfig is built as a fresh proto pointer (never a copied
// value) so copylocks stays clean — the proto messages embed a sync mutex.
type recommendCase struct {
	name string
	ks   *knowledgev1.KeyStats
	ovr  *knowledgev1.OverrideConfig
	key  string
	want Recommendation
}

// recommendCases enumerates the boundary, override, auto-as-scalar, and nil
// scenarios that lock the strictness of the `<` / `>=` boundaries, the
// hysteresis gap (1000/3000 distinct, 5/3 median), the Auto-as-Scalar
// fallthrough, and the FORCE_EDGE/FORCE_SCALAR precedence (ForceEdge wins).
func recommendCases() []recommendCase {
	return []recommendCase{
		{
			name: "promote_boundary_distinct_999_median_5",
			ks:   &knowledgev1.KeyStats{DistinctValues: 999, MedianNodesPerValue: 5, CurrentRepresentation: RepresentationScalar},
			key:  "k",
			want: RecommendPromote,
		},
		{
			name: "promote_just_misses_distinct_1000",
			ks:   &knowledgev1.KeyStats{DistinctValues: 1000, MedianNodesPerValue: 5, CurrentRepresentation: RepresentationScalar},
			key:  "k",
			want: RecommendKeepScalar,
		},
		{
			name: "promote_just_misses_median_4",
			ks:   &knowledgev1.KeyStats{DistinctValues: 999, MedianNodesPerValue: 4, CurrentRepresentation: RepresentationScalar},
			key:  "k",
			want: RecommendKeepScalar,
		},
		{
			name: "demote_distinct_trigger_3001",
			ks:   &knowledgev1.KeyStats{DistinctValues: 3001, MedianNodesPerValue: 10, CurrentRepresentation: RepresentationEdge},
			key:  "k",
			want: RecommendDemote,
		},
		{
			name: "demote_median_trigger_2",
			ks:   &knowledgev1.KeyStats{DistinctValues: 2000, MedianNodesPerValue: 2, CurrentRepresentation: RepresentationEdge},
			key:  "k",
			want: RecommendDemote,
		},
		{
			name: "hysteresis_no_flap_edge_side",
			ks:   &knowledgev1.KeyStats{DistinctValues: 2000, MedianNodesPerValue: 4, CurrentRepresentation: RepresentationEdge},
			key:  "k",
			want: RecommendKeepEdge,
		},
		{
			name: "hysteresis_no_flap_scalar_side",
			ks:   &knowledgev1.KeyStats{DistinctValues: 2000, MedianNodesPerValue: 4, CurrentRepresentation: RepresentationScalar},
			key:  "k",
			want: RecommendKeepScalar,
		},
		{
			name: "auto_treated_like_scalar_promotes",
			ks:   &knowledgev1.KeyStats{DistinctValues: 999, MedianNodesPerValue: 5, CurrentRepresentation: RepresentationAuto},
			key:  "k",
			want: RecommendPromote,
		},
		{
			name: "force_edge_override_wins_over_demote_stats",
			ks:   &knowledgev1.KeyStats{DistinctValues: 9000, MedianNodesPerValue: 1, CurrentRepresentation: RepresentationEdge},
			ovr:  &knowledgev1.OverrideConfig{ForceEdge: []string{"k"}},
			key:  "k",
			want: RecommendForceEdge,
		},
		{
			name: "force_scalar_override_wins_over_promote_stats",
			ks:   &knowledgev1.KeyStats{DistinctValues: 10, MedianNodesPerValue: 100, CurrentRepresentation: RepresentationScalar},
			ovr:  &knowledgev1.OverrideConfig{ForceScalar: []string{"k"}},
			key:  "k",
			want: RecommendForceScalar,
		},
		{
			name: "both_overrides_force_edge_wins",
			ks:   &knowledgev1.KeyStats{DistinctValues: 500, MedianNodesPerValue: 10, CurrentRepresentation: RepresentationScalar},
			ovr:  &knowledgev1.OverrideConfig{ForceEdge: []string{"k"}, ForceScalar: []string{"k"}},
			key:  "k",
			want: RecommendForceEdge,
		},
		{
			name: "nil_ks_returns_keep_scalar",
			ks:   nil,
			key:  "k",
			want: RecommendKeepScalar,
		},
	}
}

// TestRecommendAction_HysteresisBands walks the canonical scenario table and
// asserts RecommendAction returns the documented recommendation for each. The
// table is deliberately exhaustive on the 1000/3000 distinct + 5/3 median
// boundaries so any future tweak to the hysteresis bands surfaces here first.
func TestRecommendAction_HysteresisBands(t *testing.T) {
	for _, tc := range recommendCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := RecommendAction(tc.ks, tc.ovr, tc.key)
			assert.Equal(t, tc.want, got,
				"RecommendAction(%s): ks=%+v ovr=%+v", tc.name, tc.ks, tc.ovr)
		})
	}
}

// TestLiveDistinctValues asserts the live distinct count prefers the populated
// ValueDistribution map over the persisted DistinctValues field, falling back to
// the persisted field when the distribution is empty (nil → 0).
func TestLiveDistinctValues(t *testing.T) {
	assert.Equal(t, int64(0), liveDistinctValues(nil))
	// Distribution populated → its length wins over the persisted field.
	ks := &knowledgev1.KeyStats{
		DistinctValues:    99,
		ValueDistribution: map[string]int64{"a": 1, "b": 2, "c": 3},
	}
	assert.Equal(t, int64(3), liveDistinctValues(ks))
	// Distribution empty → fall back to the persisted DistinctValues.
	assert.Equal(t, int64(7), liveDistinctValues(&knowledgev1.KeyStats{DistinctValues: 7}))
}

// TestLiveMedianNodesPerValue asserts the live median computes from the
// distribution when populated (upper-middle element, matching store.medianCount)
// and falls back to the persisted MedianNodesPerValue otherwise.
func TestLiveMedianNodesPerValue(t *testing.T) {
	assert.Equal(t, int64(0), liveMedianNodesPerValue(nil))
	// Distribution {a:10,b:8,c:5,d:3,e:1} → sorted [1,3,5,8,10] → median 5.
	ks := &knowledgev1.KeyStats{
		MedianNodesPerValue: 99,
		ValueDistribution:   map[string]int64{"a": 10, "b": 8, "c": 5, "d": 3, "e": 1},
	}
	assert.Equal(t, int64(5), liveMedianNodesPerValue(ks))
	// Even length {a:2,b:4} → sorted [2,4] → upper-middle counts[1] = 4.
	assert.Equal(t, int64(4), liveMedianNodesPerValue(&knowledgev1.KeyStats{
		ValueDistribution: map[string]int64{"a": 2, "b": 4},
	}))
	// Empty distribution → fall back to the persisted median.
	assert.Equal(t, int64(6), liveMedianNodesPerValue(&knowledgev1.KeyStats{MedianNodesPerValue: 6}))
}
