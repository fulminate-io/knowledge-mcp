// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_practice_linkage.go is the client-side claim for the practice
// and linkage per-graph query shapes the server routePracticeQuery /
// routeLinkageTarget served (cmd/knowledge-server/tools/tools_query_practice.go,
// tools_query.go, tools_query_linkage.go).
//
// practice shapes:
//   - list-graphs   (no language): enumerate practice graphs
//     (RETURN_MODE_GRAPH_NAMES Execute + Stats RPC counts).
//   - mode=stats    : Stats RPC → RenderStatsBreakdown ("## Practice Graph: <lang>").
//   - search        (text + language): generic search Execute → RenderPracticeResults.
//
// linkage shapes:
//   - list-graphs   (no id/text/mode): enumerate linkage graphs + topology hint.
//   - mode=stats    : Stats RPC → RenderStatsBreakdown ("## Linkage Graph") +
//     the proxy-by-foreign_graph breakdown.
//   - id getNode    : Execute ByID → node render.
//   - search        (text): generic search Execute → RenderLinkageSearch (reuses
//     the engine proxy annotation helpers).

// InterceptQueryPracticeLinkage claims query(graph in {practice,linkage}).
func InterceptQueryPracticeLinkage(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphClient()
	switch a.Graph {
	case "practice":
		if gc == nil {
			return true, errorResult("practice: graph client unavailable")
		}
		return true, routePracticeClient(context.Background(), deps, gc, a)
	case "linkage":
		if gc == nil {
			return true, errorResult("linkage: graph client unavailable")
		}
		return true, routeLinkageClient(context.Background(), gc, a)
	default:
		return false, kgtools.ToolResult{}
	}
}

// routePracticeClient dispatches the three practice shapes.
func routePracticeClient(ctx context.Context, deps ClientDeps, gc statsRPC, a queryArgs) kgtools.ToolResult {
	// (1) No language → list practice graphs.
	if a.Language == "" {
		return listPracticeGraphs(ctx, deps)
	}
	// (2) mode=stats.
	if a.Mode == "stats" {
		resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{Target: &knowledgev1.GraphSelector{Graph: "practice", Language: a.Language}})
		if err != nil {
			return errorResult(fmt.Sprintf("practice %q graph stats failed: %s", a.Language, err.Error()))
		}
		stats := resp.GetGraphStats()
		var sb strings.Builder
		fmt.Fprintf(&sb, "## Practice Graph: %s\n\n", a.Language)
		sb.WriteString(engine.RenderStatsBreakdown(stats))
		if a.Samples {
			samples := fetchPracticeSamples(ctx, gc.Execute, a.Language, stats)
			var sampleSB strings.Builder
			engine.RenderSampleNames(&sampleSB, stats, samples)
			sb.WriteString(sampleSB.String())
		}
		return textResult(sb.String())
	}
	// (3) search/browse with language → generic search Execute.
	query := practiceQueryText(a)
	resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Queries:    []string{query},
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_SEARCH,
		}},
		Target: &knowledgev1.GraphSelector{Graph: "practice", Language: a.Language},
	})
	if err != nil {
		return errorResult("practice search failed: " + err.Error())
	}
	results, derr := engine.DecodeSearch(resp)
	if derr != nil {
		return errorResult("practice search decode failed: " + derr.Error())
	}
	return engine.RenderPracticeResults(a.Language, query, results)
}

// practiceQueryText picks the search text from the query/text fields.
func practiceQueryText(a queryArgs) string {
	if a.Text != "" {
		return a.Text
	}
	if len(a.Queries) > 0 {
		return a.Queries[0]
	}
	return ""
}

// routeLinkageClient dispatches the linkage shapes.
func routeLinkageClient(ctx context.Context, gc statsRPC, a queryArgs) kgtools.ToolResult {
	// (1) list-graphs: no id/text/mode.
	if a.ID == "" && a.Text == "" && a.Mode == "" && len(a.Queries) == 0 {
		return listLinkageGraphs(ctx, gc)
	}
	// (2) mode=stats.
	if a.Mode == "stats" {
		return linkageStatsClient(ctx, gc)
	}
	// (3) id getNode.
	if a.ID != "" {
		resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: a.ID}},
			Target: &knowledgev1.GraphSelector{Graph: "linkage"},
		})
		if err != nil {
			return errorResult(fmt.Sprintf("node %s not found in linkage graph", a.ID))
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil || len(nodes) == 0 {
			return errorResult(fmt.Sprintf("node %s not found in linkage graph", a.ID))
		}
		return engine.RenderGenericNode(nodes[0], "linkage")
	}
	// (4) search (text).
	query := practiceQueryText(a)
	resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Queries:    []string{query},
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_SEARCH,
		}},
		Target: &knowledgev1.GraphSelector{Graph: "linkage"},
	})
	if err != nil {
		return errorResult("linkage search failed: " + err.Error())
	}
	results, derr := engine.DecodeSearch(resp)
	if derr != nil {
		return errorResult("linkage search decode failed: " + derr.Error())
	}
	return engine.RenderLinkageSearch(query, results)
}

