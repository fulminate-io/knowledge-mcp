// SPDX-License-Identifier: Apache-2.0

// hive_loops.go — the hive daemon Monitor + peer machine-down reaper wiring
// (startHiveLoops), extracted from daemon.go. The loops are INSTALLED at boot,
// not started: they run only while at least one MCP session on this daemon is
// hive-active, and stop when the last one ends, so a daemon that never joins a
// hive runs neither loop and issues none of their periodic traffic. The two
// Config bools (NoHiveMonitor / NoHiveReaper) remain hard off-switches on top of
// that lifecycle. runServe defers the returned stop closure, which drains
// whatever is running — reaper first, then monitor — ahead of drainOnShutdown.
// The daemonStopDeadline const and the *client type live in daemon.go / the rest
// of the bootstrap package.
//
// TEARDOWN — the ONE bounded window this lifecycle leaves. A harness that exits
// without sending DELETE /mcp leaves its MCP session to the idle reaper, so the
// loops can outlive the last real hive activity by up to the MCP session idle
// TTL (defaultSessionIdleTTL, graphclient/http_session.go). That is the existing
// session lifetime rather than a new timer this wiring introduces, and it errs
// in the safe direction: for as long as the session is still considered live,
// the worker's lease keeps being renewed rather than dropped.
//
// RESTART — coverage and residual, both stated, because only one of them is
// closed. COVERED: a session that reconnects within the boot re-detection window
// (armed at the reaper's own machine-down threshold) is re-marked from the hive
// membership it still holds, so both loops resume with NO hive call from the
// agent. That covered case is what protects the hive skill's shipped promise to
// the worker — the daemon "keeps your claim alive while you work, even across a
// long sub-task that makes no hive calls" (assets/skills/hive/SKILL.md) — which
// a missed heartbeat would break: a peer reaper reads the member as machine-down
// and evicts it, and eviction is terminal, blocking the member's in-flight work
// instead of returning it to the queue, while that worker is alive and still
// working on it. RESIDUAL, named rather than papered over: a session whose FIRST
// reconnect lands after the window is not re-detected, and recovers on its next
// hive call instead. The window is set to the machine-down threshold precisely
// because that is the last moment re-detection could still have prevented an
// eviction, so the residual is the case this mechanism could not have saved.

package bootstrap

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// startHiveLoops INSTALLS the hive daemon Monitor and the peer machine-down
// reaper rather than starting them: it builds the lifecycle controller, arms the
// boot re-detection window, and hands the controller to the claim Registry as
// its activity and session-open hooks. Neither loop runs until a session on this
// daemon is hive-active; both stop when the last one ends. The two Config bools
// (NoHiveMonitor / NoHiveReaper, both set under --headless so an embedded daemon
// coordinates no hive) stay hard off-switches on top. Nothing here builds a
// Tier-2 supervisor either, so a daemon that never joins a hive never constructs
// an unused supervisor client.
//
// It returns the stop closure the caller defers: it drains whichever loops are
// running at shutdown — reaper first, then monitor, preserving the prior LIFO
// defer order — and is a no-op when neither is.
//
// Each Stop is bounded to daemonStopDeadline (3s) — see the unified
// shutdown-budget comment on that const and the Makefile daemon-stop drain loop.
func (c *client) startHiveLoops(cfg Config, hs *graphclient.HTTPServer) func() {
	return c.newHiveLoops(cfg, hs).stopAll
}

