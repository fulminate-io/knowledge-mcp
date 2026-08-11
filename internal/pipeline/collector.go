// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collector is the per-graph background worker that discovers nodes
// needing summary/embed and pushes them onto the global channels. One
// collector per (GraphType, GraphName), spawned by Pipeline.RegisterGraph.
//
// Lifecycle: run() launches one goroutine per ENABLED axis — runSummaryLoop
// when a summarizer is configured (summaryEnabled) and runEmbedLoop when an
// embedder is configured (embedEnabled). A disabled axis's loop never starts
// (it is gated off end-to-end). Each loop is a stoplight cycle: drain releases from
// completed workers, scan eligible IDs via the pipeline_scan MCP tool,
// push every new one onto the channel, sleep one tick, repeat. The
// in-flight set prevents duplicate queueing during the worker call
// latency window; without it, a 3-sec CLI failure would let 12+ ticks
// re-queue the same nodes (the bug that produced 25× counter inflation
// in Apr 2026).
//
// In-flight cleanup uses TWO mechanisms (defense in depth):
//  1. Worker writes NodeID to the per-collector release channel after
//     processing each item; collector drains releases at the start of
//     each cycle and removes those IDs from in-flight.
//  2. After scan, the collector intersects in-flight with the current
//     eligible set. Anything no longer eligible (success → Summary
//     populated, terminal → marker stamped) gets pruned.
//
// run() exits when ctx is canceled (by Pipeline.UnregisterGraph or
// Pipeline.Stop).
//
// Post-Phase-4: collector reads via the pipeline_scan MCP tool against
// the shared WireClient rather than calling store.NodeIDsBySummaryGap /
// store.NodeIDsByEmbedGap in-process. The dirty-gen cheap-tick skip
// remains — the scan response carries the server-side dirty_gen, so
// the collector still skips the next tick when the gen hasn't advanced.
type collector struct {
	gt        kgtypes.GraphType
	name      string
	cfg       Config
	summaryCh chan<- SummaryWork
	embedCh   chan<- EmbedWork
	metrics   *metricsState
	client    WireClient

	// summaryEnabled / embedEnabled mirror Pipeline.summaryEnabled() /
	// embedEnabled() at construction time (p.summarizer != nil / p.embedder != nil
	// are the single sources of truth, threaded in by RegisterGraph). run launches
	// a per-axis loop ONLY when its flag is set: a disabled axis's loop must never
	// start, because nothing downstream can process it and a running loop would
	// push eligible nodes onto that axis's channel forever (the nil-func loop this
	// gate fixes).
	summaryEnabled bool
	embedEnabled   bool

	// flush, when non-nil, force-seals this graph's sub-threshold segment tail
	// (both formats) and ships it. Built per-(gt,name) in RegisterGraph as a
	// closure over p.segmentMgr.Flush so the collector carries NO segmentdist
	// dependency (the consumer-side decoupling documented at pipeline.go ShipManager).
	// nil when no segment manager is wired (test fakes / no-segment path); the
	// embed-axis quiescence trigger no-ops when it is nil.
	flush func(ctx context.Context) error

	// healIfSegmentless, when non-nil, runs the auto-heal check for this
	// graph: a CHEAP zero-shipped-segments probe and, ONLY on zero, an invocation
	// of the rebuild driver (single-flight, shared with the manual rebuild_segments
	// op). Built per-(gt,name) in RegisterGraph via the bootstrap-supplied
	// healFactory (closure over p.segmentMgr probe + tools.RebuildSegments) so the
	// collector carries NO segmentdist/tools dependency — same consumer-side
	// decoupling as flush. nil when no segment manager / heal factory is wired
	// (test fakes) → the armed embed-drain heal-check no-ops.
	healIfSegmentless func(ctx context.Context) error

	// healDisarmed latches true when healIfSegmentless returns ErrHealDisarmed (the
	// per-graph heal breaker tripped). Once set, the embed-wake arm site stops re-arming
	// the per-wake heal check, so a disarmed graph stops invoking the closure every wake
	// — breaking the self-sustaining ~70s heal re-fire. Never cleared in-process (the
	// breaker re-arms only on a manual rebuild_segments or a restart).
	healDisarmed atomic.Bool

	// baseTick is this collector's BUSY poll interval, chosen at registration
	// from the bound backend's login state (Config.Tick for local,
	// Config.CloudTick for remote). Zero falls back to Config.TickOrDefault.
	// idleTick is the idle-backoff ceiling: when a scan finds no work the
	// interval grows geometrically from baseTick toward idleTick, snapping back
	// to baseTick the moment work appears. idleTick == baseTick disables
	// idle-backoff (the local case — loopback scans are cheap).
	baseTick time.Duration
	idleTick time.Duration

	// summaryWake / embedWake are buffered(1) wake channels. Pipeline.WakeAll
	// (fired by a collect) sends on them to cut a backed-off idle sleep short so
	// a freshly-collected graph re-scans within one base tick rather than
	// waiting out its idle interval (up to IdleTickMax). Buffered + coalescing:
	// a signal that arrives mid-scan stays queued so the NEXT sleep drains it
	// immediately — no lost wakeup. One per axis so a single WakeAll reliably
	// reaches both loops.
	summaryWake chan struct{}
	embedWake   chan struct{}

	// lastSummaryGen / lastEmbedGen cache the per-axis dirty-gen value from the
	// most recent scan that returned zero eligible IDs (the per-axis watermark).
	// On the next tick, discover compares this against the SHARED gen snapshot
	// (genSnapshot, below) and skips the Phase-2 detail PipelineScan entirely when
	// the snapshot gen has not advanced past it — the no-change tick costs ZERO
	// collector RPCs.
	lastSummaryGen atomic.Uint64
	lastEmbedGen   atomic.Uint64

	// genSnapshot reads the central bulk gen-poll's last-sampled per-(graph,axis)
	// dirty-gens for THIS collector's graph (returns summary, embed, and whether
	// the central loop has sampled it yet). Set by RegisterGraph to
	// Pipeline.genSnapshotFor. discover consults it to decide whether to issue a
	// Phase-2 detail PipelineScan. nil for test fakes that construct a collector
	// without the central loop → discover falls back to always issuing scanGaps
	// (the pre-two-phase behavior), so the existing collector state-machine tests
	// stay valid.
	genSnapshot func(key graphKey) (summary, embed uint64, ok bool)

	// collectInFlight reports whether a collect into THIS collector's graph is
	// running. Built per-(gt,name) by RegisterGraph from a bootstrap-supplied
	// hook, so the collector carries NO dependency on the collect machinery — the
	// same consumer-side decoupling flush and healIfSegmentless use above. nil
	// (test fakes / no runtime wired) reads as "no collect in flight", so the gate
	// is inert by default.
	collectInFlight func() bool
}

