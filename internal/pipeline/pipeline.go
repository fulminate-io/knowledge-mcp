// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// Pipeline owns the global summary/embed channels, dispatchers, worker
// pools, and per-graph collector goroutines. ONE Pipeline per client daemon
// PROCESS — created once at startup by the client bootstrap
// (cmd/knowledge/internal/bootstrap wirePipelineRuntime, the sole pipeline.New
// site) and SHARED across every MCP session the daemon serves. The
// multi-session HTTP transport holds no per-session pipeline: N concurrent
// sessions share this one pipeline + its one rate gate (the resource fix — a
// constant worker count, not workers×N). Goroutines launch in Start; collectors
// register/unregister as graphs load/unload via the client pipeline-refresh
// loop.
//
// Concurrency model: each loaded graph gets ONE collector goroutine per ENABLED
// axis — the summary loop when a summarizer is configured (summaryEnabled) and
// the embed loop when an embedder is configured (embedEnabled). A disabled axis
// has its dispatcher, worker pool, and per-collector loop all gated off, so a
// graph can run two loops (both axes), one (a single axis), or — only in the
// degenerate both-disabled case wirePipelineRuntime already prevents — none.
// Collectors push work onto the global channels;
// dispatchers batch the work; workers drain the per-batch sub-channels.
// The collector + dispatcher + worker WaitGroups are tracked separately
// so Stop's 7-step ordering (cancel collectors → wait collectors → close
// channels → drain dispatchers → close sub-channels → drain workers →
// wait workers) terminates cleanly even with full channels.
//
// Stop is idempotent via stopOnce.
type Pipeline struct {
	cfg        Config
	client     WireClient
	summarizer SummarizerFunc
	embedder   EmbedderFunc

	// resolver is the OPTIONAL login-aware backend seam (the production
	// routedWireClient over *graphclient.Router). When non-nil the pipeline
	// binds each collector to the CURRENT concrete backend at RegisterGraph
	// time and tears down + rebinds every collector on a login flip
	// (Hazard B). nil for test fakes that don't implement BackendResolver —
	// collectors then ride the shared p.client and no flip detection runs.
	resolver BackendResolver

	// lastLoggedIn caches the login state observed at the previous refreshOnce
	// tick. A transition forces a full collector teardown + rebind so the
	// per-collector dirty-gen caches reset and each collector re-binds the new
	// backend. Guarded by collectorMu (only touched inside refreshOnce, which
	// also serializes via the RefreshLoadedGraphs single-flight semaphore).
	lastLoggedIn    bool
	lastLoggedInSet bool

	// backoff is the shared exponential-backoff gate for LLM calls. One
	// instance across all summary + embed workers — the rate limit it
	// guards is global to the provider, not per-graph or per-worker.
	backoff *errBackoff

	// summaryCircuit and embedCircuit are the two INDEPENDENT per-axis LATCHED
	// pause gates. Where backoff self-clears a transient window, a circuit breaker
	// latches its OWN axis's workers paused on a zero-success storm and stays
	// paused until a human resumes (no self-heal). Each axis owns its own instance:
	// a success on one axis resets only that axis's counter, and an auto-trip
	// pauses only that axis — a failing summary axis no longer stalls healthy
	// embeddings. The single deliberate cross-axis exception is the shared-cause
	// escalation (escalation.go): an auth/quota trip on one axis can propagate to
	// the other when both share the same provider. See circuit_breaker.go.
	summaryCircuit *circuitBreaker
	embedCircuit   *circuitBreaker

	// summaryProvider and embedProvider are each axis's LLM provider identity
	// (copied from cfg at New time). They are read ONLY by the shared-cause
	// escalation coordinator (escalation.go) to gate a same-provider cross-trip:
	// an auth/quota auto-trip on one axis propagates to the other ONLY when these
	// two are equal and non-empty. Empty = unknown = no escalation for that axis.
	summaryProvider string
	embedProvider   string

	// embedRPM is the shared PROACTIVE fixed-rate pacer for embed dispatch.
	// One instance across all embed workers — the provider RPM limit is
	// global. It is the proactive companion to the reactive backoff: it paces
	// the opening burst BEFORE the first 429, where backoff only reacts after
	// one lands. Disabled (no-op) unless cfg.EmbedRPM > 0.
	embedRPM *rpmGate

	summaryCh chan SummaryWork
	embedCh   chan EmbedWork

	// Per-batch sub-channels feeding the worker pools. Sized to absorb
	// worker-count batches in flight without forcing the dispatcher to
	// block on every send. Closed by Stop after channels are drained.
	summaryBatchCh chan []SummaryWork
	embedBatchCh   chan []EmbedWork

	// Per-(GraphType, GraphName) cancel funcs for the collector
	// goroutines. RegisterGraph stores; UnregisterGraph + Stop call.
	collectorMu      sync.Mutex
	collectorCancels map[graphKey]context.CancelFunc

	// collectorWakes holds each live collector's two wake channels
	// (summary[0] + embed[1]). The central bulk gen-poll loop (genpoll.go) pokes a
	// specific (graph,axis) wake via pokeAxisWake when that pair's dirty-gen
	// advances, cutting the collector's idle-backoff sleep short so it issues its
	// Phase-2 detail PipelineScan within one base tick. (Previously WakeAll fanned
	// out across ALL of these; now WakeAll triggers one central poll and the poll
	// selectively pokes only advanced pairs.) Guarded by collectorMu alongside
	// collectorCancels (same register/unregister lifecycle).
	collectorWakes map[graphKey][]chan struct{}

	// --- Two-phase bulk gen-poll state (genpoll.go) ---
	//
	// LOCK HIERARCHY (no-deadlock property, kept explicit not emergent): genMu is
	// acquired ONLY by the gen-poll code paths (genPollOnce update + genSnapshotFor
	// read). genPollOnce releases genMu BEFORE it acquires collectorMu to poke wake
	// channels — the two mutexes are NEVER held simultaneously. collectorMu may be
	// held without genMu (register/unregister/WakeAll), but genMu is never held
	// while reaching for collectorMu.
	genMu sync.Mutex
	// genSnapshot is the shared per-(graph,axis) dirty-gen the central bulk poll
	// writes each tick; the per-collector discover reads it via genSnapshotFor to
	// decide whether to issue a Phase-2 detail PipelineScan. Guarded by genMu.
	genSnapshot map[graphKey]axisGens
	// lastPokedGen is the central loop's OWN per-(graph,axis) high-water of the gen
	// it has already poked a collector about (locks option A — the central loop
	// tracks its own poke watermark; a redundant poke is an RPC-free no-op). A
	// collector wake fires only when a returned gen ADVANCES past this. Guarded by genMu.
	lastPokedGen map[graphKey]axisGens
	// genPollWake is the buffered(1) coalescing trigger WakeAll signals so a collect
	// forces an immediate bulk gen-poll instead of waiting out the discovery tick.
	genPollWake chan struct{}

	collectorWG  sync.WaitGroup // every collector goroutine
	dispatcherWG sync.WaitGroup // dispatcher goroutines
	workerWG     sync.WaitGroup // worker goroutines

	metrics *metricsState

	// segmentMgr is the OPTIONAL client-side HNSW segment owner. When non-nil,
	// the embed-writeback seam ALSO feeds freshly-embedded binary vectors into a
	// per-graph HNSW engine and ships any newly-sealed segments. The ship is
	// BEST-EFFORT — a build/ship failure only WARNs and never fails embed
	// writeback. Server-side search is retired, so these client segments ARE the
	// search index: a dropped ship leaves the affected nodes temporarily
	// unsearchable until the next ship or rebuild, but writeback liveness takes
	// priority and it self-heals on the next embed dirty-gen. nil for test fakes.
	segmentMgr ShipManager

	// healFactory builds the per-graph auto-heal closure RegisterGraph injects
	// into each collector. Built by BOOTSTRAP (the only layer where pipeline +
	// segmentdist + tools are all visible) over the segment-presence probe
	// (*segmentdist.Manager.HasShippedSegments) and rebuild driver core
	// (tools.RebuildSegments) — kept OUT of this package so pipeline never
	// imports tools (tools already imports pipeline). nil when no segment
	// manager is wired (test fakes) → the heal closure is nil → the armed
	// embed-drain heal-check no-ops.
	healFactory func(gt kgtypes.GraphType, name string) func(ctx context.Context) error

	stopOnce sync.Once
	stopErr  error
}

