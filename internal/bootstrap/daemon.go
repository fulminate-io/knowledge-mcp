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
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// the bind-first startup change UNIFIED SHUTDOWN BUDGET (keep these in sync with the Makefile
// daemon-stop drain loop count): on SIGTERM, runServe drains SIX Stops
// SEQUENTIALLY in LIFO defer order — reaper.Stop + monitor.Stop (deferred in
// runServe), then drainOnShutdown's wireJoinDeadline wait + pipeline.Stop +
// runtime.Stop + propLoop.Stop. The budget inequality is:
//
//	reaper.Stop + monitor.Stop + wireJoinDeadline + pipeline.Stop + runtime.Stop +
//	propLoop.Stop  <=  Makefile daemon-stop drain window.
//
// Every Stop is cooperative (the monitor + reaper cancel their ctx before their
// bounded wg.Wait; PropagationLoop.Stop cancels baseCtx before its inFlight drain;
// the pipeline drains its bounded worker pool), so a clean drain finishes in a few
// seconds — the per-Stop deadlines below are abandon-backstops, not expected
// durations. Pinned to 3s each → 6 * 3s = 18s worst-case, under the Makefile's 20s
// drain window. If you change one of these, re-check the Makefile loop count (and
// the reaper/monitor Stop deadlines in runServe) so the inequality still holds.
const (
	// wireJoinDeadline bounds how long drainOnShutdown waits for the background
	// wiring goroutine (wireRuntimesBackground) to finish before it gives up and
	// drains only the already-ready subsystems.
	wireJoinDeadline = 3 * time.Second
	// daemonStopDeadline is the per-Stop drain bound for the pipeline, dream
	// Runner, PropagationLoop, hive monitor, and hive reaper. See the budget note
	// above.
	daemonStopDeadline = 3 * time.Second
)

// buildClient constructs the MCP client + runs the cheap synchronous bind-ready
// prefix (constructClient, the boot-spawn decision, the one-shot asset-drift
// hints), then returns the *client ready to BIND the HTTP MCP listener. The
// background runtimes (dream worker Runner, PropagationLoop, LLM pipeline,
// instruction bootstrap) are NOT wired here — runServe launches
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
// Deadline budget (reconciled in Phase 3): wireJoinDeadline + the three sequential
// Stop deadlines below must fit inside the Makefile daemon-stop SIGTERM window so
// a clean drain completes before SIGKILL.
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
	// Flag-gated drain in the fixed order (pipeline, dream Runner,
	// PropagationLoop). The readiness flag guarantees the handle was published
	// before we read it; the nil-check is belt-and-suspenders. Each Stop is bounded
	// to daemonStopDeadline (3s) — see the unified shutdown-budget comment on that
	// const and the Makefile daemon-stop drain loop.
	if c.PipelineReady() && c.pipeline != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), daemonStopDeadline)
		defer stopCancel()
		_ = c.pipeline.Stop(stopCtx)
	}
	if c.WorkerReady() && c.runtime != nil {
		// The dream Runner's Stop is an ABANDON-on-deadline backstop: it closes
		// stopCh + waits inFlight up to the deadline but does NOT cancel the
		// per-invocation ctxs (Start was called with context.Background()), so an
		// in-flight LLM ReAct worker is abandoned (slog.Warn) at the deadline rather
		// than cancelled. That is acceptable — a worker invocation is an LLM call,
		// not state-critical local work, and explicit Cancel() exists. Bounding the
		// deadline to 3s only keeps an abandoned worker from stretching shutdown past
		// the SIGTERM window; cooperative cancellation of the Runner is out of scope
		// for the bind-first startup change.
		c.runtime.Stop(daemonStopDeadline)
	}
	if c.PropReady() && c.propLoop != nil {
		c.propLoop.Stop(daemonStopDeadline)
	}
}

