// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// pipeline_refresh.go holds the client-side graph-CATALOG discovery on
// *Pipeline: the RefreshLoadedGraphs loop, the one-shot boot pass, the
// diff-and-dispatch pass (refreshOnce), and the login-flip teardown. Split out
// of pipeline.go to keep that file under the 500-line cap as the gen-poll state
// accreted. This is the GRAPH-SET discovery (which graphs exist); the
// per-(graph,axis) gen discovery lives in genpoll.go.

// RefreshLoadedGraphs is the client-side graph-discovery loop. Each pass reads
// the loaded-graph catalog (listLoadedGraphs → per-type RETURN_MODE_GRAPH_NAMES
// reads), diffs the response against the current per-(gt, name) collector set,
// and calls RegisterGraph / UnregisterGraph for the delta.
//
// The loop is wake-driven: it runs one pass per signal on catalogWake and is
// otherwise completely silent, rather than re-reading the catalog on a fixed
// cadence whether or not the graph set changed. Two things signal it, and
// between them they cover every way the catalog can move:
//   - the account CATALOG watermark moved, observed by genPollOnce on the
//     bulk gen-poll response it already receives (genpoll.go);
//   - a login flip, via CheckLoginFlip — the new backend is a different server
//     whose catalog bears no relation to the old one's, and a flip moves no
//     watermark, so nothing else would re-enumerate.
//
// The catalog is already populated when the loop starts: RefreshOnceForBoot runs
// the same pass once at bootstrap, before this goroutine is launched.
//
// Throttle insurance: when a whole pass is lost to a remote 429 (refreshOnce
// reports throttled), the loop backs off on a dedicated errBackoff gate and
// retries the pass it still owes rather than re-firing immediately — the
// discovery equivalent of the collector's #3 scan-error backoff. Without it a
// sustained 429 turns the retry into a tight storm against the shared limiter
// (backoff.go's documented bug class). A clean pass resets the gate. This retry
// is the loop's only remaining wait, and it is scoped to work already known to
// be owed.
//
// refreshOnce runs synchronously, so a slow pass naturally delays the next one —
// no separate single-flight guard is needed.
//
// Exits on ctx.Done.
//
// Query-origin: the ctx handed in is the daemon wire ctx, which has no
// originating tool call to inherit an operation from, so the loop stamps its own
// ONCE HERE rather than per RPC — every catalog read the loop issues derives
// from this ctx, so the stamp covers the whole loop body including anything
// added to it later. Unstamped, these reads land in the client.unstamped bucket,
// indistinguishable in the metrics from a real client stamping bug.
func (p *Pipeline) RefreshLoadedGraphs(ctx context.Context) {
	ctx = graphclient.WithOperation(ctx, graphclient.OpPipelineGraphDiscovery)
	gate := newErrBackoff(p.cfg.ErrBackoffBaseOrDefault(), p.cfg.ErrBackoffMaxOrDefault())
	// pending means "a pass is already owed" — set only by the throttled retry
	// below. It starts false because the boot pass has already run.
	pending := false
	for {
		if !pending {
			select {
			case <-ctx.Done():
				return
			case <-p.catalogWake:
			}
		}
		pending = false
		hint, throttled := p.refreshOnce(ctx)
		if throttled {
			// Sustained 429: honor the server's Retry-After (or blind exponential)
			// rather than re-firing immediately and feeding the storm. The pass is
			// still owed, so it is retried without waiting for a new wake — a wake
			// that arrived during the backoff would coalesce onto the same token.
			d := gate.failHint(hint)
			slog.Debug("pipeline.refresh: discovery throttled; backing off",
				"delay", d, "retry_after_hint", hint)
			pending = true
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
			continue
		}
		gate.ok()
	}
}

// RefreshOnceForBoot performs the one-shot startup registration pass.
// Exported so the caller (cmd/knowledge.wirePipelineRuntime) can seed
// the collector set BEFORE the background refresh goroutine starts.
//
// This is the ONLY unconditional catalog enumeration: RefreshLoadedGraphs is
// wake-driven and issues nothing until something signals it, so a client whose
// catalog never moves enumerates exactly once per daemon start. It also gives
// the gen-poll loop's own seed poll a non-empty graph set to sample.
//
// Stamps the same query-origin operation as the loop: this runs the identical
// catalog read, and its caller passes a fresh bootstrap ctx that does not
// descend from the loop's, so it needs its own stamp to keep the boot burst out
// of the client.unstamped bucket.
func (p *Pipeline) RefreshOnceForBoot(ctx context.Context) {
	p.refreshOnce(graphclient.WithOperation(ctx, graphclient.OpPipelineGraphDiscovery))
}

