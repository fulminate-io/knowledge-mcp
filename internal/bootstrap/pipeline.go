// SPDX-License-Identifier: Apache-2.0

// Client-side LLM pipeline construction.
//
// wirePipelineRuntime mirrors wireWorkerRuntime: build runtime AFTER
// the client is constructed and BEFORE the serve daemon's MCP transport
// starts, then spawn a graph-list refresh goroutine. The refresh polls the loaded-graph
// catalog every Tick (per-type RETURN_MODE_GRAPH_NAMES reads), diffs against the
// local collectorCancels map, and calls Register/Unregister for the delta
// — worst-case lag for new-graph pickup is one collector tick.

package bootstrap

import (
	"context"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
)

// routedWireClient adapts the login-aware *graphclient.Router to the pipeline's
// WireClient + BackendResolver contract. Every PipelineScan/Execute re-picks the
// backend via Router.Backend(ctx) (cloud when logged in, local otherwise), so a
// mid-session login flip re-routes the next scan + writeback without a restart.
// Backend(ctx) hands the pipeline the concrete backend to bind per collector;
// LoggedIn(ctx) lets CheckLoginFlip detect a flip and rebind every collector.
//
// Lives client-side next to wirePipelineRuntime (placement by ownership): it
// composes the Router with the pipeline's contract and is consumed only by the
// client pipeline wiring — never serialized across the boundary.
type routedWireClient struct {
	router *graphclient.Router
}

func (a routedWireClient) PipelineScan(
	ctx context.Context,
	req *knowledgev1.PipelineScanRequest,
) (*knowledgev1.PipelineScanResponse, error) {
	gc, err := a.router.Backend(ctx)
	if err != nil {
		return nil, err
	}
	return gc.PipelineScan(ctx, req)
}

func (a routedWireClient) PipelineGenPoll(
	ctx context.Context,
	req *knowledgev1.PipelineGenPollRequest,
) (*knowledgev1.PipelineGenPollResponse, error) {
	gc, err := a.router.Backend(ctx)
	if err != nil {
		return nil, err
	}
	return gc.PipelineGenPoll(ctx, req)
}

// CorpusDelta re-picks the backend (cloud-when-logged-in / local otherwise) then
// forwards the delta request — the resident thought-corpus cache's per-tick drain
// rides this, routed identically to PipelineScan.
func (a routedWireClient) CorpusDelta(
	ctx context.Context,
	req *knowledgev1.CorpusDeltaRequest,
) (*knowledgev1.CorpusDeltaResponse, error) {
	gc, err := a.router.Backend(ctx)
	if err != nil {
		return nil, err
	}
	return gc.CorpusDelta(ctx, req)
}

func (a routedWireClient) Execute(
	ctx context.Context,
	req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	gc, err := a.router.Backend(ctx)
	if err != nil {
		return nil, err
	}
	return gc.Execute(ctx, req)
}

// Backend returns the concrete login-picked backend as a pipeline.WireClient so
// the collector binds + scans + stamps one backend per registration.
func (a routedWireClient) Backend(ctx context.Context) (pipeline.WireClient, error) {
	gc, err := a.router.Backend(ctx)
	if err != nil {
		return nil, err
	}
	return gc, nil
}

// LoggedIn reports the live login state for CheckLoginFlip's flip detection.
func (a routedWireClient) LoggedIn(ctx context.Context) bool {
	return a.router.LoggedIn(ctx)
}

// Compile-time assertions: the adapter satisfies both pipeline contracts.
var (
	_ pipeline.WireClient      = routedWireClient{}
	_ pipeline.BackendResolver = routedWireClient{}
)

// attachCollectGate installs the per-graph collect-gate predicate: while a
// collect into a graph is in flight, that graph's scan loop holds off entirely,
// so the gap scan's writeback stops landing in the middle of the collect still
// uploading into the same graph.
//
// It is built HERE because bootstrap is the only layer where both the collect
// runtime and the pipeline are visible; the pipeline package must never import
// the package that owns the runtime.
//
// Each per-collector predicate is bound to THE COLLECTOR'S OWN registered
// (gt, name) — the same bare base name RegisterGraph passes to newCollector — so
// the recorded collect identity and the gated collector's name are the same
// string by construction, never two independently-derived ones.
//
// No runtime (degraded / router-less client) installs NO factory at all, so the
// per-collector predicate stays nil and the gate is inert.
func attachCollectGate(p *pipeline.Pipeline, c *client) {
	rt := c.collectRuntime
	if rt == nil {
		return
	}
	p.AttachCollectGateFactory(func(gt kgtypes.GraphType, name string) func() bool {
		return func() bool { return rt.CollectInFlightForGraph(gt, name) }
	})
}