// wireRuntimesBackground wires every client-side background runtime — dream
// worker Runner, client embedder, PropagationLoop, LLM pipeline, instruction
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

	if !f.NoWorkerRuntime {
		if err := wireWorkerRuntime(c, f); err != nil {
			slog.Warn("dream worker runtime unavailable; worker.trigger/status will return errors but other tools work",
				"error", err)
		}
		stage("wireWorkerRuntime done")
	} else {
		slog.Debug("dream worker runtime skipped (--no-worker-runtime)")
	}
	// c.runtime is assigned above (wireWorkerRuntime, or left nil on degrade);
	// the atomic Store publishes it — a reader seeing WorkerReady()==true observes
	// the wired handle. Do NOT reorder the Store before the field write.
	c.markWorkerReady()

	if ctx.Err() != nil {
		return
	}

	// Wire the client-side embedder so InterceptSearch / InterceptQuery
	// can embed query text on the client side (Phase 4.5). nil when no
	// voyage_api_key is configured — the search path then runs BM25-only.
	// The server holds no embedder at all by design, so query embedding is
	// exclusively client-side. BuildEmbedder populates c.embedder, which the
	// search/query intercepts read LAZILY via deps.Embedder() at call time —
	// there is NO wiring-order dependency between it and wirePropagationRuntime
	// below (WithTopicDeps takes only scanner + summarizer, no embedder). The
	// existing call order is preserved purely to avoid needless churn.
	c.embedder = llmproviders.BuildEmbedder()
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

	// LLM precheck runs ASYNC (2026-05-20) against the config loaded as
	// a side-effect of wireWorkerRuntime → buildRuntime →
	// config.LoadOrAutoDetect. Empty config silently no-ops; misconfigured
	// keys surface as `slog.Error "llmproviders: precheck failed"` from
	// the background goroutine rather than blocking the MCP handshake.
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

	// Construct the client-side segment Manager UNCONDITIONALLY (router-guarded),
	// BEFORE wirePipelineRuntime — see ensureSegmentManager. This is the
	// READ/CONSUME engine the search intercepts query, and it must exist even
	// offline so search serves BM25 over existing segments; wirePipelineRuntime
	// then only attaches this already-built instance to the producer for shipping.
	// The L2 cache roots under f.GraphStorage (<graph-storage>/segments) — the same
	// tilde-expanded data root the client spawns the local server with — so client
	// L2 and server store co-locate instead of leaking to a HOME-fixed path.
	c.ensureSegmentManager(f.GraphStorage)

	// Wire the client-side LLM pipeline (Phase 6). Builds summarizer +
	// embedder + worker pools, runs the initial graph-list registration,
	// then spawns a background refresh goroutine that replaces the
	// deleted server-side RegisterPipelineHooks. nil-safe Stop is run by
	// the cleanup closure.
	if err := wirePipelineRuntime(c, f); err != nil {
		slog.Warn("client pipeline wire failed; LLM background processing disabled",
			"error", err)
	}
	stage("wirePipelineRuntime done")
	// c.pipeline + c.segmentMgr assigned above (or left nil); the atomic Store
	// publishes them — readers seeing PipelineReady()==true observe the wired
	// handles. Do NOT reorder the Store before the field writes.
	c.markPipelineReady()

	// Seed agent + skill nodes from .claude/{agents,
	// skills}/*.md. Server-side projects.Bootstrap call has been
	// removed; the client owns disk I/O for code-graph + project
	// assets now. Non-fatal — startup continues on error.
	if err := runInstructionBootstrap(context.Background(), c.router, f.RootDir); err != nil {
		slog.Warn("instruction bootstrap failed; agent/skill nodes will not be seeded this session",
			"error", err)
	}
	stage("instruction bootstrap done")
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

	// Build the Tier-2 hive supervisor once. nil when config is unloaded or the
	// supervisor model is unresolvable — the escalation path then degrades to the
	// conservative log-only fallback below.
	sup, _ := llmproviders.BuildHiveSupervisor(context.Background())

	// Wire the hive daemon Monitor: it shares the *client's claim Registry (the
	// SAME instance ClaimRegistry() returns, so InterceptHive's recorded claims
	// are the ones the Monitor renews), reads live sessions via
	// hs.SessionSnapshots, renews the cloud lease via the login-routed c.router,
	// and records each tick's Mcp→harness resolution into the shared BanSet (the
	// SAME instance BanSet() returns, so the InterceptHive ban gate can translate
	// a request's Mcp-Session-Id to the banned harness id).
	//
	// The EscalationFunc runs the Tier-2 supervisor when one is configured: it
	// formats the worker transcript, judges it, and drives ack-on-behalf /
	// evict+ban / conservative resume-renew (the SupervisorHandler owns the
	// verdict→action matrix). When no supervisor is configured it falls back to a
	// log-only warning. The handler needs the monitor's ResumeRenew, but the
	// closure is passed INTO NewMonitor, so monitor is declared first and assigned
	// after — the escalate closure only runs from a tick AFTER Start, by which
	// point monitor is non-nil.
	var monitor *hivemonitor.Monitor
	escalate := func(claim hivemonitor.Claim, sessionID string, state hivemonitor.LivenessState, handle hivemonitor.TranscriptHandle) {
		if sup == nil {
			slog.Warn("hive monitor: worker no longer making progress — no supervisor configured, lease will lapse",
				"session", sessionID, "msg", claim.MsgID, "hive", claim.Hive, "state", state.String())
			return
		}
		handler := hivemonitor.NewSupervisorHandler(c.router, c.banSet, monitor, sup)
		handler.Handle(context.Background(), claim, sessionID, state, handle)
	}
	monitor = hivemonitor.NewMonitor(
		c.claimRegistry,
		hs.SessionSnapshots,
		c.router,
		c.banSet, // HarnessResolver: records mcp→harness for the ban gate.
		escalate,
		hivemonitor.DefaultMonitorConfig(),
	)
	if err := monitor.Start(context.Background()); err != nil {
		slog.Warn("hive monitor: failed to start; lease heartbeats disabled this session", "error", err)
	} else {
		defer func() {
			// daemonStopDeadline (3s): the Monitor cancels its ctx before its bounded
			// wg.Wait, so it drains sub-second; the cap is the abandon-backstop. See
			// the unified shutdown-budget comment on daemonStopDeadline.
			stopCtx, stopCancel := context.WithTimeout(context.Background(), daemonStopDeadline)
			defer stopCancel()
			_ = monitor.Stop(stopCtx)
		}()
	}

	// Wire the deterministic peer machine-down reaper alongside the Monitor: it
	// shares c.router (the same login-routed HiveCaller — EVICT routes cloud when
	// logged in, fails loud locally otherwise) and hs.SessionSnapshots (the same
	// live-session source). No supervisor / escalate closure — the reaper is
	// purely a stale-last_seen timestamp comparison, role-gated by cloud member
	// roles. Same start-error-tolerant + deferred-Stop pattern as the Monitor.
	reaper := hivemonitor.NewHiveReaper(hs.SessionSnapshots, c.router, hivemonitor.DefaultReaperConfig())
	if err := reaper.Start(context.Background()); err != nil {
		slog.Warn("hive reaper: failed to start; peer machine-down sweep disabled this session", "error", err)
	} else {
		defer func() {
			// daemonStopDeadline (3s): the reaper cancels its ctx before its bounded
			// wg.Wait, so it drains sub-second; the cap is the abandon-backstop. See
			// the unified shutdown-budget comment on daemonStopDeadline.
			stopCtx, stopCancel := context.WithTimeout(context.Background(), daemonStopDeadline)
			defer stopCancel()
			_ = reaper.Stop(stopCtx)
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	return hs.Run(ctx)
}
