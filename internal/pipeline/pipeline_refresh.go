// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"time"
)

// pipeline_refresh.go holds the client-side graph-CATALOG discovery poll on
// *Pipeline: the RefreshLoadedGraphs loop, its login-aware cadence, the one-shot
// boot pass, the per-tick diff-and-dispatch (refreshOnce), and the login-flip
// teardown. Split out of pipeline.go to keep that file under the 500-line cap as
// the gen-poll state accreted. This is the GRAPH-SET discovery (which graphs
// exist); the per-(graph,axis) gen discovery lives in genpoll.go.

// RefreshLoadedGraphs is the client-side graph-discovery poll. It polls the
// loaded-graph catalog (listLoadedGraphs → per-type RETURN_MODE_GRAPH_NAMES
// reads), diffs the response against the current per-(gt, name) collector set,
// and calls RegisterGraph / UnregisterGraph for the delta. Worst-case lag for
// graph create/destroy propagation: one poll interval — the price of a wire
// poll rather than an in-process registry hook.
//
// Cadence is remote-aware, mirroring the per-graph collector loop (cadenceFor):
// a logged-in (remote) backend polls at the slow Config.CloudTick base, a
// logged-out (local loopback) backend at the cheap Config.Tick base. Polling
// the REMOTE catalog at the 250ms local cadence fires len(eligibleTypes) wire
// RPCs every 250ms (~24 RPC/s) and saturates the backend's per-IP rate limiter —
// the cadence bug this loop previously had.
//
// Throttle insurance: when a whole tick is lost to a remote 429 (refreshOnce
// reports throttled), the loop backs off on a dedicated errBackoff gate instead
// of re-firing at the base cadence — the discovery-poll equivalent of the
// collector's #3 scan-error backoff. Without it a sustained 429 turns the poll
// into a tight retry storm against the shared limiter (backoff.go's documented
// bug class). A clean tick resets the gate.
//
// refreshOnce runs synchronously, so a slow poll naturally delays the next one —
// no separate single-flight guard is needed.
//
// Exits on ctx.Done.
func (p *Pipeline) RefreshLoadedGraphs(ctx context.Context) {
	gate := newErrBackoff(p.cfg.ErrBackoffBaseOrDefault(), p.cfg.ErrBackoffMaxOrDefault())
	for {
		hint, throttled := p.refreshOnce(ctx)
		var d time.Duration
		if throttled {
			// Sustained 429: honor the server's Retry-After (or blind exponential)
			// rather than re-polling at the base cadence and feeding the storm.
			d = gate.failHint(hint)
			slog.Debug("pipeline.refresh: discovery throttled; backing off",
				"delay", d, "retry_after_hint", hint)
		} else {
			gate.ok()
			d = p.discoveryTick(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}
}

// discoveryTick returns the base poll interval for the graph-discovery loop:
// Config.CloudTick when bound to a remote (logged-in) backend, Config.Tick for
// local loopback. Reuses cadenceFor's login-aware base so discovery and the
// per-graph collectors stay on the same remote-vs-local cadence.
func (p *Pipeline) discoveryTick(ctx context.Context) time.Duration {
	base, _ := p.cadenceFor(ctx)
	return base
}

// RefreshOnceForBoot performs the one-shot startup registration pass.
// Exported so the caller (cmd/knowledge.wirePipelineRuntime) can seed
// the collector set BEFORE the background refresh goroutine starts so
// the very first tick has a populated state to diff against.
func (p *Pipeline) RefreshOnceForBoot(ctx context.Context) {
	p.refreshOnce(ctx)
}

// refreshOnce performs one diff-and-dispatch pass. Extracted from
// RefreshLoadedGraphs so the per-tick body stays under the
// cognitive-complexity cap. Returns (rlHint, throttled) from the catalog
// enumeration so the caller can back off when the whole tick was lost to a
// remote rate-limit; the boot caller (RefreshOnceForBoot) ignores them.
func (p *Pipeline) refreshOnce(ctx context.Context) (time.Duration, bool) {
	// Hazard B: on a login-state transition, tear down + clear ALL collectors
	// BEFORE the diff so every survivor graphKey re-registers fresh against the
	// NEW backend — resetting the per-collector dirty-gen caches (collector.go)
	// to 0 (re-scan from scratch) and re-binding the concrete backend. Without
	// this a graphKey present in both catalogs would never re-register (it sits
	// in both wanted+have) and would keep scanning the new backend with a stale
	// gen → silent no-drain of the cloud gaps.
	p.handleLoginFlip(ctx)

	// listLoadedGraphs never aborts: a per-type enumeration failure (rollout 502,
	// permission_denied) is skipped, and `succeeded` reports which types this tick
	// actually enumerated. We register every wanted graph, but only UNREGISTER
	// within successfully-enumerated types — a type whose enumeration failed has
	// an incomplete wanted-set this tick, so tearing down its collectors on the
	// strength of that empty set would be the churn (and stall) we are fixing.
	graphs, succeeded, rlHint, throttled := listLoadedGraphs(ctx, p.client)
	wanted := make(map[graphKey]struct{}, len(graphs))
	for _, g := range graphs {
		wanted[graphKey(g)] = struct{}{}
	}
	p.collectorMu.Lock()
	have := make(map[graphKey]struct{}, len(p.collectorCancels))
	for k := range p.collectorCancels {
		have[k] = struct{}{}
	}
	p.collectorMu.Unlock()

	for k := range wanted {
		if _, exists := have[k]; !exists {
			p.RegisterGraph(ctx, k.GraphType, k.GraphName)
		}
	}
	for k := range have {
		if _, still := wanted[k]; !still && succeeded[k.GraphType] {
			p.UnregisterGraph(k.GraphType, k.GraphName)
		}
	}
	return rlHint, throttled
}

// handleLoginFlip detects a login-state transition since the previous tick and,
// on a flip, cancels + clears every collector so the subsequent diff re-registers
// each wanted graph fresh (reset dirty-gen cache + rebind backend — Hazard B).
// No-op when no resolver is wired (test fakes) or the state is unchanged. Reuses
// the cancel-all shape from stopSequence step 1.
func (p *Pipeline) handleLoginFlip(ctx context.Context) {
	if p.resolver == nil {
		return
	}
	now := p.resolver.LoggedIn(ctx)
	p.collectorMu.Lock()
	defer p.collectorMu.Unlock()
	if !p.lastLoggedInSet {
		p.lastLoggedIn = now
		p.lastLoggedInSet = true
		return
	}
	if now == p.lastLoggedIn {
		return
	}
	slog.Info("pipeline.refresh: login state flipped — tearing down all collectors to rebind backend + reset gen caches",
		"logged_in", now)
	for _, cancel := range p.collectorCancels {
		cancel()
	}
	p.collectorCancels = make(map[graphKey]context.CancelFunc)
	p.collectorWakes = make(map[graphKey][]chan struct{})
	p.lastLoggedIn = now
}