// newCollector constructs a collector. The actual goroutine launch is
// done by Pipeline.RegisterGraph so the WaitGroup accounting stays
// centralized.
func newCollector(gt kgtypes.GraphType, name string, cfg Config, summaryCh chan<- SummaryWork, embedCh chan<- EmbedWork, metrics *metricsState, client WireClient, baseTick, idleTick time.Duration, flush func(ctx context.Context) error, healIfSegmentless func(ctx context.Context) error, summaryEnabled, embedEnabled bool, genSnapshot func(key graphKey) (summary, embed uint64, ok bool), collectInFlight func() bool) *collector {
	return &collector{
		gt:                gt,
		name:              name,
		cfg:               cfg,
		summaryCh:         summaryCh,
		embedCh:           embedCh,
		metrics:           metrics,
		client:            client,
		baseTick:          baseTick,
		idleTick:          idleTick,
		flush:             flush,
		healIfSegmentless: healIfSegmentless,
		summaryEnabled:    summaryEnabled,
		embedEnabled:      embedEnabled,
		summaryWake:       make(chan struct{}, 1),
		embedWake:         make(chan struct{}, 1),
		genSnapshot:       genSnapshot,
		collectInFlight:   collectInFlight,
	}
}

// run launches the per-graph ticker loops, one per ENABLED axis: the summary
// loop when a summarizer is configured (summaryEnabled) and the embed loop when
// an embedder is configured (embedEnabled). Splitting them prevents the embed
// loop from being starved when the summary channel is saturated (the per-graph
// collector blocks on summary push; without the split the same goroutine never
// reaches dispatchEmbeds).
//
// A disabled axis's loop is NOT started: nothing downstream can process it, so a
// running loop would push eligible nodes onto that axis's channel forever (the
// nil-func loop). The WaitGroup Add is kept in step with the loops actually
// launched, so a collector with both axes disabled starts nothing and run
// returns immediately. (wirePipelineRuntime prevents the both-disabled case from
// reaching here, but run stays robust to it.)
//
// Exits on ctx.Done.
func (c *collector) run(ctx context.Context) {
	var wg sync.WaitGroup
	if c.summaryEnabled {
		wg.Go(func() { c.runSummaryLoop(ctx) })
	}
	if c.embedEnabled {
		wg.Go(func() { c.runEmbedLoop(ctx) })
	}
	wg.Wait()
}

