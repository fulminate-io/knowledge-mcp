// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// reconcileSegmentCoverage is the startup + periodic read-side reconcile: it
// enumerates every segment-bearing builtin (the embeddable graph types
// kgtypes.HasRebuildableSegments admits — knowledge/default explicit plus every
// instance of code, cloud, cicd, practice via ListGraphNamesOfType), probes each
// for the LIVE-resident-vs-shipped degeneracy a daemon restart
// can leave behind (a fully-embedded, un-recollected graph whose searchable pool
// collapsed to empty with no embed-drain or search to re-trigger a heal), and —
// ONLY when the healNeedsRebuild gate confirms the SHIPPED corpus is genuinely
// incomplete (not merely a lazily-loaded read engine) — heals it through
// the PROVEN RebuildSegments path — the SAME single-flight the manual rebuild op
// and the embed-drain auto-heal share, so the three triggers coalesce onto one run
// rather than racing three rebuilds.
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
// ListDelta(0); only a graph whose shipped corpus is genuinely incomplete — past
// the healNeedsRebuild gate — pays a rebuild).
func (c *client) reconcileSegmentCoverage(ctx context.Context) {
	if c.segmentMgr == nil {
		return // headless / degraded — no segment engine to reconcile.
	}
	// Periodic background loop with no originating tool call — it stamps its own
	// query-origin operation so its per-tick reads are attributable.
	ctx = graphclient.WithOperation(ctx, graphclient.OpSegmentReconcile)

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
		// reconcile diagnostic (kept per keep-debug-logging): on the degenerate branch
		// record the SHIPPED-corpus doc count vs the EMBEDDED node count. When shipped
		// covers embedded, the read engine is flagged degenerate only because it has
		// not loaded the intact corpus — a PG RebuildSegments writes the DETERMINISTIC
		// engine (not this read engine), so it cannot raise the resident count the
		// probe re-reads and the rebuild is wasted. The healNeedsRebuild gate below
		// acts on exactly this shipped-vs-embedded signal; this line makes it
		// observable per tick.
		if snapshot, serr := c.segmentMgr.ShippedManifestSnapshot(ctx, g.gt, g.name, hnsw.New().Name()); serr != nil {
			slog.Debug("bootstrap: segment reconcile degenerate-branch shipped probe failed",
				"graph_type", g.gt, "name", g.name, "error", serr)
		} else {
			shippedDocs, anyUnknown := c.segmentMgr.ShippedDocCountFromSnapshot(snapshot, hnsw.New().Name())
			embedded, eerr := tools.GraphEmbeddedCount(ctx, c.GraphCaller(), g.gt, g.name)
			slog.Debug("bootstrap: segment reconcile degenerate branch",
				"graph_type", g.gt, "name", g.name,
				"shipped_docs", shippedDocs, "any_unknown", anyUnknown,
				"embedded", embedded, "embedded_err", eerr)
		}
		// SHIPPED-COMPLETENESS GATE: ReconcileResidentDegenerate above flags
		// when the READ engine (m.managers) is below the SHIPPED corpus — which a
		// merely lazily-loaded read engine trips even when the shipped corpus is
		// COMPLETE. A PG RebuildSegments writes the DETERMINISTIC engine (m.detManagers),
		// so it can never raise the read engine's resident count; the next 5-min tick
		// re-flags and rebuilds again — the ~85 rebuilds/wk loop. healNeedsRebuild asks
		// the RIGHT question for a PG regen — is the SHIPPED/L2 corpus genuinely
		// incomplete vs the embedded node count? (HasShippedFromSnapshot +
		// segmentPoolDegenerate, then a read-engine-load attempt) — returning true ONLY
		// when the corpus is genuinely zero/incomplete AND a load cannot restore it. So
		// the expensive PG rebuild fires on genuine incompleteness, never on a lazy read
		// engine. The ReconcileResidentDegenerate call above is still made FIRST so its
		// warm-load side effect (load()+recoverIfDegenerate) is preserved. healNeedsRebuild
		// re-probes ReconcileResidentDegenerate internally on this rare degenerate branch
		// (a second cheap load/List, off the bind path); healthy graphs short-circuited at
		// `if !degenerate` above and never reach it. The MANUAL manage(rebuild_segments)
		// path (handleClientRebuildSegments) is intentionally NOT gated — an operator
		// asking for a rebuild always gets one.
		//
		// SCOPE (boot herd + hypothesis c): this collapses the STEADY-STATE 5-min re-flag
		// loop, not a boot-time one-rebuild-per-daemon herd — the RebuildSegments
		// single-flight is PER-PROCESS, so N cold fleet daemons can each pay one rebuild
		// until a complete corpus is shipped+visible. That is also how the gate mitigates
		// c: once ANY daemon ships a complete corpus, every other daemon's healNeedsRebuild
		// sees shipped-complete and skips. The residual boot rebuild is within the ticket
		// goal — post-deploy observers must not read it as a regression.
		needsRebuild, herr := c.healNeedsRebuild(ctx, g.gt, g.name)
		if herr != nil {
			slog.Warn("bootstrap: segment reconcile shipped-completeness gate failed (continuing, no rebuild)",
				"graph_type", g.gt, "name", g.name, "error", herr)
			continue
		}
		if !needsRebuild {
			slog.Debug("bootstrap: read pool degenerate but shipped corpus complete — skipping PG rebuild (load retry)",
				"graph_type", g.gt, "name", g.name)
			continue
		}
		// Heal breaker gate: once a graph has latched disarmed after
		// healBreakerTripThreshold no-progress rebuilds, skip the FUTILE RebuildSegments.
		// The recovery probe (ReconcileResidentDegenerate) above still ran — only the
		// rebuild is gated — so the legitimate ~5-min recovery path keeps working.
		if !c.healBreaker.Allow(g.gt, g.name) {
			slog.Debug("bootstrap: segment reconcile — auto-heal breaker latched for graph, skipping rebuild (recovery probe still ran)",
				"graph_type", g.gt, "name", g.name)
			continue
		}
		// Genuinely-incomplete shipped corpus (healNeedsRebuild==true): heal via the
		// PROVEN rebuild path (single-flight shared with the manual op + embed-drain
		// auto-heal — NOT a bare load).
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
		// A COMPLETED rebuild (ran; the rerr path continued above) consumes the BM25
		// arm's no-progress shot — a no-op when this pass never consulted that gate.
		if ran {
			c.armBM25HealProgress(g.gt, g.name)
		}
		// Classify against the breaker (records ONLY on ran==true) — the same strict
		// no-progress/progress rule the embed-drain trigger uses.
		c.classifyHealOutcome(ctx, g.gt, g.name, ran, scanned, built, partial)
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
//
// It ALSO wakes on the segment manager's reconcile nudge: when a graph's publish
// coverage gate becomes unsatisfiable, waiting up to the full interval wastes the
// window in which the condition is already known. The nudge changes only WHEN a pass
// runs — the woken pass is the SAME full body over every segment-bearing graph, with
// the same gates and the same rebuild entry points. It is deliberately NOT scoped to
// the nudged graphs: the full pass is already cheap by design (see
// reconcileSegmentCoverage), and scoping it would fork a second, divergent reconcile
// path.
func (c *client) runSegmentReconcileLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// NIL-MANAGER GUARD, ahead of any channel read: a headless / --no-llm-pipeline
	// daemon runs with c.segmentMgr == nil, and ReconcileNudge() would dereference a
	// nil receiver. reconcileSegmentCoverage's own nil check runs too late to cover
	// that — it is inside the body, past the select. Such a daemon keeps the plain
	// ticker loop; there is no publisher to nudge it.
	if c.segmentMgr == nil {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.reconcileSegmentCoverage(ctx)
			}
		}
	}
	nudges := c.segmentMgr.ReconcileNudge()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reconcileSegmentCoverage(ctx)
		case <-nudges:
			nudged := c.segmentMgr.TakeReconcileNudges()
			slog.Debug("bootstrap: segment reconcile woken by publish coverage-skip suppression",
				"graphs", len(nudged))
			c.reconcileSegmentCoverage(ctx)
		}
	}
}
