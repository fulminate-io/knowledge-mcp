// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// handleClientCrossGraphLink implements the CLIENT-COMPOSABLE half of the
// server handleCrossGraphLink decision tree (tools_mutate_link.go), now covering
// TWO branches:
//
//   - INTRA-PRACTICE direct link: when BOTH from and to resolve in the same
//     practice/<language> graph, it issues one LINK MutationPlan targeting that
//     practice graph via the Execute seam (the engine routes cross-graph for
//     free). The authoring path for a pattern tree that lives entirely inside
//     one practice graph (pattern → use_case / example).
//   - KNOWLEDGE-FROM to PRACTICE-TO proxy: when the FROM resolves in the
//     KNOWLEDGE graph and the TO resolves in practice/<language>, it materializes
//     the deterministic practice proxy (store.BuildCrossGraphProxy) and writes
//     it plus the from->proxy edge into the knowledge graph via the Execute
//     seam: the proxy UPSERT rides the type-blind engine MUTATION_KIND_UPSERT arm
//     (NOT the legacy handleMutateUpsert, which blocks type=proxy via
//     ValidateSystemManagedType), and the LINK rides MUTATION_KIND_LINK.
//
// FROM-GRAPH GUARD (dangling-edge fix): the proxy branch claims ONLY when a.From
// resolves in KNOWLEDGE. A code/cloud/cicd FROM (e.g. a documented
// code→pattern `uses` edge) is "not in practice" too, but the server resolves
// such a FROM through h.Code.ResolveOrProxy (auto-creating a CODE proxy) — out of
// scope for this ticket. Firing the proxy branch on a code FROM would emit
// link(raw-code-id → practice-proxy) into knowledge, where the raw code id has no
// node → a DANGLING edge. So a non-knowledge / unresolved FROM falls through to
// the LIVE legacy handleCrossGraphLink, which owns the ResolveOrProxy path.
//
// It returns (false, _) — fall through to legacy — for every shape it cannot
// fully handle: link_graph, the FROM-in-practice/TO-elsewhere direction (the
// symmetric ResolveOrProxy-on-FROM case), and a non-knowledge/unresolved FROM.
// The server's best-effort RefreshProxyKeywords is INTENTIONALLY omitted — it
// operates on the linkage graph and is not needed for link correctness (finding
// 48bbf0c8); the interactive proxy lands in knowledge.
func handleClientCrossGraphLink(ctx context.Context, deps ClientDeps, a mutateArgs) (bool, kgtools.ToolResult) {
	if a.From == "" || a.To == "" || a.Relationship == "" {
		// Missing a required field — let the legacy handler surface its own
		// "from/to/relationship are required" validation error.
		return false, kgtools.ToolResult{}
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return false, kgtools.ToolResult{}
	}
	ex, err := persistExecutor(gc)
	if err != nil {
		// No Execute seam — fall through to legacy rather than fail hard.
		return false, kgtools.ToolResult{} //nolint:nilerr // missing seam → legacy path
	}

	// link_graph:"linkage" (e.g. the interactive mutate(link, link_graph:linkage)):
	// the client OWNS this path via crossgraph.ResolveAndLink targeting the LINKAGE
	// graph, carrying the edge metadata — no server-side ResolveOrProxy. A
	// link_graph value OTHER than "linkage" (none exists today; defensive) falls
	// through to legacy.
	if a.LinkGraph != "" {
		if a.LinkGraph != "linkage" {
			return false, kgtools.ToolResult{}
		}
		handled, res, lerr := crossgraph.ResolveAndLink(ctx, gc, ex, crossgraph.LinkRequest{
			From: a.From, To: a.To, Relationship: a.Relationship,
			Graph: a.Graph, Language: a.Language, TargetGraph: "linkage",
			Weight: a.Weight, Confidence: a.Confidence, Method: a.Method,
			Evidence: a.EdgeEvidence, LastValidated: a.LastValidated,
		})
		if lerr != nil {
			return true, errorResult("link_graph:linkage: " + lerr.Error())
		}
		return handled, res
	}

	// FROM-FIRST endpoint probe with short-circuit. NO foreign-graph enumeration
	// (crossgraph.ResolveAndLink's per-type RETURN_MODE_GRAPH_NAMES reads) is
	// issued until cross-graph is confirmed.
	//
	//  - FROM not in knowledge → it is a FOREIGN FROM → cross-graph link → proceed
	//    to the generic resolve WITHOUT a second a.To-in-knowledge probe (the
	//    FROM-only short-circuit: a foreign FROM is decided on ONE probe).
	//  - FROM in knowledge → probe a.To in knowledge. If BOTH resolve in knowledge
	//    this is a knowledge↔knowledge link with no cross-graph aspect → return
	//    (false,_) BEFORE any foreign-graph enumeration so it stays on the server
	//    bare-link path. Worst case: 2 ByID probes (FROM then TO), zero list read.
	fromInKnowledge := false
	if knownFrom, ferr := render.FetchNodeIn(ctx, gc, a.From, "knowledge", ""); ferr == nil && knownFrom != nil && knownFrom.Id != "" {
		fromInKnowledge = true
	}
	if fromInKnowledge {
		if knownTO, terr := render.FetchNodeIn(ctx, gc, a.To, "knowledge", ""); terr == nil && knownTO != nil && knownTO.Id != "" {
			// Both endpoints in knowledge → no cross-graph aspect → server bare-link.
			return false, kgtools.ToolResult{}
		}
	}

	// Cross-graph confirmed. Run the slug-less→slug-ful practice-proxy migration
	// once per session (lazy-on-first-cross-graph-link, mirroring RepoResolver's
	// lazy once). Best-effort: never blocks the link being composed.
	migratePracticeProxiesOnce(ctx, gc)

	// INTRA-PRACTICE fast path: a pattern tree living entirely in one practice
	// graph (e.g. pattern → use_case / example). When the caller named the
	// practice graph (graph:practice, language set) AND both endpoints resolve in
	// practice/<language>, issue a single direct in-practice LINK with no proxy —
	// no foreign-graph enumeration needed.
	if a.Graph == "practice" && a.Language != "" {
		fromNode, ferr := render.FetchNodeIn(ctx, gc, a.From, "practice", a.Language)
		toNode, terr := render.FetchNodeIn(ctx, gc, a.To, "practice", a.Language)
		if ferr == nil && terr == nil && fromNode != nil && toNode != nil && fromNode.Id != "" && toNode.Id != "" {
			// Practice edges are lowercase (CanonicalizeEdgeTypeForGraph("practice",
			// …) == strings.ToLower).
			rel := strings.ToLower(a.Relationship)
			plan := &knowledgev1.MutationPlan{
				Kind:      knowledgev1.MutationPlan_MUTATION_KIND_LINK,
				Selection: &knowledgev1.Selection{Ids: []string{a.From}},
				EdgeSpec: &knowledgev1.EdgeSpec{
					Relationship: rel,
					ToId:         a.To,
					Forward:      true,
				},
			}
			if _, eerr := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
				Plan:   &knowledgev1.ExecuteRequest_Mutation{Mutation: plan},
				Target: &knowledgev1.GraphSelector{Graph: "practice", Language: a.Language},
			}); eerr != nil {
				return true, errorResult("intra-practice link failed: " + eerr.Error())
			}
			return true, textResult(fmt.Sprintf("Linked in practice/%s: %s -[%s]-> %s", a.Language, a.From, rel, a.To))
		}
	}

	// GENERIC cross-graph: delegate to the single-owner crossgraph composer
	// targeting the KNOWLEDGE graph (no edge metadata for the interactive path).
	// It enumerates the foreign graphs once, resolves FROM (knowledge raw-id or
	// foreign-proxy id), materializes the TO proxy, and links from→to in knowledge.
	// An unresolvable FROM → (false,_) legacy (the dangling-edge guard).
	handled, res, gerr := crossgraph.ResolveAndLink(ctx, gc, ex, crossgraph.LinkRequest{
		From: a.From, To: a.To, Relationship: a.Relationship,
		Graph: a.Graph, Language: a.Language, TargetGraph: "knowledge",
	})
	if gerr != nil {
		return true, errorResult("cross-graph link: " + gerr.Error())
	}
	return handled, res
}