// wirePipelineRuntime constructs the client-side LLM pipeline (summarize
// + embed worker pools + per-graph collectors) and attaches it to *client.
// Returns nil on success; nil + log-and-skip when --no-llm-pipeline is
// set OR neither summarizer nor embedder is configured (graceful degrade —
// the rest of the MCP loop continues to work without LLM features).
//
// The 5 long-lived background loops spawned here run under the passed ctx (the
// caller's c.wireCtx — drainOnShutdown cancels it before pipeline.Stop), so a
// shutdown unwinds them. The boot-SYNCHRONOUS calls (BuildSummarizerWithFallback,
// p.Start, RefreshOnceForBoot) run under a fresh Background bootCtx instead: a
// shutdown mid-boot must NOT abort the pipeline's own Start lifecycle —
// pipeline.Stop owns the Start-spawned dispatcher/worker WaitGroups, so binding
// Start to wireCtx would double-cancel them.
func wirePipelineRuntime(ctx context.Context, c *client, f Config) error {
	if f.NoLLMPipeline {
		slog.Info("client pipeline: skipped (--no-llm-pipeline)")
		return nil
	}
	// bootCtx backs the boot-synchronous calls that must survive a mid-boot
	// shutdown (Start lifecycle owned by pipeline.Stop). The long-lived loops below
	// use the passed ctx (c.wireCtx) so shutdown cancels them.
	bootCtx := context.Background()

	// Build the ordered summarizer chain (primary + configured fallbacks). The
	// returned FallbackChain is nil when config is unloaded (degrade-not-die);
	// non-nil carries the composite selection summarizer + background prober for
	// the multi-entry case and the single bare summarizer for the len==1 case.
	fc, err := llmproviders.BuildSummarizerWithFallback(bootCtx, config.ConsumerSummarizer, pipeline.ShouldAdvanceFallback)
	if err != nil {
		// Don't bubble — degrade-not-die. The client keeps serving
		// non-LLM tools so a misconfigured summarizer doesn't take down
		// the entire MCP loop.
		slog.Warn("client pipeline: summarizer build failed; skipping pipeline wire", "error", err)
		return nil
	}
	// chained is the multi-entry chain (len>1): only then do we wire the
	// selection wrapper + background prober + live-active status. A nil chain
	// (unloaded config) or a single entry uses the plain summarizer path.
	var sum llmproviders.Summarizer
	var chained *llmproviders.FallbackChain
	switch {
	case fc == nil:
		sum = nil
	case fc.Len() == 1:
		sum = fc.FirstSummarizer()
	default:
		sum = fc.Summarizer()
		chained = fc
	}
	emb := llmproviders.BuildEmbedder()
	if sum == nil && emb == nil {
		slog.Info("client pipeline: no summarizer or embedder configured; skipping pipeline wire")
		return nil
	}

	// Per-axis provider identity for the shared-cause escalation: the summary
	// provider is resolved from the SAME config consumer BuildSummarizer uses; the
	// embed provider is the constant 'voyage' (BuildEmbedder always constructs the
	// Voyage embedder) but only when an embedder is actually wired. Distinct
	// providers (the anthropic-summaries + voyage-embeddings case) never cross-trip.
	embedProvider := ""
	if emb != nil {
		embedProvider = "voyage"
	}
	pcfg := pipeline.Config{
		SummaryChannelSize: f.SummaryChannelSize,
		SummaryBatchSize:   f.SummaryBatchSize,
		SummaryWorkers:     f.SummaryWorkers,
		EmbedChannelSize:   f.EmbedChannelSize,
		EmbedBatchSize:     f.EmbedBatchSize,
		EmbedWorkers:       f.EmbedWorkers,
		EmbedRPM:           f.EmbedRPM,
		Tick:               f.PipelineTick,
		SummaryProvider:    resolveSummaryProvider(),
		EmbedProvider:      embedProvider,
	}

	// Login-aware routing: the pipeline scans + writes back through the Router
	// (cloud when logged in, local otherwise) instead of the fixed local client,
	// and rebinds collectors on a login flip — paid = cloud-only per the locked
	// model. The routedWireClient also satisfies pipeline.BackendResolver, which
	// the Pipeline type-asserts for per-collector backend binding + flip detection.
	p := pipeline.New(pcfg, routedWireClient{router: c.router}, adaptSummarizer(sum), adaptEmbedder(emb))

	// Wire the optional client-side HNSW segment owner: at embed writeback the
	// pipeline ALSO builds + ships HNSW segments from the binary vectors it just
	// embedded. The Router satisfies segmentdist's loginState seam
	// (LoggedIn reports cloud-vs-local, selecting the GCS source when logged in and
	// the L2-local source otherwise). Best-effort: a ship failure only WARNs and
	// never fails embed writeback (server vector path authoritative — fusion
	// finding). The L2 segment cache roots under <graph-storage>/segments — off the
	// CLIENT's --graph-storage data root (segmentCacheDirFor, daemon.go), which equals
	// the auto-spawned local server's --graph-storage, so client L2 and server store
	// co-locate rather than leaking to a HOME-fixed path.
	// ONE Manager instance, shared between the PRODUCER (this pipeline ships
	// segments into it at embed writeback) and the CONSUMER (the search
	// intercepts query it via deps.SegmentManager()). The Manager is CONSTRUCTED
	// UNCONDITIONALLY in wireRuntimesBackground (daemon.go) BEFORE this call — the
	// read path must serve BM25 over existing segments even offline — so here the
	// producer only ATTACHES the already-built instance for shipping. A nil
	// segmentMgr means construction was skipped (router-less headless client); the
	// AttachHealFactory guard below stays nil-safe for that case.
	p.AttachSegmentManager(c.segmentMgr)

	// Wire the interaction-earned working set: it is where every catalog pass gets
	// its wanted set, so the pipeline registers a collector for a graph only once
	// a search, a collect or a user write has admitted it. Shared with the rest of
	// the client rather than copied, so an admission recorded on any interaction
	// path is visible to the next pass. Attached BEFORE Start so the boot
	// registration pass below already reads it.
	p.AttachWorkingSet(c.workingSet)

	// Wire the auto-heal factory: on the embed drain after a collect armed
	// the heal-check, a code graph with ZERO shipped segments triggers a one-shot
	// rebuild (closure built over the segment-presence probe + tools.RebuildSegments,
	// single-flight shared with the manual rebuild_segments op). Guarded on the
	// segment manager being wired — without it there is no probe/shipper, so the
	// factory (and the per-collector heal closure) stay unset and the heal-check
	// no-ops (headless/degraded mode unaffected).
	if c.segmentMgr != nil {
		p.AttachHealFactory(c.buildHealFactory())
	}

	attachCollectGate(p, c)

	// Surface the LIVE active summarizer entry in pipeline status when a fallback
	// chain is wired: the callback reads the chain's health so status reports the
	// CURRENT entry (shifting on failover/recovery), not the static configured
	// provider. Set before Start so any early status call sees it.
	if chained != nil {
		p.SetActiveSummarizer(chained.ActiveEntry)
	}

	c.pipeline = p
	if err := p.Start(bootCtx); err != nil {
		return err
	}

	// Initial registration: read the working set once + register each (gt, name).
	// On a cold boot the working set is EMPTY and this registers nothing, which is
	// the rule working as designed — nothing is maintained until an interaction
	// admits a graph, and the admission wakes the refresh goroutine below. It still
	// runs BEFORE that goroutine and the gen-poll loop start, so a working set
	// already carrying members (a re-wire within a live process) is registered
	// before the loops that depend on it.
	p.RefreshOnceForBoot(bootCtx) //nolint:errcheck // best-effort initial seed

	// Boot-delay segment-coverage reconcile (one-shot, OFF the critical path): a
	// single reconcile pass fired ~segmentReconcileBootDelay after wiring, NOT
	// synchronously here. The synchronous startup reconcile was removed because, with
	// the L2-first load() (the resident set is now imported from the L2 disk cache
	// server-independently before the MCP bind), boot no longer needs a server round
	// trip to be searchable — running the all-graphs server reconcile on the bind path
	// only coupled first-search readiness to a slow/down server.
	//
	// The one-shot is still REQUIRED, not a nicety: runSegmentReconcileLoop's first
	// tick fires only at segmentReconcileInterval (5min) because it selects on
	// ticker.C with no immediate first iteration. With the synchronous reconcile gone
	// AND the per-search recoverIfDegenerate removed (Phase 3), a graph genuinely
	// degenerate after the L2-first load — a cold/partial L2 on this machine while the
	// server holds the full corpus — would otherwise sit degenerate for up to 5min
	// post-restart. The ~30s delay closes that heal gap while staying off the bind /
	// markPipelineReady path: it fires well after readiness latches, never blocking
	// the MCP listener bind.
	go c.bootDelayReconcile(ctx)

	// Catalog re-enumeration in background: wake-driven, so it costs nothing until
	// the account's catalog watermark moves or a login flip rebinds the backend.
	go p.RefreshLoadedGraphs(ctx)

	// Periodic segment-coverage reconcile in background: re-runs the same probe-heal
	// on a fixed cadence so a graph that collapses (or is never re-collected)
	// mid-session self-heals WITHOUT a search or collect. Shares the one reconcile
	// body with the startup trigger; exits on ctx.Done — ctx is c.wireCtx, which
	// drainOnShutdown cancels on shutdown, so this loop is unwound (no leak).
	go c.runSegmentReconcileLoop(ctx, segmentReconcileInterval)

	// Central two-phase bulk gen-poll in background: ONE PipelineGenPoll RPC
	// samples every loaded graph's dirty-gen (Phase 1) and selectively pokes only
	// the collectors whose gen advanced to issue their Phase-2 detail PipelineScan
	// — replacing the prior up-to-2N PipelineScan fan-out. The loop polls once at
	// start to seed the shared snapshot, then only when woken.
	go p.RunGenPollLoop(ctx)

	// Background summarizer-chain health prober (multi-entry chains only): every
	// configured interval it re-checks each limited entry with a cheap ping and
	// shifts traffic back to the highest-priority recovered entry. Shares the same
	// ctx lifecycle as the other background loops — ctx is c.wireCtx, cancelled by
	// drainOnShutdown, so it exits on ctx.Done (no leak).
	if chained != nil {
		go chained.RunHealthProbeLoop(ctx)
	}

	return nil
}

