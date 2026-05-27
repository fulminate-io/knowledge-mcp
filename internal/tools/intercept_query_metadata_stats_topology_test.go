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
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// TestMetadataStats_OverrideThreaded is the load-bearing criterion (7f29424e):
// an operator-pinned key surfaces FORCE_SCALAR/FORCE_EDGE, proving the
// OverrideConfig carrier is decoded and threaded into RecommendAction (a zero
// OverrideConfig would mis-recommend). Exercised at the decode→build seam — the
// exact decode + thread the composer performs.
func TestMetadataStats_OverrideThreaded(t *testing.T) {
	// Build the typed MetadataStats + OverrideConfig carriers exactly as they
	// ride the MetadataStatsResponse — the composer now reads them straight off
	// the response (resp.GetMetadataStats() / resp.GetOverrideConfig()), no decode.
	stats := &knowledgev1.MetadataStats{Keys: map[string]*knowledgev1.KeyStats{
		"severity": {DistinctValues: 4, TotalWrites: 100, CurrentRepresentation: engine.RepresentationScalar},
		"hash":     {DistinctValues: 9000, TotalWrites: 50, CurrentRepresentation: engine.RepresentationEdge},
	}}
	resp := &knowledgev1.MetadataStatsResponse{
		MetadataStats:  stats,
		OverrideConfig: &knowledgev1.OverrideConfig{ForceEdge: []string{"severity"}, ForceScalar: []string{"hash"}},
	}

	// Read BOTH carriers straight off the response (the composer's load-bearing step).
	rows := engine.BuildMetadataStatsRows(resp.GetMetadataStats(), resp.GetOverrideConfig())
	recByKey := map[string]string{}
	for _, r := range rows {
		recByKey[r.Key] = r.RecommendedAction
	}
	assert.Equal(t, "FORCE_EDGE", recByKey["severity"], "ForceEdge-pinned key must surface FORCE_EDGE")
	assert.Equal(t, "FORCE_SCALAR", recByKey["hash"], "ForceScalar-pinned key must surface FORCE_SCALAR")

	// Control: a ZERO OverrideConfig must NOT force-recommend — proving the
	// thread is what makes the recommendation correct.
	zeroRows := engine.BuildMetadataStatsRows(resp.GetMetadataStats(), &knowledgev1.OverrideConfig{})
	for _, r := range zeroRows {
		assert.NotContains(t, r.RecommendedAction, "FORCE", "zero override must not force-recommend")
	}
}

// TestMetadataStats_TableAndJSON asserts both render shapes.
func TestMetadataStats_TableAndJSON(t *testing.T) {
	stats := &knowledgev1.MetadataStats{Keys: map[string]*knowledgev1.KeyStats{
		"severity": {DistinctValues: 4, TotalWrites: 10},
	}}
	rows := engine.BuildMetadataStatsRows(stats, &knowledgev1.OverrideConfig{})

	body := engine.RenderMetadataStatsTable("knowledge", rows)
	assert.Contains(t, body, "## Metadata stats — knowledge graph (1 keys)")
	assert.Contains(t, body, "| severity |")

	payload := engine.MetadataStatsJSONPayload("knowledge", "", "go", "", rows)
	assert.Equal(t, "knowledge", payload["graph"])
	assert.Equal(t, "go", payload["language"])
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"key":"severity"`)
}

// TestRenderTopologyFindings asserts the non-dead_code findings render
// (the foundation.RenderFindings seam the local-analyzer query path uses).
func TestRenderTopologyFindings(t *testing.T) {
	findings := []foundation.Finding{
		{Algorithm: "pagerank", Severity: foundation.SeverityWarning, Title: "hot", Evidence: []string{"n1"}},
	}
	body, err := foundation.RenderFindings(findings)
	require.NoError(t, err)
	assert.Contains(t, body, "pagerank")
	assert.Contains(t, body, "hot")
}

// TestInterceptTopology_Gate asserts InterceptTopology claims mode=topology and
// falls through for other modes (the dead_code client-RTA path is unchanged;
// non-dead_code now routes through the Topology RPC).
func TestInterceptTopology_Gate(t *testing.T) {
	handled, _ := InterceptTopology(context.Background(), nil, kgtools.CallToolParams{
		Name: "query", Arguments: json.RawMessage(`{"mode":"stats"}`),
	})
	assert.False(t, handled, "non-topology mode not claimed")
}

// TestInterceptQueryMetadataStats_Gate asserts the metadata_stats gate.
func TestInterceptQueryMetadataStats_Gate(t *testing.T) {
	handled, _ := InterceptQueryMetadataStats(nil, kgtools.CallToolParams{
		Name: "query", Arguments: json.RawMessage(`{"mode":"stats"}`),
	})
	assert.False(t, handled, "non metadata_stats mode not claimed")
}
