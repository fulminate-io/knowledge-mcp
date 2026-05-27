// SPDX-License-Identifier: Apache-2.0

// Package tools — log correlations mode tests.
//
// These exercise handleLogsCorrelations end-to-end via handleLogsQuery.
// The pipeline fixture in tools_logs_query_test.go emits entries across
// three services (api, db, worker) but doesn't produce correlations
// naturally — correlation requires a cloud dependency checker. So the
// tests here seed CORRELATES_WITH edges directly into the log graph
// after the pipeline finishes.
package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// TestLogsQuery_CorrelationsEmpty asserts the mode renders a friendly
// message when no CORRELATES_WITH edges exist (the common case for
// graphs collected without a dependency checker).
func TestLogsQuery_CorrelationsEmpty(t *testing.T) {
	queryID := "q-corr-empty"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "correlations",
	})
	require.False(t, result.IsError, "correlations: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log correlations", "header should render")
	assert.Contains(t, text, "No CORRELATES_WITH edges",
		"empty state message should render")
}

// TestLogsQuery_CorrelationsSeeded asserts that after seeding two
// CORRELATES_WITH edges directly into the log graph, the mode returns
// both rows, sorted by score desc, with parsed services + template
// aliases.
func TestLogsQuery_CorrelationsSeeded(t *testing.T) {
	queryID := "q-corr-seeded"
	nodes, edges := buildLogCorpus(t, queryID)

	// Pick two real template IDs out of the corpus.
	templateIDs := templateNodeIDs(nodes)
	require.GreaterOrEqual(t, len(templateIDs), 2, "need ≥2 templates to seed")
	tmplA := templateIDs[0]
	tmplB := templateIDs[1]
	tmplC := templateIDs[0] // reused as A in the second edge for a distinct pair

	// Need a third template for a distinct B on edge 2. Fall back to
	// template[2] when present; else dup (the test only checks ordering
	// and shape, not edge uniqueness).
	tmplD := tmplB
	if len(templateIDs) >= 3 {
		tmplD = templateIDs[2]
	}

	// Seed two CORRELATES_WITH edges with distinct scores so the sort order is
	// deterministic. They join the corpus edges the fake's edges-union arm
	// reads, exactly as a dependency-checker run would have written them.
	edges = append(edges,
		&knowledgev1.Edge{
			FromId:     tmplA,
			ToId:       tmplB,
			Type:       string(kgtypes.EdgeCorrelatesWith),
			Confidence: 0.92,
			Method:     "test",
			Evidence:   "services=api,db resources=pod/api,pod/db score=0.920",
		},
		&knowledgev1.Edge{
			FromId:     tmplC,
			ToId:       tmplD,
			Type:       string(kgtypes.EdgeCorrelatesWith),
			Confidence: 0.55,
			Method:     "test",
			Evidence:   "services=worker,db resources=pod/worker,pod/db score=0.550",
		},
	)

	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, edges)
	h := &Handler{graphCallerOverride: fake}
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "correlations",
	})
	require.False(t, result.IsError, "correlations: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log correlations", "header should render")
	assert.Contains(t, text, "2 confirmed correlation",
		"count line should reflect seeded edges")
	// Services parsed from Evidence.
	assert.Contains(t, text, "api ↔ db", "first row services should appear")
	assert.Contains(t, text, "worker ↔ db", "second row services should appear")
	// Score sort order: 0.92 row appears before 0.55 row.
	idxHigh := stringIndex(text, "0.92")
	idxLow := stringIndex(text, "0.55")
	require.GreaterOrEqual(t, idxHigh, 0, "high-score row present")
	require.GreaterOrEqual(t, idxLow, 0, "low-score row present")
	assert.Less(t, idxHigh, idxLow, "higher score should render first")
}

// TestParseCorrelationEvidence_Shapes exercises the Evidence parser
// across the formats it's expected to handle.
func TestParseCorrelationEvidence_Shapes(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantSvcA string
		wantSvcB string
		wantResA string
		wantResB string
	}{
		{
			name:     "full",
			in:       "services=api,db resources=pod/api,pod/db score=0.92",
			wantSvcA: "api", wantSvcB: "db",
			wantResA: "pod/api", wantResB: "pod/db",
		},
		{
			name:     "services only",
			in:       "services=api,db score=0.5",
			wantSvcA: "api", wantSvcB: "db",
		},
		{
			name: "empty evidence",
			in:   "",
		},
		{
			name:     "missing second service",
			in:       "services=api, resources=pod/api,",
			wantSvcA: "api", wantSvcB: "",
			wantResA: "pod/api", wantResB: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svcA, svcB, resA, resB := parseCorrelationEvidence(tc.in)
			assert.Equal(t, tc.wantSvcA, svcA, "svcA")
			assert.Equal(t, tc.wantSvcB, svcB, "svcB")
			assert.Equal(t, tc.wantResA, resA, "resA")
			assert.Equal(t, tc.wantResB, resB, "resB")
		})
	}
}

// TestOverlapWindow_Intersects asserts the overlap math: returns the
// intersection of the two templates' FirstSeen/LastSeen, or zero when
// one side is zero-valued.
func TestOverlapWindow_Intersects(t *testing.T) {
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	a := &logwire.LogTemplate{
		FirstSeen: base,
		LastSeen:  base.Add(10 * time.Minute),
	}
	b := &logwire.LogTemplate{
		FirstSeen: base.Add(3 * time.Minute),
		LastSeen:  base.Add(20 * time.Minute),
	}
	lo, hi := overlapWindow(a, b)
	assert.Equal(t, base.Add(3*time.Minute), lo, "lo = max(firstSeen)")
	assert.Equal(t, base.Add(10*time.Minute), hi, "hi = min(lastSeen)")

	// Disjoint ranges → zero window.
	c := &logwire.LogTemplate{
		FirstSeen: base.Add(30 * time.Minute),
		LastSeen:  base.Add(40 * time.Minute),
	}
	lo2, hi2 := overlapWindow(a, c)
	assert.True(t, lo2.IsZero(), "disjoint lo should be zero")
	assert.True(t, hi2.IsZero(), "disjoint hi should be zero")

	// One side missing timestamps → zero window.
	d := &logwire.LogTemplate{}
	lo3, hi3 := overlapWindow(a, d)
	assert.True(t, lo3.IsZero())
	assert.True(t, hi3.IsZero())
}

// stringIndex is a small helper so tests don't pull strings.Index into
// every file.
func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
