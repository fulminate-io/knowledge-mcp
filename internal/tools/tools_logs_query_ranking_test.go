// SPDX-License-Identifier: Apache-2.0

// Package tools — template signal-strength ranking tests.
//
// Cover the scoring formula (count × correlation-boost × severity ×
// recency), the dispatch path that wires it into overview/drill-down,
// and the bidirectional CORRELATES_WITH edge-counting helper.
package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestRankTemplatesBySignal_OrderingMatchesScore verifies the ordering
// across a hand-rolled fixture: a high-severity high-count template
// outranks a high-count INFO template, which in turn outranks a
// low-count anything.
func TestRankTemplatesBySignal_OrderingMatchesScore(t *testing.T) {
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	templates := []*logwire.LogTemplate{
		{ID: "t-info-low", Pattern: "low info", Severity: logwire.SeverityInfo,
			Count: 1, FirstSeen: base, LastSeen: base},
		{ID: "t-err-high", Pattern: "high err", Severity: logwire.SeverityError,
			Count: 50, FirstSeen: base, LastSeen: base.Add(time.Minute)},
		{ID: "t-info-mid", Pattern: "mid info", Severity: logwire.SeverityInfo,
			Count: 20, FirstSeen: base, LastSeen: base.Add(30 * time.Second)},
	}
	rows := rankTemplatesBySignal(nil, templates)
	require.Len(t, rows, 3)
	assert.Equal(t, "t-err-high", rows[0].Template.ID,
		"ERR×50 should outrank INFO×20 and INFO×1")
	assert.Equal(t, "t-info-mid", rows[1].Template.ID,
		"INFO×20 should outrank INFO×1")
	assert.Equal(t, "t-info-low", rows[2].Template.ID)
}

// TestRankTemplatesBySignal_ZeroCountSkipped asserts templates with
// zero entries get score 0 and sink to the bottom.
func TestRankTemplatesBySignal_ZeroCountSkipped(t *testing.T) {
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	templates := []*logwire.LogTemplate{
		{ID: "zero", Pattern: "zero", Severity: logwire.SeverityError, Count: 0,
			FirstSeen: base, LastSeen: base},
		{ID: "one", Pattern: "one", Severity: logwire.SeverityInfo, Count: 1,
			FirstSeen: base, LastSeen: base},
	}
	rows := rankTemplatesBySignal(nil, templates)
	require.Len(t, rows, 2)
	assert.Equal(t, "one", rows[0].Template.ID, "non-zero count outranks zero")
	assert.InDelta(t, 0.0, rows[1].Score, 1e-9, "zero count → zero score")
}

// TestSeverityWeight pins the per-severity multipliers so re-tuning
// requires an explicit test change.
func TestSeverityWeight(t *testing.T) {
	assert.InDelta(t, 5.0, logs.SeverityWeight(logwire.SeverityCritical), 1e-9)
	assert.InDelta(t, 3.0, logs.SeverityWeight(logwire.SeverityError), 1e-9)
	assert.InDelta(t, 1.5, logs.SeverityWeight(logwire.SeverityWarn), 1e-9)
	assert.InDelta(t, 1.0, logs.SeverityWeight(logwire.SeverityInfo), 1e-9)
	assert.InDelta(t, 0.5, logs.SeverityWeight(logwire.SeverityDebug), 1e-9)
	assert.InDelta(t, 0.25, logs.SeverityWeight(logwire.SeverityTrace), 1e-9)
	// Unknown severity passes through with neutral weight.
	assert.InDelta(t, 1.0, logs.SeverityWeight("UNKNOWN"), 1e-9)
}

