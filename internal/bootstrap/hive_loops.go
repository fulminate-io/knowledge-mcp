// SPDX-License-Identifier: Apache-2.0

// hive_loops.go — the hive daemon Monitor + peer machine-down reaper wiring
// (startHiveLoops), extracted from daemon.go. runServe defers the returned stop
// closure so the hive Stops drain ahead of drainOnShutdown. The
// daemonStopDeadline const and the *client type live in daemon.go / the rest of
// the bootstrap package.

package bootstrap

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// startHiveLoops wires the hive daemon Monitor and the peer machine-down reaper,
// each gated on its Config bool (NoHiveMonitor / NoHiveReaper, both set under
// --headless so an embedded daemon coordinates no hive). It returns a stop
// closure the caller defers: it drains whichever loops actually started — reaper
// first, then monitor, preserving the prior LIFO defer order — and is a no-op for
// a gated-off or failed-to-start loop. BuildHiveSupervisor is built INSIDE the
// monitor gate so headless never constructs an unused Tier-2 LLM supervisor client.
//
// Each Stop is bounded to daemonStopDeadline (3s) — see the unified
// shutdown-budget comment on that const and the Makefile daemon-stop drain loop.
func (c *client) startHiveLoops(cfg Config, hs *graphclient.HTTPServer) func() {
	var monitorStop, reaperStop func()

	if !cfg.NoHiveMonitor {
		// Build the Tier-2 hive supervisor once. nil when config is unloaded or the
		// supervisor model is unresolvable — the escalation path then degrades to the
		// conservative log-only fallback below.
		sup, _ := llmproviders.BuildHiveSupervisor(context.Background())

		// The Monitor shares the *client's claim Registry (the SAME instance
		// ClaimRegistry() returns, so InterceptHive's recorded claims are the ones the
		// Monitor renews), reads live sessions via hs.SessionSnapshots, renews the
		// cloud lease via the login-routed c.router, and records each tick's
		// Mcp→harness resolution into the shared BanSet (so the InterceptHive ban gate
		// can translate an Mcp-Session-Id to the banned harness id).
		//
		// The EscalationFunc runs the Tier-2 supervisor when one is configured
		// (ack-on-behalf / evict+ban / conservative resume-renew via SupervisorHandler),
		// else a log-only warning. monitor is declared first and assigned after because
		// the escalate closure passed INTO NewMonitor needs the monitor's ResumeRenew;
		// escalate only runs from a tick AFTER Start, by which point monitor is non-nil.
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
			monitorStop = func() {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), daemonStopDeadline)
				defer stopCancel()
				_ = monitor.Stop(stopCtx)
			}
		}
	}

	if !cfg.NoHiveReaper {
		// The reaper shares c.router (EVICT routes cloud when logged in, fails loud
		// locally otherwise) and hs.SessionSnapshots. No supervisor / escalate closure
		// — it is a stale-last_seen timestamp comparison, role-gated by cloud member
		// roles. Same start-error-tolerant + bounded-Stop pattern as the Monitor.
		reaper := hivemonitor.NewHiveReaper(hs.SessionSnapshots, c.router, hivemonitor.DefaultReaperConfig())
		if err := reaper.Start(context.Background()); err != nil {
			slog.Warn("hive reaper: failed to start; peer machine-down sweep disabled this session", "error", err)
		} else {
			reaperStop = func() {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), daemonStopDeadline)
				defer stopCancel()
				_ = reaper.Stop(stopCtx)
			}
		}
	}

	return func() {
		// Reaper first, then monitor — preserves the prior LIFO defer drain order.
		if reaperStop != nil {
			reaperStop()
		}
		if monitorStop != nil {
			monitorStop()
		}
	}
}
