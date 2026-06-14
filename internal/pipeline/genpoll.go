// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// genpoll.go holds the client-side TWO-PHASE bulk gen-poll: ONE central loop on
// *Pipeline issues a single PipelineGenPoll RPC per tick covering EVERY loaded
// graph (Phase 1 — server reads __graphs once, samples per-(graph,axis) dirty-gen,
// NO gap walk), then selectively pokes only the collectors whose gen advanced past
// the loop's own watermark. Each poked collector's discover then issues the
// existing per-axis PipelineScan as the Phase-2 detail fetch. This REPLACES the
// prior up-to-2N concurrent PipelineScan fan-out per tick (collector_discover.go)
// with one bulk poll + selective wake.
//
// LOCK HIERARCHY (explicit, not emergent — see the Pipeline struct's genMu doc):
// genMu is acquired ONLY here (genPollOnce's snapshot update + genSnapshotFor's
// read). genPollOnce RELEASES genMu before it acquires collectorMu to poke wakes —
// the two mutexes are never held at the same time.

// axisGens is the per-(graph) pair of dirty-gens the bulk poll tracks, one per
// drained axis. The zero value (0, 0) is the cold-start state.
type axisGens struct {
	summary uint64
	embed   uint64
}

// wake-channel index → axis mapping. collectorWakes[key] is built as
// []chan struct{}{c.summaryWake, c.embedWake} (pipeline_collectors.go), so index 0
// is the summary wake and index 1 is the embed wake. These named constants keep
// the index→axis mapping explicit and resilient to reordering.
const (
	summaryWakeIdx = 0
	embedWakeIdx   = 1
)

