// SPDX-License-Identifier: Apache-2.0

// daemon.go — shared client-wiring extraction (buildClient) plus the
// `knowledge serve` HTTP MCP daemon entrypoint (runServe).
//
// buildClient holds the full client construction + background-wire
// sequence the streamable-HTTP daemon path (runServe) needs. (It was
// originally factored out so the now-deleted per-session stdio entrypoint
// could share it; the daemon is now the only caller.) The two
// long-running background loops (LLM pipeline, PropagationLoop) are
// drained by the returned cleanup closure rather than caller-scope
// `defer`s.

package bootstrap

import (
	"context"
	"flag"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	"github.com/fulminate-io/knowledge-mcp/internal/profiling"
	"github.com/fulminate-io/knowledge-mcp/internal/rerank"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// the bind-first startup change UNIFIED SHUTDOWN BUDGET (keep these in sync with the Makefile
// daemon-stop drain loop count): on SIGTERM, runServe drains SIX bounded stages
// SEQUENTIALLY in LIFO defer order — drainOnShutdown's wireJoinDeadline wait +
// the segment backlog drain + pipeline.Stop + propLoop.Stop +
// collectRuntime.Stop + segmentMgr.Close. The budget inequality is:
//
//	wireJoinDeadline + segment backlog drain + pipeline.Stop +
//	propLoop.Stop + collectRuntime.Stop + segmentMgr.Close
//	  <=  Makefile daemon-stop drain window.
//
// Every stage is cooperative (PropagationLoop.Stop and CollectRuntime.Stop cancel
// baseCtx before their inFlight drain; the pipeline drains its bounded worker pool;
// the segment drain skips any graph whose ship outlives the window; segmentMgr.Close
// joins mergers that abandon their work at the next coarse boundary), so a clean
// drain finishes in a few seconds — the per-stage deadlines below are
// abandon-backstops, not expected durations. Pinned to 3s each → 6 * 3s = 18s
// worst-case, under the Makefile's 27s drain window. If you change one of these,
// re-check the Makefile loop count so the inequality still holds.
//
// segmentMgr.Close BECAME A BOUNDED STAGE when engine Close started JOINING its
// merge goroutine rather than merely signaling it. It previously waited for
// nothing and cost nothing here. It now waits, and Manager.Close loops SERIALLY
// over every per-graph engine, so its cost is a SUM across graphs — which is why it
// is bounded like its siblings rather than left unbounded.
const (
	// wireJoinDeadline bounds how long drainOnShutdown waits for the background
	// wiring goroutine (wireRuntimesBackground) to finish before it gives up and
	// drains only the already-ready subsystems.
	wireJoinDeadline = 3 * time.Second
	// daemonStopDeadline is the per-stage drain bound for the segment backlog
	// drain, pipeline, PropagationLoop and collect runtime. See the budget note
	// above.
	daemonStopDeadline = 3 * time.Second
)

// buildClient constructs the MCP client + runs the cheap synchronous bind-ready
// prefix (constructClient, the boot-spawn decision, the one-shot asset-drift
// hints), then returns the *client ready to BIND the HTTP MCP listener. The
// background runtimes (PropagationLoop, segment Manager, LLM pipeline)
// are NOT wired here — runServe launches
// c.wireRuntimesBackground in a goroutine AFTER the listener binds (Bind-first startup:
// bind first, wire in the background) so first-call latency is ~25ms instead of
// blocking on the ~2.6s wire chain. The serve daemon (runServe) is its sole
// caller.
//
// Returns the constructed *client, a cleanup closure, and an error. The cleanup
// closure cancels the background wiring ctx, bounded-joins the wiring goroutine
// (wireJoinDeadline), then drains ONLY the subsystems whose readiness flag is
// set — so a SIGTERM mid-wiring never Stops a nil/half-wired handle. The error
// return is always nil today — every wire failure is slog.Warn'd in
// wireRuntimesBackground and degrades; the error slot is reserved for future
// prefix wiring that genuinely cannot degrade.
//
// Startup timing instrumentation: each `stage` call emits elapsed-since-entry at
// slog.Debug to help diagnose the MCP-host first-connect flake. Cheap (only fires
// under --log-level=debug).
//
// Keeping the (always-nil-today) error in the signature avoids a caller-rippling
// change when future wiring genuinely cannot degrade.
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

	// Install the shared-source sync-transport builder SYNCHRONOUSLY here,
	// before the MCP listener binds and before wireRuntimesBackground is
	// spawned as a goroutine. The push/pull intercepts read this package
	// func-value from handler goroutines; a single pre-bind write establishes
	// a happens-before edge with every read, so no atomic/mutex is needed.
	// Wiring it from the background goroutine instead would be an
	// unsynchronized data race against those reads. c.cloudTokenSource is
	// already set (by constructClient) so the builder presents the one shared
	// cloud credential.
	tools.SetSyncTransportBuilder(c.buildCloudSyncTransport)

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

	// Set up the cancelable wiring ctx + done signal the background wiring
	// goroutine and the cleanup closure share (bind-first startup). wireRuntimesBackground
	// closes wireDone when the chain finishes (even on an early degrade), and
	// cleanup cancels wireCancel then bounded-joins wireDone before draining.
	wireCtx, wireCancel := context.WithCancel(context.Background())
	c.wireCtx = wireCtx
	c.wireCancel = wireCancel
	c.wireDone = make(chan struct{})

	return c, c.drainOnShutdown, nil
}