// graphKey is the Pipeline-internal map key for collector tracking.
type graphKey struct {
	GraphType kgtypes.GraphType
	GraphName string
}

// SummarizerFunc is the abstraction worker code uses to call the
// configured LLM summarizer. Post-Phase-4 the implementation lives
// client-side in cmd/knowledge/internal/llmproviders.BuildSummarizer.
// Defined as a function-type rather than an interface so the test suite
// can inject a fake without depending on the store package's *compositeDB
// internals. Exported so callers wiring the pipeline (e.g. the cmd/knowledge
// serve daemon bootstrap) can adapt their summarizer to this signature.
type SummarizerFunc func(ctx context.Context, chunks []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error)

// EmbedderFunc is the symmetric abstraction for the embedder. The pipeline
// worker feeds the embedder the SERVER-COMPOSED EmbedText carried on each work
// item (no client-side text composition, no node re-fetch). Returns
// the per-id binary vectors keyed by ID (NOT just a count) so the writeback
// path lands the bytes alongside the summary in a single
// mutate(update_batch) RPC.
type EmbedderFunc func(ctx context.Context, items []EmbedItem) (map[string][]byte, error)

// New constructs a Pipeline. The summarizer and embedder are injected
// (rather than discovered at Start time) so test harnesses can supply
// fakes without touching the underlying provider clients.
//
// client is the WireClient the pipeline issues all read + writeback
// MCP tool calls against. Production wires *server.GraphClient; tests
// wire a fake satisfying the same narrow surface.
func New(cfg Config, client WireClient, summarizer SummarizerFunc, embedder EmbedderFunc) *Pipeline {
	// The production client (bootstrap routedWireClient) also satisfies
	// BackendResolver — bind it so collectors get the login-routed concrete
	// backend and refreshOnce can detect a flip. Test fakes that implement only
	// WireClient leave resolver nil → collectors ride p.client, no flip rebind.
	resolver, _ := client.(BackendResolver)
	return &Pipeline{
		cfg:              cfg,
		client:           client,
		summarizer:       summarizer,
		embedder:         embedder,
		resolver:         resolver,
		backoff:          newErrBackoff(cfg.ErrBackoffBaseOrDefault(), cfg.ErrBackoffMaxOrDefault()),
		summaryCircuit:   newCircuitBreaker(cfg.CircuitBreakerThresholdOrDefault()),
		embedCircuit:     newCircuitBreaker(cfg.CircuitBreakerThresholdOrDefault()),
		summaryProvider:  cfg.SummaryProvider,
		embedProvider:    cfg.EmbedProvider,
		embedRPM:         newRPMGate(cfg.EmbedRPMOrDefault()),
		summaryCh:        make(chan SummaryWork, cfg.SummaryChannelSizeOrDefault()),
		embedCh:          make(chan EmbedWork, cfg.EmbedChannelSizeOrDefault()),
		summaryBatchCh:   make(chan []SummaryWork, cfg.SummaryWorkersOrDefault()),
		embedBatchCh:     make(chan []EmbedWork, cfg.EmbedWorkersOrDefault()),
		collectorCancels: make(map[graphKey]context.CancelFunc),
		collectorWakes:   make(map[graphKey][]chan struct{}),
		genSnapshot:      make(map[graphKey]axisGens),
		lastPokedGen:     make(map[graphKey]axisGens),
		genPollWake:      make(chan struct{}, 1),
		metrics:          &metricsState{},
	}
}