// refreshOnce performs one diff-and-dispatch pass. Extracted from
// RefreshLoadedGraphs so the loop body stays under the
// cognitive-complexity cap. Returns (rlHint, throttled) from the catalog
// enumeration so the caller can back off when the whole pass was lost to a
// remote rate-limit; the boot caller (RefreshOnceForBoot) ignores them.
func (p *Pipeline) refreshOnce(ctx context.Context) (time.Duration, bool) {
	// Hazard B (login-state transitions) is NOT handled here. This pass performs
	// the catalog diff only; the teardown that a flip requires — cancel + clear
	// ALL collectors so every survivor graphKey re-registers fresh against the
	// NEW backend, resetting the per-collector dirty-gen caches (collector.go) to
	// 0 and re-binding the concrete backend — lives in CheckLoginFlip below,
	// driven by the client's activity hook on every tool call rather than by this
	// poll. The hazard itself is unchanged: a graphKey present in both catalogs
	// sits in wanted+have and would never re-register on the diff alone, so
	// without that teardown it would keep scanning the new backend with a stale
	// gen → silent no-drain of the cloud gaps.

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

	registered := 0
	for k := range wanted {
		if _, exists := have[k]; !exists {
			p.RegisterGraph(ctx, k.GraphType, k.GraphName)
			registered++
		}
	}
	for k := range have {
		if _, still := wanted[k]; !still && succeeded[k.GraphType] {
			p.UnregisterGraph(k.GraphType, k.GraphName)
		}
	}
	// A freshly-registered graph has no entry in the central gen snapshot yet, so
	// its collector's discover cannot cheap-tick and falls through to a real
	// PipelineScan every tick until it drains. One bulk poll seeds the snapshot for
	// every new graph at once, which is what the two-phase protocol exists to do.
	if registered > 0 {
		p.WakeAll()
	}
	return rlHint, throttled
}

// CheckLoginFlip detects a login-state transition and, on a flip, rebinds
// everything the OLD backend's identity was baked into. Returns true when it flipped.
//
// Beyond handleLoginFlip's per-collector teardown it also clears the CENTRAL
// two-phase gen-poll caches and signals both wake channels: the new backend is a
// different server whose generations bear no relation to the old one's, and
// nothing else would ever re-enumerate (a flip does not move catalog_gen) or
// re-sample (the rebuilt collectors have no snapshot entry).
//
// Lock discipline (the hierarchy pinned on the Pipeline struct): handleLoginFlip
// takes collectorMu internally, so genMu is taken only AFTER it returns — the two
// mutexes are never held together.
func (p *Pipeline) CheckLoginFlip(ctx context.Context) bool {
	if !p.handleLoginFlip(ctx) {
		return false
	}
	// The central two-phase caches outlive the per-collector teardown above, and
	// both are keyed to the OLD backend: genSnapshot's sampled gens describe a
	// server we are no longer talking to, and lastPokedGen is the poke high-water
	// genPollOnce compares against with `>`, so a new backend reporting LOWER gens
	// would never poke a collector again.
	p.genMu.Lock()
	clear(p.genSnapshot)
	clear(p.lastPokedGen)
	p.genMu.Unlock()
	p.wakeCatalog()
	p.WakeAll()
	return true
}

// handleLoginFlip detects a login-state transition since the previous observation
// and, on a flip, cancels + clears every collector so the NEXT catalog diff
// re-registers each wanted graph fresh (reset dirty-gen cache + rebind backend —
// Hazard B). Reached only through CheckLoginFlip, which the client's per-tool-call
// activity hook drives; the catalog poll no longer calls it.
// Returns true only on an actual transition: false for the nil-resolver case (test
// fakes), for the first observation (which only seeds the state), and when the
// state is unchanged. Reuses the cancel-all shape from stopSequence step 1.
func (p *Pipeline) handleLoginFlip(ctx context.Context) bool {
	if p.resolver == nil {
		return false
	}
	now := p.resolver.LoggedIn(ctx)
	p.collectorMu.Lock()
	defer p.collectorMu.Unlock()
	if !p.lastLoggedInSet {
		p.lastLoggedIn = now
		p.lastLoggedInSet = true
		return false
	}
	if now == p.lastLoggedIn {
		return false
	}
	slog.Info("pipeline.refresh: login state flipped — tearing down all collectors to rebind backend + reset gen caches",
		"logged_in", now)
	for _, cancel := range p.collectorCancels {
		cancel()
	}
	p.collectorCancels = make(map[graphKey]context.CancelFunc)
	p.collectorWakes = make(map[graphKey][]chan struct{})
	p.lastLoggedIn = now
	return true
}
