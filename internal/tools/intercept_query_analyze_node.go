// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"

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
// Recipe (grown from the server HandleAnalyzeNode,
// cmd/knowledge-server/internal/codegraph/analyze.go): (1) Execute ByID for the
// subject; (2) traverse(CALLS, in, caller_depth) for callers; (3) traverse(CALLS,
// out, callee_depth) for callees; (4) the same two walks over TEST_CALLS for the
// call traffic whose source is test code; render via engine.RenderAnalyzeNode.
// Depths clamp 1..3, include_source defaults true — matching the server.
//
// The TEST_CALLS walks are the one place this arm goes beyond the server recipe.
// They are SEPARATE walks per edge type rather than one walk requesting both,
// because each side's group reconstruction reads its whole edge slice: a mixed
// slice would let a test-side candidate group suppress a production caller
// through the frontier short-circuit, and at depth above 1 a mixed walk yields
// mixed paths with no per-node attribution to sort them back out.

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
	callers, callerEdges, callersClamped, cerr := traverseCallNodes(ctx, exec, target, a.ID, kgtypes.EdgeCalls, false, callerDepth)
	if cerr != nil {
		return errorResult("analyze callers: " + cerr.Error())
	}
	// (3) Callees — traverse(CALLS, out, callee_depth).
	callees, calleeEdges, calleesClamped, eerr := traverseCallNodes(ctx, exec, target, a.ID, kgtypes.EdgeCalls, true, calleeDepth)
	if eerr != nil {
		return errorResult("analyze callees: " + eerr.Error())
	}
	// (4) The same two walks over TEST_CALLS. A failure here is an error on the
	// same terms as the production walks: rendering an empty test section after a
	// failed walk would be indistinguishable from a symbol that genuinely has no
	// test callers, which is the silent exclusion this opt-in exists to remove.
	testCallers, testCallerEdges, testCallersClamped, tcerr := traverseCallNodes(ctx, exec, target, a.ID, kgtypes.EdgeTestCalls, false, callerDepth)
	if tcerr != nil {
		return errorResult("analyze test callers: " + tcerr.Error())
	}
	testCallees, testCalleeEdges, testCalleesClamped, tecerr := traverseCallNodes(ctx, exec, target, a.ID, kgtypes.EdgeTestCalls, true, calleeDepth)
	if tecerr != nil {
		return errorResult("analyze test callees: " + tecerr.Error())
	}
	// ANY of the four walks hitting the ceiling makes the whole view partial:
	// the sections are rendered as one call graph, so a clamp on one side is not
	// disclosed by the other three coming back whole.
	walkClamped := callersClamped || calleesClamped || testCallersClamped || testCalleesClamped

	// (5) Reconstruct groups per side and hydrate every candidate. Unlike the
	// traverse arm, analyze ALWAYS enriches: its candidates are not guaranteed to
	// appear in its own node slices, so it needs the hydrate even when every group
	// is already complete.
	callerSide := analyzeGroupSide(ctx, exec, target, a.ID, callers, callerEdges)
	calleeSide := analyzeGroupSide(ctx, exec, target, a.ID, callees, calleeEdges)
	testCallerSide := analyzeGroupSide(ctx, exec, target, a.ID, testCallers, testCallerEdges)
	testCalleeSide := analyzeGroupSide(ctx, exec, target, a.ID, testCallees, testCalleeEdges)

	candidates := map[string]*knowledgev1.Node{}
	reached := map[string]bool{}
	incomplete := false
	for _, side := range []analyzeSide{callerSide, calleeSide, testCallerSide, testCalleeSide} {
		maps.Copy(candidates, side.candidates)
		for id := range side.reached {
			reached[id] = true
		}
		incomplete = incomplete || side.incomplete
	}

	// This arm bypasses engine.Render, so it discloses a clamped walk for itself.
	// Incomplete on AnalyzeView answers a NARROWER question — whether group
	// reconstruction was partial — and is not a substitute.
	return engine.WithTruncationNoticeFor(textResult(engine.RenderAnalyzeNode(engine.AnalyzeView{
		RepoLabel:        repoLabelFor(a.Repo, a.Branch),
		Subject:          subjNodes[0],
		Callers:          callerSide.plain,
		Callees:          calleeSide.plain,
		CallerGroups:     callerSide.groups,
		CalleeGroups:     calleeSide.groups,
		TestCallers:      testCallerSide.plain,
		TestCallees:      testCalleeSide.plain,
		TestCallerGroups: testCallerSide.groups,
		TestCalleeGroups: testCalleeSide.groups,
		Candidates:       candidates,
		Reached:          reached,
		IncludeSource:    includeSource,
		Incomplete:       incomplete,
	})), walkClamped, len(callers)+len(callees)+len(testCallers)+len(testCallees))
}

// analyzeSide is one direction's resolved view: the plain entries the section
// lists, the groups it renders as blocks, and the candidate hydrate they read.
type analyzeSide struct {
	plain      []*knowledgev1.Node
	groups     []engine.CandidateGroup
	candidates map[string]*knowledgev1.Node
	reached    map[string]bool
	incomplete bool
}

