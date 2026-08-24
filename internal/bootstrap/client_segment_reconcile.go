// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// segmentGraphRef names one segment-bearing graph instance. It is the key the
// reconcile pass enumerates and the key the per-graph delta horizons are held under.
type segmentGraphRef struct {
	gt   kgtypes.GraphType
	name string
}

// mergePending is what one delta pull produced and what the caller must commit ONLY
// after the drain that made it durable.
type mergePending struct {
	// Horizon is the server-served horizon this window was pulled up to.
	Horizon int64
	// Merged is how many live items were handed to the local segments.
	Merged int
	// Pull reports whether a pull happened at all this pass. A graph with no horizon
	// of any kind pulls nothing, and a commit must not advance anything for it.
	Pull bool
}

// consumeSegmentDelta pulls one graph's bounded delta window and lands BOTH halves —
// the deletes on the local pool and the live items into the local segments — unless
// deltaScope excludes it this pass (see reconcileSegmentCoverageScoped).
//
// WHERE THE HORIZON COMES FROM, in order:
//  1. the in-memory horizon carried by this process;
//  2. otherwise the DURABLE merge horizon;
//  3. otherwise the durable REBUILD watermark;
//  4. otherwise NO PULL AT ALL.
//
// CLAUSE 4 IS THE LOAD RULE AND IT IS DELIBERATE. A zero-watermark scan of this axis
// is the full vectored corpus, so seeding an unseeded graph from zero would make
// every process pay one full-corpus read per such graph, and merging that window
// would be the whole-corpus rebuild this path exists to replace. An unseeded graph
// therefore pulls nothing and waits for the coverage backstop's rotation, which seeds
// its horizon on both of that arm's writing exits — bounded at one rotation, once per
// machine, against a full-corpus read on EVERY boot today.
//
// THE CONSEQUENCE, stated rather than hidden: until a graph's horizon is seeded it
// learns no server-side deletes from this feed either. Hard deletes never rode this
// feed at all, so nothing about that story changes.
//
// Best-effort throughout, like every other arm of this pass: a failure WARNs and the
// pass moves on. A window that does not land this tick lands on the next one, because
// the caller only commits the horizon after a successful drain.
func (c *client) consumeSegmentDelta(
	ctx context.Context, g segmentGraphRef, deltaScope map[segmentGraphRef]struct{},
) mergePending {
	if deltaScope != nil {
		if _, signaled := deltaScope[g]; !signaled {
			return mergePending{}
		}
	}
	scanner := c.PipelineScanner()
	if scanner == nil {
		return mergePending{} // degraded client — no scan seam to read the feed through.
	}

	since, ok := c.mergeHorizonFor(g)
	if !ok {
		slog.Debug("bootstrap: segment delta has no horizon for this graph yet — pulling nothing until the backstop seeds one",
			"graph_type", g.gt, "name", g.name)
		return mergePending{}
	}

	out, err := tools.MergeSegmentDelta(
		ctx, scanner, c.SegmentShipper(), c.segmentMgr, c.segmentMgr, g.gt, g.name, since)
	if err != nil {
		slog.Warn("bootstrap: segment delta merge failed (continuing; the horizon is not advanced, so the window is re-read next pass)",
			"graph_type", g.gt, "name", g.name, "error", err)
		return mergePending{}
	}
	if out.Learned > 0 {
		slog.Info("bootstrap: segment delta landed server-side deletes on the local pool",
			"graph_type", g.gt, "name", g.name, "learned", out.Learned, "carried", out.Carried)
	}
	if out.Merged > 0 {
		slog.Info("bootstrap: segment delta merged co-worker updates into the local segments",
			"graph_type", g.gt, "name", g.name, "merged", out.Merged, "since", since)
	}
	return mergePending{Horizon: out.Horizon, Merged: out.Merged, Pull: true}
}