// newHiveLoops assembles, arms and installs the controller. It is separate from
// startHiveLoops only so the lifecycle can be driven and observed directly in
// tests, which the returned stop closure alone does not allow.
func (c *client) newHiveLoops(cfg Config, hs *graphclient.HTTPServer) *hiveLoops {
	// BOTH hive loops are background daemons with no originating tool call, so
	// both take the SAME stamping caller — declared once here rather than
	// per-loop, because the two constructions drifting apart is exactly how the
	// reaper ended up issuing its sweeps as client.unstamped while the monitor's
	// were attributed. It is wrapped at CONSTRUCTION rather than inside
	// hivemonitor because graphclient imports hivemonitor — the reverse import
	// would be a cycle.
	hiveCaller := hiveCallerStampingOperation{inner: c.router}

	// The reaper config is fetched exactly ONCE: it configures the reaper AND
	// supplies the boot re-detection window length, and a second call would be a
	// second source of truth for the same number.
	reaperCfg := hivemonitor.DefaultReaperConfig()

	l := &hiveLoops{
		noMonitor:       cfg.NoHiveMonitor,
		noReaper:        cfg.NoHiveReaper,
		hive:            hiveCaller,
		snapshots:       hs.SessionSnapshots,
		registry:        c.claimRegistry,
		ban:             c.banSet,
		reaperCfg:       reaperCfg,
		redetectChecked: map[string]bool{},
	}

	// Arm the boot re-detection window at a DERIVED length: the reaper's own
	// machine-down threshold. Re-detection is capped at one browse per resolved
	// session regardless of window length, so the length does not trade cost
	// against coverage — it selects which sessions are still eligible. The right
	// interval is therefore the one within which re-detection can still prevent
	// an eviction, which is exactly the staleness a peer reaper measures against:
	// shorter forfeits members that were still saveable, longer buys nothing
	// because past the threshold the member has already been evicted. This runs
	// once per daemon start, immediately before the HTTP server serves.
	l.redetectUntil = time.Now().Add(reaperCfg.MachineDownThreshold)

	// Both hooks are nil-safe on the Registry, so a *client built without one is
	// unaffected.
	c.claimRegistry.SetHiveActivityHook(l.reconcile)
	c.claimRegistry.SetSessionOpenHook(l.onSessionOpened)
	return l
}

// hiveLoops owns the hive daemon Monitor and the peer machine-down reaper for
// one daemon and reconciles them against the number of hive-active MCP sessions:
// both loops run while at least one session is hive-active on this daemon and
// neither runs otherwise. The two Config bools (NoHiveMonitor / NoHiveReaper,
// both set under --headless so an embedded daemon coordinates no hive) sit ON TOP
// as hard off-switches — a gated-off loop never starts, whatever the session count.
//
// Every dependency is an interface or a func value rather than a concrete
// client/router type, so the controller is constructible in a test with a fake
// HiveCaller.
type hiveLoops struct {
	noMonitor bool                                 // cfg.NoHiveMonitor, captured at wiring time
	noReaper  bool                                 // cfg.NoHiveReaper
	hive      hivemonitor.HiveCaller               // the stamping caller: both loops, the supervisor handler and the re-detection browse
	snapshots func() []hivemonitor.SessionSnapshot // HTTPServer.SessionSnapshots
	registry  *hivemonitor.Registry                // the *client's claim Registry — the SAME instance
	ban       *hivemonitor.BanSet
	reaperCfg hivemonitor.ReaperConfig // fetched once in newHiveLoops

	mu      sync.Mutex
	monitor *hivemonitor.Monitor
	reaper  *hivemonitor.HiveReaper
	// sup is the Tier-2 supervisor, resolved on the first activation and cached
	// thereafter — supBuilt records that the resolution HAPPENED, so a daemon
	// with no supervisor config caches the nil rather than re-resolving on every
	// activation.
	sup      llmproviders.Supervisor
	supBuilt bool
	// redetectUntil is the boot re-detection window deadline; a zero value means
	// the window is disarmed and session-open events do nothing at all.
	redetectUntil time.Time
	// redetectChecked records the MCP session ids re-detection has already
	// resolved, so each is browsed at most once per daemon start.
	redetectChecked map[string]bool
}

// reconcile is the Registry activity hook: it starts the loops while any session
// is hive-active and stops them when none is.
//
// It RE-READS the count rather than trusting a transition edge. The Registry
// fires the hook outside its lock, so two transitions racing can deliver their
// invocations in either order; re-reading makes whichever invocation runs last
// observe the true count and reconcile to it.
func (l *hiveLoops) reconcile() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.registry.HiveActiveCount() > 0 {
		l.startLocked()
		return
	}
	l.stopLocked()
}