// analyzeGroupSide reconstructs one direction's groups, applies the frontier
// short-circuit, and removes every candidate from the plain node list.
//
// THE REMOVAL IS WHAT KEEPS THE SECTION COUNT HONEST: a candidate is rendered
// inside its group block, so listing it as a plain caller too would report one
// ambiguous reference as N callers AND as one group of N — the exact "N
// alternatives read as N facts" defect this ticket removes.
func analyzeGroupSide(
	ctx context.Context,
	exec engine.ExecuteFn,
	target *knowledgev1.GraphSelector,
	startID string,
	nodes []*knowledgev1.Node,
	edges []knowledgev1.Edge,
) analyzeSide {
	groups, _ := engine.GroupCandidateEdges(edges)
	if len(groups) == 0 {
		return analyzeSide{plain: nodes}
	}

	// The frontier rule applies here too: with caller_depth/callee_depth above 1,
	// a node reachable only THROUGH a group must not be listed as a plain entry.
	results := make([]engine.TraversalResult, 0, len(nodes))
	for _, n := range nodes {
		results = append(results, engine.TraversalResult{Node: n, Distance: 1})
	}
	kept, reached, incomplete := engine.FrontierFilter(startID, results, edges, groups)

	// Candidate facts come from TWO sources, in this order. (1) The traversal
	// results already in hand, which hold the candidate nodes whenever the walk
	// reached them — free, and the common case. (2) The enrichment hydrate, which
	// runs only when a group was observed incomplete, which is exactly the case
	// where a candidate was NOT in the walk. Enrichment overlays the walk.
	//
	// Sourcing (1) locally is what lets EnrichCandidateGroups keep its zero-read
	// early exit for complete groups: forcing a hydrate there would add an Execute
	// to every forward traversal in the product for facts already decoded.
	candidates := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		if n != nil {
			candidates[n.Id] = n
		}
	}
	enriched, hydrated, err := engine.EnrichCandidateGroups(ctx, exec, groups, target)
	if err != nil {
		// Best-effort: render what is known and say so, rather than turning a
		// successful analyze into an error because an enrichment nicety failed.
		slog.Warn("analyze: candidate enrichment failed; rendering the observed members only", "error", err)
		incomplete = true
	}
	// THE PARTIALS ARE USED ON BOTH PATHS, and that is the contract rather than a
	// nicety. EnrichCandidateGroups is documented best-effort and returns whatever
	// it DID complete alongside its error — most visibly on the clamped-hydrate
	// path, where it hands back the enriched groups and every candidate the server
	// did return. Taking them only on the success path threw that work away on
	// exactly the path that produces it, so a clamped hydrate lost the names of
	// candidates that came back fine. On the read-failure paths enriched is the
	// unenriched input and hydrated is nil, so both statements are no-ops there.
	groups = enriched
	maps.Copy(candidates, hydrated)

	isCandidate := map[string]bool{}
	for gi := range groups {
		for mi := range groups[gi].Members {
			isCandidate[groups[gi].Members[mi].ToId] = true
		}
	}
	plain := make([]*knowledgev1.Node, 0, len(kept))
	for _, r := range kept {
		if r.Node == nil || isCandidate[r.Node.Id] {
			continue
		}
		plain = append(plain, r.Node)
	}
	return analyzeSide{plain: plain, groups: groups, candidates: candidates, reached: reached, incomplete: incomplete}
}

// traverseCallNodes issues ONE RETURN_MODE_TRAVERSAL Execute over the given call
// edge type (EdgeCalls or EdgeTestCalls) in the given direction (forward=out=
// callees, !forward=in=callers) and returns the traversed nodes (the start node
// is NOT in the result — BFS yields distance ≥1 only) TOGETHER WITH the walked
// edges. The edge type travels in the existing Selection — no wire change.
//
// IncludeEdgeMetadata is set unconditionally: without the per-edge Method the
// composer cannot tell a multi-candidate group from N independent callers, and
// this arm previously requested no edges at all — which is why a group rendered
// here as N separate callers and no render-only change could fix it.
//
// The third return is the response's truncated flag, threaded verbatim from
// resp.GetTruncated() and never derived from len(). This walk carries NO Limit,
// so the server row ceiling engages at 10,000 traversal rows, and the arm
// bypasses engine.Render — so without this thread a clamped walk renders as a
// complete-looking call graph with callers silently missing.
func traverseCallNodes(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, id string, edgeType kgtypes.EdgeType, forward bool, depth int) ([]*knowledgev1.Node, []knowledgev1.Edge, bool, error) {
	fwd := forward
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection:           &knowledgev1.Selection{FromId: []string{id}, EdgeTypes: []string{string(edgeType)}},
			Forward:             &fwd,
			MaxHops:             int32(depth),
			ReturnMode:          knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL,
			IncludeEdgeMetadata: true,
		}},
		Target: target,
	})
	if err != nil {
		return nil, nil, false, err
	}
	results, derr := engine.DecodeTraversal(resp)
	if derr != nil {
		return nil, nil, false, derr
	}
	nodes := make([]*knowledgev1.Node, 0, len(results))
	for _, r := range results {
		nodes = append(nodes, r.Node)
	}
	return nodes, engine.EdgesFromProto(resp.GetTraversalEdges()), resp.GetTruncated(), nil
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
