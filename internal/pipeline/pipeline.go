// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// Pipeline owns the global summary/embed channels, dispatchers, worker
// pools, and per-graph collector goroutines. One Pipeline per server
// process. Created by cmd/knowledge-server's startup; goroutines launched
// in Start; collectors registered/unregistered as graphs load/unload via
// the registry hooks (see pipeline_hooks.go in package store).
//
// Concurrency model: each loaded graph gets two collector goroutines (one
// summary, one embed). Collectors push work onto the global channels;
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

	// backoff is the shared exponential-backoff gate for LLM calls. One
	// instance across all summary + embed workers — the rate limit it
	// guards is global to the provider, not per-graph or per-worker.
	backoff *errBackoff

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

	collectorWG  sync.WaitGroup // every collector goroutine
	dispatcherWG sync.WaitGroup // dispatcher goroutines
	workerWG     sync.WaitGroup // worker goroutines

	metrics *metricsState

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
// internals. Exported so callers wiring the pipeline (e.g. cmd/knowledge
// runMCPMode) can adapt their summarizer to this signature.
type SummarizerFunc func(ctx context.Context, chunks []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error)

// EmbedderFunc is the symmetric abstraction for the embedder. The pipeline
// worker feeds the embedder the SERVER-COMPOSED EmbedText carried on each work
// item (FUL-305 — no client-side text composition, no node re-fetch). Returns
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
	return &Pipeline{
		cfg:              cfg,
		client:           client,
		summarizer:       summarizer,
		embedder:         embedder,
		backoff:          newErrBackoff(cfg.ErrBackoffBaseOrDefault(), cfg.ErrBackoffMaxOrDefault()),
		summaryCh:        make(chan SummaryWork, cfg.SummaryChannelSizeOrDefault()),
		embedCh:          make(chan EmbedWork, cfg.EmbedChannelSizeOrDefault()),
		summaryBatchCh:   make(chan []SummaryWork, cfg.SummaryWorkersOrDefault()),
		embedBatchCh:     make(chan []EmbedWork, cfg.EmbedWorkersOrDefault()),
		collectorCancels: make(map[graphKey]context.CancelFunc),
		metrics:          &metricsState{},
	}
}

// Start launches the dispatcher + worker goroutines. One-shot — call
// once after constructing the Pipeline. Pipeline.Stop terminates them.
func (p *Pipeline) Start(ctx context.Context) error {
	// Dispatchers (one per channel).
	p.dispatcherWG.Add(2)
	go func() {
		defer p.dispatcherWG.Done()
		runSummaryDispatcher(ctx, p.summaryCh, p.summaryBatchCh, p.cfg.SummaryBatchSizeOrDefault())
	}()
	go func() {
		defer p.dispatcherWG.Done()
		runEmbedDispatcher(ctx, p.embedCh, p.embedBatchCh, p.cfg.EmbedBatchSizeOrDefault())
	}()

	// Worker pools.
	for range p.cfg.SummaryWorkersOrDefault() {
		p.workerWG.Add(1)
		go runSummaryWorker(ctx, p, p.summaryBatchCh, &p.workerWG)
	}
	for range p.cfg.EmbedWorkersOrDefault() {
		p.workerWG.Add(1)
		go runEmbedWorker(ctx, p, p.embedBatchCh, &p.workerWG)
	}

	slog.Info("pipeline: starting",
		"summary_workers", p.cfg.SummaryWorkersOrDefault(),
		"summary_batch", p.cfg.SummaryBatchSizeOrDefault(),
		"embed_workers", p.cfg.EmbedWorkersOrDefault(),
		"embed_batch", p.cfg.EmbedBatchSizeOrDefault(),
		"tick", p.cfg.TickOrDefault())
	return nil
}

// Stop runs the 7-step shutdown sequence per ticket Section D. Returns
// when (a) every goroutine has exited or (b) ctx fires, whichever first.
// Idempotent via stopOnce.
//
// Sequence:
//  1. Cancel every collector context (collectors stop pushing).
//  2. Wait collectorWG (collectors fully exited; no new work in flight).
//  3. Close summaryCh + embedCh (dispatchers see EOF, drain partial batches).
//  4. Wait dispatcherWG (dispatchers exited; sub-channels won't see new sends).
//  5. Close summaryBatchCh + embedBatchCh (workers see EOF, drain final batches).
//  6. Wait workerWG (workers exited).
//  7. Return.
//
// stopOnce.Do guards against double-close panics if Stop is invoked
// concurrently from server-shutdown + test-cleanup paths.
func (p *Pipeline) Stop(ctx context.Context) error {
	p.stopOnce.Do(func() { p.stopErr = p.stopSequence(ctx) })
	return p.stopErr
}

