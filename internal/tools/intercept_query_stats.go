// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_stats.go is the client-side claim for query(mode:stats) on the
// KNOWLEDGE (default) graph — the one stats surface no other intercept owned. The
// per-graph stats intercepts gate elsewhere: InterceptQueryCloudCICD claims
// graph∈{cloud,cicd}, InterceptQueryModulesCodeStats claims graph==code, and
// InterceptQueryPracticeLinkage claims graph∈{practice,linkage}. A bare
// query(mode:stats) (no graph, or graph=knowledge) matched NONE of them and fell
// through to the post-cutover generic deny (the GAP-A regression). This intercept
// closes that gap with the SAME Stats RPC → DecodeGraphStats → RenderStatsBreakdown
// template every other graph uses, under a "## Knowledge Graph" header.

// InterceptQueryStats claims query(mode:stats) for graph in {"",knowledge}. Returns
// (false,_) for any other tool/graph/mode so the next chain step takes over.
func InterceptQueryStats(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Mode != "stats" || (a.Graph != "" && a.Graph != "knowledge") {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("knowledge: graph client unavailable")
	}
	sc, ok := gc.(statsRPC)
	if !ok {
		return true, errorResult("knowledge stats: stats seam unavailable")
	}
	return true, knowledgeStats(context.Background(), sc, a)
}

// knowledgeStats renders the default knowledge graph stats body: Stats RPC →
// DecodeGraphStats → RenderStatsBreakdown under the "## Knowledge Graph" header
// (+ bounded sample names when samples=true). The empty-graph GraphSelector
// (Graph:"") targets the default knowledge graph, mirroring the server's
// knowledge stats target.
func knowledgeStats(ctx context.Context, gc statsRPC, a queryArgs) kgtools.ToolResult {
	resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{Target: &knowledgev1.GraphSelector{Graph: ""}})
	if err != nil {
		return errorResult("knowledge graph stats failed: " + err.Error())
	}
	stats := resp.GetGraphStats()
	var sb strings.Builder
	sb.WriteString("## Knowledge Graph\n\n")
	sb.WriteString(engine.RenderStatsBreakdown(stats))
	if a.Samples {
		samples := fetchKnowledgeSamples(ctx, gc.Execute, stats)
		engine.RenderSampleNames(&sb, stats, samples)
	}
	return textResult(sb.String())
}

// fetchKnowledgeSamples fetches up to 2 sample nodes per node type for the
// knowledge stats sample enrichment (bounded by node-type count, dozens — NOT N+1
// over nodes). One Match(type).Limit(2) Execute per node type against the default
// knowledge graph (empty GraphSelector).
func fetchKnowledgeSamples(ctx context.Context, exec engine.ExecuteFn, stats *knowledgev1.GraphStats) map[kgtypes.NodeType][]*knowledgev1.Node {
	byType := stats.GetNodesByType()
	samples := make(map[kgtypes.NodeType][]*knowledgev1.Node, len(byType))
	target := &knowledgev1.GraphSelector{Graph: ""}
	for nt := range byType {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeType: nt},
				Limit:     2,
			}},
			Target: target,
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