// embedEnabled reports whether the embed axis is live. It is the SINGLE
// source of truth for the embed gate: when no embedder is configured (no
// Voyage key → BuildEmbedder returns nil → adaptEmbedder(nil) → nil func),
// the entire embed axis stays OFF — no embed dispatcher, no embed worker
// pool, and no per-collector runEmbedLoop. Without this gate a nil embedder
// would nil-panic-loop: collectors re-discover embed-eligible nodes forever,
// the recovered worker panic stamps no marker so the nodes stay eligible,
// embedCh fills, and summarization starves.
func (p *Pipeline) embedEnabled() bool { return p.embedder != nil }

// summaryEnabled is the symmetric gate for the summary axis: when no
// summarizer is configured (BuildSummarizer returns nil when summarization is
// disabled / config not loaded → nil SummarizerFunc), the entire summary axis
// stays OFF — no summary dispatcher, no summary worker pool, and no
// per-collector runSummaryLoop. The same nil-func-loop hazard the embed gate
// fixes applies identically to a nil summarizer.
func (p *Pipeline) summaryEnabled() bool { return p.summarizer != nil }

// Start launches the dispatcher + worker goroutines. One-shot — call
// once after constructing the Pipeline. Pipeline.Stop terminates them.
//
// Each axis (its dispatcher + worker pool, and the per-collector loop) starts
// ONLY when its LLM function is configured: the summary axis on summaryEnabled,
// the embed axis on embedEnabled. The WaitGroup Add counts are kept EXACTLY in
// step with the goroutines actually launched so Stop never waits on a goroutine
// that never started. When no axis is enabled, Start launches nothing and Stop
// still returns cleanly.
func (p *Pipeline) Start(ctx context.Context) error {
	summaryOn := p.summaryEnabled()
	embedOn := p.embedEnabled()

	// Dispatchers — each axis only when its LLM function is configured.
	if summaryOn {
		p.dispatcherWG.Go(func() {
			runSummaryDispatcher(ctx, p.summaryCh, p.summaryBatchCh, p.cfg.SummaryBatchSizeOrDefault())
		})
	}
	if embedOn {
		p.dispatcherWG.Go(func() {
			runEmbedDispatcher(ctx, p.embedCh, p.embedBatchCh, p.cfg.EmbedBatchSizeOrDefault())
		})
	}

	// Worker pools — each axis only when its LLM function is configured.
	if summaryOn {
		for range p.cfg.SummaryWorkersOrDefault() {
			p.workerWG.Add(1)
			go runSummaryWorker(ctx, p, p.summaryBatchCh, &p.workerWG)
		}
	}
	if embedOn {
		for range p.cfg.EmbedWorkersOrDefault() {
			p.workerWG.Add(1)
			go runEmbedWorker(ctx, p, p.embedBatchCh, &p.workerWG)
		}
	}

	slog.Info("pipeline: starting",
		"summary_enabled", summaryOn,
		"summary_workers", p.cfg.SummaryWorkersOrDefault(),
		"summary_batch", p.cfg.SummaryBatchSizeOrDefault(),
		"embed_enabled", embedOn,
		"embed_workers", p.cfg.EmbedWorkersOrDefault(),
		"embed_batch", p.cfg.EmbedBatchSizeOrDefault(),
		"tick", p.cfg.TickOrDefault())
	return nil
}