// linkageStatsClient renders the linkage stats body + the proxy-by-foreign_graph
// breakdown (one extra Match(NodeProxy) Execute — bounded by the proxy set, the
// linkage-specific enrichment the server linkageStats appended).
func linkageStatsClient(ctx context.Context, gc statsRPC) kgtools.ToolResult {
	resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{Target: &knowledgev1.GraphSelector{Graph: "linkage"}})
	if err != nil {
		return errorResult("linkage graph stats failed: " + err.Error())
	}
	stats := resp.GetGraphStats()
	var sb strings.Builder
	sb.WriteString("## Linkage Graph\n\n")
	sb.WriteString(engine.RenderStatsBreakdown(stats))
	sb.WriteString(renderLinkageProxyBreakdown(ctx, gc))
	return textResult(sb.String())
}

// renderLinkageProxyBreakdown fetches the proxy nodes (one Match(NodeProxy)
// Execute, bounded by the proxy set) and renders the proxy-by-foreign_graph
// breakdown the server linkageStats appended. Returns "" when there are no
// proxies / the fetch fails (degrade gracefully — the stats body still renders).
func renderLinkageProxyBreakdown(ctx context.Context, gc statsRPC) string {
	resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{NodeType: string(kgtypes.NodeProxy)},
		}},
		Target: &knowledgev1.GraphSelector{Graph: "linkage"},
	})
	if err != nil {
		return ""
	}
	proxies, derr := engine.DecodeNodes(resp)
	if derr != nil || len(proxies) == 0 {
		return ""
	}
	counts := make(map[string]int)
	for _, n := range proxies {
		fg := kgtypes.Value(n, "foreign_graph")
		if fg == "" {
			fg = "unknown"
		}
		counts[fg]++
	}
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("\n### Proxy Breakdown\n")
	for _, k := range keys {
		fmt.Fprintf(&sb, "- %s: %d proxies\n", k, counts[k])
	}
	return sb.String()
}

// listPracticeGraphs enumerates the loaded practice graphs (RETURN_MODE_GRAPH_NAMES
// Execute via listGraphNamesOfType + per-graph Stats counts).
func listPracticeGraphs(ctx context.Context, deps ClientDeps) kgtools.ToolResult {
	names, err := listGraphNamesOfType(ctx, deps, "practice")
	if err != nil {
		return errorResult("practice list-graphs failed: " + err.Error())
	}
	if len(names) == 0 {
		return textResult("No practice graphs found.")
	}
	gc := deps.GraphClient()
	var sb strings.Builder
	fmt.Fprintf(&sb, "Practice graphs (%d):\n\n", len(names))
	for _, name := range names {
		nodes, edges := graphCounts(ctx, gc, "practice", name)
		fmt.Fprintf(&sb, "- **%s** — %d nodes, %d edges\n", name, nodes, edges)
	}
	sb.WriteString("\nUse `query({ \"graph\": \"practice\", \"language\": \"go\" })` to browse a specific practice graph.")
	return textResult(sb.String())
}

// listLinkageGraphs enumerates the loaded linkage graphs + the topology hint.
func listLinkageGraphs(ctx context.Context, gc statsRPC) kgtools.ToolResult {
	// The linkage graph is a single instance (empty name); fetch its counts.
	nodes, edges := graphCounts(ctx, gc, "linkage", "")
	if nodes == 0 && edges == 0 {
		return textResult("No linkage graph found. Linkage graphs are created by the tier-1 linker when code-to-cloud relationships are detected.")
	}
	var sb strings.Builder
	sb.WriteString("Linkage graph:\n\n")
	fmt.Fprintf(&sb, "- %d nodes, %d edges\n", nodes, edges)
	return textResult(sb.String())
}

// fetchPracticeSamples fetches up to 2 sample nodes per node type for the
// practice stats sample enrichment (bounded by node-type count).
func fetchPracticeSamples(ctx context.Context, exec engine.ExecuteFn, language string, stats *knowledgev1.GraphStats) map[kgtypes.NodeType][]*knowledgev1.Node {
	byType := stats.GetNodesByType()
	samples := make(map[kgtypes.NodeType][]*knowledgev1.Node, len(byType))
	for nt := range byType {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeType: nt},
				Limit:     2,
			}},
			Target: &knowledgev1.GraphSelector{Graph: "practice", Language: language},
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