// mergeHorizonFor resolves the window's start for one graph, walking the three seed
// sources in order. ok=false is clause 4 — no horizon of any kind, so no pull.
func (c *client) mergeHorizonFor(g segmentGraphRef) (int64, bool) {
	c.deltaHorizonMu.Lock()
	since, carried := c.deltaHorizon[g]
	c.deltaHorizonMu.Unlock()
	if carried {
		return since, true
	}

	// The durable merge horizon: what the last landed merge for this graph was
	// scanned up to, which survives a restart precisely so the next process re-merges
	// one bounded window rather than the corpus.
	if h, err := c.segmentMgr.LoadMergeWatermark(g.gt, g.name); err != nil {
		slog.Warn("bootstrap: segment delta could not read the merge horizon (skipping this graph this pass)",
			"graph_type", g.gt, "name", g.name, "error", err)
		return 0, false
	} else if h > 0 {
		return h, true
	}

	// The durable rebuild watermark: the last horizon a landed rebuild published up
	// to. A graph that has landed one has a genuine bound to read from.
	w, _, err := c.segmentMgr.LoadRebuildState(g.gt, g.name)
	if err != nil {
		slog.Warn("bootstrap: segment delta could not read the rebuild state to seed its horizon (skipping this graph this pass)",
			"graph_type", g.gt, "name", g.name, "error", err)
		return 0, false
	}
	if w > 0 {
		return w, true
	}
	return 0, false
}

// commitMergeWatermark is part TWO of the merge's two-part commit, and the caller
// runs it ONLY on the branch where the drain that shipped the merge succeeded. A
// skipped commit leaves the horizon where it was, so the same window is re-pulled next
// tick and the same items are re-merged — idempotent, because the add is keyed by id.
func (c *client) commitMergeWatermark(g segmentGraphRef, pending mergePending) {
	if !pending.Pull || pending.Horizon <= 0 {
		return
	}
	c.deltaHorizonMu.Lock()
	if c.deltaHorizon == nil {
		c.deltaHorizon = make(map[segmentGraphRef]int64)
	}
	advanced := pending.Horizon > c.deltaHorizon[g]
	if advanced {
		c.deltaHorizon[g] = pending.Horizon
	}
	c.deltaHorizonMu.Unlock()
	if !advanced {
		return
	}
	if err := c.segmentMgr.SaveMergeWatermark(g.gt, g.name, pending.Horizon); err != nil {
		slog.Warn("bootstrap: segment delta could not persist the merge horizon (continuing; the window is re-read next process)",
			"graph_type", g.gt, "name", g.name, "error", err)
	}
}

// reconcileSegmentCoverage is the startup + periodic read-side reconcile: it walks
// the graphs this client has interacted with that carry rebuildable segments (see
// segmentBearingGraphs — a local working-set read, no enumeration RPC), probes each
// for the LIVE-resident-vs-shipped degeneracy a daemon restart
// can leave behind (a fully-embedded, un-recollected graph whose searchable pool
// collapsed to empty with no embed-drain or search to re-trigger a heal), and —
// ONLY when the healNeedsRebuild gate confirms the SHIPPED corpus is genuinely
// incomplete (not merely a lazily-loaded read engine) — heals it through
// the PROVEN RebuildSegments path — the SAME single-flight the manual rebuild op
// and the embed-drain auto-heal share, so those triggers coalesce onto one run
// rather than racing several rebuilds.
//
// IT CARRIES A SECOND, INDEPENDENT REBUILD REASON: the re-bucket trigger. A graph
// whose resident layout is a full doubling behind the partition count its corpus now
// derives is re-bucketed once, through that same single-flight. It is not a heal —
// such a graph is perfectly healthy and would otherwise leave this pass at the
// healthy-graph continue — so it has its own detector, its own log line and its own
// placement, and only the rebuild entry point and the breaker are shared.
//
// It is the recovery lever the prior two fixes left a gap for: the read-side
// recoverIfDegenerate only runs lazily inside a Search, and the write-side
// auto-heal only fires on the collect-armed embed-drain edge — neither event fires
// for a graph that is fully embedded and never re-collected, so its empty pool had
// no trigger to repopulate. This reconcile is INDEPENDENT of both events.
//
// Best-effort throughout: a nil segment manager (headless/--no-llm-pipeline) is a
// no-op; a per-graph probe or rebuild error WARNs and continues to the next graph,
// never blocking boot or the periodic tick. The probing stays cheap: the healthy
// graphs each pay one cache-first load + one atomic resident count + at most one
// ListDelta(0), plus the re-bucket detector's two local reads per format, which
// touch no source at all. A rebuild is paid only by a graph whose shipped corpus is
// genuinely incomplete (past the healNeedsRebuild gate) or whose layout is a full
// doubling behind its corpus — and the latter at most once per crossing, because the
// landed reset makes the detector read false again.
func (c *client) reconcileSegmentCoverage(ctx context.Context) {
	c.reconcileSegmentCoverageScoped(ctx, nil)
}