// maxDrainSkips bounds how many consecutive cycles the drain-before-rescan
// gate (#2) skips a scan while a prior batch is still in flight. After this
// many skips the loop force-rescans even with items in flight, so a dropped
// worker release (e.g. a panic before the release write) can't wedge the
// collector — the forced scan re-runs pruneInFlightItems to clear stale IDs.
// The forced scan is dedup-safe (in-flight items are skipped on push), so the
// only cost of a high value is slower recovery from a wedged in-flight set;
// the only cost of a low value is an occasional redundant scan during a long
// legitimate drain. At the cloud base of 5s, 12 skips ≈ 60s recovery.
const maxDrainSkips = 12

// loopAxis bundles the per-axis wiring the shared discovery loop needs so the
// summary and embed loops share ONE implementation of the drain-gate (#2),
// idle-backoff (#1), and scan-error backoff (#3). Duplicating that control
// flow per axis is exactly how the two loops drift apart, so they don't.
type loopAxis struct {
	axis    string          // "summary" | "embed" — the pipeline_scan axis
	lastGen *atomic.Uint64  // per-axis dirty-gen cache (c.lastSummaryGen / lastEmbedGen)
	relSize int             // release-channel buffer
	backoff *errBackoff     // #3 per-axis scan-error gate (separate from the worker LLM gate)
	wake    <-chan struct{} // collect-fired wake (c.summaryWake / embedWake) — cuts the idle sleep short
	// push sends one item's Work onto the axis channel, returning false on
	// ctx cancel (caller exits the loop). The axis-specific Work struct +
	// target channel live here so runLoop stays axis-agnostic.
	push func(ctx context.Context, item *knowledgev1.PipelineScanItem, release chan<- string) bool
}

// runSummaryLoop is the summary-axis discovery cycle (shared runLoop with the
// summary wiring). No client-side graph-type gate: the server returns
// empty items for non-summarizable graph types, so a redundant client gate
// would only duplicate that decision.
func (c *collector) runSummaryLoop(ctx context.Context) {
	c.runLoop(ctx, loopAxis{
		axis:    "summary",
		lastGen: &c.lastSummaryGen,
		relSize: c.cfg.SummaryChannelSizeOrDefault(),
		backoff: newErrBackoff(c.cfg.ErrBackoffBaseOrDefault(), c.cfg.ErrBackoffMaxOrDefault()),
		wake:    c.summaryWake,
		push: func(ctx context.Context, item *knowledgev1.PipelineScanItem, release chan<- string) bool {
			select {
			case <-ctx.Done():
				return false
			case c.summaryCh <- SummaryWork{
				GraphType: c.gt, GraphName: item.GetGraphName(), NodeID: item.GetNodeId(),
				SummarizeText: item.GetSummarizeText(), Release: release, Backend: c.client,
			}:
				return true
			}
		},
	})
}

