// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestKnowledgeStats asserts the GAP-A knowledge stats body: the Stats RPC →
// DecodeGraphStats → RenderStatsBreakdown template under the "## Knowledge Graph"
// header. Reuses modFake (a statsRPC: Stats + Execute) — deps.GraphClient() is a
// *graphclient.GraphClient in production, which satisfies the same statsRPC seam.
func TestKnowledgeStats(t *testing.T) {
	f := &modFake{stats: &knowledgev1.GraphStats{
		NodeCount: 128, EdgeCount: 64,
		NodesByType: map[string]int64{"thought": 70, "finding": 40, "decision": 18},
	}}
	res := knowledgeStats(context.Background(), f, queryArgs{Mode: "stats"})
	require.False(t, res.IsError, textBodyTools(res))
	body := textBodyTools(res)
	assert.Contains(t, body, "## Knowledge Graph")
	assert.Contains(t, body, "Nodes: 128")
	assert.Contains(t, body, "Edges: 64")
	assert.Contains(t, body, "### Nodes by Type")
	assert.Contains(t, body, "- thought: 70")
	assert.Contains(t, body, "- finding: 40")
}

// NOTE: the "claimed, not denied" end-to-end assertion for the knowledge stats
// path lives in the bootstrap package (query_domain_dispatch_test.go) where a
// real in-process *graphclient.GraphClient backs deps.GraphClient() — the tools
// package cannot inject a fake GraphClient (the accessor returns the concrete
// type), so the helper body is covered by TestKnowledgeStats above and the
// gate-only fall-through is covered by TestInterceptQueryStats_Gate below.

// TestInterceptQueryStats_Gate asserts the intercept claims ONLY the knowledge/
// default stats shape and falls through for everything else (so the per-graph
// stats intercepts downstream keep ownership of their graphs).
func TestInterceptQueryStats_Gate(t *testing.T) {
	cases := []struct {
		name string
		args string
		tool string
	}{
		{"non-query tool", `{"mode":"stats"}`, "search"},
		{"cloud stats → cloud intercept owns it", `{"mode":"stats","graph":"cloud","account":"acme"}`, "query"},
		{"code stats → code intercept owns it", `{"mode":"stats","graph":"code","repo":"r"}`, "query"},
		{"practice stats → practice intercept owns it", `{"mode":"stats","graph":"practice","language":"go"}`, "query"},
		{"knowledge but not stats mode", `{"mode":"recent","text":"x"}`, "query"},
		{"knowledge default mode (no stats)", `{"type":"finding"}`, "query"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handled, _ := InterceptQueryStats(nil, kgtools.CallToolParams{
				Name: tc.tool, Arguments: json.RawMessage(tc.args),
			})
			assert.False(t, handled, "%s must NOT be claimed by InterceptQueryStats", tc.name)
		})
	}
}
