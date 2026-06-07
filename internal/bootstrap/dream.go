// SPDX-License-Identifier: Apache-2.0

// Client-side dream.Runner construction.
//
// Post-Phase-F the server-side chokepoint that emitted tool-* events on
// every MCP tool call is gone — no tool-* events ever cross the wire to
// the client's local EventBus. The only events firing on the bus owned
// by THIS Runner are worker-* events emitted from the Runner's own
// runWorker (worker-started / worker-completed). Those are filtered by
// the self-trigger guard in dispatchLoop (OriginIsDreamWorker), so a
// worker subscribed to tool-completed can never re-fire on its own
// child invocations.
//
// The practical upshot: Runner.Start(ctx) walks Registry.All and installs
// triggers, but in the client-side topology installed triggers are mostly
// dead — they exist for shape consistency with the server-side wiring
// they replace and for any future event source the client adds. Calling
// Start() at boot is therefore mostly registry validation: it surfaces
// load errors (worker rows that fail to decode, malformed triggers) so
// the operator sees them at startup instead of when a manual trigger is
// fired hours later.
//
// Worker invocation in the client topology happens through the manual
// path (worker.trigger MCP intercept, Phase H) which calls into
// Runner.OnManualTrigger directly — no event bus involvement. The bus
// remains live so worker-started / worker-completed status events can
// still be observed if the client ever subscribes to them, but no
// trigger-driven dispatch happens today.

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/dream"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/workercrud"
)