// Metrics returns a Snapshot of the current pipeline counters with the
// channel-depth fields populated from len(channel).
func (p *Pipeline) Metrics() Metrics {
	m := p.metrics.snapshot()
	m.SummaryQueued = int64(len(p.summaryCh))
	m.EmbedQueued = int64(len(p.embedCh))
	return m
}

// ResetFailedCounters zeroes the session-lifetime failed counters. Called
// after clear_llm_failures removes the on-disk markers so the status
// output reflects the live state.
func (p *Pipeline) ResetFailedCounters() {
	p.metrics.resetFailed()
}

// PausePipeline latches BOTH axes paused with an operator-supplied reason —
// manual pause is deliberately WHOLE-PIPELINE (it pauses the summary AND embed
// breakers), unlike an auto-trip which is per-axis. Both summary and embed
// workers block at their wait sites until ResumePipeline is called. Manual pause
// and an auto-trip share the same per-axis latch — there is no self-heal from
// either.
func (p *Pipeline) PausePipeline(reason string) {
	p.summaryCircuit.pause(reason)
	p.embedCircuit.pause(reason)
}

// ResumePipeline clears the paused latch on BOTH axes and wakes every parked
// worker. It is the ONLY exit from a circuit break (auto-trip or manual pause),
// and resumes whichever axis/axes are paused regardless of how they tripped.
func (p *Pipeline) ResumePipeline() {
	p.summaryCircuit.resume()
	p.embedCircuit.resume()
}

// PipelineStatus returns the current per-axis paused state for operator
// surfacing. Its per-axis aggregation lives in escalation.go alongside the
// cross-axis escalation coordinator (both are cross-axis coordination over the
// two breakers, kept out of pipeline.go for the 500-line cap).

// EnqueueIDs pushes (gt, name, id) tuples directly onto the summary +
// embed channels, skipping the pipeline_scan discovery latency for IDs
// the caller already knows are new. Used by the collect interceptor's
// short-circuit path: uploadChunks returns the
// list of newly-uploaded node hashes; passing them here avoids a
// round-trip through the next collector tick.
//
// Non-blocking on full channels — drop with a debug log rather than
// blocking the calling goroutine. The collector tick will re-discover
// dropped IDs on the next pass (correctness floor preserved).
//
// Best-effort dedup: callers should NOT pre-check the in-flight set
// (that lives inside the collector goroutine); duplicates land in the
// channel and the collector's pruneInFlightItems handles them on the
// next tick.
//
// No client-side graph-type eligibility gate (Option B): every id
// is pushed onto BOTH channels regardless of the graph's summary/embed
// eligibility. The off-axis work is discarded server-side — the worker
// composes/reads text and the eligibility decision is the server's
// (NodeIDsBySummaryGap / NodeIDsByEmbedGap short-circuit on graph type).
// Pushing both axes keeps the eligibility consultation off the client.
func (p *Pipeline) EnqueueIDs(gt kgtypes.GraphType, name string, ids []string) {
	for _, id := range ids {
		select {
		case p.summaryCh <- SummaryWork{GraphType: gt, GraphName: name, NodeID: id}:
		default:
			slog.Debug("pipeline.enqueue: summary channel full; dropping (next tick will rediscover)", "id", id)
		}
		select {
		case p.embedCh <- EmbedWork{GraphType: gt, GraphName: name, NodeID: id}:
		default:
			slog.Debug("pipeline.enqueue: embed channel full; dropping (next tick will rediscover)", "id", id)
		}
	}
}

// RefreshLoadedGraphs / discoveryTick / RefreshOnceForBoot / refreshOnce /
// handleLoginFlip — the client-side graph-CATALOG discovery poll — live in
// pipeline_refresh.go (split out to keep this file under the 500-line cap).
