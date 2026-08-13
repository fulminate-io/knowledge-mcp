// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
)

// pipeline_refresh.go holds the client-side collector-registration pass on
// *Pipeline: the RefreshLoadedGraphs loop, the one-shot boot pass, the
// diff-and-dispatch pass (refreshOnce), and the login-flip teardown. Split out
// of pipeline.go to keep that file under the 500-line cap as the gen-poll state
// accreted. This decides the GRAPH SET (which graphs this client drains, read
// off the working set); the per-(graph,axis) gen discovery lives in genpoll.go.
//
// REGISTRATION IS THE RESOURCE THIS FILE GATES. A registered collector is a
// goroutine pair plus a gen-poll entry plus a scan cadence, so a graph outside
// the working set gets no collector AT ALL rather than a registered-but-idle
// one — there is no cheaper place downstream to decline the work.

// RefreshLoadedGraphs is the client-side graph-discovery loop. Each pass reads
// the WORKING SET — the graphs this client process has directly interacted with
// — diffs it against the current per-(gt, name) collector set, and calls
// RegisterGraph / UnregisterGraph for the delta.
//
// A pass costs ZERO RPCs. The wanted set is a local map read, not a remote
// catalog enumeration, which is what makes the scoping true rather than merely
// filtered: a graph this client never touched is never asked about, never
// registered, and therefore never scanned, enriched or written back to.
//
// The loop is wake-driven: it runs one pass per signal and is otherwise
// completely silent. Two things signal it, and between them they cover every way
// the wanted set can move:
//   - an ADMISSION — a search, a collect or a user write earned a new graph its
//     place, which is the only thing that can ADD to the wanted set;
//   - a login flip, via CheckLoginFlip — the new backend is a different server,
//     and the flip tears down every collector so each survivor must re-register
//     against it.
//
// The collector set is already populated when the loop starts: RefreshOnceForBoot
// runs the same pass once at bootstrap, before this goroutine is launched. The
// loop runs one more pass on entry anyway — it costs no RPC, and it is what makes
// an admission landing in the gap between that boot pass and this goroutine
// actually being scheduled take effect rather than wait for an unrelated signal.
//
// There is no throttle gate here, deliberately, and its absence is not an
// oversight to be repaired: a pass performs no RPC, so nothing in it can be
// rate-limited. RunGenPollLoop keeps ITS gate (genpoll.go) because the bulk
// gen-poll still issues a real RPC for admitted graphs and can still be throttled.
//
// refreshOnce runs synchronously, so a slow pass naturally delays the next one —
// no separate single-flight guard is needed.
//
// Exits on ctx.Done.
//
// Query-origin: this loop carries NO operation stamp of its own, and that is a
// consequence of the pass issuing no RPC rather than an omission. A stamp exists
// to attribute load, and there is none here to attribute; every RPC the pipeline
// does issue stamps itself at its own call site (the gap scan, the writeback and
// the bulk gen-poll each apply their own term immediately before the call), so a
// stamp here could not label anything even indirectly through the collectors this
// pass registers.
func (p *Pipeline) RefreshLoadedGraphs(ctx context.Context) {
	// REGISTER THE ADMISSION WAITER FIRST, THEN READ MEMBERSHIP, THEN WAIT. Wake
	// hands out a per-caller channel and a channel registered after an admission
	// does not carry that admission, so registering here — before the pass below —
	// is what stops an admission that lands between this goroutine's launch and its
	// first scheduling from being lost. The pass at the top of the loop closes the
	// same window from the other side: it reads the set that already exists rather
	// than assuming the first interesting thing happens after we start waiting.
	//
	// A nil working set yields a nil channel. Receiving on a nil channel blocks
	// forever, which is CORRECT here: that arm simply never fires and the loop still
	// serves ctx.Done and catalogWake. It must NOT be "fixed" into a default arm —
	// a default would spin this loop.
	admitted := p.workingSet.Wake()
	for {
		p.refreshOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-p.catalogWake:
		case <-admitted:
		}
	}
}

// RefreshOnceForBoot performs the one-shot startup registration pass.
// Exported so the caller (cmd/knowledge.wirePipelineRuntime) can seed
// the collector set BEFORE the background refresh goroutine starts.
//
// It registers whatever the working set already holds, which on a cold boot is
// NOTHING: membership is in-memory and per-process, so a freshly started daemon
// maintains no graph until the first interaction admits one. That is the intended
// consequence of the rule, not a gap for a boot-time seed to fill. It also gives
// the gen-poll loop's own seed poll whatever graph set exists to sample — an
// empty one issues no RPC at all.
//
// This is a WIRING wrapper, not a second mechanism: it delegates to the same
// refreshOnce the loop runs, so scoping that pass scoped this one too, and there
// is no separate boot discovery path to go looking for.
//
// Carries no query-origin stamp, for the same reason the loop carries none: the
// pass issues no RPC to attribute.
func (p *Pipeline) RefreshOnceForBoot(ctx context.Context) {
	p.refreshOnce(ctx)
}

// wantedGraphs returns every graph the pipeline should currently drain: the
// members of the client's working set, filtered to the types this pipeline
// enriches (pipelineDrainsType). It is a local read and issues NO RPCs.
//
// A nil working set yields NOTHING, which is the default-deny direction: an
// unwired pipeline drains nothing rather than everything.
//
// Registered CUSTOM graph types need no discovery step here. A custom-type
// member arrives already carrying its own GraphType — recorded by whichever
// interaction admitted it — so the type is known from the member itself rather
// than from a registry browse.
func (p *Pipeline) wantedGraphs() []GraphRef {
	members := p.workingSet.Members()
	out := make([]GraphRef, 0, len(members))
	for _, m := range members {
		if !pipelineDrainsType(m.GraphType) {
			continue
		}
		out = append(out, GraphRef{GraphType: m.GraphType, GraphName: m.Name})
	}
	return out
}

// refreshOnce performs one diff-and-dispatch pass. Extracted from
// RefreshLoadedGraphs so the loop body stays under the
// cognitive-complexity cap.
func (p *Pipeline) refreshOnce(ctx context.Context) {
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

	// The wanted set is a LOCAL read of the working set, so it has no per-type
	// failure mode and no partially-known state: it is either the whole truth or
	// the process is gone. That is why the unregister arm below tears down every
	// collector no longer wanted, with no per-type success guard — the guard that
	// used to sit there existed solely to stop a FAILED remote enumeration from
	// mistaking an empty response for an empty account, and there is no longer a
	// remote enumeration to fail.
	graphs := p.wantedGraphs()
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
		if _, still := wanted[k]; !still {
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
