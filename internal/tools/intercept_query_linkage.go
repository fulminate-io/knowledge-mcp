// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_linkage.go holds the LINKAGE routing arm of the
// practice/linkage query intercept (the practice arm stays in
// intercept_query_practice_linkage.go). Split out verbatim to keep both files
// under the file-length limit — a pure move, no behavior change. The linkage
// shapes (list-graphs, mode=stats + proxy breakdown, id getNode, retired ranked
// search) are dispatched by routeLinkageClient.

// routeLinkageClient dispatches the linkage shapes. raw is the caller's
// verbatim payload, threaded explicitly (rather than stashed on queryArgs) so
// the per-arm accounting gate cannot be forgotten at a claim point.
func routeLinkageClient(ctx context.Context, gc statsRPC, a queryArgs, raw json.RawMessage) kgtools.ToolResult {
	// (1) list-graphs: no id/text/mode.
	if a.ID == "" && a.Text == "" && a.Mode == "" && len(a.Queries) == 0 {
		if err := accountQueryParams(armLinkageListGraphs, raw); err != nil {
			return errorResult(err.Error())
		}
		return listLinkageGraphs(ctx, gc)
	}
	// (2) mode=stats.
	if a.Mode == "stats" {
		if err := accountQueryParams(armLinkageStats, raw); err != nil {
			return errorResult(err.Error())
		}
		return linkageStatsClient(ctx, gc, a.Format)
	}
	// (3) id getNode.
	if a.ID != "" {
		if err := accountQueryParams(armLinkageGetNode, raw); err != nil {
			return errorResult(err.Error())
		}
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
	// (4) ranked text search RETIRED. linkage proxies denormalize
	// source-graph text and carry no unique client-indexable content, so there is
	// no client linkage search index. The index-free ops above (list-graphs, stats
	// + proxy breakdown, id getNode) — and proxy read-through — are unaffected.
	if err := accountQueryParams(armLinkageSearchRetired, raw); err != nil {
		return errorResult(err.Error())
	}
	return rankedSearchRetiredResult("linkage")
}

// linkageStatsClient renders the linkage stats body + the proxy-by-foreign_graph
// breakdown (one extra Match(NodeProxy) Execute — bounded by the proxy set, the
// linkage-specific enrichment the server linkageStats appended).
func linkageStatsClient(ctx context.Context, gc statsRPC, format string) kgtools.ToolResult {
	resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{Target: &knowledgev1.GraphSelector{Graph: "linkage"}})
	if err != nil {
		return errorResult("linkage graph stats failed: " + err.Error())
	}
	stats := resp.GetGraphStats()
	if format == "json" {
		// JSON omits the proxy-by-foreign_graph breakdown (markdown-only enrichment).
		return jsonResult(map[string]any{
			"graph":               "linkage",
			"node_count":          stats.GetNodeCount(),
			"edge_count":          stats.GetEdgeCount(),
			"binary_vector_count": stats.GetBinaryVectorCount(),
			"nodes_by_type":       stats.GetNodesByType(),
			"edges_by_type":       stats.GetEdgesByType(),
		})
	}
	var sb strings.Builder
	sb.WriteString("## Linkage Graph\n\n")
	sb.WriteString(engine.RenderStatsBreakdown(stats))
	sb.WriteString(renderLinkageProxyBreakdown(ctx, gc))
	return textResult(sb.String())
}

// renderLinkageProxyBreakdown drains the proxy nodes in bounded keyset pages and
// renders the proxy-by-foreign_graph breakdown the server linkageStats appended.
// Returns "" when there are no proxies / the fetch fails (degrade gracefully —
// the stats body still renders).
//
// The nodes stay HYDRATED because the breakdown reads their foreign_graph
// metadata, which no ids carrier serves; the paging is what bounds the read.
func renderLinkageProxyBreakdown(ctx context.Context, gc statsRPC) string {
	proxies, err := paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		resp, rerr := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeType: string(kgtypes.NodeProxy)},
				Limit:     int32(paging.BrowsePageSize),
				// SET on every page including the first, where the value is empty:
				// presence is what selects the keyset browse.
				AfterId:   &cursor,
				SkipTotal: true, // the drain never reads Total
			}},
			Target: &knowledgev1.GraphSelector{Graph: "linkage"},
		})
		if rerr != nil {
			return nil, rerr
		}
		return engine.DecodeNodes(resp)
	}, paging.BrowsePageSize)
	if err != nil || len(proxies) == 0 {
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

// listLinkageGraphs enumerates the loaded linkage graphs + the topology hint.
func listLinkageGraphs(ctx context.Context, gc statsRPC) kgtools.ToolResult {
	// The linkage graph is a single instance (empty name); fetch its counts.
	nodes, edges, err := graphCounts(ctx, gc, "linkage", "")
	// A FAILED READ IS NOT AN ABSENT GRAPH, and this listing is where conflating
	// the two did the most damage: graphCounts used to answer (0, 0) on any error,
	// which fell straight into the zero check below and reported the graph as NOT
	// FOUND. The error is taken FIRST, ahead of that check, so an unreachable
	// graph is never rendered as a missing one.
	if err != nil {
		return errorResult(fmt.Sprintf(
			"linkage list-graphs: could not read the linkage graph's counts: %v — this is a READ FAILURE, not an absent graph", err))
	}
	if nodes == 0 && edges == 0 {
		return textResult("No linkage graph found. Linkage graphs are created by the tier-1 linker when code-to-cloud relationships are detected.")
	}
	var sb strings.Builder
	sb.WriteString("Linkage graph:\n\n")
	fmt.Fprintf(&sb, "- %d nodes, %d edges\n", nodes, edges)
	return textResult(sb.String())
}