// startLocked brings up whichever loops are not gated off. It is idempotent: a
// second hive-active session finds the loops already running and builds nothing,
// so there is no double-start and no leaked first pair.
func (l *hiveLoops) startLocked() {
	if l.monitor != nil || l.reaper != nil {
		return
	}
	if !l.supBuilt {
		// nil when config is unloaded or the supervisor model is unresolvable —
		// the escalation path then degrades to the conservative log-only fallback.
		l.sup, _ = llmproviders.BuildHiveSupervisor(context.Background())
		l.supBuilt = true
	}
	l.startMonitorLocked()
	l.startReaperLocked()
	if l.monitor != nil || l.reaper != nil {
		slog.Info("hive loops: started for this daemon's hive session(s)",
			"monitor", l.monitor != nil, "reaper", l.reaper != nil)
	}
}

// startMonitorLocked builds and starts a FRESH Monitor. Fresh per activation
// rather than reused: the Monitor carries per-claim state (follow offsets, idle
// timers, escalated flags) meaningful only within one hive session, and carrying
// it across sessions would suppress a later session's escalation for a recycled
// message id.
//
// The Monitor shares the *client's claim Registry (the SAME instance
// ClaimRegistry() returns, so InterceptHive's recorded claims are the ones the
// Monitor renews), reads live sessions via HTTPServer.SessionSnapshots, renews
// the cloud lease through the stamping caller, and records each tick's
// Mcp→harness resolution into the shared BanSet (so the InterceptHive ban gate
// can translate an Mcp-Session-Id to the banned harness id).
func (l *hiveLoops) startMonitorLocked() {
	if l.noMonitor {
		return
	}
	sup := l.sup

	// The EscalationFunc runs the Tier-2 supervisor when one is configured
	// (ack-on-behalf / evict+ban / conservative resume-renew via SupervisorHandler),
	// else a log-only warning. monitor is declared first and assigned after because
	// the escalate closure passed INTO NewMonitor needs the monitor's ResumeRenew;
	// escalate only runs from a tick AFTER Start, by which point monitor is non-nil.
	//
	// The escalation path builds its handler through supervisorHandler, which
	// takes the stamping caller, so the supervisor's ack-on-behalf / evict RPCs
	// are attributed like the loops around them.
	var monitor *hivemonitor.Monitor
	escalate := func(claim hivemonitor.Claim, sessionID string, state hivemonitor.LivenessState, handle hivemonitor.TranscriptHandle) {
		if sup == nil {
			slog.Warn("hive monitor: worker no longer making progress — no supervisor configured, lease will lapse",
				"session", sessionID, "msg", claim.MsgID, "hive", claim.Hive, "state", state.String())
			return
		}
		l.supervisorHandler(monitor, sup).Handle(context.Background(), claim, sessionID, state, handle)
	}
	monitor = hivemonitor.NewMonitor(
		l.registry,
		l.snapshots,
		l.hive,
		l.ban, // HarnessResolver: records mcp→harness for the ban gate.
		escalate,
		hivemonitor.DefaultMonitorConfig(),
	)
	if err := monitor.Start(context.Background()); err != nil {
		slog.Warn("hive monitor: failed to start; lease heartbeats disabled this session", "error", err)
		return
	}
	l.monitor = monitor
}

// supervisorHandler builds the Tier-2 escalation handler for one activation. It
// is a named seam rather than an inline call inside the escalate closure so a
// test can construct the SAME handler production does and observe which caller
// its RPCs travel through; the closure itself is unreachable from a test.
//
// resume is the live Monitor, whose ResumeRenew is the conservative
// un-escalate path. The fields it reads are set at construction and never
// mutated, so it needs no lock.
//
// hive is the STAMPING caller. The supervisor's ack-on-behalf and evict RPCs
// are background daemon traffic with no originating tool call, exactly like the
// monitor's heartbeats and the reaper's sweeps, so they carry the same
// query-origin operation instead of arriving with none.
func (l *hiveLoops) supervisorHandler(resume *hivemonitor.Monitor, sup llmproviders.Supervisor) *hivemonitor.SupervisorHandler {
	return hivemonitor.NewSupervisorHandler(l.hive, l.ban, resume, sup)
}