// TestScoreTemplate_Recency asserts the recency decay maps oldest to
// 0.5 and newest to 1.0 within the graph window, and degenerate
// timestamps fall back to 1.0.
func TestScoreTemplate_Recency(t *testing.T) {
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	first := base
	last := base.Add(10 * time.Minute)

	// Template at the oldest end → recency = 0.5.
	tOld := &logwire.LogTemplate{Count: 10, Severity: logwire.SeverityInfo,
		FirstSeen: first, LastSeen: first}
	scoreOld := logs.ScoreTemplate(tOld, 0, first, last)
	// 10 × 1 (no corr boost) × 1 (INFO weight) × 0.5 (recency) = 5.
	assert.InDelta(t, 5.0, scoreOld, 1e-9)

	// Template at the newest end → recency = 1.0.
	tNew := &logwire.LogTemplate{Count: 10, Severity: logwire.SeverityInfo,
		FirstSeen: last, LastSeen: last}
	scoreNew := logs.ScoreTemplate(tNew, 0, first, last)
	// 10 × 1 × 1 × 1.0 = 10.
	assert.InDelta(t, 10.0, scoreNew, 1e-9)

	// Degenerate window → recency 1.0 (no adjustment).
	scoreDeg := logs.ScoreTemplate(tNew, 0, time.Time{}, time.Time{})
	assert.InDelta(t, 10.0, scoreDeg, 1e-9)
}

// TestCountTemplateCorrelationPeers_BothDirections asserts both
// outgoing and incoming CORRELATES_WITH edges are counted, deduped by
// peer ID.
func TestCountTemplateCorrelationPeers_BothDirections(t *testing.T) {
	queryID := "q-corr-count"
	nodes, edges := buildLogCorpus(t, queryID)

	templateIDs := templateNodeIDs(nodes)
	require.GreaterOrEqual(t, len(templateIDs), 3, "need ≥3 templates for both-direction test")
	a, b, c := templateIDs[0], templateIDs[1], templateIDs[2]

	// Seed: a→b (outgoing from a), c→a (incoming to a). Expect a's
	// peer count = 2 (b and c). The edges join the corpus the *logState
	// indexes by OutEdges/InEdges.
	edges = append(edges,
		&knowledgev1.Edge{FromId: a, ToId: b, Type: string(kgtypes.EdgeCorrelatesWith), Confidence: 0.5,
			Method: "test", Evidence: "services=x,y"},
		&knowledgev1.Edge{FromId: c, ToId: a, Type: string(kgtypes.EdgeCorrelatesWith), Confidence: 0.5,
			Method: "test", Evidence: "services=x,z"},
	)

	st := buildLogStateFromCorpus(nodes, edges)
	got := countTemplateCorrelationPeers(st, a)
	assert.Equal(t, 2, got, "a should see both b (out) and c (in) as peers")

	// b only has an incoming edge from a → 1 peer.
	assert.Equal(t, 1, countTemplateCorrelationPeers(st, b))
	// c only has an outgoing edge to a → 1 peer.
	assert.Equal(t, 1, countTemplateCorrelationPeers(st, c))
}

// TestLogsQuery_OverviewIncludesTopTemplates verifies the overview
// renders the new "Top templates by signal" section.
func TestLogsQuery_OverviewIncludesTopTemplates(t *testing.T) {
	queryID := "q-overview-rank"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID,
	})
	require.False(t, result.IsError, "overview: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Top templates by signal",
		"overview should include the new top-templates section")
	// The section header should come BEFORE the per-label sections so
	// the agent reads it first.
	idxTop := strings.Index(text, "Top templates by signal")
	idxLabels := strings.Index(text, "### service") // synthetic fixture has 'service' label
	require.GreaterOrEqual(t, idxTop, 0)
	if idxLabels >= 0 {
		assert.Less(t, idxTop, idxLabels,
			"top-templates section should render before per-label distributions")
	}
}

// TestSortTemplatesBySignal_InPlace asserts the in-place sort used by
// drill-down promotes higher-signal templates to the front.
func TestSortTemplatesBySignal_InPlace(t *testing.T) {
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	templates := []*logwire.LogTemplate{
		{ID: "low", Pattern: "low", Severity: logwire.SeverityInfo, Count: 1,
			FirstSeen: base, LastSeen: base},
		{ID: "high", Pattern: "high", Severity: logwire.SeverityError, Count: 100,
			FirstSeen: base, LastSeen: base.Add(time.Minute)},
		{ID: "mid", Pattern: "mid", Severity: logwire.SeverityWarn, Count: 10,
			FirstSeen: base, LastSeen: base.Add(30 * time.Second)},
	}
	sortTemplatesBySignal(nil, templates)
	assert.Equal(t, "high", templates[0].ID)
	assert.Equal(t, "mid", templates[1].ID)
	assert.Equal(t, "low", templates[2].ID)
}
