// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
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
func InterceptQueryCloudCICD(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
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
	// mode:topology belongs to the topology intercept, which resolves cloud/cicd
	// instances itself (account-keyed). Claiming it here would silently render a
	// resource browse instead of running the analyzer — the mode must fall
	// through, not be swallowed.
	if a.Mode == "topology" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult(a.Graph + ": graph client unavailable")
	}

	// (1) list-graphs: no account, no id/text, mode != stats.
	if a.Account == "" && a.ID == "" && a.Text == "" && a.Mode != "stats" {
		if err := accountQueryParams(armCloudCICDListGraphs, params.Arguments); err != nil {
			return true, errorResult(err.Error())
		}
		return true, listResourceGraphs(ctx, deps, kind)
	}
	// account is required for every other shape.
	if a.Account == "" && a.ID == "" {
		return true, errorResult(kind.requireMsg)
	}

	// (3) id getNode.
	if a.ID != "" {
		if err := accountQueryParams(armCloudCICDGetNode, params.Arguments); err != nil {
			return true, errorResult(err.Error())
		}
		return true, resourceGetNode(ctx, gc.Execute, kind, a)
	}

	// (2) mode=stats.
	if a.Mode == "stats" {
		if err := accountQueryParams(armCloudCICDStats, params.Arguments); err != nil {
			return true, errorResult(err.Error())
		}
		sc, ok := gc.(statsRPC)
		if !ok {
			return true, errorResult(a.Graph + " stats: stats seam unavailable")
		}
		return true, resourceStats(ctx, sc, kind, a)
	}

	// (4) ranked search (text): account-scoped client engine search. Served
	// entirely CLIENT-side via composeResourceSearchClient (Manager.Search →
	// RRF → hydrate → RenderResourceSearch); never a server RETURN_MODE_SEARCH.
	if query := resourceQueryText(a); query != "" {
		if err := accountQueryParams(armCloudCICDSearch, params.Arguments); err != nil {
			return true, errorResult(err.Error())
		}
		// Readiness gate (bind-first startup): the mgr==nil case below is already nil-safe (no
		// panic) but emits a permanent-degrade message that misleads during the
		// bind-first wiring window. Add the uniform not-ready pre-check so the
		// window is distinguishable from a genuinely-unwired pipeline.
		if !deps.PipelineReady() {
			return true, errorResult(kind.graph + " search: daemon still starting — LLM pipeline not ready yet, retry shortly")
		}
		mgr := deps.SegmentManager()
		if mgr == nil {
			return true, errorResult(kind.graph + " search: client segment engine unavailable")
		}
		return true, composeResourceSearchClient(ctx, deps, mgr, kind, a.Account, query, a.Format)
	}

	// (5) browse (optionally resource_type-prefixed).
	if err := accountQueryParams(armCloudCICDBrowse, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
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
		// Route the instance name into the right selector field per graph family
		// (practice→Language, cloud/cicd→Account, else→Name) — resourceTarget is
		// Account-only and silently zeroed practice/other counts.
		Target: graphsel.GraphSelectorFor(kgtypes.GraphType(graph), name, false),
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
	if a.Format == "json" {
		return jsonResult(map[string]any{
			"graph":               kind.graph,
			"account":             a.Account,
			"node_count":          stats.GetNodeCount(),
			"edge_count":          stats.GetEdgeCount(),
			"binary_vector_count": stats.GetBinaryVectorCount(),
			"nodes_by_type":       stats.GetNodesByType(),
			"edges_by_type":       stats.GetEdgesByType(),
		})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s Graph: %s\n\n", kind.listLabel, a.Account)
	sb.WriteString(engine.RenderStatsBreakdown(stats))
	var sampleFailures []string
	if a.Samples {
		var samples map[kgtypes.NodeType][]*knowledgev1.Node
		samples, sampleFailures = fetchTypeSamples(ctx, statsExecOf(gc), kind.graph, a.Account, stats)
		var sampleSB strings.Builder
		engine.RenderSampleNames(&sampleSB, stats, samples)
		sb.WriteString(sampleSB.String())
	}
	// appendNotice concatenates into Content[0].Text, which would CORRUPT a JSON
	// payload — safe here only because the format:"json" arm returned above, so
	// this render is text. If this arm ever gains a JSON envelope, the disclosure
	// moves to a separate block or a payload key.
	return appendNotice(textResult(sb.String()), sampleFailureNotice(sampleFailures))
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
			// SkipTotal is FALSE, matching the sibling practice browse:
			// RenderResourceBrowse reads Total for its header count AND for the
			// "_Use offset=N to see more._" footer, so skipping it would delete
			// paging and put the PAGE length back in the header as a corpus figure.
			// Limit+Offset still bound the rows; only the count is unbounded.
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
	// format:"json" is HONORED here, not silently dropped. It used to be: this arm
	// never read a.Format and RenderResourceBrowse returned markdown
	// unconditionally, so query(graph:"cloud", format:"json") got prose with no
	// error and no field saying the requested format was not served — a declared
	// request parameter silently ignored on a read path.
	//
	// The envelope is engine.BrowseJSONResult, the SAME {graph, type, results,
	// total, truncated} shape the server browse returns for every other node type
	// and the shape the rules arm already honors format:"json" with — never a
	// bespoke resource map.
	//
	// THE JSON RETURN PRECEDES THE MARKDOWN EMPTY-CASE below, exactly as the rules
	// arm's comment warns: otherwise an empty account serializes as the "No
	// resources" prose and breaks the caller's JSON.parse.
	if a.Format == "json" {
		return engine.WithTruncationNotice(
			// The opt-in is FALSE as a statement about this arm: the cloud and
			// CICD browse arms REJECT include_tombstones, so no tombstoned row can
			// reach this envelope for tombstoned_at to project.
			engine.BrowseJSONResult(kind.graph, string(kind.nodeType), nodes,
				int(resp.GetTotal()), a.Fields, resp.GetTruncated(), false), resp)
	}

	// EVERY return path below wraps, not just the populated one. The disclosure is
	// then unconditional on the arm's exits rather than conditional on which
	// branch ran, so a later change to the empty-case predicate or to the
	// truncation verdict cannot silently reintroduce a path that returns without
	// disclosing. On today's server the empty case is a no-op — ceilingEngaged
	// needs rowCount >= effective and zero rows never reach it.
	if len(nodes) == 0 {
		msg := fmt.Sprintf("No resources in %s graph %q.", kind.graph, a.Account)
		if a.ResourceType != "" {
			msg = fmt.Sprintf("No resources matching type prefix %q in %s graph %q.", a.ResourceType, kind.graph, a.Account)
		}
		return engine.WithTruncationNotice(textResult(msg), resp)
	}
	return engine.WithTruncationNotice(
		engine.RenderResourceBrowse(kind.render, a.Account, nodes, offset, int(resp.GetTotal()), a.ResourceType), resp)
}

// resourceTarget builds the GraphSelector for a cloud/cicd graph (account-keyed).
func resourceTarget(graph, account string) *knowledgev1.GraphSelector {
	return &knowledgev1.GraphSelector{Graph: graph, Account: account}
}

// The cloud/cicd RANKED-SEARCH arm (resourceQueryText,
// composeResourceSearchClient) lives in the sibling
// intercept_query_cloud_cicd_search.go, so this file stays under the 500-line
// cap while the browse and stats arms grow their truncation and totals
// disclosure.

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
//
// The second return is the node types whose read FAILED, sorted. It exists
// because the two bare continues this loop used to carry made an exec error, an
// RPC failure and a decode failure all render as a missing sample section with
// no error, no warning and no count — a reader saw a shorter list and could not
// tell a type with no resources from a type whose read broke.
//
// A GENUINELY EMPTY TYPE IS NOT A FAILURE and is still skipped silently. That
// split is the substance of the fix: the old `derr != nil || len(nodes) == 0`
// conjunction collapsed a fault the reader must know about into an ordinary,
// correct answer, and reporting both as failures would make every empty resource
// type look broken — a new false statement rather than a fix.
func fetchTypeSamples(
	ctx context.Context, exec engine.ExecuteFn, graph, account string, stats *knowledgev1.GraphStats,
) (map[kgtypes.NodeType][]*knowledgev1.Node, []string) {
	byType := stats.GetNodesByType()
	samples := make(map[kgtypes.NodeType][]*knowledgev1.Node, len(byType))
	var failed []string
	for nt := range byType {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeType: nt},
				Limit:     2,
				SkipTotal: true,
			}},
			Target: resourceTarget(graph, account),
		})
		if err != nil {
			failed = append(failed, nt)
			continue
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil {
			failed = append(failed, nt)
			continue
		}
		if len(nodes) == 0 {
			continue // no rows of this type: an ordinary answer, not a fault.
		}
		samples[kgtypes.NodeType(nt)] = nodes
	}
	sort.Strings(failed)
	return samples, failed
}

