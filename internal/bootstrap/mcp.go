// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	"github.com/fulminate-io/knowledge-mcp/internal/profiling"
)

// mcpToolJSON is the wire shape MCP hosts expect under tools/list result.
// Matches the camelCase JSON tag the MCP spec requires for inputSchema.
type mcpToolJSON struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// runMCPMode builds the MCP stdio client and runs the stdin loop.
// tools/list is served from a client-side schema cache built lazily from the
// client-owned catalog (tools.AllToolSchemas) on the first request — the client
// never constructs a kgtools.Handler, which would pull every collector + store
// registration transitively.
// manage({operation: "status"}) is intercepted client-side so the
// fail-fast message is readable even when the server is down.
//
// runMCPMode also wires the client-side dream.Runner via
// wireWorkerRuntime so worker.trigger / worker.status MCP intercepts
// (Phase H) have a runtime to dispatch through. Wiring failure
// (typically a malformed dream config) degrades to "no worker runtime"
// — c.runtime stays nil; ast/collect/search/etc intercepts and the MCP
// stdio loop continue to work, only the worker-trigger path errors.
// Runner.Stop is nil-safe so the deferred drain is unconditional.
func runMCPMode(f Config) error {
	// Startup timing instrumentation. Each `stage` call emits elapsed-
	// since-startup to help diagnose the MCP-host first-connect flake
	// (ticket fb39323b...). Cheap (slog.Debug only fires under
	// --log-level=debug); enable via `--log-level debug --log-file
	// ~/.knowledge/knowledge-client.log` and grep for "client.startup:".
	t0 := time.Now()
	stage := func(name string) {
		slog.Debug("client.startup", "stage", name, "elapsed", time.Since(t0))
	}
	stage("runMCPMode entered")

	initPprof(f, stage)

	// Try to reach the server; spawn one if needed. Done before
	// constructClient so the worker-runtime wiring (which dials the
	// server during ListWorkers) sees a healthy server. Errors here
	// are surfaced via slog (stderr) so the MCP stdio protocol on
	// stdout stays uncontaminated. Tool calls will still see
	// EnsureServer's per-call dial fallback as a backstop, but the
	// proactive spawn here avoids first-call latency.
	if err := ensureServerReachable(f.Port, f.RootDir, f.GraphStorage); err != nil {
		slog.Warn("knowledge-server not reachable and spawn failed; tool calls will return errors until the server is started", "error", err)
	}
	stage("ensureServerReachable done")

	// One-shot ~/.claude asset drift check. Logs a hint via slog
	// (stderr) when the embedded agents/skills don't match what's
	// installed under ~/.claude — most MCP hosts surface stderr in
	// their debug log, so users see the hint without having to know
	// `knowledge doctor` exists. Cheap (~10ms file walk + sha256s).
	hintClaudeAssetsIfStale()
	// Managed-block drift check for ~/.claude/CLAUDE.md (managed region
	// only, so user prose never false-positives).
	hintClaudeMDIfStale()
	// Same one-shot drift check for the codex twin: skills under
	// ~/.agents/skills and agents under ~/.codex/agents. AGENTS.md is
	// excluded (managed-block merge — a mismatch is expected).
	hintCodexAssetsIfStale()
	stage("asset drift hints done")

	c := constructClient(f)
	stage("constructClient done")
	if !f.NoWorkerRuntime {
		if err := wireWorkerRuntime(c, f); err != nil {
			slog.Warn("dream worker runtime unavailable; worker.trigger/status will return errors but other tools work",
				"error", err)
		}
		stage("wireWorkerRuntime done")
	} else {
		slog.Debug("dream worker runtime skipped (--no-worker-runtime)")
	}

	// Wire the client-side PropagationLoop (BCN4 v2 Phase 6). Runs
	// hourly cluster detection + valence/magnitude propagation in a
	// background goroutine. The deferred Stop is nil-safe; on-demand
	// reflective tool calls still run via InterceptThoughts (Phase 7)
	// even when the loop is disabled.
	if !f.NoPropagationRuntime {
		wirePropagationRuntime(c)
		stage("wirePropagationRuntime done")
	} else {
		slog.Debug("propagation loop skipped (--no-propagation-runtime)")
	}

	// LLM precheck runs ASYNC (2026-05-20) against the config loaded as
	// a side-effect of wireWorkerRuntime → buildRuntime →
	// config.LoadOrAutoDetect. Empty config silently no-ops; misconfigured
	// keys surface as `slog.Error "llmproviders: precheck failed"` from
	// the background goroutine rather than blocking the MCP handshake.
	// The synchronous variant blocked startup for ~5.4s on a typical
	// multi-provider config and pushed the MCP host's first-connect past
	// its tolerance — see ticket fb39323b... + the client.startup stage
	// timings. Caller no longer aborts on precheck failure; the misconfig
	// trips at first tool use with a clearer per-call error.
	//
	// Gated on !NoWorkerRuntime for the SAME reason wireWorkerRuntime is:
	// config.LoadOrAutoDetect is loaded inside wireWorkerRuntime, so a
	// --no-worker-runtime child (a CLI provider's MCP tool-server child,
	// e.g. claude-cli / codex-cli dream workers) never loaded config and
	// RunPrecheck → config.Active() would panic. The child is a pure
	// tool-call receiver — it makes no LLM calls of its own, so it has no
	// consumer to precheck.
	if !f.NoWorkerRuntime {
		_ = llmproviders.RunPrecheck(context.Background(), f.SkipLLMPrecheck)
		stage("llmproviders.RunPrecheck spawned (async)")
	}

	// Wire the client-side embedder so InterceptSearch / InterceptQuery
	// can embed query text on the client side (Phase 4.5). nil when no
	// voyage_api_key is configured — the search path then runs BM25-only.
	// The server holds no embedder at all (governing contract 147fda42),
	// so query embedding is exclusively client-side.
	c.embedder = llmproviders.BuildEmbedder()
	stage("llmproviders.BuildEmbedder done")

	// Wire the client-side LLM pipeline (Phase 6). Builds summarizer +
	// embedder + worker pools, runs the initial graph-list registration,
	// then spawns a background refresh goroutine that replaces the
	// deleted server-side RegisterPipelineHooks. nil-safe Stop is
	// deferred below.
	if err := wirePipelineRuntime(c, f); err != nil {
		slog.Warn("client pipeline wire failed; LLM background processing disabled",
			"error", err)
	}
	stage("wirePipelineRuntime done")
	defer func() {
		if c.pipeline != nil {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer stopCancel()
			_ = c.pipeline.Stop(stopCtx)
		}
	}()
	defer c.runtime.Stop(60 * time.Second)
	// PropagationLoop.Stop is nil-safe (mirrors dream.Runner.Stop) so
	// the deferred drain is unconditional even when wirePropagationRuntime
	// degraded at boot or --no-propagation-runtime was passed.
	defer c.propLoop.Stop(60 * time.Second)

	// Fire the client-side auto-prune runner asynchronously. Opt-in via
	// the [retention] config section; no-op when absent. Non-fatal on
	// failure — slog.Warn'd inside, never blocks the MCP handshake.
	wireAutoPrune(c)
	stage("wireAutoPrune done")

	// FUL-246 Phase 3c: seed agent + skill nodes from .claude/{agents,
	// skills}/*.md. Server-side projects.Bootstrap call has been
	// removed; the client owns disk I/O for code-graph + project
	// assets now (FUL-241). Non-fatal — startup continues on error.
	if err := runInstructionBootstrap(context.Background(), c.router, f.RootDir); err != nil {
		slog.Warn("instruction bootstrap failed; agent/skill nodes will not be seeded this session",
			"error", err)
	}
	stage("instruction bootstrap done")

	c.mcpClient = graphclient.NewMCPClient(graphclient.MCPClientConfig{
		Client:          c.local,
		Port:            c.port,
		Version:         c.version,
		HandleToolsList: c.handleToolsList,
		InterceptChain:  c.runInterceptChain,
		Dispatch:        c.engineDispatch,
	})
	stage("NewMCPClient done — entering Run loop")
	c.Run()
	return nil
}

