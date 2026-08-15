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
	"github.com/fulminate-io/knowledge-mcp/internal/paging"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_modules_codestats.go is the client-side claim for
// query(graph:code, mode:modules) (list_modules) and query(graph:code,
// mode:stats) (code stats). Ports the server HandleListModules /
// ListModulesOnGraph (codegraph/tools.go) and the code-stats path
// (tools_query.go routeCodeTarget → handleCodeStats).

// modulesCodeStatsArgs reads the modules + code-stats query args.
type modulesCodeStatsArgs struct {
	Graph      string   `json:"graph"`
	Mode       string   `json:"mode"`
	Repo       string   `json:"repo"`
	Repos      []string `json:"repos"`
	Branch     string   `json:"branch"`
	PathPrefix string   `json:"path_prefix"`
	Samples    bool     `json:"samples"`
	Format     string   `json:"format"`
}

// InterceptQueryModulesCodeStats claims query(graph:code, mode in
// {modules,stats}).
func InterceptQueryModulesCodeStats(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a modulesCodeStatsArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Graph != "code" || (a.Mode != "modules" && a.Mode != "stats") {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult(a.Mode + ": graph client unavailable")
	}
	if a.Mode == "modules" {
		if err := accountQueryParams(armCodeModules, params.Arguments); err != nil {
			return true, errorResult(err.Error())
		}
		return true, composeListModules(ctx, deps, gc.Execute, a)
	}
	if err := accountQueryParams(armCodeStats, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	sc, ok := gc.(statsRPC)
	if !ok {
		return true, errorResult(a.Mode + ": stats seam unavailable")
	}
	return true, composeCodeStats(ctx, sc, a)
}

// composeListModules dispatches single-repo vs multi-repo (repos[] / repo=all)
// list_modules. Each repo: Match(package) + Match(file) + client rollup.
func composeListModules(ctx context.Context, deps ClientDeps, exec engine.ExecuteFn, a modulesCodeStatsArgs) kgtools.ToolResult {
	if len(a.Repos) > 0 || a.Repo == "all" {
		return composeListModulesMultiRepo(ctx, deps, exec, a)
	}
	body := listModulesForRepo(ctx, exec, a.Repo, a.PathPrefix)
	if body == "" {
		return textResult(engine.FormatCodeWithRepo(a.Repo, "No modules found."))
	}
	return textResult(engine.FormatCodeWithRepo(a.Repo, body))
}

// composeListModulesMultiRepo resolves the repo set (repos[] or repo=all → all
// loaded code graphs) and renders per-repo module listings.
func composeListModulesMultiRepo(ctx context.Context, deps ClientDeps, exec engine.ExecuteFn, a modulesCodeStatsArgs) kgtools.ToolResult {
	repos := a.Repos
	if len(repos) == 0 {
		names, err := listGraphNamesOfType(ctx, deps, "code")
		if err != nil {
			return errorResult("resolve repos: " + err.Error())
		}
		repos = names
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Modules across %d repos\n\n", len(repos))
	for _, repo := range repos {
		fmt.Fprintf(&sb, "## [%s]\n\n", repo)
		body := listModulesForRepo(ctx, exec, repo, a.PathPrefix)
		if body == "" {
			sb.WriteString("No modules found.\n\n")
		} else {
			sb.WriteString(body)
			sb.WriteString("\n")
		}
	}
	return textResult(sb.String())
}

// listModulesForRepo drains the package and file browses for one repo graph in
// bounded keyset pages and renders via engine.RenderListModules (in-memory
// rollup, no N+1).
//
// The two arms use DIFFERENT carriers because the rollup consumes them
// asymmetrically: packages are hydrated because the listing renders each one's
// Summary, while files are enumerated as bare ids because the rollup reads only
// their path. The ids ARE the paths — the collector builds every file node with
// Id == FilePath (parser/populate.go).
func listModulesForRepo(ctx context.Context, exec engine.ExecuteFn, repo, pathPrefix string) string {
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: repo}
	packages := drainNodesOfType(ctx, exec, target, kgtypes.NodePackage)
	filePaths := drainIDsOfType(ctx, exec, target, kgtypes.NodeFile)
	return engine.RenderListModules(packages, filePaths, pathPrefix)
}

// typeBrowsePage builds one bounded keyset page of a type browse. AfterId is SET
// on every page including the first, where the value is empty: its PRESENCE is
// what selects the keyset browse, and an omitted cursor would page in the
// backend's default order so the cursor taken from page 1 skips every lower id.
func typeBrowsePage(nt kgtypes.NodeType, cursor *string, mode knowledgev1.ReturnMode) *knowledgev1.QueryPlan {
	return &knowledgev1.QueryPlan{
		Selection:  &knowledgev1.Selection{NodeType: string(nt)},
		ReturnMode: mode,
		Limit:      int32(paging.BrowsePageSize),
		AfterId:    cursor,
		SkipTotal:  true, // the drain consumes only the payload, never Total
	}
}

// drainNodesOfType drains every hydrated node of nt in bounded keyset pages.
func drainNodesOfType(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, nt kgtypes.NodeType) []*knowledgev1.Node {
	nodes, err := paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		resp, rerr := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: typeBrowsePage(nt, &cursor, knowledgev1.ReturnMode_RETURN_MODE_UNSPECIFIED)},
			Target: target,
		})
		if rerr != nil {
			return nil, rerr
		}
		return engine.DecodeNodes(resp)
	}, paging.BrowsePageSize)
	if err != nil {
		return nil
	}
	return nodes
}