// stopSequence is the body of Stop, factored out so the Stop method
// itself stays under the 80-line cap.
func (p *Pipeline) stopSequence(ctx context.Context) error {
	// Step 1: cancel every collector.
	p.collectorMu.Lock()
	for _, cancel := range p.collectorCancels {
		cancel()
	}
	p.collectorCancels = make(map[graphKey]context.CancelFunc)
	p.collectorMu.Unlock()

	// Step 2: wait collectors, bounded by ctx.
	if err := waitWithCtx(ctx, &p.collectorWG); err != nil {
		return err
	}

	// Step 3: close summary + embed channels (dispatcher EOF).
	close(p.summaryCh)
	close(p.embedCh)

	// Step 4: wait dispatchers. Note: dispatcher functions close their
	// own output (batch) channels via deferred close — Step 5 below is
	// implicit, NOT an explicit close here (would be double-close).
	if err := waitWithCtx(ctx, &p.dispatcherWG); err != nil {
		return err
	}

	// Step 5: per-batch sub-channels are closed by dispatcher's defer.
	// Step 6: wait workers (they observe EOF via the dispatcher's close).
	return waitWithCtx(ctx, &p.workerWG)
}

// waitWithCtx waits for wg to reach zero or ctx to fire. Returns ctx.Err
// on timeout / cancel.
func waitWithCtx(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RegisterGraph spawns the per-graph collector goroutines (summary +
// embed) for (gt, name). Called by the registry hook (Phase 6) when a
// graph loads. Re-registration of an already-tracked graph is a no-op
// — the registry hook fires once per load.
//
// No client-side graph-type eligibility gate (FUL-307, Option B): the
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
func (p *Pipeline) RegisterGraph(gt kgtypes.GraphType, name string) {
	key := graphKey{GraphType: gt, GraphName: name}
	p.collectorMu.Lock()
	defer p.collectorMu.Unlock()
	if _, exists := p.collectorCancels[key]; exists {
		return
	}
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is stored in collectorCancels and invoked by UnregisterGraph / stopSequence
	p.collectorCancels[key] = cancel
	c := newCollector(gt, name, p.cfg, p.summaryCh, p.embedCh, p.metrics, p.client)
	p.collectorWG.Go(func() {
		c.run(ctx)
	})
}

// UnregisterGraph cancels the collector context and removes the entry
// from the tracking map. Called by the registry hook on graph unload.
// Safe if (gt, name) is not registered.
func (p *Pipeline) UnregisterGraph(gt kgtypes.GraphType, name string) {
	key := graphKey{GraphType: gt, GraphName: name}
	p.collectorMu.Lock()
	cancel, exists := p.collectorCancels[key]
	delete(p.collectorCancels, key)
	p.collectorMu.Unlock()
	if exists {
		cancel()
	}
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

// EnqueueIDs pushes (gt, name, id) tuples directly onto the summary +
// embed channels, skipping the pipeline_scan discovery latency for IDs
// the caller already knows are new. Used by the collect interceptor's
// short-circuit path (BCN5 Phase 6 step 3): uploadChunks returns the
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
// No client-side graph-type eligibility gate (FUL-307, Option B): every id
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

// RefreshLoadedGraphs is the client-side graph-discovery poll. It polls the
// loaded-graph catalog every Tick (listLoadedGraphs → per-type
// RETURN_MODE_GRAPH_NAMES reads), diffs the response against the current per-(gt,
// name) collector set, and calls RegisterGraph / UnregisterGraph for the delta.
// Worst-case lag for graph create/destroy propagation: one collector tick — the
// price of a wire poll rather than an in-process registry hook.
//
// Single-flight semantics: a slow list_graphs call must not overlap
// with the next tick's call. The single-token semaphore channel skips
// a tick when a previous refresh hasn't completed.
//
// Exits on ctx.Done.
func (p *Pipeline) RefreshLoadedGraphs(ctx context.Context) {
	inFlight := make(chan struct{}, 1)
	tick := p.cfg.TickOrDefault()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		select {
		case inFlight <- struct{}{}:
		default:
			// Previous refresh still running; skip this tick.
			continue
		}
		p.refreshOnce(ctx)
		<-inFlight
	}
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
// cognitive-complexity cap.
func (p *Pipeline) refreshOnce(ctx context.Context) {
	graphs, err := listLoadedGraphs(ctx, p.client)
	if err != nil {
		slog.Debug("pipeline.refresh: list_graphs failed; will retry next tick", "error", err)
		return
	}
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
			p.RegisterGraph(k.GraphType, k.GraphName)
		}
	}
	for k := range have {
		if _, still := wanted[k]; !still {
			p.UnregisterGraph(k.GraphType, k.GraphName)
		}
	}
}
