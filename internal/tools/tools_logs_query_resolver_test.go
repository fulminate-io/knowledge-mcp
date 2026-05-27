// SPDX-License-Identifier: Apache-2.0

// Package tools — resolver-trace mode tests.
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestLogsQuery_ResolverTraceShape asserts the trace renders both
// resolved and unresolved sections plus the summary header. Every
// stream from the synthetic fixture is unresolved (no cloud graph
// is loaded in tests) so the unresolved table should populate.
func TestLogsQuery_ResolverTraceShape(t *testing.T) {
	queryID := "q-resolver-shape"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "resolver",
	})
	require.False(t, result.IsError, "resolver: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Resolver trace", "header should render")
	// All streams unresolved (no cloud graph) → unresolved section
	// should populate, resolved section should announce 0.
	assert.Contains(t, text, "Unresolved", "unresolved section should render")
	// Service labels from the synthetic fixture should appear in the
	// unresolved table.
	assert.Contains(t, text, "service",
		"the unresolved table should include the service column")
}

// TestLogsQuery_ResolverTraceWithEmittedBy asserts that seeding an
// EMITTED_BY edge from a stream's label moves that stream into the
// resolved section.
func TestLogsQuery_ResolverTraceWithEmittedBy(t *testing.T) {
	queryID := "q-resolver-resolved"
	nodes, edges := buildLogCorpus(t, queryID)

	// Pick the first stream + one of its label nodes (resolved off the
	// corpus's HAS_LABEL edges), and seed an EMITTED_BY edge from that label
	// to a synthetic cloud-proxy ID.
	streamID := firstNodeIDOfType(t, nodes, kgtypes.NodeLogStream)
	labelNodes := collectChildNodesOfType(buildLogStateFromCorpus(nodes, edges), streamID,
		kgwire.OutgoingEdges, kgtypes.EdgeHasLabel, kgtypes.NodeLogLabel)
	require.NotEmpty(t, labelNodes, "stream should have label nodes")
	labelID := labelNodes[0].Id

	const proxyID = "cloud:test:Pod/test-pod"
	edges = append(edges, &knowledgev1.Edge{
		FromId: labelID, ToId: proxyID, Type: string(kgtypes.EdgeEmittedBy),
		Method: "test",
	})

	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, edges)
	h := &Handler{graphCallerOverride: fake}
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "resolver",
	})
	require.False(t, result.IsError)
	text := resultText(result)

	assert.Contains(t, text, "Resolved", "resolved section should render")
	assert.Contains(t, text, proxyID, "the seeded proxy ID should appear in the resolved table")
}

// TestSplitResolverRows asserts the partition into resolved vs
// unresolved respects the Resolved field length, in stable order.
func TestSplitResolverRows(t *testing.T) {
	rows := []resolverRow{
		{Alias: "a", Resolved: []string{"proxy1"}},
		{Alias: "b", Resolved: nil},
		{Alias: "c", Resolved: []string{"proxy2"}},
		{Alias: "d", Resolved: []string{}},
	}
	resolved, unresolved := splitResolverRows(rows)
	require.Len(t, resolved, 2)
	require.Len(t, unresolved, 2)
	assert.Equal(t, "a", resolved[0].Alias)
	assert.Equal(t, "c", resolved[1].Alias)
	assert.Equal(t, "b", unresolved[0].Alias)
	assert.Equal(t, "d", unresolved[1].Alias)
}

// TestJoinResolvedProxies covers the empty / single / multi rendering.
func TestJoinResolvedProxies(t *testing.T) {
	assert.Equal(t, "—", joinResolvedProxies(nil))
	assert.Equal(t, "`one`", joinResolvedProxies([]string{"one"}))
	got := joinResolvedProxies([]string{"a", "b"})
	assert.True(t, strings.Contains(got, "`a`") && strings.Contains(got, "`b`"),
		"both proxies should appear, got: %s", got)
}