// drainIDsOfType drains every node id of nt in bounded keyset pages.
func drainIDsOfType(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, nt kgtypes.NodeType) []string {
	ids, err := paging.DrainKeysetIDs(func(afterID string) ([]string, error) {
		cursor := afterID
		resp, rerr := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: typeBrowsePage(nt, &cursor, knowledgev1.ReturnMode_RETURN_MODE_IDS)},
			Target: target,
		})
		if rerr != nil {
			return nil, rerr
		}
		return resp.GetIds(), nil
	}, paging.BrowsePageSize)
	if err != nil {
		return nil
	}
	return ids
}

// composeCodeStats renders the code graph stats via the Stats RPC +
// RenderStatsBreakdown (Phase 1) with a per-repo header.
func composeCodeStats(ctx context.Context, gc statsRPC, a modulesCodeStatsArgs) kgtools.ToolResult {
	resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{Target: &knowledgev1.GraphSelector{Graph: "code", Repo: a.Repo, Branch: a.Branch}})
	if err != nil {
		return errorResult(fmt.Sprintf("code %q graph stats failed: %s", a.Repo, err.Error()))
	}
	stats := resp.GetGraphStats()
	if a.Format == "json" {
		return jsonResult(map[string]any{
			"graph":               "code",
			"repo":                a.Repo,
			"node_count":          stats.GetNodeCount(),
			"edge_count":          stats.GetEdgeCount(),
			"binary_vector_count": stats.GetBinaryVectorCount(),
			"nodes_by_type":       stats.GetNodesByType(),
			"edges_by_type":       stats.GetEdgesByType(),
		})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Code Graph: %s\n\n", repoLabelFor(a.Repo, a.Branch))
	sb.WriteString(engine.RenderStatsBreakdown(stats))
	if a.Samples {
		samples := fetchCodeTypeSamples(ctx, gc.Execute, a.Repo, stats)
		engine.RenderSampleNames(&sb, stats, samples)
	}
	return textResult(sb.String())
}

// fetchCodeTypeSamples fetches up to 2 sample nodes per node type for the code
// stats sample enrichment (bounded by node-type count). Uses the code Repo
// instance key (NOT account).
func fetchCodeTypeSamples(ctx context.Context, exec engine.ExecuteFn, repo string, stats *knowledgev1.GraphStats) map[kgtypes.NodeType][]*knowledgev1.Node {
	byType := stats.GetNodesByType()
	samples := make(map[kgtypes.NodeType][]*knowledgev1.Node, len(byType))
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: repo}
	for nt := range byType {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeType: nt},
				Limit:     2,
				SkipTotal: true,
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