// resolveSummaryProvider returns the summary axis's LLM provider identity for the
// shared-cause escalation gate, resolved from the SAME config consumer
// BuildSummarizer uses (config.ConsumerSummarizer). Degrade-not-die: an unloaded
// config or a resolve error yields "" (unknown) so that axis never participates
// in a cross-trip — never an error that blocks pipeline wiring.
func resolveSummaryProvider() string {
	if !config.Loaded() {
		return ""
	}
	sec, err := config.Active().Resolve(config.ConsumerSummarizer)
	if err != nil {
		return ""
	}
	return sec.Provider.String()
}

// adaptSummarizer converts an llmproviders.Summarizer to the pipeline
// package's SummarizerFunc shape. nil → nil so the pipeline's worker
// treats it as a no-op (the dispatcher still routes batches but the
// worker bails on first call when summarizer is nil).
func adaptSummarizer(s llmproviders.Summarizer) pipeline.SummarizerFunc {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, chunks []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return s.SummarizeBatch(ctx, chunks)
	}
}

// adaptEmbedder converts an embed.BinaryEmbedder to the pipeline package's
// EmbedderFunc shape. Returns per-id binary vectors keyed by ID so the
// writeback batch can land each vector alongside its node in one RPC.
// nil → nil (pipeline embed path bails when embedder is nil).
func adaptEmbedder(e embed.BinaryEmbedder) pipeline.EmbedderFunc {
	if e == nil {
		return nil
	}
	return func(ctx context.Context, items []pipeline.EmbedItem) (map[string][]byte, error) {
		texts := make([]string, len(items))
		for i, it := range items {
			texts[i] = it.Text
		}
		vecs, err := e.EmbedBinaryBatch(ctx, texts)
		if err != nil {
			return nil, err
		}
		out := make(map[string][]byte, len(items))
		for i, it := range items {
			if i >= len(vecs) {
				break
			}
			out[it.ID] = vecs[i]
		}
		return out, nil
	}
}
