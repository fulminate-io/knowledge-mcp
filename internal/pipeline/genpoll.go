// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// genpoll.go holds the client-side TWO-PHASE bulk gen-poll: ONE central loop on
// *Pipeline issues a single PipelineGenPoll RPC per WAKE covering EVERY loaded
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

// axisGens is the per-(graph) set of poll-axis watermarks the bulk poll tracks,
// one per axis. The zero value is the cold-start state.
type axisGens struct {
	summary uint64
	embed   uint64
	// segmentStamp is the segment cheap-tick axis and it is a TIMESTAMP, not a
	// counter: the maximum vector-write / erasure-append stamp the server holds for
	// this graph, in unix nanos. It is tracked here for the same reason the two gens
	// are — so the poll can tell an ADVANCE from a repeat — but it is compared and
	// consumed differently, see the "segment" case in genPollOnce.
	segmentStamp int64
	// corpusStamp is the CORPUS cheap-tick axis: the maximum Node.updated_at the
	// server holds for this graph, in unix nanos. It rides the same "segment" entry
	// as segmentStamp above but is a SEPARATE quantity, and the separation is the
	// point: segmentStamp folds vector-write and erasure-append times, so a BM25 arm
	// gated on it would wake on embedding activity — the coupling this decoupling removes —
	// and would still miss deletes, which move neither the embed gen nor that stamp.
	//
	// UNLIKE segmentStamp IT IS NOT NUDGE-DEBOUNCED HERE. The BM25 arm compares it
	// against its OWN durable cursor position on its own tick rather than being poked
	// by this loop, so the snapshot records the served value and the arm decides. See
	// corpusStampFor.
	corpusStamp int64
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
// PipelineGenPoll RPC per poll (genPollOnce) covering every loaded graph and
// selectively pokes the collectors whose per-(graph,axis) gen advanced. It mirrors
// RefreshLoadedGraphs's structure exactly: an errBackoff gate, a wake channel it
// parks on, and a clean exit on ctx.Done.
//
// The loop is wake-driven: one poll per signal on genPollWake, plus ONE seed poll
// at start, and nothing at all in between. The wake sources are a collect or any
// bulk write (WakeAll), the client's activity hook when the response watermark
// moves, a new graph's registration, and a login flip.
//
// THE SEED POLL IS NOT OPTIONAL. A collector's discover skips its Phase-2
// PipelineScan only when the shared snapshot already KNOWS its graph; until the
// central poll has run once, genSnapshotFor reports the graph unknown and EVERY
// collector falls through to a real scan on every one of its own ticks — the
// fan-out this two-phase protocol exists to remove. One poll at loop entry
// populates the snapshot for every loaded graph at once. It has a graph set to
// sample because bootstrap registers collectors (via RefreshOnceForBoot) BEFORE
// launching this goroutine; were the set empty, genPollOnce's zero-graph path
// issues no RPC and wakes the catalog loop instead, so even a graphless boot
// recovers rather than parking forever.
//
// Throttle insurance (same as RefreshLoadedGraphs): when the whole poll is lost to
// a remote 429, back off on the dedicated gate via its Retry-After hint and retry
// the poll still owed rather than re-firing immediately and feeding the storm. A
// clean poll resets the gate. That retry is the loop's only remaining wait, and it
// is scoped to work already known to be owed.
//
// genPollOnce runs synchronously, so a slow poll naturally delays the next one — no
// separate single-flight guard is needed.
func (p *Pipeline) RunGenPollLoop(ctx context.Context) {
	gate := newErrBackoff(p.cfg.ErrBackoffBaseOrDefault(), p.cfg.ErrBackoffMaxOrDefault())
	// pending means "a poll is already owed". It starts TRUE for the boot seed
	// documented above; afterwards only the throttled retry sets it.
	pending := true
	for {
		if !pending {
			select {
			case <-ctx.Done():
				return
			case <-p.genPollWake:
			}
		}
		pending = false
		hint, throttled := p.genPollOnce(ctx)
		if throttled {
			d := gate.failHint(hint)
			slog.Debug("pipeline.genpoll: bulk poll throttled; backing off",
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

// genPollOnce performs one bulk gen-poll pass: build the loaded-graph set from the
// collector registry, issue ONE PipelineGenPoll RPC, update the shared snapshot,
// and poke the collectors whose gen advanced past the loop's watermark. It also
// compares the account CATALOG watermark the same response carries and wakes the
// catalog-discovery loop when it moved. With NO graphs registered it issues no RPC
// at all and wakes only the catalog loop. Returns (retryAfterHint, throttled) so
// the caller backs off when the whole poll was lost to a remote 429.
//
// Strictly follows the pinned lock hierarchy: collectorMu (read the graph set) →
// [no locks while the RPC is in flight] → genMu (update snapshot + compute pokes)
// → collectorMu (deliver pokes). genMu and collectorMu are never held together,
// and neither the pokes nor the catalog wake are delivered while genMu is held.
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
		// No collectors means a gen poll can teach us nothing — there are no graphs to
		// sample. Look at the CATALOG directly instead, which is the only thing that can
		// move us out of this state. Bounded by the upstream cool-off, so a permanently
		// graphless client still wakes at most once per activity window.
		p.wakeCatalog()
		return 0, false
	}

	reqGraphs := make([]*knowledgev1.PipelineGenPollGraph, 0, len(keys))
	for _, k := range keys {
		reqGraphs = append(reqGraphs, &knowledgev1.PipelineGenPollGraph{
			GraphType: string(k.GraphType),
			GraphName: k.GraphName,
		})
	}

	// (2) ONE bulk RPC — no locks held while it is in flight.
	// The cheap once-per-tick bulk poll. Distinct from the gap scans it gates so
	// the metrics can show the poll staying cheap while scans stay rare.
	ctx = graphclient.WithOperation(ctx, graphclient.OpPipelineGenPoll)
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
	pokes, segmentNudges, catalogMoved := p.applyGenPollResponse(resp)

	// (4) Deliver the selective pokes under collectorMu (genMu already released).
	// Each poke is a non-blocking coalescing send — identical to WakeAll — so a
	// poke to an already-signaled collector is a no-op and never blocks the loop.
	for _, pk := range pokes {
		p.pokeAxisWake(pk.key, pk.wakeIdx)
	}
	// The catalog wake is delivered here for the same reason as the pokes: never
	// while holding genMu.
	if catalogMoved {
		p.wakeCatalog()
	}
	// The segment nudges likewise. Each records one graph on the segment manager's
	// reconcile-nudge set, which the reconcile loop drains on its next wake — this
	// adds no loop, no ticker and no RPC, only a new CALLER of a wake path that
	// already runs end to end.
	//
	// nil when no segment manager is wired (a degraded or headless client, and every
	// pipeline test that does not exercise this axis), which is a real absence rather
	// than a swallowed failure: there is nothing to nudge.
	if nudge := p.segmentNudger(); nudge != nil {
		for _, key := range segmentNudges {
			nudge(key.GraphType, key.GraphName)
		}
	}
	return 0, false
}

// genPoke names one collector wake the poll decided to deliver: which graph, and
// which per-axis wake channel on it.
type genPoke struct {
	key     graphKey
	wakeIdx int
}

// applyGenPollResponse is genPollOnce's step (3): it folds one poll response into
// the shared snapshot and returns the wakes that response earned — the collector
// pokes, the segment nudges, and whether the account catalog watermark moved.
//
// IT OWNS THE WHOLE genMu SPAN AND NOTHING ELSE. Taking and releasing genMu inside
// this function is what keeps genPollOnce's pinned lock hierarchy checkable by
// reading one function: every value this returns is computed under genMu, and every
// delivery its caller makes afterwards happens with genMu already released. Deliver
// nothing from in here — a wake sent under genMu is the exact inversion the caller's
// doc comment forbids.
func (p *Pipeline) applyGenPollResponse(
	resp *knowledgev1.PipelineGenPollResponse,
) (pokes []genPoke, segmentNudges []graphKey, catalogMoved bool) {
	// Segment nudges are collected SEPARATELY from collector pokes because they
	// travel to a different destination — the segment manager's reconcile-nudge
	// recorder, not a collector wake channel — and, like the pokes, they are
	// delivered only AFTER genMu is released.
	p.genMu.Lock()
	defer p.genMu.Unlock()
	for _, e := range resp.GetEntries() {
		key := graphKey{GraphType: kgtypes.GraphType(e.GetGraphType()), GraphName: e.GetGraphName()}
		cur := p.genSnapshot[key]
		poked := p.lastPokedGen[key]
		switch e.GetAxis() {
		case "summary":
			cur.summary = e.GetDirtyGen()
			if e.GetDirtyGen() > poked.summary {
				poked.summary = e.GetDirtyGen()
				pokes = append(pokes, genPoke{key: key, wakeIdx: summaryWakeIdx})
			}
		case "embed":
			cur.embed = e.GetDirtyGen()
			if e.GetDirtyGen() > poked.embed {
				poked.embed = e.GetDirtyGen()
				pokes = append(pokes, genPoke{key: key, wakeIdx: embedWakeIdx})
			}
		case "segment":
			// THE COMPARISON IS AGAINST THE LAST STAMP THIS CLIENT POKED ON, exactly
			// as the two axes above compare against their own poke history — NOT
			// against the durable merge watermark. That distinction is structural, not
			// stylistic: the merge watermark advances only to the SERVED SAFE HORIZON,
			// which by construction LAGS the maximum stamp, so `stamp > watermark`
			// stays true indefinitely while writes continue and the nudge would fire on
			// EVERY poll — turning a cheap tick into a continuous rebuild loop at
			// roughly 415ms per rebuilt partition. Debouncing against our own poke
			// history is self-limiting: one nudge per distinct advance, however far
			// behind any durable position sits.
			//
			// `>` AND NOT `!=`, and this differs deliberately from the catalog_gen
			// comparison a few lines below. catalog_gen is an account-scoped
			// PER-REPLICA SAMPLE where a backward move is still movement worth
			// reacting to. This is a monotonic PER-GRAPH MAXIMUM, so a backward move
			// means nothing here and `!=` would wake on noise. The two neighboring
			// comparisons are therefore correctly different; do not "harmonize" them.
			//
			// A ZERO IS SKIPPED BY THE SAME `>`: zero is what the server serves for a
			// graph whose stamp has never been recorded, and it must produce no nudge.
			cur.segmentStamp = e.GetSegmentDeltaStampNanos()
			if e.GetSegmentDeltaStampNanos() > poked.segmentStamp {
				poked.segmentStamp = e.GetSegmentDeltaStampNanos()
				segmentNudges = append(segmentNudges, key)
			}
			// The corpus stamp rides the same entry and is RECORDED WITHOUT A NUDGE,
			// deliberately. Its consumer (the BM25 arm) holds a durable per-graph
			// cursor and compares against it on its own tick, so a poke here would be
			// a second, weaker trigger for a decision the arm already makes correctly —
			// and one that could fire for a graph whose cursor is already past the
			// stamp. Recording is all this loop owes it.
			cur.corpusStamp = e.GetCorpusDeltaStampNanos()
		}
		p.genSnapshot[key] = cur
		p.lastPokedGen[key] = poked
	}
	// The account CATALOG watermark rides the same response. Its contract, quoted
	// from engine.proto: "The same per-replica SAMPLE caveat as freshness_gen
	// applies — compare for change, not for increase." Hence `!=` and never `>`: a
	// backward move across replicas or a restart is still movement, and treating it
	// as noise would strand the client on a stale catalog.
	//
	// 0 is skipped entirely — it is what a server serves BEFORE ITS FIRST BUMP, and
	// the permanent value from any flavor that maintains no watermark at all.
	// Skipping also leaves the last real observation intact.
	//
	// The FIRST observation records without waking: the boot pass already
	// enumerated the catalog.
	if cg := resp.GetCatalogGen(); cg != 0 {
		if p.lastCatalogGenSet && cg != p.lastCatalogGen {
			catalogMoved = true
		}
		p.lastCatalogGen = cg
		p.lastCatalogGenSet = true
	}
	return pokes, segmentNudges, catalogMoved
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

// corpusStampFor returns the bulk poll's last-sampled corpus cheap-tick stamp for
// one graph — the maximum Node.updated_at the server holds — plus whether the
// central loop has sampled that graph yet. Takes ONLY genMu, per the pinned lock
// hierarchy, exactly as genSnapshotFor does.
//
// THE ok FLAG IS THE ARM'S FAIL-OPEN CONDITION, and it must stay distinguishable
// from a zero stamp. ok=false means the poll has not run for this graph yet, and
// the arm DRAINS rather than skipping: treating "not yet sampled" as "nothing
// changed" would keep a freshly-registered graph out of the BM25 corpus until some
// unrelated event happened to poll it. A zero stamp WITH ok=true is different — it
// is the server's honest "never recorded", and an empty graph has nothing to drain.
func (p *Pipeline) corpusStampFor(key graphKey) (stamp int64, ok bool) {
	p.genMu.Lock()
	defer p.genMu.Unlock()
	g, ok := p.genSnapshot[key]
	return g.corpusStamp, ok
}

// SegmentStampFor returns the bulk poll's last-sampled SEGMENT cheap-tick stamp for
// one graph — the maximum of the server's vector-write and erasure-append stamps,
// in unix nanos — plus whether the central loop has sampled that graph yet.
//
// IT IS EXPORTED BECAUSE ITS CONSUMER IS OUTSIDE THIS PACKAGE, unlike the two
// unexported siblings above whose consumers are the collector loops. The
// fuse-caught-up predicate lives in bootstrap (it needs the segment manager's local
// watermark, which this package does not have), and bootstrap already holds the
// Pipeline. That direction is the only one available: pipeline must never import
// bootstrap.
//
// THE ok FLAG MUST STAY DISTINGUISHABLE FROM A ZERO STAMP, and here the two mean
// opposite things for the caller. ok=false is "the poll has not sampled this graph
// yet", so nothing is known and no comparison may be made. A zero stamp WITH
// ok=true is the server's honest "never recorded" — an unseeded graph. A consumer
// that collapsed them would treat an unsampled graph as one with no changes, which
// is the same silent-zero failure the interface's own SegmentDeltaStampNanos doc
// logs loudly about.
//
// Takes ONLY genMu, per the pinned lock hierarchy, exactly as its siblings do.
func (p *Pipeline) SegmentStampFor(gt kgtypes.GraphType, name string) (stamp int64, ok bool) {
	if p == nil {
		return 0, false
	}
	p.genMu.Lock()
	defer p.genMu.Unlock()
	g, ok := p.genSnapshot[graphKey{GraphType: gt, GraphName: name}]
	return g.segmentStamp, ok
}
