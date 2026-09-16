// SPDX-License-Identifier: Apache-2.0

// daemon_drain.go — the serve daemon's cleanup closure, lifted out of daemon.go
// for the file-length cap. Nothing but the location changed: buildClient still
// returns it, and the deadline budget it documents is still reconciled against
// the Makefile's daemon-stop SIGTERM window.

package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/profiling"
)

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
	// The account ladder's session rung is PROCESS-WIDE state this client
	// installed, so it is released first and unconditionally: everything below
	// is a subsystem of this client, while that one outlives it and would point
	// at a torn-down store.
	if c.releaseSessionAccounts != nil {
		c.releaseSessionAccounts()
	}
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
	if c.PipelineReady() && c.pipelineStop != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), daemonStopDeadline)
		defer stopCancel()
		// REPORTED, NOT DISCARDED: Stop's error names WHICH of its three bounded
		// waits did not finish (collectors, dispatchers, workers) — the difference
		// between knowing the embed workers were still running at exit and not.
		if err := c.pipelineStop(stopCtx); err != nil {
			slog.Warn("pipeline did not finish draining before the shutdown deadline; abandoning the wait",
				"deadline", daemonStopDeadline, "error", err)
		}
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