// runEmbedLoop is the embed-axis discovery cycle (shared runLoop with the embed
// wiring). The graph-type note above applies identically (NodeIDsByEmbedGap
// short-circuits server-side).
func (c *collector) runEmbedLoop(ctx context.Context) {
	c.runLoop(ctx, loopAxis{
		axis:    "embed",
		lastGen: &c.lastEmbedGen,
		relSize: c.cfg.EmbedChannelSizeOrDefault(),
		backoff: newErrBackoff(c.cfg.ErrBackoffBaseOrDefault(), c.cfg.ErrBackoffMaxOrDefault()),
		wake:    c.embedWake,
		push: func(ctx context.Context, item *knowledgev1.PipelineScanItem, release chan<- string) bool {
			select {
			case <-ctx.Done():
				return false
			case c.embedCh <- EmbedWork{
				GraphType: c.gt, GraphName: item.GetGraphName(), NodeID: item.GetNodeId(),
				EmbedText:  item.GetEmbedText(),
				Bm25Fields: bm25FieldsFromProto(item.GetBm25Fields()),
				Release:    release, Backend: c.client,
			}:
				return true
			}
		},
	})
}

// runLoop is the shared stoplight discovery cycle for one axis. Each iteration:
// drain releases, then — unless a prior batch is still in flight (#2) — scan,
// prune, and push every new item, sleeping the current interval before
// repeating. Four throttles keep remote (logged-in) scan volume bounded:
//
//	#1 idle-backoff   — an empty scan grows the sleep geometrically from baseTick
//	                    toward idleTick; any work snaps it back to baseTick.
//	#2 drain-gate     — while items are still in flight, skip the scan (just keep
//	                    draining) until the batch clears or maxDrainSkips forces one.
//	#3 scan backoff   — a scan ERROR (e.g. a remote 429) backs off on the axis's
//	                    own errBackoff instead of retrying at the base cadence.
//	#4 collect-gate   — while a collect into this graph is in flight, skip the scan
//	                    entirely; the rows it would enrich are still landing.
//
// Exits on ctx.Done.
func (c *collector) runLoop(ctx context.Context, ax loopAxis) {
	inFlight := make(map[string]struct{})
	release := make(chan string, ax.relSize)

	base := c.baseTick
	if base <= 0 {
		base = c.cfg.TickOrDefault()
	}
	// idleMax == base disables idle-backoff (the local case).
	idleMax := max(c.idleTick, base)
	interval := base
	skipped := 0
	// pendingSinceFlush latches true once this axis has pushed/in-flight work
	// since the last quiescence flush, and is cleared the moment the gap drains
	// (items==0 && inFlight==0) and the flush fires. The latch makes the flush
	// fire ONCE per drain rather than on every post-drain idle scan.
	pendingSinceFlush := false
	// healArmed mirrors pendingSinceFlush for the auto-heal check, but is
	// armed by the COLLECT (a sleepForWake byWake return on the embed axis), NOT
	// by embed work — the already-embedded new-user case has ZERO embed work, so a
	// work-gated latch would never fire. The next embed drain edge consumes it
	// (maybeHealCheck, below) and clears it, so the heal fires ONCE per collect.
	// LOCAL, not a struct field; only the embed loop arms it (summary byWake is
	// ignored).
	healArmed := false
	// gatedSince latches the start of a GATED RUN and is zero whenever the
	// collect-gate is down. LOCAL, like pendingSinceFlush and healArmed above and
	// for the same reason — it is per-loop state, not per-collector state.
	// noteGateTransition consumes and returns it so the gate's two markers fire on
	// the run's EDGES rather than on every skipped iteration.
	var gatedSince time.Time

	for {
		drainReleases(release, inFlight)

		// #2 drain-before-rescan: a prior batch is still draining — don't issue
		// another scan, just keep draining — until it clears or the safety cap
		// forces a rescan (which also prunes any stuck in-flight IDs).
		if len(inFlight) > 0 && skipped < maxDrainSkips {
			skipped++
			if !c.sleepFor(ctx, base) {
				return
			}
			continue
		}
		skipped = 0

		// #4 collect-gate: a collect into this graph is in flight, so the freshly
		// uploaded rows are still landing and the collect's own finalize has not
		// run. Scanning now is what puts this loop's writeback in the way of that
		// finalize. Skip the scan entirely until the collect completes.
		//
		// DELIBERATELY NO FORCE-THROUGH CAP, unlike the drain-gate above. Forcing a
		// scan through mid-collect would reinstate the very contention this gate
		// removes, so the asymmetry is the point and not an oversight. The defense
		// against a stuck gate lives on the other side instead: the collect
		// runtime lowers the gate when a run ends by ANY route — success, error or
		// recovered panic — and ignores an entry that has been held beyond its
		// max-hold bound, which covers a collect that never ends at all.
		//
		// sleepFor, NOT sleepForWake: a wake delivered while the gate is up must
		// stay queued on the buffered channel for the first ungated iteration to
		// consume. Draining it here would trade a whole collect's worth of
		// enrichment for nothing. The cost of not consuming it is one base tick of
		// extra latency after the gate clears.
		gated := c.collectInFlight != nil && c.collectInFlight()
		gatedSince = c.noteGateTransition(ax, gated, gatedSince)
		if gated {
			if !c.sleepFor(ctx, base) {
				return
			}
			continue
		}

		items, err := c.discover(ctx, ax.axis, ax.lastGen)
		if err != nil {
			// #3 scan-error backoff (insurance): a rate-limit / transient scan
			// failure backs off on the axis gate rather than re-firing at the
			// base cadence, so a limit can't re-trigger a storm. A remote 429
			// carries a Retry-After, which we honor verbatim; any other scan
			// error falls back to exponential.
			hint, _ := rateLimitHint(err)
			d := ax.backoff.failHint(hint)
			slog.Debug("pipeline.collector: scan failed; backing off",
				"graph_type", c.gt, "name", c.name, "axis", ax.axis, "delay", d, "retry_after_hint", hint, "error", err)
			if !c.sleepFor(ctx, d) {
				return
			}
			continue
		}
		ax.backoff.ok()

		pruneInFlightItems(inFlight, items)
		if !c.pushNewItems(ctx, ax, items, inFlight, release) {
			return
		}

		// Quiescence flush: on the embed axis, force-seal this graph's
		// sub-threshold segment tail ONCE per drain. Latch while work is present
		// or in flight; fire on the drain-complete edge (empty scan AND nothing in
		// flight). Per-drain, not per-tick — the latch blocks re-firing across the
		// idle-backoff empty scans below. Safe ordering: a node leaves inFlight
		// only after the worker's deferred release, which runs after the engine
		// buffered its vectors, so inFlight==0 implies every embedded doc is
		// staged before Flush seals.
		pendingSinceFlush = c.maybeQuiescenceFlush(ctx, ax, len(items), len(inFlight), pendingSinceFlush)

		// Auto-heal: consume the collect-armed healArmed latch on the SAME
		// embed drain edge. Stays armed while work is present; on the armed embed
		// drain edge it runs the cheap zero-segments + coverage-ratio probe and (on
		// zero OR degraded coverage) the rebuild driver, then disarms — once per
		// collect. Independent of pendingSinceFlush: the already-embedded case has
		// zero embed work so only this collect-driven latch fires for it.
		healArmed = c.maybeHealCheck(ctx, ax, len(items), len(inFlight), healArmed)

		// #1 idle-backoff: work found → fast base cadence; empty → grow toward
		// idleMax so a fully-drained graph costs ~one scan per idleMax. The idle
		// sleep is the ONLY one a collect wake interrupts — a long idle interval
		// should yield immediately to fresh work, but a rate-limit backoff (#3)
		// and a drain-gate wait must run to completion.
		if len(items) > 0 {
			interval = base
		} else {
			interval = nextIdleInterval(interval, idleMax)
		}
		alive, byWake := c.sleepForWake(ctx, interval, ax.wake)
		if !alive {
			return
		}
		// A collect-wake on the embed axis arms the auto-heal check; the next embed
		// drain edge consumes it (maybeHealCheck above). A breaker-disarmed graph
		// (healDisarmed latched) stops re-arming so the closure is no longer invoked
		// per wake — ending the self-sustaining heal re-fire.
		if byWake && ax.axis == "embed" && !c.healDisarmed.Load() {
			// Re-fire observability (kept): every embed-wake re-arm is one turn of the
			// auto-heal cadence. Logging it makes the re-fire source (organic gen-advance
			// wake vs the heal/flush's own writes waking the loop) visible per cycle.
			slog.Debug("pipeline.collector: embed-wake re-armed auto-heal latch",
				"graph_type", c.gt, "name", c.name)
			healArmed = true
		}
	}
}

