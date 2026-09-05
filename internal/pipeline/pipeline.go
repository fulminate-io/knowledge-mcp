// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
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

	// lastLoggedIn and lastAccountID cache the BACKEND IDENTITY observed at the
	// previous flip check: the login state and the selected Fulminate account.
	// A transition in EITHER forces a full collector teardown + rebind so the
	// per-collector dirty-gen caches reset and each collector re-binds the new
	// backend. Guarded by collectorMu (only touched inside handleLoginFlip,
	// whose callers are the exported CheckLoginFlip — driven per tool call by
	// the client's activity hook — and nothing else).
	//
	// lastLoggedInSet gates the first-observation seeding for BOTH fields; a
	// second flag could drift out of step with it.
	lastLoggedIn    bool
	lastAccountID   string
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

	// embedDtype is the resolved [embedder] representation this pipeline's
	// vectors are produced in (copied from cfg at New time). It is read by the
	// HNSW ship path, which tags every document with it so the sealed segment
	// carries the dtype its bytes actually are. See Config.EmbedDtype.
	embedDtype string

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
	// writes on every poll; the per-collector discover reads it via genSnapshotFor
	// to decide whether to issue a Phase-2 detail PipelineScan. Guarded by genMu.
	genSnapshot map[graphKey]axisGens
	// lastPokedGen is the central loop's OWN per-(graph,axis) high-water of the gen
	// it has already poked a collector about (locks option A — the central loop
	// tracks its own poke watermark; a redundant poke is an RPC-free no-op). A
	// collector wake fires only when a returned gen ADVANCES past this. Guarded by genMu.
	lastPokedGen map[graphKey]axisGens
	// lastCatalogGen is the account CATALOG watermark last seen on a gen-poll
	// response. Compared for CHANGE, never for increase: the served value is a
	// per-replica SAMPLE and may move backward across replicas or restarts
	// (engine.proto freshness_gen). Guarded by genMu.
	lastCatalogGen uint64
	// lastCatalogGenSet records whether lastCatalogGen holds an observation yet, so
	// the FIRST sample is recorded without waking. Guarded by genMu.
	lastCatalogGenSet bool
	// genPollWake is the buffered(1) coalescing trigger the central bulk gen-poll
	// loop (RunGenPollLoop) waits on. Signaled by WakeAll — a collect or any bulk
	// write, a new graph's registration, a login flip, and the client's activity
	// hook when the response watermark moves. Past the loop's one seed poll at
	// start, a signal here is the only thing that produces a gen poll.
	genPollWake chan struct{}
	// catalogWake is the buffered(1) coalescing trigger the CATALOG-discovery loop
	// (RefreshLoadedGraphs) waits on. Signaled when the account's catalog_gen moved
	// (genPollOnce) or a login flip rebound the backend (CheckLoginFlip).
	catalogWake chan struct{}

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
	// (the segment manager's presence probe) and rebuild driver core
	// (tools.RebuildSegments) — kept OUT of this package so pipeline never
	// imports tools (tools already imports pipeline). nil when no segment
	// manager is wired (test fakes) → the heal closure is nil → the armed
	// embed-drain heal-check no-ops.
	healFactory func(gt kgtypes.GraphType, name string) func(ctx context.Context) error

	// balanceFactory builds the per-graph QUIESCENCE-EDGE balance closure RegisterGraph
	// injects into each collector. Built by BOOTSTRAP for the same reason healFactory
	// is: it closes over the segment manager, the coverage read and the reap invoker,
	// none of which this package may see (tools already imports pipeline, so the
	// reverse edge would be a cycle).
	//
	// nil when nothing is wired (test fakes) → the per-collector closure is nil → the
	// balance edge no-ops, exactly as an unwired heal factory leaves the heal edge
	// inert. The factory itself also returns nil for a graph with no rebuildable
	// segments, the same gate healFactory applies.
	balanceFactory func(gt kgtypes.GraphType, name string) func(ctx context.Context) error

	// evictGraph drops a graph from the client's working set. Wired by BOOTSTRAP
	// over client.RemoveFromWorkingSet.
	//
	// IT IS A FUNC RATHER THAN A *workingset.Set, deliberately: the eviction owner
	// is the client, and this package should not learn how membership is recorded
	// or logged — the same reason AttachWorkingSet takes the set behind an
	// accessor. nil when nothing is wired (test fakes) → the collector's
	// durable-not-found arm still ENDS the lane, it just evicts nothing.
	evictGraph func(gt kgtypes.GraphType, name, reason string)

	// workingSet is the set of graphs THIS client process has directly interacted
	// with, and it is the sole source of the catalog pass's wanted set: the
	// pipeline drains a graph only once a search, a collect or a user write has
	// admitted it. nil means EMPTY, never unrestricted — see AttachWorkingSet.
	// Owned by the client (bootstrap) and shared, not copied, so an admission
	// recorded on any interaction path is visible to the next catalog pass.
	workingSet *workingset.Set

	// localPresence narrows the admitted set to the graphs this MACHINE can
	// actually serve — for code graphs, the ones whose repo is checked out here.
	// Owned by the client (bootstrap) for the same reason collectGateFactory is:
	// the answer comes from the machine-local repo manifest in the tools package,
	// which this package must never import. nil is PERMISSIVE — see
	// AttachLocalPresence, and note the direction is deliberately the opposite of
	// workingSet's.
	localPresence func(gt kgtypes.GraphType, name string) bool

	// collectGateFactory builds the per-graph collect-gate predicate RegisterGraph
	// injects into each collector as throttle #4 (runLoop). Built by BOOTSTRAP for
	// the same reason healFactory is: it closes over the collect runtime, and this
	// package must never import the tools package that owns it (tools already
	// imports pipeline, so the reverse edge would be a cycle). nil when nothing is
	// wired (test fakes, degraded client) → the per-collector predicate is nil →
	// the gate is inert and every scan proceeds exactly as before.
	collectGateFactory func(gt kgtypes.GraphType, name string) func() bool

	// collectEpochFactory builds the per-graph COLLECT EPOCH source RegisterGraph
	// injects into each collector. Built by BOOTSTRAP over the same collect runtime
	// as collectGateFactory, for the same import-cycle reason.
	//
	// ITS NIL CASE IS THE OPPOSITE OF collectGateFactory's, deliberately: nil means
	// the consumer has NO epoch source and must decline to answer, never that the
	// epoch is zero. See AttachCollectEpochFactory for why a zero would silently
	// disable the staleness expiry it exists to provide.
	collectEpochFactory func(gt kgtypes.GraphType, name string) func() uint64

	// activeSummarizer, when set, returns the "provider/model" label of the LIVE
	// active summarizer entry (the fallback chain's highest-priority healthy
	// entry). PipelineStatus reads it to surface the current summarizer rather
	// than the static configured one. nil when no chain is wired (single entry /
	// no summarizer / tests) — PipelineStatus then leaves the field empty. Set
	// once at wiring time via SetActiveSummarizer; read concurrently by status
	// calls, but the callback itself owns its synchronization (the production
	// callback reads a thread-safe chainHealth).
	activeSummarizer func() string

	// segmentNudge, when set, records that a graph's segment cheap-tick stamp
	// advanced past the last stamp this client poked on, so the segment reconcile
	// loop pulls that graph's delta now rather than at its next periodic tick.
	// Installed by the wiring layer via SetSegmentNudger; nil on a degraded or
	// headless client that has no segment manager, where there is simply nothing to
	// nudge.
	//
	// GUARDED BY genMu because RunGenPollLoop reads it while the wiring layer may
	// still be installing it — the same publication hazard the rest of this struct's
	// late-wired fields have, and the poll is the only reader.
	segmentNudge func(gt kgtypes.GraphType, name string)

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
	// backend and CheckLoginFlip can detect a flip. Test fakes that implement only
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
		embedDtype:       cfg.EmbedDtype,
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
		catalogWake:      make(chan struct{}, 1),
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

	// Dispatchers — each axis only when its LLM function is configured. The unit
	// they batch to is the LEASE, not the provider-call cap: a worker takes a
	// lease, spends it as N provider calls, and writes it back in ONE
	// transaction. The provider-call cap still applies, inside the worker.
	if summaryOn {
		p.dispatcherWG.Go(func() {
			runSummaryDispatcher(ctx, p.summaryCh, p.summaryBatchCh, p.cfg.SummaryLeaseSizeOrDefault())
		})
	}
	if embedOn {
		p.dispatcherWG.Go(func() {
			runEmbedDispatcher(ctx, p.embedCh, p.embedBatchCh, p.cfg.EmbedLeaseSizeOrDefault())
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

	// summary_lease / embed_lease are the writeback unit; summary_batch /
	// embed_batch are the provider-call cap. Both are logged because they are now
	// distinct and an operator sizing either one needs to see both without
	// reading source.
	slog.Info("pipeline: starting",
		"summary_enabled", summaryOn,
		"summary_workers", p.cfg.SummaryWorkersOrDefault(),
		"summary_lease", p.cfg.SummaryLeaseSizeOrDefault(),
		"summary_batch", p.cfg.SummaryBatchSizeOrDefault(),
		"embed_enabled", embedOn,
		"embed_workers", p.cfg.EmbedWorkersOrDefault(),
		"embed_lease", p.cfg.EmbedLeaseSizeOrDefault(),
		"embed_batch", p.cfg.EmbedBatchSizeOrDefault(),
		"tick", p.cfg.TickOrDefault())
	return nil
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

// RefreshLoadedGraphs / RefreshOnceForBoot / refreshOnce / CheckLoginFlip /
// handleLoginFlip — the client-side graph-CATALOG discovery — live in
// pipeline_refresh.go (split out to keep this file under the 500-line cap).