// sampleFailureNotice names the node types whose sample read failed, so a stats
// render showing fewer sample sections than the graph has types says WHICH are
// missing rather than leaving the reader to infer emptiness. An empty list
// renders "", which appendNotice treats as a no-op.
func sampleFailureNotice(types []string) string {
	if len(types) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"_Sample names could not be read for %d node type(s): %s. Those sections are MISSING rather "+
			"than empty — re-run to retry._",
		len(types), strings.Join(types, ", "))
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

// ListGraphNamesOfType is the exported cross-package seam over listGraphNamesOfType
// (the same RETURN_MODE_GRAPH_NAMES enumeration the status coverage table uses):
// the bootstrap segment-coverage reconcile enumerates code repos through it so it
// probes exactly the graph set manage(status) reports. Empty names are dropped.
func ListGraphNamesOfType(ctx context.Context, deps ClientDeps, graphType string) ([]string, error) {
	return listGraphNamesOfType(ctx, deps, graphType)
}

// listOverlayKeysOfBase enumerates the OVERLAY keys of a single base graph via
// the same Execute seam (RETURN_MODE_GRAPH_NAMES) with overlay_of set to the base.
//
// THE RETURNED NAME FORM IS BACKEND-DEPENDENT: the returned GraphInfo.Name values
// are the FULL "base@overlay" key on the CLOUD backend and the BARE overlay name on the OSS/local backend, whose registry_lookup.go listOverlays sets gi.Name to the overlay with the base prefix already stripped.
// CALLERS MUST THEREFORE NORMALIZE WITH bareOverlayName rather than assuming
// either form — assuming the composed one drops every OSS overlay, assuming the
// bare one composes a doubled base prefix.
//
// What holds on BOTH backends: this returns ONLY the overlay keys — NOT the base
// graph itself and NOT every graph (the base name comes from the separate
// listGraphNamesOfType enumeration) — and empty names are dropped. It is a
// distinct call shape from listGraphNamesOfType (which returns base names and
// filters @-keys), hence a sibling rather than an extension.
func listOverlayKeysOfBase(ctx context.Context, deps ClientDeps, graphType, base string) ([]string, error) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, fmt.Errorf("graph client unavailable")
	}
	infos, err := fetchGraphNamesOfType(ctx, gc, graphType, base)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, gi := range infos {
		if gi.Name != "" {
			keys = append(keys, gi.Name)
		}
	}
	return keys, nil
}