// noteGateTransition emits the collect-gate's two EDGE markers and returns the
// next value of the caller's gatedSince latch — the same shape as
// maybeQuiescenceFlush and maybeHealCheck below, and for the same reason: the
// latch is per-loop local state, so the edge logic belongs beside it rather than
// inline in the loop.
//
// EDGE-TRIGGERED, NOT PER-ITERATION, AND THAT IS THE WHOLE DESIGN. The gate skips
// once per base tick for the entire length of a collect, so an unconditional line
// here would emit dozens per collect per axis — which is exactly why the
// neighboring drain-gate logs nothing at all. Two lines per gated run is the
// whole budget: enough to show the gate engaged and released, cheap enough to
// leave on at Debug alongside the other per-cycle lines in this file.
//
// BOTH MESSAGE STRINGS ARE LOCKED. Without them the gate is silent — it skips the
// scan and continues, so nothing shows whether it held, released, or wedged shut.
// They are what an operator greps out of the daemon log to tell those three apart,
// and what this package's own gate tests assert on verbatim; rewording either
// breaks that evidence trail with no other symptom. Each is written unbroken on
// one line.
func (c *collector) noteGateTransition(ax loopAxis, gated bool, gatedSince time.Time) time.Time {
	switch {
	case gated && gatedSince.IsZero():
		slog.Debug("pipeline.collector: gap scan gated by in-flight collect",
			"graph_type", c.gt, "name", c.name, "axis", ax.axis)
		return time.Now()
	case !gated && !gatedSince.IsZero():
		slog.Debug("pipeline.collector: gap scan resumed after collect",
			"graph_type", c.gt, "name", c.name, "axis", ax.axis,
			"gated_for", time.Since(gatedSince).Round(time.Millisecond))
		return time.Time{}
	}
	return gatedSince
}