// RunGenPollLoop is the central two-phase bulk gen-poll loop. It issues ONE
// PipelineGenPoll RPC per tick (genPollOnce) covering every loaded graph and
// selectively pokes the collectors whose per-(graph,axis) gen advanced. It mirrors
// RefreshLoadedGraphs's structure exactly: an errBackoff gate, a login-aware tick
// cadence (discoveryTick — the SAME base the discovery + collector loops use), an
// immediate-trigger wake channel (genPollWake, signaled by WakeAll), and a clean
// exit on ctx.Done.
//
// Throttle insurance (same as RefreshLoadedGraphs): when the whole poll is lost to
// a remote 429, back off on the dedicated gate via its Retry-After hint rather than
// re-firing at the base cadence and feeding the storm. A clean tick resets the gate.
//
// genPollOnce runs synchronously, so a slow poll naturally delays the next one — no
// separate single-flight guard is needed.
func (p *Pipeline) RunGenPollLoop(ctx context.Context) {
	gate := newErrBackoff(p.cfg.ErrBackoffBaseOrDefault(), p.cfg.ErrBackoffMaxOrDefault())
	for {
		hint, throttled := p.genPollOnce(ctx)
		var d time.Duration
		if throttled {
			d = gate.failHint(hint)
			slog.Debug("pipeline.genpoll: bulk poll throttled; backing off",
				"delay", d, "retry_after_hint", hint)
		} else {
			gate.ok()
			d = p.discoveryTick(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-p.genPollWake:
			// A collect (WakeAll) signaled — poll immediately rather than waiting
			// out the discovery tick.
		case <-time.After(d):
		}
	}
}

// genPollOnce performs one bulk gen-poll pass: build the loaded-graph set from the
// collector registry, issue ONE PipelineGenPoll RPC, update the shared snapshot,
// and poke the collectors whose gen advanced past the loop's watermark. Returns
// (retryAfterHint, throttled) so the caller backs off when the whole poll was lost
// to a remote 429.
//
// Strictly follows the pinned lock hierarchy: collectorMu (read the graph set) →
// [no locks while the RPC is in flight] → genMu (update snapshot + compute pokes)
// → collectorMu (deliver pokes). genMu and collectorMu are never held together.
func (p *Pipeline) genPollOnce(ctx context.Context) (time.Duration, bool) {
	// (1) Snapshot the loaded-graph set under collectorMu, then release it before
	// the RPC. The pipeline asks only for the graphs it currently drains (its
	// collector registry) — the EXPLICIT-set production path.
	p.collectorMu.Lock()
	keys := make([]graphKey, 0, len(p.collectorCancels))
	for k := range p.collectorCancels {
		keys = append(keys, k)
	}
	p.collectorMu.Unlock()

	if len(keys) == 0 {
		return 0, false // no collectors yet — nothing to poll
	}

	reqGraphs := make([]*knowledgev1.PipelineGenPollGraph, 0, len(keys))
	for _, k := range keys {
		reqGraphs = append(reqGraphs, &knowledgev1.PipelineGenPollGraph{
			GraphType: string(k.GraphType),
			GraphName: k.GraphName,
		})
	}

	// (2) ONE bulk RPC — no locks held while it is in flight.
	resp, err := p.client.PipelineGenPoll(ctx, &knowledgev1.PipelineGenPollRequest{Graphs: reqGraphs})
	if err != nil {
		if hint, isRL := rateLimitHint(err); isRL {
			return hint, true
		}
		slog.Debug("pipeline.genpoll: bulk poll failed", "error", err)
		return 0, false
	}

	// (3) Update the shared snapshot and compute which (graph,axis) pairs advanced
	// past the loop's own poke watermark — all under genMu, which is RELEASED before
	// any collectorMu access below.
	type poke struct {
		key     graphKey
		wakeIdx int
	}
	var pokes []poke
	p.genMu.Lock()
	for _, e := range resp.GetEntries() {
		key := graphKey{GraphType: kgtypes.GraphType(e.GetGraphType()), GraphName: e.GetGraphName()}
		cur := p.genSnapshot[key]
		poked := p.lastPokedGen[key]
		switch e.GetAxis() {
		case "summary":
			cur.summary = e.GetDirtyGen()
			if e.GetDirtyGen() > poked.summary {
				poked.summary = e.GetDirtyGen()
				pokes = append(pokes, poke{key: key, wakeIdx: summaryWakeIdx})
			}
		case "embed":
			cur.embed = e.GetDirtyGen()
			if e.GetDirtyGen() > poked.embed {
				poked.embed = e.GetDirtyGen()
				pokes = append(pokes, poke{key: key, wakeIdx: embedWakeIdx})
			}
		}
		p.genSnapshot[key] = cur
		p.lastPokedGen[key] = poked
	}
	p.genMu.Unlock()

	// (4) Deliver the selective pokes under collectorMu (genMu already released).
	// Each poke is a non-blocking coalescing send — identical to WakeAll — so a
	// poke to an already-signaled collector is a no-op and never blocks the loop.
	for _, pk := range pokes {
		p.pokeAxisWake(pk.key, pk.wakeIdx)
	}
	return 0, false
}

// pokeAxisWake delivers a single non-blocking coalescing wake to one collector's
// per-axis wake channel (summaryWakeIdx / embedWakeIdx). Mirrors WakeAll's send
// shape: a wake already queued is a no-op (coalesce), and the send never blocks.
// A no-op when the collector is no longer registered or lacks the indexed channel.
func (p *Pipeline) pokeAxisWake(key graphKey, wakeIdx int) {
	p.collectorMu.Lock()
	defer p.collectorMu.Unlock()
	wakes, ok := p.collectorWakes[key]
	if !ok || wakeIdx >= len(wakes) {
		return
	}
	select {
	case wakes[wakeIdx] <- struct{}{}:
	default: // already signaled — coalesce
	}
}

// genSnapshotFor returns the bulk poll's last-sampled (summary, embed) dirty-gens
// for one graph, plus whether the central loop has sampled it yet. The per-collector
// discover consults this to decide whether to issue a Phase-2 detail PipelineScan.
// Takes ONLY genMu (per the pinned lock hierarchy).
func (p *Pipeline) genSnapshotFor(key graphKey) (summary, embed uint64, ok bool) {
	p.genMu.Lock()
	defer p.genMu.Unlock()
	g, ok := p.genSnapshot[key]
	return g.summary, g.embed, ok
}
