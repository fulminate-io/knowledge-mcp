// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestRenderStatsBreakdown_Golden asserts the rendered body matches the
// server formatStatsBreakdown byte-shape (scalar block + sorted-by-count-desc
// Nodes-by-Type / Edges-by-Type sections), consuming the typed proto GraphStats
// the caller passes straight from resp.GetGraphStats().
func TestRenderStatsBreakdown_Golden(t *testing.T) {
	stats := &knowledgev1.GraphStats{
		NodeCount:         5,
		EdgeCount:         3,
		VectorCount:       2,
		BinaryVectorCount: 1,
		TextDocCount:      4,
		HasBm25:           true,
		HasHnsw:           false,
		NodesByType:       map[string]int64{"finding": 3, "decision": 2},
		EdgesByType:       map[string]int64{"informed-by": 2, "relates-to": 1},
	}
	want := "Nodes: 5\n" +
		"Edges: 3\n" +
		"Vectors: 2\n" +
		"Binary vectors: 1\n" +
		"Text documents: 4\n" +
		"BM25: true\n" +
		"HNSW: false\n" +
		"\n### Nodes by Type\n" +
		"- finding: 3\n" +
		"- decision: 2\n" +
		"\n### Edges by Type\n" +
		"- informed-by: 2\n" +
		"- relates-to: 1\n"
	assert.Equal(t, want, RenderStatsBreakdown(stats))
}

// TestRenderStatsBreakdown_EmptyMaps asserts no empty section headers are
// emitted for a graph with no node/edge types.
func TestRenderStatsBreakdown_EmptyMaps(t *testing.T) {
	stats := &knowledgev1.GraphStats{NodeCount: 0, EdgeCount: 0}
	got := RenderStatsBreakdown(stats)
	assert.NotContains(t, got, "### Nodes by Type")
	assert.NotContains(t, got, "### Edges by Type")
}

// TestRenderStatsBreakdown_NilSafe asserts a nil proto GraphStats renders the
// all-zero scalar block without panicking (the proto getters are nil-safe).
func TestRenderStatsBreakdown_NilSafe(t *testing.T) {
	got := RenderStatsBreakdown(nil)
	assert.Contains(t, got, "Nodes: 0\n")
	assert.NotContains(t, got, "### Nodes by Type")
}

// TestRenderSampleNames_Pure asserts RenderSampleNames consumes the supplied
// pre-fetched samples (no fetching of its own), orders by node-type count desc,
// and falls back to ID when SymbolName is empty.
func TestRenderSampleNames_Pure(t *testing.T) {
	stats := &knowledgev1.GraphStats{
		NodesByType: map[string]int64{"finding": 3, "decision": 1},
	}
	samples := map[kgtypes.NodeType][]*knowledgev1.Node{
		"finding": {
			{Id: "f1", SymbolName: "Finding One"},
			{Id: "f2", SymbolName: ""},
		},
		"decision": {{Id: "d1", SymbolName: "Decision A"}},
	}
	var sb strings.Builder
	RenderSampleNames(&sb, stats, samples)
	want := "\n### Sample names by type\n" +
		"- finding: Finding One, f2\n" +
		"- decision: Decision A\n"
	assert.Equal(t, want, sb.String())
}

// TestRenderSampleNames_Empty asserts no section is emitted when NodesByType is
// empty.
func TestRenderSampleNames_Empty(t *testing.T) {
	var sb strings.Builder
	RenderSampleNames(&sb, &knowledgev1.GraphStats{}, nil)
	assert.Empty(t, sb.String())
}
