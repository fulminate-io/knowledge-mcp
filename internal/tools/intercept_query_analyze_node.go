// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_analyze_node.go is the client-side claim for
// query(graph:code, id:...) — the codegraph analyze surface. The codegraph
// package relocates client-side as a violation-to-MOVE (ASTs + analysis are
// client-only); analyze is a generic graph walk (traverse CALLS) + a client
// render, no code-specific server logic.
//
// Recipe (direct map of the server HandleAnalyzeNode,
// cmd/knowledge-server/internal/codegraph/analyze.go): (1) Execute ByID for the
// subject; (2) traverse(CALLS, in, caller_depth) for callers; (3) traverse(CALLS,
// out, callee_depth) for callees; render via engine.RenderAnalyzeNode. Depths
// clamp 1..3, include_source defaults true — matching the server.

// analyzeNodeArgs is the analyze view of the query args. The query(graph:code,
// id) surface maps id→node_id; caller_depth/callee_depth/include_source ride
// through when supplied (server routeCodeTarget maps only node_id+repo, so via
// that surface the depths take their defaults — but accept them here for parity).
type analyzeNodeArgs struct {
	Graph         string  `json:"graph"`
	ID            string  `json:"id"`
	Repo          string  `json:"repo"`
	Branch        string  `json:"branch"`
	Mode          string  `json:"mode"`
	CallerDepth   flexInt `json:"caller_depth"`
	CalleeDepth   flexInt `json:"callee_depth"`
	IncludeSource *bool   `json:"include_source"`
}

// InterceptQueryAnalyzeNode claims query(graph:code) with id set and mode not
// stats (the server routeCodeTarget fast-path shape). Returns (false,_) otherwise.
func InterceptQueryAnalyzeNode(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a analyzeNodeArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Graph != "code" || a.ID == "" || a.Mode == "stats" {
		return false, kgtools.ToolResult{}
	}
	if err := accountQueryParams(armAnalyzeNode, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("analyze: graph client unavailable")
	}
	return true, composeAnalyzeNode(ctx, gc.Execute, a)
}

// composeAnalyzeNode runs the ByID + two CALLS traversals and renders.
func composeAnalyzeNode(ctx context.Context, exec engine.ExecuteFn, a analyzeNodeArgs) kgtools.ToolResult {
	callerDepth := clampDepth(int(a.CallerDepth))
	calleeDepth := clampDepth(int(a.CalleeDepth))
	includeSource := true
	if a.IncludeSource != nil {
		includeSource = *a.IncludeSource
	}
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: a.Repo, Branch: a.Branch}

	// (1) Subject ByID.
	subjResp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: a.ID}},
		Target: target,
	})
	if err != nil {
		return errorResult("node not found: " + err.Error())
	}
	subjNodes, derr := engine.DecodeNodes(subjResp)
	if derr != nil || len(subjNodes) == 0 {
		return errorResult("node not found: " + a.ID)
	}

	// (2) Callers — traverse(CALLS, in, caller_depth).
	callers, cerr := traverseCallNodes(ctx, exec, target, a.ID, false, callerDepth)
	if cerr != nil {
		return errorResult("analyze callers: " + cerr.Error())
	}
	// (3) Callees — traverse(CALLS, out, callee_depth).
	callees, eerr := traverseCallNodes(ctx, exec, target, a.ID, true, calleeDepth)
	if eerr != nil {
		return errorResult("analyze callees: " + eerr.Error())
	}

	repoLabel := repoLabelFor(a.Repo, a.Branch)
	return textResult(engine.RenderAnalyzeNode(repoLabel, subjNodes[0], callers, callees, includeSource))
}

// traverseCallNodes issues ONE RETURN_MODE_TRAVERSAL Execute over the CALLS edge
// in the given direction (forward=out=callees, !forward=in=callers) and returns
// the traversed nodes (the start node is NOT in the result — BFS yields distance
// ≥1 only).
func traverseCallNodes(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, id string, forward bool, depth int) ([]*knowledgev1.Node, error) {
	fwd := forward
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection:  &knowledgev1.Selection{FromId: []string{id}, EdgeTypes: []string{string(kgtypes.EdgeCalls)}},
			Forward:    &fwd,
			MaxHops:    int32(depth),
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL,
		}},
		Target: target,
	})
	if err != nil {
		return nil, err
	}
	results, derr := engine.DecodeTraversal(resp)
	if derr != nil {
		return nil, derr
	}
	nodes := make([]*knowledgev1.Node, 0, len(results))
	for _, r := range results {
		nodes = append(nodes, r.Node)
	}
	return nodes, nil
}

// clampDepth applies the server's 1..3 clamp (analyze.go: <=0 → 1, >3 → 3).
func clampDepth(d int) int {
	if d <= 0 {
		return 1
	}
	if d > 3 {
		return 3
	}
	return d
}

// repoLabelFor mirrors ResolvedGraph.RepoLabel: "repo" or "repo@branch".
func repoLabelFor(repo, branch string) string {
	if branch != "" {
		return repo + "@" + branch
	}
	return repo
}