// reconcileSegmentCoverageScoped is the pass body. deltaScope selects which graphs
// the pass VISITS AT ALL:
//
//   - nil — the PERIODIC SWEEP (startup and the interval ticker). Every graph the
//     walk yields is visited, which is what guarantees a delete reaches the pool within one
//     interval even when nothing local signaled it: a collect that only REMOVES
//     files writes nothing here, so it produces no nudge, and a nudge-only consumer
//     would never learn about it.
//   - non-nil — a NUDGE-WOKEN pass. ONLY the graphs that signaled change are
//     visited. That is the cross-graph fan-out bound, and it tightened from
//     scoping only the delta read to scoping the whole walk because the recorder
//     set grew: the search nudge can fire once per graph per cool-off window, and
//     the per-graph body is not free — the degeneracy probe alone costs one
//     shipped-count read per format arm. A woken pass that ran the full body over
//     every segment-bearing graph would multiply that by the graph count on a
//     cadence measured in minutes.
//
// FILTERING THE WALK RATHER THAN FORKING THE BODY is deliberate. Running only the
// merge and drain arms for nudged graphs would be cheaper still, but it would
// silently retire the arms the other recorders exist to reach: a
// coverage-suppression nudge fires precisely when the read engine is stuck below
// the publish coverage ratio, and what unsticks it is the manifest-completeness arm
// and the degeneracy → heal → rebuild chain, neither of which is the drain.
// Filtering keeps every recorder's lever intact and keeps reconcileOneGraph's body
// byte-identical.
//
// The delta read is bounded on the OTHER axis too, independently of the scope: it
// is scoped by the graph's merge horizon, so after the first pass of a process a
// graph with no changes costs one empty page.
func (c *client) reconcileSegmentCoverageScoped(ctx context.Context, deltaScope map[segmentGraphRef]struct{}) {
	if c.segmentMgr == nil {
		return // headless / degraded — no segment engine to reconcile.
	}
	// Periodic background loop with no originating tool call — it stamps its own
	// query-origin operation so its per-tick reads are attributable.
	ctx = graphclient.WithOperation(ctx, graphclient.OpSegmentReconcile)

	// Reset the coverage-repair arm's per-pass round-robin bookkeeping before the
	// walk, so exactly one graph can claim this tick's repair slot. PERIODIC PASSES
	// ONLY: the backstop returns at its first gate on a woken pass, so a woken pass
	// claims no slots and would leave the offer count at zero — and the next periodic
	// pass's wrap condition would then be false, skipping a whole rotation for a
	// cursor sitting past its end. Gating the reset here leaves the rotation
	// bookkeeping entirely untouched by woken passes.
	if deltaScope == nil {
		c.beginRepairTick()
	}

	for _, g := range c.segmentBearingGraphs() {
		if deltaScope != nil {
			if _, signaled := deltaScope[g]; !signaled {
				continue // a woken pass is the nudged graphs' pass, not a corpus sweep.
			}
		}
		c.reconcileOneGraph(ctx, g, deltaScope)
	}

	// Once per SWEEP, never per graph — see EnforceResidencyBudget's own doc.
	c.segmentMgr.EnforceResidencyBudget()
}

