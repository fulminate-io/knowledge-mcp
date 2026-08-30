// SPDX-License-Identifier: Apache-2.0

// The client-side MCP intercept chain.
//
// runInterceptChain is what the serve daemon wires as InterceptManageOp, so
// every upstream MCP tool call traverses it. A tool claimed client-side is
// served here; anything unclaimed falls through to the server.
//
// The chain's ORDER is load-bearing and each ordering constraint is commented at
// its own call site — a rewriter that must precede its consumers, a criterion
// gate that must precede the generic mutate fall-through, a terminal claim that
// must run last.

package bootstrap

import (
	"context"
	"slices"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// runInterceptChain dispatches an incoming MCP tool call through the
// client-side interceptor chain the serve daemon wires for upstream traffic:
// daemon.go hands it to NewMCPClient as InterceptManageOp, so every upstream
// MCP tool call from the host traverses the chain.
//
// Returning (false, _) means no interceptor handled the call; the caller
// then falls back to forwarding to the server (NewMCPClient does this).
//
// ctx is the CALLER's request context: the query-origin operation stamped at the
// dispatch entry point, plus the HTTP daemon's per-session values (session id,
// workspace cwd). It is threaded into EVERY intercept — each issues its RPCs on
// it, so canceling the tool call stops the work it started.
//
// Being the single funnel for the dispatch entry point, this is also where the client
// samples account activity: after the chain runs it checks the response
// watermark and, on movement, wakes the LLM pipeline (see client_freshness.go).
// That is what makes pipeline freshness activity-driven instead of periodic.
func (c *client) runInterceptChain(ctx context.Context, params kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult) {
	start := time.Now()
	rewritten, handled, res := c.runInterceptChainInner(ctx, params)
	// Watermark-triggered pipeline freshness: this call's RPCs (if any) have
	// already returned, so their responses' account watermark is observable now.
	// Runs on BOTH the handled and the fall-through path — a read that changed
	// nothing still tells us whether SOMETHING in the account moved. A call that
	// falls through issues its RPC AFTER this point, so its watermark is seen by
	// the next call; that one-call lag is the price of a single hook site.
	c.checkPipelineFreshness(ctx)
	if handled {
		// Always-on latency footer for client-side intercepts so LLM
		// callers see consistent timing alongside server-side tool
		// results (which append their own footer in HandleToolsCall).
		// Calls that fall through to the server skip this footer because
		// the server will append its own with handler-only timing.
		res = appendClientDurationFooter(res, time.Since(start))
	}
	return rewritten, handled, res
}

// runInterceptChainInner is the actual chain — kept separate so the outer
// function can wrap with the timing footer in one place. ctx is threaded into
// every intercept AND the three cluster helpers, reaching the leaves they dispatch.
func (c *client) runInterceptChainInner(ctx context.Context, params kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult) {
	// Phase 4: rewrite-style intercept that fills repo: + branch:
	// (and the staleness trio when staleness:true) on code-graph tool
	// calls. Runs BEFORE InterceptSearch because the rewriter / embedder
	// in InterceptSearch decodes the same args struct and expects repo:
	// already to be present.
	//
	// Two return shapes: (params, true, errResult) short-circuits the
	// chain with a typed error (missing-repo / missing-branch from a
	// non-matching cwd); (newParams, false, _) continues the chain with
	// rewritten args — the rewritten params propagate to the caller so
	// fall-through tool calls (file_symbols, list_branches, …) reach the
	// server with the injected repo+branch.
	rewritten, handled, res := tools.InjectRepoIfCodeGraph(ctx, c, params)
	if handled {
		return params, true, res
	}
	params = rewritten

	if handled, res := tools.InterceptSearch(ctx, c, params); handled {
		return params, true, res
	}
	// Per-graph + composite-mode + code query-domain intercepts. MUST run
	// BEFORE InterceptQuery — InterceptQuery embeds + routes a default/hybrid-mode
	// query through engine.Dispatch → compileQuery, which default-denies ONLY
	// code/logs, so a cloud/cicd/practice/linkage text-search would otherwise FALL
	// THROUGH to a GENERIC search and the per-graph renderers (renderResourceSearch
	// / renderPracticeResults) would never run. Each member self-gates on graph (or
	// mode), so a knowledge/default query falls cleanly through to InterceptQuery.
	if handled, res := runQueryDomainIntercepts(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptQuery(ctx, c, params); handled {
		return params, true, res
	}
	// Phase 6: dead_code analyzer runs client-side because the
	// RTA pipeline needs filesystem access to packages.Load. Other
	// topology algorithms fall through to the server.
	if handled, res := tools.InterceptTopology(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptManage(ctx, c, params); handled {
		return params, true, res
	}
	// `sync` rides the EngineService.Sync RPC client-side. Self-gates on
	// params.Name=="sync".
	if handled, res := tools.InterceptSync(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptLogsManage(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptLogsQuery(ctx, c, params); handled {
		return params, true, res
	}
	// Cluster of query-rendering intercepts (plan_tree,
	// list_projects, decisions, evidence, lineage, rules, examine
	// projects). Extracted out of this chain to keep the
	// runInterceptChainInner statement count under the lint cap.
	if handled, res := runQueryRenderingIntercepts(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptLogsTraversal(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptAst(ctx, c, params); handled {
		return params, true, res
	}
	// Deterministic verified-negation gate. MUST run BEFORE both InterceptThoughts
	// (claims thoughts(think) supersession) and InterceptMutate (claims
	// mutate(update status:invalidated) / mutate(link relationship:contradicts)) so
	// it precedes every write handler that lands a negation. It self-gates: it
	// returns (false,_) for any non-negation call (and on nil gc, fail-open), so a
	// normal think/mutate falls cleanly through to the handler below. On a missing /
	// hallucinated / stale quote it returns (true, errResult) — the negation never
	// reaches its write handler. Placed before InterceptThoughts (the earlier of the
	// two negation claimants) satisfies the before-both ordering.
	if handled, res := tools.InterceptNegationGate(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptThoughts(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptAssemble(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptGraphType(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptCreateProject(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptCreateTicket(ctx, c, params); handled {
		return params, true, res
	}
	// Cluster of project-domain create / record / help
	// handlers relocated client-side. Each one returns
	// (false, _) for the wrong tool name so they no-op for unrelated
	// calls. Extracted out of this chain to keep the runInterceptChainInner
	// statement count under the lint cap.
	if handled, res := runProjectDomainIntercepts(ctx, c, params); handled {
		return params, true, res
	}
	// Phase 2: mutate(create, type:criterion) moved client-
	// side. Must fire BEFORE InterceptMutate, whose create arm claims
	// finding/research/rule by type and ALSO any other type-bearing create
	// carrying ticket_id/session/links (the context-linked arm is keyed on
	// trio presence, never on node type — so a criterion create carrying a
	// ticket_id is exactly what it would claim), so the criterion
	// orchestration runs before any generic-mutate fall-through.
	if handled, res := tools.InterceptAddCriterion(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptMutate(ctx, c, params); handled {
		return params, true, res
	}
	handled, res = c.runTerminalIntercepts(ctx, params)
	return params, handled, res
}

// runTerminalIntercepts runs the chain's tail: the delete param guard, then the
// terminal collect claim.
//
// The delete tool has no client intercept of its own — its arguments are decoded
// in the engine package, which cannot reach the param-accounting helper — so the
// guard exists purely to account for them and falls THROUGH on a clean payload,
// leaving the delete itself to reach engine.Dispatch unchanged.
func (c *client) runTerminalIntercepts(ctx context.Context, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if handled, res := tools.InterceptDeleteGuard(ctx, c, params); handled {
		return true, res
	}
	return tools.InterceptCollect(ctx, c, params)
}

// runQueryRenderingIntercepts dispatches the query-domain
// intercepts (plan_tree, evidence, lineage, rules, examine projects).
// Extracted from runInterceptChainInner to satisfy the funlen
// lint cap. Bare container-type browse (plan/project/ticket/research/
// document) is deliberately NOT claimed here — it falls through to the
// engine browse arm so it paginates like every other node type. A decision
// browse is the same case and has no claimant here either: its two live
// routes are the engine browse arm and, for a text-bearing query, the
// knowledge search arm upstream.
func runQueryRenderingIntercepts(ctx context.Context, c *client, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if handled, res := tools.InterceptQueryPlanTree(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryEvidence(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryLineage(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryRules(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryExamineProjects(ctx, c, params); handled {
		return true, res
	}
	// Phase 2: general query(mode:examine) for every OTHER knowledge node
	// type (NodeFile, NodeThought, finding, decision, ...). Must run AFTER
	// InterceptQueryExamineProjects so project-domain types get the richer
	// project-tree variant; this claims the rest, relocating the server
	// handleInspectNode path client-side before the engineDispatch fall-through.
	if handled, res := tools.InterceptQueryExamine(ctx, c, params); handled {
		return true, res
	}
	return false, kgtools.ToolResult{}
}

// runQueryDomainIntercepts dispatches the per-graph + composite-mode +
// code query-domain intercepts. Modeled on runQueryRenderingIntercepts — a flat
// gate-and-delegate sequence. Each member self-gates (on graph or mode) and
// returns (false,_) for a call it doesn't own, so the order among members is
// immaterial; the load-bearing constraint is that the SINGLE call site in
// runInterceptChainInner runs BEFORE InterceptQuery (see the call-site comment).
// Phases 4-5 append their composite-mode / code intercepts into THIS helper —
// one call site total, not one per intercept.
func runQueryDomainIntercepts(ctx context.Context, c *client, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	// GAP-A: query(mode:stats) on the knowledge/default graph. The per-graph stats
	// intercepts below each gate on their own graph (cloud/cicd/code/practice/
	// linkage), so a bare knowledge stats matched none and fell through to the
	// generic deny. This member self-gates on mode==stats && graph∈{"",knowledge}.
	if handled, res := tools.InterceptQueryStats(ctx, c, params); handled {
		return true, res
	}
	// Phase 2: knowledge text-search modes (mode=recent now; text/default
	// added by the sibling reroute step) are served by the CLIENT knowledge engine
	// (composeKnowledgeSearch) instead of a server RETURN_MODE_SEARCH dispatch.
	// Self-gates on graph∈{"",knowledge} + the claimed mode.
	if handled, res := tools.InterceptQueryKnowledgeSearch(ctx, c, params); handled {
		return true, res
	}
	// Registered CUSTOM graph text-search (query(graph:<custom>, mode:text|recent|
	// default-text)) is served by the CLIENT segment engine (composeRegisteredGraphSearch)
	// instead of a server RETURN_MODE_SEARCH dispatch. Self-gates on a non-empty,
	// non-builtin graph + a claimed text-search shape; runs before tools.InterceptQuery
	// (the generic embed+engine.Dispatch tail) so a custom graph never reaches the
	// retired server search.
	if handled, res := tools.InterceptQueryRegisteredGraphSearch(ctx, c, params); handled {
		return true, res
	}
	// transformers/checks ranked text search: REFUSED by name. These two builtins
	// carry no ranked index, and the arm directly above declines them for being
	// builtin — the same ejection that left them unclaimed on the search rail. Left
	// unclaimed here they reached tools.InterceptQuery, whose generic dispatch tail
	// rendered server rows under a "_search mode: BM25-only_" footer for graphs that
	// carry no BM25 segments, and two of the four published text modes fell past
	// even that to the generic engine deny. Self-gates on the two graphs plus a
	// text-search shape, so every index-free op on them (browse, by-id, stats,
	// modules) still falls through to the path that serves it — which matters,
	// because the refusal hands the caller exactly those browses.
	if handled, res := tools.InterceptQueryUnrankedBuiltin(ctx, c, params); handled {
		return true, res
	}
	// mode:stats for the two builtins that had no stats arm (checks, transformers),
	// plus the vocabulary refusal for a graph value naming no graph at all. Both
	// cases met the generic engine deny before this member, since "stats" is not an
	// engine-reducible mode. Self-gates on mode==stats AND its own graph set, so a
	// real graph another arm owns is DECLINED rather than shadowed — which is why
	// its position relative to the per-graph stats arms does not change behavior.
	if handled, res := tools.InterceptQueryBuiltinStats(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryCloudCICD(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryPracticeLinkage(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryCorrelationsPivot(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryExplainTimeline(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryMetadataStats(ctx, c, params); handled {
		return true, res
	}
	// Phase 5: codegraph relocation. The code composers self-gate on
	// graph=code (analyze: id+non-stats; code-search: text/queries; code-stats:
	// mode=stats; list_modules: mode=modules) or their top-level tool name
	// (file_symbols). They run AFTER InjectRepoIfCodeGraph (the first chain step)
	// so repo:/branch: are already injected. compileQuery default-denies code, so
	// these MUST claim client-side before the engineDispatch fall-through.
	if handled, res := tools.InterceptQueryAnalyzeNode(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryCodeSearch(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryModulesCodeStats(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptFileSymbols(ctx, c, params); handled {
		return true, res
	}
	return false, kgtools.ToolResult{}
}

// runProjectDomainIntercepts dispatches an MCP call across the
// project-domain intercepts (create_plan / create_research /
// create_test_plan / record_decision / help) plus the client-owned
// tools that joined them here for the same lint reason
// (analyze_usage / manage_checks). Each intercept
// returns (false, _) when params.Name doesn't match its tool — the
// chain falls through with no extra work. Extracted from
// runInterceptChainInner to satisfy the funlen lint cap; the dispatch
// order matters only insofar as the underlying intercepts all gate on
// tool name (no overlap).
func runProjectDomainIntercepts(ctx context.Context, c *client, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if handled, res := tools.InterceptCreatePlan(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptCreateResearch(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptCreateTestPlan(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptRecordDecision(ctx, c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptHelp(ctx, c, params); handled {
		return true, res
	}
	// analyze_usage: client-owned agent-flow analyzer over the local transcript cache.
	if handled, res := tools.InterceptAnalyzeUsage(ctx, c, params); handled {
		return true, res
	}
	// manage_checks: client-owned authoring/inventory/run surface for the corpus
	// checks. Client-side for the same reason ast is — running a check walks the
	// caller's working tree, which only this side can see.
	if handled, res := tools.InterceptManageChecks(ctx, c, params); handled {
		return true, res
	}
	return false, kgtools.ToolResult{}
}

// appendClientDurationFooter mirrors the server-side footer in
// HandleToolsCall — same format, same semantics, applied to client-side
// intercept results so timing is uniform across both dispatch paths.
// Tagged "client" so a perf hunt can tell at a glance whether the call
// ran locally vs traveled to the server.
func appendClientDurationFooter(result kgtools.ToolResult, d time.Duration) kgtools.ToolResult {
	footer := "\n_took " + formatClientDuration(d) + " (client)_"
	for _, v := range slices.Backward(result.Content) {
		if v.Type == "text" {
			v.Text += footer
			return result
		}
	}
	result.Content = append(result.Content, kgtools.ContentBlock{Type: "text", Text: footer})
	return result
}

// formatClientDuration mirrors formatToolDuration from the server side.
// Sub-millisecond as µs, sub-second as ms, otherwise seconds with
// hundredth-of-a-second precision.
func formatClientDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	default:
		return d.Round(10 * time.Millisecond).String()
	}
}