// startReaperLocked builds and starts a FRESH HiveReaper. It shares the same
// stamping caller (EVICT routes cloud when logged in, fails loud locally
// otherwise) and the same session snapshots as the Monitor. No supervisor /
// escalate closure — it is a stale-last_seen timestamp comparison, role-gated by
// cloud member roles. Same start-error-tolerant pattern as the Monitor.
func (l *hiveLoops) startReaperLocked() {
	if l.noReaper {
		return
	}
	reaper := hivemonitor.NewHiveReaper(l.snapshots, l.hive, l.reaperCfg)
	if err := reaper.Start(context.Background()); err != nil {
		slog.Warn("hive reaper: failed to start; peer machine-down sweep disabled this session", "error", err)
		return
	}
	l.reaper = reaper
}

// stopLocked drains whichever loops are running, reaper first then monitor,
// preserving the prior LIFO defer drain order.
//
// A Stop that exceeds its deadline is logged and its field is still cleared:
// both Stops cancel their loop's context BEFORE waiting, so a deadline-exceeded
// loop is already cancelled and drains on its own, whereas keeping the pointer
// would wedge the controller permanently in "running". Because every activation
// builds fresh instances, the worst case of a slow Stop is one briefly
// overlapping tick, never a permanent double loop.
func (l *hiveLoops) stopLocked() {
	if l.reaper != nil {
		stopHiveLoopBounded("reaper", l.reaper.Stop)
		l.reaper = nil
	}
	if l.monitor != nil {
		stopHiveLoopBounded("monitor", l.monitor.Stop)
		l.monitor = nil
	}
}

// stopHiveLoopBounded runs one loop's Stop under the shared daemon stop deadline.
func stopHiveLoopBounded(loop string, stop func(context.Context) error) {
	stopCtx, stopCancel := context.WithTimeout(context.Background(), daemonStopDeadline)
	defer stopCancel()
	if err := stop(stopCtx); err != nil {
		slog.Warn("hive loops: bounded stop exceeded its deadline; the loop is already cancelled and drains on its own",
			"loop", loop, "error", err)
	}
}

// stopAll drains everything that is running. This is the closure runServe defers
// at shutdown.
func (l *hiveLoops) stopAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopLocked()
}

// running reports whether each loop is currently up. The production cadences are
// 30s and 60s, so no test can observe a tick, and a goroutine count alone cannot
// tell WHICH loop started — this is how the lifecycle is asserted.
func (l *hiveLoops) running() (monitor, reaper bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.monitor != nil, l.reaper != nil
}

// onSessionOpened is the Registry session-open hook: the boot re-detection pass
// that restores hive membership a daemon restart would otherwise drop on the
// floor. A worker whose harness reconnects after a restart still holds a
// non-evicted hive_member in the cloud, but this daemon has forgotten it, so
// nothing would renew its lease until its next hive call.
//
// Outside the boot window this returns immediately and issues no RPC, so
// re-detection contributes exactly zero traffic in the steady state.
func (l *hiveLoops) onSessionOpened(sessionID string) {
	l.mu.Lock()
	if l.redetectUntil.IsZero() || time.Now().After(l.redetectUntil) {
		l.mu.Unlock()
		return
	}
	// Collect under the lock, act outside it — see redetectSession's deadlock note.
	var snaps []hivemonitor.SessionSnapshot
	if l.snapshots != nil {
		snaps = l.snapshots()
	}
	checked := make(map[string]bool, len(l.redetectChecked))
	maps.Copy(checked, l.redetectChecked)
	l.mu.Unlock()

	slog.Debug("hive loops: boot re-detection pass", "session", sessionID, "sessions", len(snaps))
	for _, snap := range snaps {
		if checked[snap.ID] {
			continue
		}
		l.redetectSession(context.Background(), snap)
	}
}