// drainOnShutdown is the serve daemon's cleanup closure (returned by buildClient).
// It (a) cancels the wiring ctx so any in-flight wire stage + the
// propagation/pipeline loops unwind; (b) bounded-joins the wiring goroutine
// (wireJoinDeadline) so it never blocks forever on a stuck stage; (c) drains ONLY
// the subsystems whose readiness flag is set — the Phase-1 atomic flags double as
// drain-eligibility gates, so a field is read only AFTER its write was published
// by the mark*Ready atomic Store, and a nil/half-wired handle is never Stopped.
// Each Stop stays nil-safe as defense-in-depth.
//
// Deadline budget (reconciled in Phase 3): wireJoinDeadline + the five sequential
// Stop deadlines below — the segment backlog drain, pipeline.Stop, propLoop.Stop,
// collectRuntime.Stop and the segmentMgr.Close join — must fit inside the Makefile
// daemon-stop SIGTERM window so a clean drain completes before SIGKILL.
func (c *client) drainOnShutdown() {
	if c.wireCancel != nil {
		c.wireCancel()
	}
	if c.wireDone != nil {
		select {
		case <-c.wireDone:
		case <-time.After(wireJoinDeadline):
			slog.Warn("wiring did not finish before shutdown; draining only ready subsystems")
		}
	}
	// The segment backlog drains FIRST, ahead of pipeline.Stop, because the segment
	// manager is reached through the pipeline: once that Stop returns there is no
	// producer left to ship what is queued, and the in-memory backlog is discarded
	// silently. Gated on the same pipeline readiness flag — a daemon that never
	// finished wiring the pipeline has no segment manager to drain.
	if c.PipelineReady() && c.segmentMgr != nil {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), daemonStopDeadline)
		c.drainSegmentBacklog(drainCtx)
		drainCancel()
	}
	// Flag-gated drain in the fixed order (segment backlog, pipeline,
	// PropagationLoop, the always-constructed collect runtime, then the segment
	// engines). The readiness flag guarantees the handle was published before we
	// read it; the nil-check is belt-and-suspenders. Each Stop is bounded to
	// daemonStopDeadline (3s) — see the unified shutdown-budget comment on that
	// const and the Makefile daemon-stop drain loop.
	if c.PipelineReady() && c.pipeline != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), daemonStopDeadline)
		defer stopCancel()
		_ = c.pipeline.Stop(stopCtx)
	}
	if c.PropReady() && c.propLoop != nil {
		c.propLoop.Stop(daemonStopDeadline)
	}
	// collectRuntime is constructed synchronously at boot (constructClient), never
	// in the background-wire window, so it needs no readiness flag — a plain
	// nil-guard suffices. Stop cancels its baseCtx (unwinding an in-flight detached
	// collect at the next RPC boundary) then bounded-drains the run goroutine.
	if c.collectRuntime != nil {
		c.collectRuntime.Stop(daemonStopDeadline)
	}
	// The segment engines close LAST, after every producer above has stopped: each
	// per-graph engine runs a merger goroutine that only Close stops, and closing
	// it while the backlog drain or the pipeline could still publish into that
	// engine would retire the background worker out from under live work. Same
	// readiness gate as the backlog drain — markPipelineReady is the atomic Store
	// that publishes c.segmentMgr, so it is the only barrier under which this
	// field may be read.
	//
	// IT IS A BOUNDED STAGE, and it did not used to be. Engine Close now JOINS its
	// merge goroutine instead of only signaling it, so this call WAITS — and
	// Manager.Close loops serially over every per-graph engine, making that wait a
	// SUM across graphs rather than a single merge. The join is unconditional
	// inside the engine, which is what gives every other caller a real guarantee;
	// the bound belongs HERE, in the process that is exiting anyway. At daemon exit
	// the write a late merger would make is a content-addressed L2 blob the next
	// boot rebuilds, so abandoning it after the deadline costs nothing; in a test
	// the same write lands in a directory being torn down, which is the defect the
	// unconditional engine-level join exists to prevent.
	if c.PipelineReady() && c.segmentMgr != nil {
		closeDone := make(chan struct{})
		go func() {
			defer close(closeDone)
			c.segmentMgr.Close()
		}()
		select {
		case <-closeDone:
		case <-time.After(daemonStopDeadline):
			slog.Warn("segment engines did not finish closing before the shutdown deadline; abandoning the join",
				"deadline", daemonStopDeadline)
		}
	}
	// The pprof endpoint is released after everything above, so a goroutine or heap
	// dump stays pullable for the whole drain — the window where a wedged subsystem
	// is worth profiling. No-op when --pprof was never passed and no
	// manage(pprof_start) ever ran. Without it this is the one listener the daemon
	// opens and never closes. It carries no deadline because it waits for nothing —
	// Close drops the listener and the open connections and returns — so it is not
	// one of the SIX bounded stages the shutdown-budget inequality is reconciled
	// against. (segmentMgr.Close above USED to share that property and no longer
	// does: it joins its merge goroutines and is now a bounded stage itself.)
	profiling.Stop()
}

