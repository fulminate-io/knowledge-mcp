// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"math"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// referenceTemporalScore is the server formula, reproduced here independently as
// the oracle the client port must match EXACTLY (temporal_rerank.go:16-25).
func referenceTemporalScore(updatedAtNanos int64, halfLifeDays float64) float64 {
	if halfLifeDays <= 0 {
		halfLifeDays = 30
	}
	if updatedAtNanos == 0 {
		return 0.5
	}
	ageDays := time.Since(time.Unix(0, updatedAtNanos)).Hours() / 24.0
	return math.Pow(2.0, -ageDays/halfLifeDays)
}

// TestApplyTemporalRerank_MatchesServerBoost asserts the client UpdatedAt
// half-life rerank mirrors the server ApplyTemporalReranking(30) EXACTLY: a BOOST
// score *= (1 + 2^(-age/half)), a zero/IsZero UpdatedAt → 0.5 neutral, and a
// half-life ≤ 0 floored to 30. Both the boosted scores and the re-sorted order
// must match the independent oracle.
func TestApplyTemporalRerank_MatchesServerBoost(t *testing.T) {
	now := time.Now()
	day := int64(24 * time.Hour)
	// Three rows: fresh (today), old (90 days), and never-updated (zero).
	fresh := now.UnixNano()
	old := now.UnixNano() - 90*day
	rows := []engine.SearchResult{
		{Node: &knowledgev1.Node{Id: "old", UpdatedAt: old}, Score: 1.0},
		{Node: &knowledgev1.Node{Id: "fresh", UpdatedAt: fresh}, Score: 1.0},
		{Node: &knowledgev1.Node{Id: "zero", UpdatedAt: 0}, Score: 1.0},
	}

	// Independent oracle: same input, server formula, then stable sort desc.
	want := make([]engine.SearchResult, len(rows))
	copy(want, rows)
	for i := range want {
		want[i].Score = want[i].Score * (1.0 + referenceTemporalScore(want[i].Node.GetUpdatedAt(), 30))
	}
	sort.SliceStable(want, func(i, j int) bool { return want[i].Score > want[j].Score })

	applyTemporalRerank(rows, 30)

	require.Len(t, rows, len(want))
	for i := range rows {
		assert.Equal(t, want[i].Node.GetId(), rows[i].Node.GetId(), "row %d order matches oracle", i)
		assert.InDelta(t, want[i].Score, rows[i].Score, 1e-9, "row %d boosted score matches oracle", i)
	}
	// Sanity: fresh (boost≈2.0) outranks old (boost between 1 and 2) outranks the
	// zero-timestamp neutral (boost 1.5)? Fresh boosts ~2, old (90d, half 30) boosts
	// 1+2^-3=1.125, zero boosts 1.5 → order: fresh, zero, old.
	assert.Equal(t, "fresh", rows[0].Node.GetId(), "freshest row ranks first")
	assert.Equal(t, "zero", rows[1].Node.GetId(), "neutral (0.5) outranks the 90-day-old row")
	assert.Equal(t, "old", rows[2].Node.GetId(), "the 90-day-old row ranks last")
}

// TestComputeTemporalScore_Rules pins the three discrete rules: IsZero → 0.5,
// half-life ≤ 0 floors to 30, and a node updated exactly half-life days ago
// scores 0.5 (the half-life definition).
func TestComputeTemporalScore_Rules(t *testing.T) {
	// IsZero (no timestamp) → 0.5 regardless of half-life.
	assert.InDelta(t, 0.5, computeTemporalScore(0, 30), 1e-12)
	assert.InDelta(t, 0.5, computeTemporalScore(0, 0), 1e-12)

	// A node updated exactly 30 days ago with half-life 30 scores ~0.5.
	thirtyDaysAgo := time.Now().Add(-30 * 24 * time.Hour).UnixNano()
	assert.InDelta(t, 0.5, computeTemporalScore(thirtyDaysAgo, 30), 1e-3)

	// half-life ≤ 0 floors to 30 → same 0.5 at 30 days.
	assert.InDelta(t, 0.5, computeTemporalScore(thirtyDaysAgo, 0), 1e-3)
	assert.InDelta(t, 0.5, computeTemporalScore(thirtyDaysAgo, -5), 1e-3)
}
