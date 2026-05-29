// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_cloud_cicd.go is the client-side claim for the cloud and cicd
// per-graph query shapes the server routeCloudQuery / routeCICDQuery served
// (cmd/knowledge-server/tools/tools_query_cloud.go, tools_query_cicd.go). One
// PARAMETRIC intercept covers both graphs — the only differences are the graph
// string, the node type, the list/collect labels, and the Region/Provider
// secondary key (carried by the Phase-1 engine.resourceKind via the renderers).
//
// Four shapes (mirroring the server routes):
//   - list-graphs   (no account, no id/text, mode != stats): enumerate the
//     loaded graphs of this type (RETURN_MODE_GRAPH_NAMES Execute) + per-graph
//     counts (Stats RPC) — NOT a server-side store.ListGraphs (filesystem-blind).
//   - mode=stats    : Stats RPC → decodeGraphStats → renderStatsBreakdown with
//     the per-account header (+ bounded renderSampleNames when samples=true).
//   - id getNode    : Execute ByID against the per-account graph → renderResourceNode.
//   - browse        : Execute Match(resource-node) + (resource_type → OP_PREFIX
//     metadata predicate, server-side-predicated, NOT a client full-scan filter)
//     → renderResourceBrowse.
type resourceGraphKind struct {
	graph       string              // "cloud" / "cicd"
	nodeType    kgtypes.NodeType    // NodeCloudResource / NodeCICDResource
	listLabel   string              // "Cloud" / "CI/CD" (list header noun)
	collectHint string              // the collect() hint shown when no graphs exist
	browseHint  string              // the next-call hint on a non-empty list
	render      engine.ResourceKind // Phase-1 parametric render kind (label+secondaryKey)
	requireMsg  string              // user-friendly "requires account" message
}

var (
	cloudGraphKind = resourceGraphKind{
		graph:       "cloud",
		nodeType:    kgtypes.NodeCloudResource,
		listLabel:   "Cloud",
		collectHint: "No cloud graphs found. Use `collect({ \"type\": \"<aws|gcp|azure|k8s>\", \"id\": \"<account>\" })` to collect cloud resources.",
		browseHint:  "\nUse `query({ \"graph\": \"cloud\", \"account\": \"myaccount\" })` to browse a specific cloud graph.",
		render:      engine.ResourceKindCloud,
		requireMsg:  "cloud graph queries require 'account' parameter",
	}
	cicdGraphKind = resourceGraphKind{
		graph:       "cicd",
		nodeType:    kgtypes.NodeCICDResource,
		listLabel:   "CI/CD",
		collectHint: "No CI/CD graphs found. Use `collect({ \"type\": \"github\", \"id\": \"my-org\" })` to collect CI/CD resources.",
		browseHint:  "\nUse `query({ \"graph\": \"cicd\", \"account\": \"github-myorg\" })` to browse a specific CI/CD graph.",
		render:      engine.ResourceKindCICD,
		requireMsg:  "CI/CD graph queries require 'account' parameter",
	}
)

// InterceptQueryCloudCICD claims query(graph in {cloud,cicd}). Returns (false,_)
// for any other tool/graph so the next chain step takes over.
func InterceptQueryCloudCICD(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	var kind resourceGraphKind
	switch a.Graph {
	case "cloud":
		kind = cloudGraphKind
	case "cicd":
		kind = cicdGraphKind
	default:
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult(a.Graph + ": graph client unavailable")
	}
	ctx := context.Background()

	// (1) list-graphs: no account, no id/text, mode != stats.
	if a.Account == "" && a.ID == "" && a.Text == "" && a.Mode != "stats" {
		return true, listResourceGraphs(ctx, deps, kind)
	}
	// account is required for every other shape.
	if a.Account == "" && a.ID == "" {
		return true, errorResult(kind.requireMsg)
	}

	// (3) id getNode.
	if a.ID != "" {
		return true, resourceGetNode(ctx, gc.Execute, kind, a)
	}

	// (2) mode=stats.
	if a.Mode == "stats" {
		sc, ok := gc.(statsRPC)
		if !ok {
			return true, errorResult(a.Graph + " stats: stats seam unavailable")
		}
		return true, resourceStats(ctx, sc, kind, a)
	}

	// (4) browse (optionally resource_type-prefixed).
	return true, resourceBrowse(ctx, gc.Execute, kind, a)
}

// listResourceGraphs enumerates the loaded graphs of this type via the generic
// RETURN_MODE_GRAPH_NAMES Execute read (the client graph-overview source — NOT
// the filesystem-blind server store.ListGraphs) and fetches per-graph node/edge
// counts via the Stats RPC. Bounded by the (low) number of loaded graphs.
func listResourceGraphs(ctx context.Context, deps ClientDeps, kind resourceGraphKind) kgtools.ToolResult {
	names, err := listGraphNamesOfType(ctx, deps, kind.graph)
	if err != nil {
		return errorResult(kind.graph + " list-graphs failed: " + err.Error())
	}
	if len(names) == 0 {
		return textResult(kind.collectHint)
	}
	gc := deps.GraphCaller()
	sc, ok := gc.(statsRPC)
	if !ok {
		return errorResult(kind.graph + " list-graphs: stats seam unavailable")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s graphs (%d):\n\n", kind.listLabel, len(names))
	for _, name := range names {
		nodes, edges := graphCounts(ctx, sc, kind.graph, name)
		fmt.Fprintf(&sb, "- **%s** — %d nodes, %d edges\n", name, nodes, edges)
	}
	sb.WriteString(kind.browseHint)
	return textResult(sb.String())
}

// graphCounts fetches the node/edge counts for one named graph via the Stats
// RPC. A failed fetch degrades to zeros (the listing still names the graph).
func graphCounts(ctx context.Context, gc statsRPC, graph, name string) (int, int) {
	resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{
		Target: resourceTarget(graph, name),
	})
	if err != nil {
		return 0, 0
	}
	stats := resp.GetGraphStats()
	return int(stats.GetNodeCount()), int(stats.GetEdgeCount())
}