// segmentBearingGraphs is the graph set every arm of this pass walks: the graphs
// THIS CLIENT HAS INTERACTED WITH — searched, collected into, or written to — AND,
// for code graphs, whose codebase this machine actually holds. Membership is both
// conditions, not just the first. It reads the working set locally and COSTS NO RPC: it asks no
// backend to enumerate anything, which is the point. The routed per-type
// enumeration it replaced (one ListGraphNamesOfType per embeddable builtin) ran on
// every periodic tick and every nudge-woken pass, and a working set consulted only
// AFTER such an enumeration would have left those reads in place.
//
// A graph nobody interacts with is therefore never probed, healed or published by
// this client. Stated so a future reader does not "fix" it: a graph NO client
// interacts with stops being enriched and stops having segments published — that is
// INTENDED, not a regression.
//
// The local-presence half is intended in the same way and for a sharper reason: a
// code graph whose repo is not checked out on this machine is never probed, healed
// or published BY THIS CLIENT, even after a user interaction admits it. This client
// cannot read that codebase, so any segment work it did would be built from nothing;
// the machine that DOES hold the repo is the one that publishes for it. Interaction
// alone is not enough to make this machine the right one to do the work.
//
// There is no knowledge/default seed and no seed of any other kind. knowledge is a
// member exactly when an interaction earned it, like every other graph, and
// workingset.Normalize is what guarantees it then arrives under the "default"
// instance name however the interaction spelled it — so the two-spellings drift the
// old explicit seed existed to prevent cannot return through this path.
//
// The kgtypes.HasRebuildableSegments gate survives unchanged and still mirrors
// segCoveredFor's matching gate (manage_status_coverage.go), so the reconcile probes
// exactly the graph set manage(status) reports as segment-bearing — linkage +
// transformers (sync-eligible but non-embeddable) are skipped, they carry no
// rebuildable segments.
//
// IT IS A SHARED HELPER ON PURPOSE. The periodic reconcile pass and the
// clean-shutdown backlog drain both need "every segment-bearing graph", and two
// independent enumerations of that set is exactly the drift this area has already
// produced once — an instrument asking knowledge/"" while the reconcile seeded
// knowledge/"default". One definition means the drain cannot walk a different set
// from the pass that queued the work.
func (c *client) segmentBearingGraphs() []segmentGraphRef {
	members := c.workingSet.Members()
	graphs := make([]segmentGraphRef, 0, len(members))
	for _, m := range members {
		if !kgtypes.HasRebuildableSegments(m.GraphType) {
			continue // linkage / transformers — no rebuildable segments.
		}
		if !c.graphLocallyPresent(m.GraphType, m.Name) {
			continue // code graph whose checkout this machine does not hold.
		}
		graphs = append(graphs, segmentGraphRef{gt: m.GraphType, name: m.Name})
	}
	return graphs
}

// graphLocallyPresent reports whether background work may touch this graph on
// THIS machine. Every family but code passes unconditionally — they have no
// checkout to hold — and the type check comes first so a non-code member
// short-circuits before any I/O. A code graph passes only when this machine
// actually holds its repo.
func (c *client) graphLocallyPresent(gt kgtypes.GraphType, name string) bool {
	present := c.presentLocally(gt, name)
	// Report only the CODE branch: no other family is gated, so a line for one
	// would claim a decision that was never made.
	if !present && gt == kgtypes.GraphCode {
		c.logPresenceSkipOnce(gt, name)
	}
	return present
}

// presentLocally is the predicate itself, without the reporting. The graph-type
// check precedes the manifest read so a non-code member costs no I/O.
func (c *client) presentLocally(gt kgtypes.GraphType, name string) bool {
	if c.localPresence != nil {
		return c.localPresence(gt, name)
	}
	if gt != kgtypes.GraphCode {
		return true
	}
	return tools.LocalCodeRepoPresent(name)
}

// logPresenceSkipOnce reports a code graph declined for background work, at most
// once per graph per process. A silent skip is indistinguishable from a broken
// gate, but the predicate is consulted on every reconcile tick and every catalog
// pass, so a line per call would be a metronome of noise on exactly the machines
// this helps. Latched, like AdmitGraph's first-admission line.
func (c *client) logPresenceSkipOnce(gt kgtypes.GraphType, name string) {
	key := string(gt) + "\x00" + name
	c.presenceSkipMu.Lock()
	if _, seen := c.presenceSkipLogged[key]; seen {
		c.presenceSkipMu.Unlock()
		return
	}
	if c.presenceSkipLogged == nil {
		c.presenceSkipLogged = map[string]struct{}{}
	}
	c.presenceSkipLogged[key] = struct{}{}
	c.presenceSkipMu.Unlock()

	slog.Info("working set: code graph skipped for background work - no local checkout on this machine (user reads are unaffected)",
		"graph_type", gt, "graph", name)
}

