// SPDX-License-Identifier: Apache-2.0

// Package tools — explain-mode tests.
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestLogsQuery_ExplainSpecificPair seeds a single correlation between
// two templates and asserts the explain block surfaces the score,
// services, and time-window context for that exact pair.
func TestLogsQuery_ExplainSpecificPair(t *testing.T) {
	queryID := "q-explain-pair"
	nodes, edges := buildLogCorpus(t, queryID)

	templateIDs := templateNodeIDs(nodes)
	require.GreaterOrEqual(t, len(templateIDs), 2)
	a, b := templateIDs[0], templateIDs[1]

	edges = append(edges, &knowledgev1.Edge{
		FromId: a, ToId: b, Type: string(kgtypes.EdgeCorrelatesWith),
		Confidence: 0.91, Method: "test",
		Evidence: "services=svcA,svcB resources=pod/A,pod/B score=0.910",
	})

	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, edges)
	h := &Handler{graphCallerOverride: fake}
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "explain",
		Extra: map[string]string{"a": a, "b": b},
	})
	require.False(t, result.IsError, "explain: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Correlation explanation")
	assert.Contains(t, text, "Score: **0.91**")
	assert.Contains(t, text, "method=test")
	assert.Contains(t, text, "svcA", "service A should appear")
	assert.Contains(t, text, "svcB", "service B should appear")
	assert.Contains(t, text, "pod/A", "resource A should appear")
}

// TestLogsQuery_ExplainAnchorAllPeers asserts id=<single-template>
// returns one block per correlation involving that template.
func TestLogsQuery_ExplainAnchorAllPeers(t *testing.T) {
	queryID := "q-explain-anchor"
	nodes, edges := buildLogCorpus(t, queryID)

	templateIDs := templateNodeIDs(nodes)
	require.GreaterOrEqual(t, len(templateIDs), 3)
	a, b, c := templateIDs[0], templateIDs[1], templateIDs[2]

	edges = append(edges,
		&knowledgev1.Edge{FromId: a, ToId: b, Type: string(kgtypes.EdgeCorrelatesWith), Confidence: 0.8,
			Method: "test", Evidence: "services=x,y"},
		&knowledgev1.Edge{FromId: a, ToId: c, Type: string(kgtypes.EdgeCorrelatesWith), Confidence: 0.4,
			Method: "test", Evidence: "services=x,z"},
	)

	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, edges)
	h := &Handler{graphCallerOverride: fake}
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "explain", ID: a,
	})
	require.False(t, result.IsError, "explain: %s", resultText(result))
	text := resultText(result)

	// Two blocks expected — one per peer.
	assert.Contains(t, text, "#1 —", "first block header")
	assert.Contains(t, text, "#2 —", "second block header")
	assert.Contains(t, text, "0.80", "first edge score")
	assert.Contains(t, text, "0.40", "second edge score")
}

// TestLogsQuery_ExplainMissingArgs asserts the handler errors
// helpfully when neither id nor extra={a,b} is supplied.
func TestLogsQuery_ExplainMissingArgs(t *testing.T) {
	queryID := "q-explain-missing"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "explain",
	})
	require.True(t, result.IsError, "missing args should error")
	assert.True(t, strings.Contains(resultText(result), "id=") ||
		strings.Contains(resultText(result), "extra="),
		"error should hint at the required arg shape, got: %s", resultText(result))
}

// TestLogsQuery_ExplainNoEdge asserts that asking to explain a
// non-existent correlation pair returns a clear error.
func TestLogsQuery_ExplainNoEdge(t *testing.T) {
	queryID := "q-explain-noedge"
	nodes, _ := buildLogCorpus(t, queryID)

	templateIDs := templateNodeIDs(nodes)
	require.GreaterOrEqual(t, len(templateIDs), 2)
	a, b := templateIDs[0], templateIDs[1]

	// No correlation edge seeded — only the corpus's structural edges.
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "explain",
		Extra: map[string]string{"a": a, "b": b},
	})
	require.True(t, result.IsError)
	assert.Contains(t, resultText(result), "no CORRELATES_WITH edge")
}
