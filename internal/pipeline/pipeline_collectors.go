// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// pipeline_collectors.go holds the per-graph collector lifecycle on *Pipeline:
// registration (with backend binding + cadence selection), the collect-fired
// wake, cadence/backend resolution, and teardown. Split out of pipeline.go to
// keep that file under the 500-line cap as the cadence + wake state accreted.

// RegisterGraph spawns the per-graph collector goroutines for (gt, name): one
// loop per ENABLED axis — the summary loop when a summarizer is configured
// (p.summaryEnabled()) and the embed loop when an embedder is configured
// (p.embedEnabled()), both threaded onto the collector here. Called by the
// registry hook (Phase 6) when a graph loads. Re-registration of an
// already-tracked graph is a no-op — the registry hook fires once per load.
//
// No client-side graph-type eligibility gate (Option B): the
// collector is spawned for EVERY loaded graph regardless of summary/embed
// eligibility. A non-eligible graph (logs/web/pdf/linkage) is idle-cheap:
// the server's pipeline_scan handler short-circuits NodeIDsBySummaryGap /
// NodeIDsByEmbedGap on the graph type and returns empty, so the collector
// does one empty scan then cheap-tick-polls forever (no per-tick O(N) walk).
// The graph-type eligibility decision lives server-side exclusively.
//
// MUST NOT call back into the store registry synchronously per ticket
// reviewer R1: graph-load → notifyGraphLoaded → RegisterGraph runs while
// the registry's writeMu may still be held by callers in the resolution
// path. The collector goroutines lazy-Retrieve on their first tick so
// no synchronous lookup happens here.
func (p *Pipeline) RegisterGraph(ctx context.Context, gt kgtypes.GraphType, name string) {
	key := graphKey{GraphType: gt, GraphName: name}
	// Resolve the CONCRETE backend this collector scans + stamps. Login-routed
	// via the resolver (cloud when logged in, local otherwise); falls back to
	// the shared p.client when no resolver is wired (test fakes) or when the
	// resolver can't pick a backend right now (ErrNoBackend — the collector
	// still spawns and re-scans next tick once a backend is reachable).
	backend := p.resolveBackend(ctx)
	p.collectorMu.Lock()
	defer p.collectorMu.Unlock()
	if _, exists := p.collectorCancels[key]; exists {
		return
	}
	cctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is stored in collectorCancels and invoked by UnregisterGraph / stopSequence
	p.collectorCancels[key] = cancel
	base, idleMax := p.cadenceFor(ctx)
	// Build the per-graph quiescence-flush closure over p.segmentMgr.
	// nil when no segment manager is wired (test fakes) so the collector's
	// embed-axis drain trigger no-ops; otherwise force-seal + ship THIS graph's
	// sub-threshold tail on drain. Closes over (gt, name) so each collector
	// flushes only its own graph. Keeps the segmentdist dependency off the
	// collector (it sees only this func).
	var flush func(ctx context.Context) error
	if p.segmentMgr != nil {
		flush = func(fctx context.Context) error { return p.segmentMgr.Flush(fctx, gt, name) }
	}
	// Build the per-graph auto-heal closure from the bootstrap-supplied
	// factory (closure over the segment-presence probe + the rebuild driver). nil
	// when no factory is wired (test fakes / no segment manager) so the collector's
	// armed embed-drain heal-check no-ops. Closes over (gt, name) so each collector
	// heals only its own graph. Keeps the segmentdist/tools dependency off the
	// collector (it sees only this func).
	var heal func(ctx context.Context) error
	if p.healFactory != nil {
		heal = p.healFactory(gt, name)
	}
	c := newCollector(gt, name, p.cfg, p.summaryCh, p.embedCh, p.metrics, backend, base, idleMax, flush, heal, p.summaryEnabled(), p.embedEnabled(), p.genSnapshotFor)
	p.collectorWakes[key] = []chan struct{}{c.summaryWake, c.embedWake}
	p.collectorWG.Go(func() {
		c.run(cctx)
	})
}

// WakeAll triggers ONE immediate central bulk gen-poll (genPollWake) so a collect
// (or any bulk write) re-polls every loaded graph's dirty-gen in a SINGLE RPC and
// selectively pokes only the (graph,axis) collectors whose gen advanced — rather
// than fanning out a wake to all 2N per-collector loops (which previously each
// then issued their own PipelineScan, the flood this two-phase protocol removes).
// A graph that had backed off toward IdleTickMax is roused within one poll only if
// its gen actually moved; an unchanged graph stays idle (no wasted scan).
//
// Non-blocking + coalescing: the send on the buffered(1) genPollWake is a no-op
// when a poll is already queued, so repeated collects don't pile up. Safe to call
// before Start / with no central loop running (test fakes): the buffered signal
// simply sits until the loop drains it, or is harmlessly never consumed.
func (p *Pipeline) WakeAll() {
	select {
	case p.genPollWake <- struct{}{}:
	default: // a poll is already queued — coalesce
	}
}

// cadenceFor returns the (base, idleMax) poll cadence a freshly-registered
// collector should use. Logged-in (remote) backend → the slow Config.CloudTick
// base with idle-backoff up to Config.IdleTickMax. Logged-out (local) →
// Config.Tick with idleMax == base (idle-backoff disabled; loopback scans are
// cheap and latency-to-first-summary should stay low). A login flip
// re-registers every collector (handleLoginFlip), so the cadence is re-derived
// on flip and never stale. No resolver wired (test fakes) → local cadence.
func (p *Pipeline) cadenceFor(ctx context.Context) (base, idleMax time.Duration) {
	if p.resolver != nil && p.resolver.LoggedIn(ctx) {
		return p.cfg.CloudTickOrDefault(), p.cfg.IdleTickMaxOrDefault()
	}
	base = p.cfg.TickOrDefault()
	return base, base
}

// resolveBackend returns the concrete backend a freshly-registered collector
// should scan + stamp. Uses the login-aware resolver when wired; falls back to
// the shared p.client otherwise (test fakes, or a transient ErrNoBackend).
func (p *Pipeline) resolveBackend(ctx context.Context) WireClient {
	if p.resolver == nil {
		return p.client
	}
	be, err := p.resolver.Backend(ctx)
	if err != nil || be == nil {
		return p.client
	}
	return be
}

// UnregisterGraph cancels the collector context and removes the entry
// from the tracking map. Called by the registry hook on graph unload.
// Safe if (gt, name) is not registered.
func (p *Pipeline) UnregisterGraph(gt kgtypes.GraphType, name string) {
	key := graphKey{GraphType: gt, GraphName: name}
	p.collectorMu.Lock()
	cancel, exists := p.collectorCancels[key]
	delete(p.collectorCancels, key)
	delete(p.collectorWakes, key)
	p.collectorMu.Unlock()
	if exists {
		cancel()
	}
}