// resourceStats renders the per-account stats body: Stats RPC → decodeGraphStats
// → renderStatsBreakdown with the per-account header (+ bounded sample names
// when samples=true).
func resourceStats(ctx context.Context, gc statsRPC, kind resourceGraphKind, a queryArgs) kgtools.ToolResult {
	resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{Target: resourceTarget(kind.graph, a.Account)})
	if err != nil {
		return errorResult(fmt.Sprintf("%s %q graph stats failed: %s", kind.graph, a.Account, err.Error()))
	}
	stats := resp.GetGraphStats()
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s Graph: %s\n\n", kind.listLabel, a.Account)
	sb.WriteString(engine.RenderStatsBreakdown(stats))
	if a.Samples {
		samples := fetchTypeSamples(ctx, statsExecOf(gc), kind.graph, a.Account, stats)
		var sampleSB strings.Builder
		engine.RenderSampleNames(&sampleSB, stats, samples)
		sb.WriteString(sampleSB.String())
	}
	return textResult(sb.String())
}

// resourceGetNode renders a single resource node: Execute ByID against the
// per-account graph → renderResourceNode (Phase 1).
func resourceGetNode(ctx context.Context, exec engine.ExecuteFn, kind resourceGraphKind, a queryArgs) kgtools.ToolResult {
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: a.ID}},
		Target: resourceTarget(kind.graph, a.Account),
	})
	if err != nil {
		return errorResult(fmt.Sprintf("node %s not found in %s graph %q", a.ID, kind.graph, a.Account))
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil || len(nodes) == 0 {
		return errorResult(fmt.Sprintf("node %s not found in %s graph %q", a.ID, kind.graph, a.Account))
	}
	return engine.RenderResourceNode(kind.render, a.Account, nodes[0])
}

// resourceBrowse renders a browse listing. The resource_type filter compiles to
// a SERVER-SIDE OP_PREFIX metadata predicate on the Match browse (NOT a client
// full-scan + filter) so the result set is bounded by the matched nodes.
func resourceBrowse(ctx context.Context, exec engine.ExecuteFn, kind resourceGraphKind, a queryArgs) kgtools.ToolResult {
	limit := int(a.Limit)
	if limit <= 0 {
		limit = 20
	}
	offset := int(a.Offset)

	sel := &knowledgev1.Selection{NodeType: string(kind.nodeType)}
	if a.ResourceType != "" {
		sel.MetadataPredicates = []*knowledgev1.MetadataPredicate{
			{Key: "resource_type", Op: knowledgev1.MetadataPredicate_OP_PREFIX, Value: a.ResourceType},
		}
	}
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection: sel,
			Limit:     int32(limit),
			Offset:    int32(offset),
		}},
		Target: resourceTarget(kind.graph, a.Account),
	})
	if err != nil {
		return errorResult(kind.graph + " browse failed: " + err.Error())
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return errorResult(kind.graph + " browse decode failed: " + derr.Error())
	}
	if len(nodes) == 0 {
		msg := fmt.Sprintf("No resources in %s graph %q.", kind.graph, a.Account)
		if a.ResourceType != "" {
			msg = fmt.Sprintf("No resources matching type prefix %q in %s graph %q.", a.ResourceType, kind.graph, a.Account)
		}
		return textResult(msg)
	}
	return engine.RenderResourceBrowse(kind.render, a.Account, nodes, offset, a.ResourceType)
}

// resourceTarget builds the GraphSelector for a cloud/cicd graph (account-keyed).
func resourceTarget(graph, account string) *knowledgev1.GraphSelector {
	return &knowledgev1.GraphSelector{Graph: graph, Account: account}
}

// statsRPC is the narrow view of *GraphClient the stats/list paths need —
// Stats + Execute. Declared so the helpers can be tested with a fake.
type statsRPC interface {
	Stats(ctx context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error)
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// statsExecOf adapts a statsRPC to the engine.ExecuteFn the sample-fetch helper
// consumes.
func statsExecOf(gc statsRPC) engine.ExecuteFn {
	return gc.Execute
}

// fetchTypeSamples fetches up to 2 sample nodes per node type (bounded by the
// node-type count, dozens — NOT N+1 over nodes) for the renderSampleNames
// enrichment. One Match(type).Limit(2) Execute per node type.
func fetchTypeSamples(ctx context.Context, exec engine.ExecuteFn, graph, account string, stats *knowledgev1.GraphStats) map[kgtypes.NodeType][]*knowledgev1.Node {
	byType := stats.GetNodesByType()
	samples := make(map[kgtypes.NodeType][]*knowledgev1.Node, len(byType))
	for nt := range byType {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeType: nt},
				Limit:     2,
			}},
			Target: resourceTarget(graph, account),
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

// listGraphNamesOfType enumerates the loaded graph names of the given type via
// the generic Execute seam (RETURN_MODE_GRAPH_NAMES, the fetchGraphNamesOfType
// helper) — the client graph-overview source. Empty names are dropped.
func listGraphNamesOfType(ctx context.Context, deps ClientDeps, graphType string) ([]string, error) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, fmt.Errorf("graph client unavailable")
	}
	infos, err := fetchGraphNamesOfType(ctx, gc, graphType)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, gi := range infos {
		if gi.Name != "" {
			names = append(names, gi.Name)
		}
	}
	return names, nil
}
