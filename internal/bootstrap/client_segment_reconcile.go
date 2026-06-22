// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// reconcileSegmentCoverage is the startup + periodic read-side reconcile: it
// enumerates every segment-bearing builtin (the embeddable graph types
// kgtypes.HasRebuildableSegments admits — knowledge/default explicit plus every
// instance of code, cloud, cicd, practice via ListGraphNamesOfType), probes each
// for the LIVE-resident-vs-shipped degeneracy a daemon restart
// can leave behind (a fully-embedded, un-recollected graph whose searchable pool
// collapsed to empty with no embed-drain or search to re-trigger a heal), and
// heals a degenerate one through the PROVEN RebuildSegments path — the SAME
// single-flight the manual rebuild op and the embed-drain auto-heal share, so the
// three triggers coalesce onto one run rather than racing three rebuilds.
//
// It is the recovery lever the prior two fixes left a gap for: the read-side
// recoverIfDegenerate only runs lazily inside a Search, and the write-side
// auto-heal only fires on the collect-armed embed-drain edge — neither event fires
// for a graph that is fully embedded and never re-collected, so its empty pool had
// no trigger to repopulate. This reconcile is INDEPENDENT of both events.
//
// Best-effort throughout: a nil segment manager (headless/--no-llm-pipeline) is a
// no-op; a per-graph probe or rebuild error WARNs and continues to the next graph,
// never blocking boot or the periodic tick. The probe is cheap (the healthy graphs
// each pay one cache-first load + one atomic resident count + at most one
// ListDelta(0); only a genuinely degenerate graph pays a rebuild).
func (c *client) reconcileSegmentCoverage(ctx context.Context) {
	if c.segmentMgr == nil {
		return // headless / degraded — no segment engine to reconcile.
	}

	// Every segment-bearing builtin: knowledge/default (seeded explicitly — its
	// default instance has an empty enumerated name that ListGraphNamesOfType drops)
	// plus every instance of the other embeddable builtins (code, cloud, cicd,
	// practice), enumerated through the SAME ListGraphNamesOfType seam the status
	// coverage table uses (the *client satisfies tools.ClientDeps). The
	// kgtypes.HasRebuildableSegments gate mirrors segCoveredFor's matching gate
	// (manage_status_coverage.go) so the reconcile probes exactly the graph set
	// manage(status) reports as segment-bearing — linkage + transformers (sync-
	// eligible but non-embeddable) are skipped, they carry no rebuildable segments.
	type graphRef struct {
		gt   kgtypes.GraphType
		name string
	}
	graphs := []graphRef{{gt: kgtypes.GraphKnowledge, name: "default"}}
	for _, gt := range kgtypes.SyncEligibleGraphTypes() {
		if gt == kgtypes.GraphKnowledge {
			continue // already seeded explicitly above (empty default-instance name).
		}
		if !kgtypes.HasRebuildableSegments(gt) {
			continue // linkage / transformers — no rebuildable segments.
		}
		names, err := tools.ListGraphNamesOfType(ctx, c, string(gt))
		if err != nil {
			slog.Warn("bootstrap: segment reconcile could not enumerate graphs of type (skipping this type this pass)",
				"graph_type", gt, "error", err)
			continue
		}
		for _, name := range names {
			graphs = append(graphs, graphRef{gt: gt, name: name})
		}
	}

	for _, g := range graphs {
		degenerate, err := c.segmentMgr.ReconcileResidentDegenerate(ctx, g.gt, g.name)
		if err != nil {
			slog.Warn("bootstrap: segment reconcile probe failed (continuing)",
				"graph_type", g.gt, "name", g.name, "error", err)
			continue
		}
		if !degenerate {
			continue // healthy (or disarmed) — no rebuild.
		}
		// Degenerate live pool: heal via the PROVEN rebuild path (single-flight shared
		// with the manual op + embed-drain auto-heal — NOT a bare load).
		ran, scanned, built, partial, pruned, rerr := tools.RebuildSegments(
			ctx, c.PipelineScanner(), c.segmentMgr, g.gt, g.name)
		if rerr != nil {
			slog.Warn("bootstrap: segment reconcile rebuild failed (continuing)",
				"graph_type", g.gt, "name", g.name, "error", rerr)
			continue
		}
		slog.Info("bootstrap: segment reconcile rebuilt a degenerate live pool",
			"graph_type", g.gt, "name", g.name,
			"ran", ran, "scanned", scanned, "built", built, "partial", partial, "pruned", len(pruned))
	}
}

// segmentReconcileInterval is the periodic cadence of runSegmentReconcileLoop — a
// fixed default for v1 (not config-driven). The probe is cheap (count compare +
// floor gate before the List RPC), so a few-minute cadence catches a mid-session
// collapse promptly without meaningful steady-state cost.
const segmentReconcileInterval = 5 * time.Minute

// segmentReconcileBootDelay is the delay before the one-shot boot reconcile fires
// (wirePipelineRuntime). It runs OFF the bind / markPipelineReady critical path — a
// graph degenerate after the L2-first load (cold/partial local L2 while the server
// holds the full corpus) heals within this delay rather than waiting up to the full
// segmentReconcileInterval for the periodic loop's first tick. It is deliberately
// long enough to fire well after readiness latches (so it never blocks the MCP
// listener bind) yet far shorter than the 5min periodic cadence.
const segmentReconcileBootDelay = 30 * time.Second

// bootDelayReconcile runs ONE reconcileSegmentCoverage pass segmentReconcileBootDelay
// after boot, OFF the bind / markPipelineReady critical path — the boot-time heal for
// a graph left degenerate after the L2-first load (cold/partial local L2 while the
// server holds the full corpus), which would otherwise wait up to the full
// segmentReconcileInterval for the periodic loop's first tick. Spawned with `go` from
// wirePipelineRuntime so the delay is awaited HERE, never on the wiring path. Exits
// promptly on ctx.Done (no leak); best-effort (reconcileSegmentCoverage swallows
// per-graph errors).
func (c *client) bootDelayReconcile(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(segmentReconcileBootDelay):
		c.reconcileSegmentCoverage(ctx)
	}
}

// runSegmentReconcileLoop fires reconcileSegmentCoverage on a fixed-interval ticker
// until ctx is canceled — the PERIODIC trigger (independent of embed-drain and
// search) for a graph that collapses, or was never re-collected, mid-session. It
// shares the one reconcile body with the startup trigger, so the startup-vs-periodic
// fork is just two call sites of the same function. Mirrors the RefreshLoadedGraphs
// select{ctx.Done / timer} loop shape; exits promptly on ctx.Done (no leak).
func (c *client) runSegmentReconcileLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reconcileSegmentCoverage(ctx)
		}
	}
}