// maybeQuiescenceFlush implements the embed-axis quiescence edge-latch.
// It returns the next pendingSinceFlush latch value. While the gap has items or
// in-flight work it latches true. On the drain-complete edge — empty scan AND
// nothing in flight AND the latch is set — it fires c.flush ONCE (embed axis
// only, and only when a flush closure is wired) and clears the latch so the
// post-drain idle scans do not re-fire it. A flush error only WARNs (best-effort,
// mirroring the embed-writeback ship path): the sub-threshold tail being unsealed
// is non-fatal and the next drain retries. A dirty-gen cheap-tick skip arrives
// here as items==0 and is treated identically to an empty scan; that is correct
// because a skip only happens after a prior empty scan already cleared the latch.
func (c *collector) maybeQuiescenceFlush(ctx context.Context, ax loopAxis, items, inFlight int, pending bool) bool {
	if items > 0 || inFlight > 0 {
		return true
	}
	if !pending || ax.axis != "embed" || c.flush == nil {
		return pending
	}
	slog.Info("pipeline.collector: embed gap drained — quiescence flush (force-seal sub-threshold tail)",
		"graph_type", c.gt, "name", c.name)
	if err := c.flush(ctx); err != nil {
		slog.Warn("pipeline.collector: quiescence flush failed (best-effort; next drain retries)",
			"graph_type", c.gt, "name", c.name, "error", err)
	}
	return false
}