// wireRuntimesBackground wires every client-side background runtime — client
// embedder, PropagationLoop, LLM pipeline, instruction
// bootstrap — in the SAME order buildClient used to wire them synchronously, but
// off the bind-critical path (bind-first startup). runServe launches it in a goroutine after
// the HTTP MCP listener binds. Each stage keeps its slog stage line + its
// slog.Warn degrade-on-error; the corresponding mark*Ready() atomic flag is set
// AFTER the stage regardless of whether it wired a live runtime or degraded to
// nil, so the readiness gates (Phase 1) and the flag-gated shutdown drain can
// distinguish the wiring window from a permanent degrade.
//
// ctx is the cancelable wiring ctx from buildClient; between stages it checks
// ctx.Err() so a SIGTERM mid-wiring stops launching further subsystems (the
// bounded join in cleanup is the hard guard). wireDone is closed on return (via
// defer) so cleanup's join always completes even on an early degrade/return.
func (c *client) wireRuntimesBackground(ctx context.Context, f Config) {
	defer close(c.wireDone)

	t0 := time.Now()
	stage := func(name string) {
		slog.Debug("client.startup", "stage", name, "elapsed", time.Since(t0))
	}

	// Load ~/.knowledge/config FIRST, on every serve, independently of any
	// runtime below. It must precede llmproviders.BuildEmbedder so the query
	// embedder + rerank resolve the config voyage_api_key rather than falling
	// straight to VOYAGE_API_KEY, and it must precede the LLM precheck, which
	// is gated on there being a config to precheck.
	loadBootConfig(f)

	if ctx.Err() != nil {
		return
	}

	// Wire the client-side embedder so InterceptSearch / InterceptQuery
	// can embed query text on the client side (Phase 4.5). nil when the
	// resolved [embedder] axis has no credential and no base_url — the
	// search path then runs BM25-only. This is the QUERY-role embedder:
	// search-time query text gets the query input role, which the corpus
	// side (the index pipeline) does not share.
	// The server holds no embedder at all by design, so query embedding is
	// exclusively client-side. c.embedder is read LAZILY by the
	// search/query intercepts via deps.Embedder() at call time — there is
	// NO wiring-order dependency between it and wirePropagationRuntime
	// below (WithTopicDeps takes only scanner + summarizer, no embedder).
	// The existing call order is preserved purely to avoid needless churn.
	queryEmbedder, err := llmproviders.BuildEmbedder(ctx, embed.InputRoleQuery)
	if err != nil {
		// Degrade-not-die, matching how this site already treats a nil
		// embedder — but the misconfiguration is logged rather than
		// swallowed, so a malformed [embedder] section is visible.
		slog.Warn("client runtime: query embedder build failed; search runs BM25-only", "error", err)
		queryEmbedder = nil
	}
	c.embedder = queryEmbedder
	stage("llmproviders.BuildEmbedder done")

	// Wire the client-side PropagationLoop. Runs
	// hourly cluster detection + valence/magnitude propagation in a
	// background goroutine. The Stop is nil-safe; on-demand
	// reflective tool calls still run via InterceptThoughts (Phase 7)
	// even when the loop is disabled.
	if !f.NoPropagationRuntime {
		wirePropagationRuntime(c, f)
		stage("wirePropagationRuntime done")
	} else {
		slog.Debug("propagation loop skipped (--no-propagation-runtime)")
	}
	// c.propLoop assigned above (or left nil); the atomic Store publishes it —
	// readers seeing PropReady()==true observe the wired handle. Do NOT reorder
	// the Store before the field write.
	c.markPropReady()

	if ctx.Err() != nil {
		return
	}

	// LLM precheck runs ASYNC (2026-05-20) against the config loadBootConfig
	// installed at the top of this function. Empty config silently no-ops;
	// misconfigured keys surface as `slog.Error "llmproviders: precheck failed"`
	// from the background goroutine rather than blocking the MCP handshake.
	//
	// Gated on config.Loaded() — precheck the configured providers when there is
	// a config to precheck. A normal serve loads one and still runs the precheck;
	// a --headless serve still makes no LLM call, because applyHeadless sets
	// SkipLLMPrecheck and RunPrecheck's own skip arm returns immediately.
	if config.Loaded() {
		// rerank.CheckProvider is supplied HERE, at the composition root, so
		// neither llmproviders nor llm/precheck carries a dependency on rerank:
		// llmproviders forwards the value straight to precheck.RunAll and the
		// binary is the only package that names both sides. Passing nil is
		// refused loudly at both hops rather than skipping the axis.
		_ = llmproviders.RunPrecheck(context.Background(), f.SkipLLMPrecheck, rerank.CheckProvider)
		stage("llmproviders.RunPrecheck spawned (async)")
	}

	// Construct the client-side segment Manager UNCONDITIONALLY (router-guarded),
	// BEFORE wirePipelineRuntime — see ensureSegmentManager. This is the
	// READ/CONSUME engine the search intercepts query, and it must exist even
	// offline so search serves BM25 over existing segments; wirePipelineRuntime
	// then only attaches this already-built instance to the producer for shipping.
	// The L2 cache roots under f.GraphStorage (<graph-storage>/segments) — the same
	// tilde-expanded data root the client spawns the local server with — so client
	// L2 and server store co-locate instead of leaking to a HOME-fixed path.
	c.ensureSegmentManager(f.GraphStorage, f.SegmentResidencyBudgetBytes)

	// Wire the client-side LLM pipeline (Phase 6). Builds summarizer +
	// embedder + worker pools, runs the initial graph-list registration,
	// then spawns a background refresh goroutine that replaces the retired
	// server-side RegisterPipelineHooks wire-up. nil-safe Stop is run by
	// the cleanup closure.
	if err := wirePipelineRuntime(ctx, c, f); err != nil {
		slog.Warn("client pipeline wire failed; LLM background processing disabled",
			"error", err)
	}
	stage("wirePipelineRuntime done")
	// c.pipeline + c.segmentMgr assigned above (or left nil); the atomic Store
	// publishes them — readers seeing PipelineReady()==true observe the wired
	// handles. Do NOT reorder the Store before the field writes.
	c.markPipelineReady()

	// Background hourly transcript upload — gated on NoTranscriptUpload (set under
	// --headless) so an embedded daemon spawns no upload loops. See
	// maybeStartTranscriptUpload for the full rationale.
	c.maybeStartTranscriptUpload(ctx, f)
	stage("transcript upload loop spawned")
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
	// Record whether --root was explicitly set (vs the "." default) via the
	// SAME shared helper ParseFlags uses, so the ast walk-root guard sees an
	// identical was-set bit on both parse paths.
	applyRootDirSet(fs, &cfg)

	// Normalize --headless into its implied gate set ONCE, before cfg flows into
	// its consumer: buildClient → wireRuntimesBackground (the worker /
	// propagation / pipeline / transcript gates) reads the expanded bools.
	applyHeadless(&cfg)

	// Mirror bootstrap.Run's setup (run.go:62-71) — runServe is a
	// subcommand entry that does not pass through Run.
	var logLevelVar slog.LevelVar // defaults to Info
	if err := logLevelVar.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		slog.Warn("invalid --log-level, using info", "value", cfg.LogLevel)
	}
	setupLogging(&cfg, &logLevelVar)
	applyMemoryLimit()
	cfg.GraphStorage = expandTilde(cfg.GraphStorage)

	// AFTER setupLogging, so the "pprof profiling enabled" line (or the bind
	// warning) lands on the configured sink rather than a default handler.
	applyPprof(&cfg)

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

	hs := graphclient.NewHTTPServer(c.mcpClient, *httpPort, cfg.AllowedWebOrigins)

	// Bind-first (bind-first startup): the MCPClient + HTTPServer above reference c only via
	// func-field injection (InterceptChain=c.runInterceptChain,
	// Dispatch=c.engineDispatch) and reach the runtime handles lazily through the
	// Phase-1 accessors, so they are safe to construct before the runtimes wire.
	// Launch the background wiring chain HERE, behind the listener, so net.Listen
	// (hs.Run below) executes ~25ms after process start instead of blocking on the
	// ~2.6s wire chain. The cleanup closure (deferred above) cancels c.wireCtx and
	// bounded-joins this goroutine on shutdown; the runtime-dependent tool surfaces
	// return the loud "daemon still starting" error until each stage's readiness
	// flag is set.
	go c.wireRuntimesBackground(c.wireCtx, cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	return hs.Run(ctx)
}