// buildRuntime is the single-source construction path for the client-side
// dream.Runner. Both wireWorkerRuntime (the serve daemon path) and Phase I's
// runWorkerSubcommand (CLI `knowledge worker run` path) call through here
// so the construction order — config load → bus → registry(client) →
// runner(reg, bus, client, graphStorage) — stays in one place.
//
// gc is the login-aware Execute seam (runtimeLister). wireWorkerRuntime
// passes the stdio client's c.router so the dream Registry's worker-list
// loopback routes per-call to cloud when logged in (no local server) and
// to the local graph otherwise. The CLI subcommand path runs BEFORE the
// MCP client is built and so constructs its own local *graphclient.GraphClient
// (which also satisfies runtimeLister) — the signature stays uniform across
// both callers.
//
// graphStorage is the absolute path to ~/.knowledge/ (already
// tilde-expanded by main()); the Runner writes per-worker logs under
// <graphStorage>/workers/<name>.log.
//
// The Registry takes the Execute seam directly and resolves workers via a
// wire-loopback query.
// runInterceptChain dispatches an incoming MCP tool call through the same
// client-side interceptor chain the serve daemon wires for upstream traffic.
// Used in two places:
//
//  1. The serve daemon (daemon.go) hands it to NewMCPClient as
//     InterceptManageOp — every upstream MCP tool call from the host
//     traverses the chain.
//  2. buildRuntime composes it via dispatchForRunner into a dream.DispatchFunc
//     so the worker's eino tool dispatch runs the SAME chain-then-engine.Dispatch
//     standard path upstream MCP traffic uses — no worker-specific plumbing, no
//     bespoke server-stub round-trip for client-resident tools (ast, collect,
//     future manage ops).
//
// Returning (false, _) means no interceptor handled the call; the caller
// then falls back to forwarding to the server (NewMCPClient does this for
// MCP traffic; mcpTool.InvokableRun does it for worker traffic).
//
// ctx carries the per-session workspace cwd (HTTP transport) so
// InjectRepoIfCodeGraph resolves code-graph calls against the session's repo;
// the stdio transport passes context.Background(). It is threaded into the
// cwd-dependent intercepts (InjectRepoIfCodeGraph, InterceptTopology).
func (c *client) runInterceptChain(ctx context.Context, params kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult) {
	start := time.Now()
	rewritten, handled, res := c.runInterceptChainInner(ctx, params)
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
// the cwd-dependent intercepts (InjectRepoIfCodeGraph, InterceptTopology).
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

	if handled, res := tools.InterceptSearch(c, params); handled {
		return params, true, res
	}
	// Per-graph + composite-mode + code query-domain intercepts. MUST run
	// BEFORE InterceptQuery — InterceptQuery embeds + routes a default/hybrid-mode
	// query through engine.Dispatch → compileQuery, which default-denies ONLY
	// code/logs, so a cloud/cicd/practice/linkage text-search would otherwise FALL
	// THROUGH to a GENERIC search and the per-graph renderers (renderResourceSearch
	// / renderPracticeResults) would never run. Each member self-gates on graph (or
	// mode), so a knowledge/default query falls cleanly through to InterceptQuery.
	if handled, res := runQueryDomainIntercepts(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptQuery(c, params); handled {
		return params, true, res
	}
	// Phase 6: dead_code analyzer runs client-side because the
	// RTA pipeline needs filesystem access to packages.Load. Other
	// topology algorithms fall through to the server.
	if handled, res := tools.InterceptTopology(ctx, c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptManage(c, params); handled {
		return params, true, res
	}
	// `sync` rides the EngineService.Sync RPC client-side. Self-gates on
	// params.Name=="sync".
	if handled, res := tools.InterceptSync(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptLogsManage(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptLogsQuery(c, params); handled {
		return params, true, res
	}
	// Cluster of query-rendering intercepts (plan_tree,
	// list_projects, decisions, evidence, lineage, rules, examine
	// projects). Extracted out of this chain to keep the
	// runInterceptChainInner statement count under the lint cap.
	if handled, res := runQueryRenderingIntercepts(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptLogsTraversal(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptAst(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptThoughts(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptAssemble(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptWorker(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptGraphType(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptCreateProject(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptCreateTicket(c, params); handled {
		return params, true, res
	}
	// Cluster of project-domain create / record / help
	// handlers relocated client-side. Each one returns
	// (false, _) for the wrong tool name so they no-op for unrelated
	// calls. Extracted out of this chain to keep the runInterceptChainInner
	// statement count under the lint cap.
	if handled, res := runProjectDomainIntercepts(c, params); handled {
		return params, true, res
	}
	// Phase 2: mutate(create, type:criterion) moved client-
	// side. Must fire BEFORE InterceptMutate (gates on op in
	// {update, delete} — never claims create) so the criterion
	// orchestration runs before any generic-mutate fall-through.
	if handled, res := tools.InterceptAddCriterion(c, params); handled {
		return params, true, res
	}
	if handled, res := tools.InterceptMutate(c, params); handled {
		return params, true, res
	}
	handled, res = tools.InterceptCollect(c, params)
	return params, handled, res
}

// runQueryRenderingIntercepts dispatches the query-domain
// intercepts (plan_tree, list_projects, decisions, evidence, lineage,
// rules, examine projects). Extracted from runInterceptChainInner to
// satisfy the funlen lint cap.
func runQueryRenderingIntercepts(c *client, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if handled, res := tools.InterceptQueryPlanTree(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryListProjects(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryDecisions(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryEvidence(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryLineage(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryRules(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryExamineProjects(c, params); handled {
		return true, res
	}
	// Phase 2: general query(mode:examine) for every OTHER knowledge node
	// type (NodeFile, NodeThought, finding, decision, ...). Must run AFTER
	// InterceptQueryExamineProjects so project-domain types get the richer
	// project-tree variant; this claims the rest, relocating the server
	// handleInspectNode path client-side before the engineDispatch fall-through.
	if handled, res := tools.InterceptQueryExamine(c, params); handled {
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
func runQueryDomainIntercepts(c *client, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	// GAP-A: query(mode:stats) on the knowledge/default graph. The per-graph stats
	// intercepts below each gate on their own graph (cloud/cicd/code/practice/
	// linkage), so a bare knowledge stats matched none and fell through to the
	// generic deny. This member self-gates on mode==stats && graph∈{"",knowledge}.
	if handled, res := tools.InterceptQueryStats(c, params); handled {
		return true, res
	}
	// Phase 2: knowledge text-search modes (mode=recent now; text/default
	// added by the sibling reroute step) are served by the CLIENT knowledge engine
	// (composeKnowledgeSearch) instead of a server RETURN_MODE_SEARCH dispatch.
	// Self-gates on graph∈{"",knowledge} + the claimed mode.
	if handled, res := tools.InterceptQueryKnowledgeSearch(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryCloudCICD(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryPracticeLinkage(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryCorrelationsPivot(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryExplainTimeline(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryMetadataStats(c, params); handled {
		return true, res
	}
	// Phase 5: codegraph relocation. The code composers self-gate on
	// graph=code (analyze: id+non-stats; code-search: text/queries; code-stats:
	// mode=stats; list_modules: mode=modules) or their top-level tool name
	// (file_symbols). They run AFTER InjectRepoIfCodeGraph (the first chain step)
	// so repo:/branch: are already injected. compileQuery default-denies code, so
	// these MUST claim client-side before the engineDispatch fall-through.
	if handled, res := tools.InterceptQueryAnalyzeNode(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryCodeSearch(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptQueryModulesCodeStats(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptFileSymbols(c, params); handled {
		return true, res
	}
	return false, kgtools.ToolResult{}
}

// runProjectDomainIntercepts dispatches an MCP call across the
// project-domain intercepts (create_plan / create_research /
// create_test_plan / record_decision / help). Each intercept
// returns (false, _) when params.Name doesn't match its tool — the
// chain falls through with no extra work. Extracted from
// runInterceptChainInner to satisfy the funlen lint cap; the dispatch
// order matters only insofar as the underlying intercepts all gate on
// tool name (no overlap).
func runProjectDomainIntercepts(c *client, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if handled, res := tools.InterceptCreatePlan(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptCreateResearch(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptCreateTestPlan(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptRecordDecision(c, params); handled {
		return true, res
	}
	if handled, res := tools.InterceptHelp(c, params); handled {
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

// dispatchForRunner builds the dream.DispatchFunc the worker's eino tools route
// every call through — the EXACT standard sequence MCPClient.handleMCPToolCall
// performs (cmd/knowledge/internal/graphclient/mcp_client.go): run the client intercept chain; if a tool
// is intercepted client-side return its result; otherwise engineDispatch the
// REWRITTEN args (so InjectRepoIfCodeGraph's repo:+branch: fill propagates) through
// the single engine.Dispatch → Execute passthrough. The worker shares the ONE
// client tool path, so engineDispatch is the only passthrough.
func (c *client) dispatchForRunner() dream.DispatchFunc {
	return func(ctx context.Context, name string, args json.RawMessage) (kgtools.ToolResult, error) {
		rewritten, handled, res := c.runInterceptChain(ctx, kgtools.CallToolParams{Name: name, Arguments: args})
		if handled {
			return res, nil
		}
		return c.engineDispatch(ctx, name, rewritten.Arguments)
	}
}

// runtimeLister is the Execute-only seam buildRuntime takes so it can be
// handed the login-aware *graphclient.Router (cloud when logged in, local
// otherwise) rather than the bare local *graphclient.GraphClient. The
// argument flows ONLY to the dream Registry's worker-list lister
// (workercrud.New); the worker tool-dispatch path is wired separately via
// c.dispatchForRunner. Mirrors thought.Caller — both *graphclient.GraphClient
// and *graphclient.Router satisfy it structurally.
type runtimeLister interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

func buildRuntime(gc runtimeLister, port int, graphStorage string, dispatch dream.DispatchFunc) (*dream.Runner, error) {
	cfgPath, err := defaultConfigPath()
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	// Loopback bind addr — the client always talks to 127.0.0.1:<port>,
	// so config.LoadOrAutoDetect's local-precedence path runs (matches
	// the server's loadConfigForListener semantics for loopback bind).
	bindAddr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	cfg, wroteStarter, err := config.LoadOrAutoDetect(cfgPath, bindAddr)
	if err != nil {
		return nil, fmt.Errorf("load/auto-detect config: %w", err)
	}
	if wroteStarter {
		slog.Info("auto-detected provider, wrote starter config",
			"path", cfgPath,
			"provider", cfg.Default.Provider,
			"model", cfg.Default.Model,
		)
	}

	bus := dream.NewEventBus()
	// Wire the dream Registry through workercrud.Client so it reuses the
	// query-tool wire path. workercrud.Client.List itself is wire-loopback;
	// the indirection saves no round-trips but eliminates dream's bespoke
	// wire-row decoder (~50 lines).
	reg := dream.NewRegistry(workercrud.New(gc))
	// The MCP tool catalog is client-owned. The Runner carries it so
	// BuildAllowedTools can filter the worker allowlist locally (and without
	// dream importing tools — that would cycle, since tools imports dream).
	runner := dream.NewRunner(reg, bus, graphStorage, dispatch, tools.AllToolSchemas())
	return runner, nil
}

// wireWorkerRuntime constructs the client-side dream.Runner and wires it
// into the *client. It hands buildRuntime the login-aware c.router so the
// Registry's worker-list loopback routes to cloud when logged in (no local
// server) and local otherwise, and assigns the returned Runner to
// c.runtime. After construction it calls Runner.Start(ctx) to install
// triggers; per the file-level docstring this is mostly registry
// validation in the client topology, so a Start failure is logged and
// the runtime is still kept (manual triggers — the only invocation path
// that matters today — work without Start).
func wireWorkerRuntime(c *client, f Config) error {
	runner, err := buildRuntime(c.router, f.Port, f.GraphStorage, c.dispatchForRunner())
	if err != nil {
		return err
	}
	c.runtime = runner
	if err := runner.Start(context.Background()); err != nil {
		slog.Warn("dream runner Start failed; manual triggers still work", "error", err)
	}
	return nil
}

// defaultConfigPath returns the path to ~/.knowledge/config, mirroring
// cmd/knowledge-server/server.go::loadConfigForListener. Extracted so
// buildRuntime stays under the function-line cap.
func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".knowledge", "config"), nil
}