// redetectSession re-detects one session's hive membership: resolve its
// transcript, browse its hive_member nodes once, and mark the session
// hive-active when it still holds a member the cloud has not evicted.
//
// DEADLOCK HAZARD: MarkHiveActive fires the activity hook, which is reconcile,
// which takes l.mu — so neither the browse nor the mark may run while l.mu is
// held. That is why the caller collects under the lock and calls this outside it;
// do not "simplify" the pass back under the lock.
//
// A session whose transcript does NOT resolve is left UNCHECKED: peer-cwd
// resolution is best-effort and degrades, so a transient miss must not
// permanently forfeit that session's recovery path — it is retried on a later
// session-open event still inside the window. The cost bound is therefore one
// browse per RESOLVED session, once.
func (l *hiveLoops) redetectSession(ctx context.Context, snap hivemonitor.SessionSnapshot) {
	handle, err := hivemonitor.ResolveTranscript(ctx, snap)
	if err != nil {
		slog.Warn("hive loops: boot re-detection transcript resolve error", "session", snap.ID, "error", err)
		return
	}
	if !handle.Resolved() {
		return
	}
	l.mu.Lock()
	l.redetectChecked[snap.ID] = true
	l.mu.Unlock()

	if !l.hasLiveMember(ctx, handle.HarnessSessionID) {
		return
	}
	slog.Info("hive loops: re-detected an existing hive member after restart", "session", snap.ID)
	l.registry.MarkHiveActive(snap.ID)
}

// hasLiveMember reports whether the harness session still holds a hive_member
// the cloud has not evicted. One predicate-narrowed browse, mirroring the
// Monitor's memberHivesFor query shape rather than inventing a second
// member-lookup shape. A read failure reports false — re-detection is a recovery
// path, and the session recovers on its next hive call.
func (l *hiveLoops) hasLiveMember(ctx context.Context, harnessSessionID string) bool {
	if l.hive == nil || harnessSessionID == "" {
		return false
	}
	resp, err := l.hive.Execute(ctx, &knowledgev1.ExecuteRequest{
		Target: &knowledgev1.GraphSelector{Graph: "knowledge"},
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{
				NodeTypes: []string{"hive_member"},
				MetadataPredicates: []*knowledgev1.MetadataPredicate{
					{Key: "session", Op: knowledgev1.MetadataPredicate_OP_EQ, Value: harnessSessionID},
				},
			},
		}},
	})
	if err != nil {
		slog.Warn("hive loops: boot re-detection member read failed", "member", harnessSessionID, "error", err)
		return false
	}
	for _, n := range resp.GetNodes() {
		if n.GetStatus() != "evicted" && n.GetMetadata()["status"] != "evicted" {
			return true
		}
	}
	return false
}

// hiveCallerStampingOperation wraps the injected HiveCaller so BOTH hive
// daemons — the monitor's lease heartbeats and the reaper's machine-down sweeps
// and evictions — carry a query-origin operation, and so does the Tier-2
// supervisor's ack-on-behalf and evict traffic on the monitor's escalation path.
// The wrapper lives HERE rather than in hivemonitor because graphclient imports
// hivemonitor (http_session.go), so hivemonitor importing graphclient would
// close an import cycle. Bootstrap imports both, which makes it the natural
// place to compose them.
type hiveCallerStampingOperation struct {
	inner hivemonitor.HiveCaller
}

func (h hiveCallerStampingOperation) Hive(
	ctx context.Context, req *knowledgev1.HiveRequest,
) (*knowledgev1.HiveResponse, error) {
	return h.inner.Hive(graphclient.WithOperation(ctx, graphclient.OpHiveMonitor), req)
}

// Execute stamps the same operation on the per-tick hive_member reads (the
// Monitor's liveness pass and the reaper's role-gate + stale-member scan).
// Both halves of HiveCaller must be wrapped: stamping only the renew RPC would
// leave the graph read arriving unstamped from the same background loop.
func (h hiveCallerStampingOperation) Execute(
	ctx context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	return h.inner.Execute(graphclient.WithOperation(ctx, graphclient.OpHiveMonitor), req)
}
