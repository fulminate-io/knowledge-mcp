// SPDX-License-Identifier: Apache-2.0

// daemon.go — shared client-wiring extraction (buildClient) plus the
// `knowledge serve` HTTP MCP daemon entrypoint (runServe).
//
// buildClient holds the full client construction + background-wire
// sequence the streamable-HTTP daemon path (runServe) needs. (It was
// originally factored out so the now-deleted per-session stdio entrypoint
// could share it; the daemon is now the only caller.) The three
// long-running background loops (LLM pipeline, dream Runner,
// PropagationLoop) are drained by the returned cleanup closure rather
// than caller-scope `defer`s.

package bootstrap

import (
	"context"
	"flag"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// buildClient constructs the MCP client and wires every client-side
// background runtime (dream worker Runner, PropagationLoop, LLM pipeline)
// plus the one-shot asset-drift hints and instruction
// bootstrap (constructClient through instruction bootstrap). The serve
// daemon (runServe) is its sole caller.
//
// Returns the constructed *client, a cleanup closure, and an error. The
// cleanup closure drains the three long-running background loops in a
// fixed order (pipeline.Stop with a 30s timeout, runtime.Stop(60s),
// propLoop.Stop(60s)); all three Stop calls are nil-safe so the closure is
// unconditional. The error return is always nil today — every wire failure
// here is slog.Warn'd and degrades (the loop continues with that runtime
// disabled); the error slot is reserved for future wiring that genuinely
// cannot degrade.
//
// Startup timing instrumentation: each `stage` call emits elapsed-since-
// entry at slog.Debug to help diagnose the MCP-host first-connect flake.
// Cheap (only fires under --log-level=debug).
//
// Keeping the (always-nil-today) error in the signature avoids a
// caller-rippling change when future wiring genuinely cannot degrade.
//
//nolint:unparam // error result reserved for future non-degradable wiring; see above.
func buildClient(f Config) (*client, func(), error) {
	t0 := time.Now()
	stage := func(name string) {
		slog.Debug("client.startup", "stage", name, "elapsed", time.Since(t0))
	}

	// Build the client first so the boot-spawn decision can read the live
	// login state (c.router.LoggedIn). A logged-in user routes every
	// fall-through op to cloud via Dispatch and operates with no local
	// knowledge-server, so the proactive spawn below is skipped for them.
	c := constructClient(f)
	stage("constructClient done")

	maybeSpawnLocalServer(c, f)
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

	if !f.NoWorkerRuntime {
		if err := wireWorkerRuntime(c, f); err != nil {
			slog.Warn("dream worker runtime unavailable; worker.trigger/status will return errors but other tools work",
				"error", err)
		}
		stage("wireWorkerRuntime done")
	} else {
		slog.Debug("dream worker runtime skipped (--no-worker-runtime)")
	}

	// Wire the client-side PropagationLoop. Runs
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
	// its tolerance — see the client.startup stage
	// timings. Caller no longer aborts on precheck failure; the misconfig
	// trips at first tool use with a clearer per-call error.
	//
	// Gated on !NoWorkerRuntime for the SAME reason wireWorkerRuntime is:
	// config.LoadOrAutoDetect is loaded inside wireWorkerRuntime, so a
	// --no-worker-runtime process (which skips wireWorkerRuntime) never
	// loaded config and RunPrecheck → config.Active() would panic. Such a
	// process makes no LLM calls of its own, so it has no consumer to
	// precheck anyway.
	if !f.NoWorkerRuntime {
		_ = llmproviders.RunPrecheck(context.Background(), f.SkipLLMPrecheck)
		stage("llmproviders.RunPrecheck spawned (async)")
	}

	// Wire the client-side embedder so InterceptSearch / InterceptQuery
	// can embed query text on the client side (Phase 4.5). nil when no
	// voyage_api_key is configured — the search path then runs BM25-only.
	// The server holds no embedder at all by design, so query embedding is
	// exclusively client-side.
	c.embedder = llmproviders.BuildEmbedder()
	stage("llmproviders.BuildEmbedder done")

	// Wire the client-side LLM pipeline (Phase 6). Builds summarizer +
	// embedder + worker pools, runs the initial graph-list registration,
	// then spawns a background refresh goroutine that replaces the
	// deleted server-side RegisterPipelineHooks. nil-safe Stop is run by
	// the returned cleanup closure.
	if err := wirePipelineRuntime(c, f); err != nil {
		slog.Warn("client pipeline wire failed; LLM background processing disabled",
			"error", err)
	}
	stage("wirePipelineRuntime done")

	// Seed agent + skill nodes from .claude/{agents,
	// skills}/*.md. Server-side projects.Bootstrap call has been
	// removed; the client owns disk I/O for code-graph + project
	// assets now. Non-fatal — startup continues on error.
	if err := runInstructionBootstrap(context.Background(), c.router, f.RootDir); err != nil {
		slog.Warn("instruction bootstrap failed; agent/skill nodes will not be seeded this session",
			"error", err)
	}
	stage("instruction bootstrap done")

	// cleanup drains the three long-running background loops in a fixed
	// order (pipeline, dream Runner, PropagationLoop). All three Stop calls
	// are nil-safe, so this is unconditional.
	cleanup := func() {
		if c.pipeline != nil {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer stopCancel()
			_ = c.pipeline.Stop(stopCtx)
		}
		c.runtime.Stop(60 * time.Second)
		// PropagationLoop.Stop is nil-safe (mirrors dream.Runner.Stop) so
		// this drain is unconditional even when wirePropagationRuntime
		// degraded at boot or --no-propagation-runtime was passed.
		c.propLoop.Stop(60 * time.Second)
	}

	return c, cleanup, nil
}

// runServe is the `knowledge serve` daemon entry — the sole MCP-serving
// path. It composes buildClient with the streamable-HTTP MCP transport: it
// wires the client + background runtimes, builds the MCPClient with the
// shared MCPClientConfig (intercept chain + compile-or-DENY dispatch), then
// serves it over HTTP on a loopback port until SIGTERM/SIGINT.
//
// Shared-pipeline invariant: buildClient wires exactly ONE LLM pipeline
// (wirePipelineRuntime, the sole pipeline.New site) onto the one *client, and
// the HTTPServer multiplexes every MCP session over that same *client. So N
// concurrent sessions share ONE pipeline + one rate gate — the per-session
// HTTPServer/httpSession state holds no pipeline reference. This is the whole
// point of the daemon: the worker count is constant across sessions, not
// multiplied by session count.
//
// runServe is a subcommand entry invoked from RunSubcommand and so bypasses
// bootstrap.Run; it reproduces Run's logging + GOMEMLIMIT + tilde-expansion
// setup (run.go:62-71) itself.
func runServe(args []string) error {
	fs := flag.NewFlagSet("knowledge serve", flag.ContinueOnError)
	var cfg Config
	registerConfigFlags(fs, &cfg)
	httpPort := fs.Int("http-port", graphclient.DefaultMCPHTTPPort, "Loopback TCP port for the streamable-HTTP MCP endpoint (/mcp). Distinct from --port (the graph server).")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Mirror bootstrap.Run's setup (run.go:62-71) — runServe is a
	// subcommand entry that does not pass through Run.
	var logLevelVar slog.LevelVar // defaults to Info
	if err := logLevelVar.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		slog.Warn("invalid --log-level, using info", "value", cfg.LogLevel)
	}
	setupLogging(&cfg, &logLevelVar)
	applyMemoryLimit()
	cfg.GraphStorage = expandTilde(cfg.GraphStorage)

	c, cleanup, err := buildClient(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	// Build the MCPClient with the shared MCPClientConfig (handleToolsList +
	// intercept chain + compile-or-DENY engineDispatch from mcp.go) so the
	// HTTP transport routes through the same intercept + dispatch path.
	c.mcpClient = graphclient.NewMCPClient(graphclient.MCPClientConfig{
		Client:          c.local,
		Port:            c.port,
		Version:         c.version,
		HandleToolsList: c.handleToolsList,
		InterceptChain:  c.runInterceptChain,
		Dispatch:        c.engineDispatch,
		LoggedIn:        c.router.LoggedIn,
	})

	hs := graphclient.NewHTTPServer(c.mcpClient, *httpPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	return hs.Run(ctx)
}