// drainSegmentBacklog ships whatever is queued in the in-memory segment backlog,
// once, on the clean-shutdown path. Manager.dirty is in-memory and the periodic
// drain cadence is segmentReconcileInterval, so without this every clean daemon stop
// discards up to one full interval of queued documents with no record of what was
// lost. It closes the non-crash half of that: the crash half is inherently
// after-the-fact and is what the repair arm exists to find on the next boot.
//
// BOUNDED BY THE CALLER'S CONTEXT, and the bound is not optional. A graph whose
// drain does not finish inside the shutdown window is logged and skipped: a
// shutdown that hangs past the SIGTERM window is strictly worse than a backlog the
// repair arm picks up next boot. Best-effort per graph, mirroring every arm of the
// reconcile pass — one graph's failure must not cost the others their drain.
//
// The walk is serial across graphs deliberately. Draining concurrently at shutdown
// would multiply peak memory and contend for the ship path at the moment the
// process is least able to absorb either.
//
// The single log line naming what drained and what was skipped IS the clean-shutdown
// attribution record — the thing whose absence made this loss untraceable.
func (c *client) drainSegmentBacklog(ctx context.Context) {
	if c.segmentMgr == nil {
		return // headless / degraded — no segment engine, so no backlog.
	}
	ctx = graphclient.WithOperation(ctx, graphclient.OpSegmentReconcile)

	var drained, skipped []string
	for _, g := range c.segmentBearingGraphs() {
		label := string(g.gt) + "/" + g.name
		if ctx.Err() != nil {
			// The window closed mid-walk: record the remainder rather than pushing
			// past the deadline the shutdown budget depends on.
			skipped = append(skipped, label)
			continue
		}
		if err := c.segmentMgr.ReEmitDirtyBuckets(ctx, g.gt, g.name); err != nil {
			slog.Warn("bootstrap: shutdown backlog drain failed for a graph (continuing; the repair arm picks it up next boot)",
				"graph_type", g.gt, "name", g.name, "error", err)
			skipped = append(skipped, label)
			continue
		}
		drained = append(drained, label)
	}
	slog.Info("bootstrap: clean-shutdown segment backlog drain",
		"drained", drained, "skipped", skipped)
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
// It ALSO wakes on the segment manager's reconcile nudge, which now has THREE
// recorders: a publish coverage gate becoming unsatisfiable, the backlog byte-cap
// crossing, and a user search asking for its graph's delta sooner than the next
// tick. Waiting up to the full interval wastes the window in which the condition is
// already known.
//
// The woken pass runs the SAME per-graph body with the same gates and the same
// rebuild entry points, but it is SCOPED to the nudged graphs (see
// reconcileSegmentCoverageScoped). That scoping is what makes a search-driven
// recorder affordable: the body costs shipped-count reads per graph, and the search
// nudge can fire once per graph per cool-off window, so an unscoped woken pass would
// multiply that cost by the whole graph count on an interactive cadence.
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
			// The drained set SCOPES THE WHOLE WALK on this woken pass — the cross-graph
			// fan-out bound. The per-graph body is unchanged, so the sooner-look does not
			// fork a second, divergent reconcile path; only the graph set narrows.
			nudged := c.segmentMgr.TakeReconcileNudges()
			scope := make(map[segmentGraphRef]struct{}, len(nudged))
			for _, n := range nudged {
				scope[segmentGraphRef{gt: n.GraphType, name: n.Name}] = struct{}{}
			}
			// The message names the MECHANISM rather than any one recorder: three
			// different conditions reach this wake, so naming one of them would
			// misattribute the other two.
			slog.Debug("bootstrap: segment reconcile woken by nudge",
				"graphs", len(nudged))
			c.reconcileSegmentCoverageScoped(ctx, scope)
		}
	}
}
