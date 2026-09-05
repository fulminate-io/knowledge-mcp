// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"maps"
	"slices"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// graph_stats_body.go holds the SELECTOR-DRIVEN graph-stats render: one Stats
// RPC, rendered as a markdown breakdown under a caller-supplied header or as a
// json envelope, with optional bounded per-type sample names.
//
// The package carries three near-identical copies of this body — the knowledge,
// practice and cloud/cicd stats arms — differing only in their GraphSelector,
// their markdown header and their json identity keys. This is that body factored
// once, with web/pdf as its first caller. THE THREE EXISTING CALLERS ARE
// DELIBERATELY NOT RETROFITTED: convergence here is lazy by standing direction,
// and one of those files is owned by concurrent work. A later touch of any of
// them is the right moment to fold it in.

// renderGraphStatsBody renders one graph's stats for the selector it is handed.
//
// header is the markdown heading the text arm opens with; the json arm ignores
// it and emits the identity as graph/name fields instead.
//
// extraFields carries caller-resolved lines the generic Stats RPC cannot know
// about — the web/pdf arm passes the collected graph's collector_schema_version
// through it. Rendered as additional lines after the breakdown in the text arm
// and merged into the map in the json arm. Nil means no extra lines.
func renderGraphStatsBody(
	ctx context.Context,
	gc statsRPC,
	sel *knowledgev1.GraphSelector,
	header string,
	a queryArgs,
	extraFields map[string]string,
) kgtools.ToolResult {
	resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{Target: sel})
	if err != nil {
		return errorResult(sel.GetGraph() + " graph stats failed: " + err.Error())
	}
	stats := resp.GetGraphStats()
	if a.Format == "json" {
		out := map[string]any{
			"graph":               sel.GetGraph(),
			"name":                sel.GetName(),
			"node_count":          stats.GetNodeCount(),
			"edge_count":          stats.GetEdgeCount(),
			"binary_vector_count": stats.GetBinaryVectorCount(),
			"nodes_by_type":       stats.GetNodesByType(),
			"edges_by_type":       stats.GetEdgesByType(),
		}
		for _, k := range slices.Sorted(maps.Keys(extraFields)) {
			out[k] = extraFields[k]
		}
		return jsonResult(out)
	}
	var sb strings.Builder
	sb.WriteString(header + "\n\n")
	sb.WriteString(engine.RenderStatsBreakdown(stats))
	for _, k := range slices.Sorted(maps.Keys(extraFields)) {
		sb.WriteString(k + ": " + extraFields[k] + "\n")
	}
	if a.Samples {
		engine.RenderSampleNames(&sb, stats, fetchGraphSamples(ctx, statsExecOf(gc), sel, stats))
	}
	return textResult(sb.String())
}

// fetchGraphSamples fetches up to 2 sample nodes per node TYPE against the same
// selector renderGraphStatsBody was handed.
//
// It exists rather than reusing a sibling because both siblings bake the graph
// identity into their own bodies — one hardcodes the practice target and takes a
// language string, the other hardcodes the empty knowledge target and takes no
// selector at all. A selector-driven render needs a selector-driven sample fetch.
//
// BOUNDED BY THE NODE-TYPE COUNT, which is dozens — this is not an N+1 over
// nodes. A type that errors or comes back empty is skipped, because a missing
// sample is a missing enrichment, not a failed stats read.
func fetchGraphSamples(
	ctx context.Context,
	exec engine.ExecuteFn,
	sel *knowledgev1.GraphSelector,
	stats *knowledgev1.GraphStats,
) map[kgtypes.NodeType][]*knowledgev1.Node {
	byType := stats.GetNodesByType()
	samples := make(map[kgtypes.NodeType][]*knowledgev1.Node, len(byType))
	for nt := range byType {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeType: nt},
				Limit:     2,
				SkipTotal: true,
			}},
			Target: sel,
		})
		if err != nil {
			continue
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil || len(nodes) == 0 {
			continue
		}
		samples[kgtypes.NodeType(nt)] = nodes
	}
	return samples
}
