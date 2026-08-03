// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
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
//     seam: the proxy UPSERT rides the engine MUTATION_KIND_UPSERT arm, on whose
//     type allowlist `proxy` sits — so the body bypasses create-time validation,
//     which is what keeps this path working (the create-time system-managed-type
//     guard rejects type=proxy outright). The LINK rides MUTATION_KIND_LINK.
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
// operates on the linkage graph and is not needed for link correctness; the
// interactive proxy lands in knowledge.
// gateCrossGraphLink runs the cross-graph composer's param accounting. It is
// the arm's SINGLE gate call site, deliberately invoked from each point where
// the composer has COMMITTED to claiming the call rather than once at the top:
// this arm's surface is narrower than the engine LINK arm's, so gating before
// the claim decision would reject shapes the composer never handles and the
// engine routes correctly.
func gateCrossGraphLink(a mutateArgs) error {
	return accountMutateParams(armLinkCrossGraph, a)
}

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
	// graph, carrying the edge metadata — no server-side ResolveOrProxy.
	//
	// DECLINING A link_graph SHAPE IS NOW A REJECT, NOT A FALL-THROUGH. Any
	// link_graph call this composer does not claim — a value other than
	// "linkage" (none exists today; defensive), or a linkage call whose
	// ResolveAndLink declines — reaches the engine LINK arm's accounting, which
	// rejects link_graph because the engine provably cannot route it
	// (engine/compile_mutate.go returns (nil,false) for any non-empty
	// LinkGraph). The decline is therefore terminal rather than a hand-off.
	// Note the transient case: ResolveAndLink declines on a foreign-graph
	// ENUMERATION failure, so an infra blip surfaces as that rejection and the
	// same call can succeed on retry — which is why the rejection carries a
	// retry hint rather than the generic not-routed wording.
	if a.LinkGraph != "" {
		if a.LinkGraph != "linkage" {
			return false, kgtools.ToolResult{}
		}
		if err := gateCrossGraphLink(a); err != nil {
			return true, errorResult(err.Error())
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
	//    (false,_) BEFORE any foreign-graph enumeration. The declined call then
	//    routes through the cloud-aware engine dispatch (generic
	//    MUTATION_KIND_LINK Execute), not a local server. Worst case: 2 ByID
	//    probes (FROM then TO), zero list read.
	fromInKnowledge := false
	if knownFrom, ferr := render.FetchNodeIn(ctx, gc, a.From, "knowledge", ""); ferr == nil && knownFrom != nil && knownFrom.Id != "" {
		fromInKnowledge = true
	}
	if fromInKnowledge {
		if knownTO, terr := render.FetchNodeIn(ctx, gc, a.To, "knowledge", ""); terr == nil && knownTO != nil && knownTO.Id != "" {
			// Both endpoints in knowledge → no cross-graph aspect → return
			// (false,_); the bare link is handled by the cloud-aware engine
			// dispatch (generic MUTATION_KIND_LINK Execute), not a local server.
			return false, kgtools.ToolResult{}
		}
	}

	// Cross-graph confirmed — the composer OWNS this call from here, so this is
	// where its param accounting belongs. Everything above is a decline path
	// that must reach the engine LINK arm with its own (wider) surface.
	if err := gateCrossGraphLink(a); err != nil {
		return true, errorResult(err.Error())
	}

	// Run the slug-less→slug-ful practice-proxy migration
	// once per session (lazy-on-first-cross-graph-link, mirroring RepoResolver's
	// lazy-on-first-use shape). Best-effort: never blocks the link being composed.
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