// engineDispatch is the MCPClientConfig.Dispatch closure: it routes every
// post-intercept tool call through the compile-or-DENY engine dispatcher. The §A
// reducible shapes compile to Engine.Execute; an unrecognized shape is denied
// legibly — every LLM-facing tool either compiles here or is claimed by a client
// intercept upstream. Defined as a method (not an inline closure) so runMCPMode
// stays under the funlen cap and graphclient stays import-clean of the
// higher-level cmd/knowledge/internal tool packages (the func-field injection
// mirrors InterceptChain).
func (c *client) engineDispatch(ctx context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error) {
	return engine.Dispatch(ctx, c.router.Execute, tool, args)
}

// handleToolsList answers a tools/list JSON-RPC request from the cached
// schema set. On first call it builds the client-owned tool catalog
// (tools.AllToolSchemas) locally — the client is the source of truth for its own
// tool surface. Subsequent calls serve from the in-process cache.
func (c *client) handleToolsList(req kgtools.JSONRPCRequest) *kgtools.JSONRPCResponse {
	schemas, err := c.loadSchemas(context.Background())
	if err != nil {
		return &kgtools.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &kgtools.RPCError{
				Code:    -32603, // JSON-RPC internal error
				Message: "schema handshake failed: " + err.Error() + " — server may still be starting; retry tools/list",
			},
		}
	}

	tools := make([]mcpToolJSON, len(schemas))
	for i, s := range schemas {
		tools[i] = mcpToolJSON(s)
	}
	return &kgtools.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"tools": tools},
	}
}

// Run starts the MCP stdio loop, blocking until stdin closes.
func (c *client) Run() {
	c.mcpClient.Run()
}

func initPprof(f Config, stage func(string)) {
	if f.PprofPort != 0 && f.PprofPort != profiling.DefaultPort {
		profiling.SetPort(f.PprofPort)
	}
	if f.Pprof {
		profiling.EnsureServer()
		stage("pprof endpoint up")
	}
}
