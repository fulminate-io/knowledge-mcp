// SPDX-License-Identifier: Apache-2.0

// Package tools holds the client-side MCP tool intercepts for the knowledge
// client binary: the manage(status), collect, and backend-write-through
// paths that must run in-process. Collectors stream chunks to the graph
// server rather than running server-side; backend writes (Linear, Jira,
// GitHub) hit the third-party API inline from this package BEFORE the
// local mutate reaches the server.
//
// Every entry point takes a ClientDeps argument rather than a concrete
// *client type — the main package supplies accessors so this package has
// no import cycle back into cmd/knowledge.
package tools

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs/cloudresolver"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// BackendResolver routes between configured external project/ticket
// backends (Linear, Jira, GitHub Issues, ...). Production wires this to
// the cmd/knowledge/internal/backends/provider package; tests inject a
// fake to drive InterceptCreateProject / InterceptCreateTicket /
// InterceptMutate through a scripted Backend without setting
// LINEAR_API_KEY on the test process.
//
// Method semantics:
//   - Default returns the first configured backend (closed-switch order
//     in provider.Available), or nil when no backend is configured.
//     InterceptCreateProject calls this — empty result means "fall
//     through to the local-only path", populated means "write through".
//   - ByName returns the backend matching name, or nil when either the
//     name is unknown or that backend is not currently configured.
//     InterceptCreateTicket calls this with the parent project's
//     `backend` metadata value; InterceptMutate calls this with the
//     target node's `backend` metadata value.
type BackendResolver interface {
	Default() backends.Backend
	ByName(name string) backends.Backend
}

// GraphCaller is the narrow surface InterceptCreateProject /
// InterceptCreateTicket / InterceptMutate use to forward the local
// portion of a backend-backed call back into the MCP tool dispatch.
// Production wires this to a thin adapter over the same
// *graphclient.GraphClient the rest of the client uses; tests inject
// a fake that records the (name, args) pair so assertions can verify
// the forward shape without an over-the-wire test.
//
// The interface stays this narrow on purpose — it is the base handle every
// intercept takes, then type-asserts UP to the carrier seams it needs
// (render.Executor / Indexer / Exporter / topologyFetcher). Execute is the base
// seam — every read/write rides the Execute carrier, and the production
// graphClientCaller satisfies it. Adding more would over-widen the test surface
// every intercept has to satisfy.
type GraphCaller interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// LocalLiveness is the liveness-only view of the LOCAL graph server the
// tools layer is allowed to reach. It deliberately exposes ONLY Healthy +
// Status — NO Execute / CRUD carrier — so no tools-layer code can pull a
// graph-write off the local accessor and bypass the login-aware Router. The
// local server is sync-only when logged in; every read/write routes via
// GraphCaller() (the Router). *graphclient.GraphClient satisfies this
// (Healthy + Status); the narrowing is what makes "grab the bare local
// client and Execute on it" fail to compile.
type LocalLiveness interface {
	Healthy() bool
	Status() (map[string]any, error)
}

// CloudSubgraphFetcher is the narrow read seam the logs collector pulls the
// in-memory cloud-resource slice through to drive its CloudResolver and
// DependencyChecker. Satisfied in production by *remote.UploadSink.
//
// It is its own seam rather than a second method on collector.Sink because
// collector.Sink is implemented by every sink AND every sink wrapper: adding a
// cloud-fetch method there would force each of them to carry a capability it
// never serves. Widening collector.Sink is out of scope by design.
type CloudSubgraphFetcher interface {
	FetchCloudSubgraph(ctx context.Context, graphNames []string, typePrefixes []string) (*cloudresolver.CloudSubgraph, error)
}

// ClientDeps is the narrow surface the cmd/knowledge main package exposes
// to this internal/tools package. Keep it minimal — every new accessor
// widens the coupling. The accessors cover:
//
//   - LocalLiveness: liveness check + Status RPC for handleServerStatus.
//     A liveness-only view (Healthy + Status, no Execute) of the LOCAL
//     server — manage(status) is the local-daemon-only path; the
//     logged-in path reports cloud Stats via CloudStatusInfo instead.
//   - Sink: collector.Sink used by collect to stream chunks to the
//     server's IngestService.
//   - RootDir: project root directory the user passed via --root. The
//     client-side ast intercept walks this directory to find source
//     files (the server has no repo, especially in remote-server mode).
//   - Embedder: client-side BinaryEmbedder used by InterceptSearch /
//     InterceptQuery to embed query text on the client side so the
//     server-side compositor short-circuits its own embed call (Phase
//     4.5). Returns nil when no voyage_api_key is configured — search
//     falls back to BM25-only via the existing nil-embedder path.
//   - BackendResolver: resolves backend adapters (linear / jira / ...)
//     for the client-side InterceptCreateProject / InterceptCreateTicket
//     / InterceptMutate intercepts. Returns a resolver that always
//     returns nil from Default/ByName when no backend is configured —
//     intercepts fall through to local-only.
//   - GraphCaller: forwards intercept tail-calls (the local-mutate half
//     of a successful backend write-through) back into the MCP tool
//     dispatch. Returns nil only when the client was constructed without
//     a GraphClient (degraded headless mode) — intercepts fail fast in
//     that case rather than performing the backend write with no way to
//     persist the local-graph mirror. Returns the routed
//     *graphclient.Router that dispatches per-call to local or cloud
//     based on live auth state.
//   - LocalGraphCaller: returns a GraphCaller that ALWAYS targets the
//     local server (bypasses routing). Sync push uses it to read + push the
//     local graph, and sync pull uses it to apply fetched cloud bytes to the
//     local graph via OverwriteGraph — both are local-only regardless of login
//     state. (The post-collect linker + postpopulate tail follow the data via
//     the login-routed GraphCaller instead, since the collect sink writes to
//     cloud when logged in.) Returns nil only when no local server is wired
//     (cloud-first user); those callers' existing nil-guards surface the
//     degraded-mode error.
type ClientDeps interface {
	LocalLiveness() LocalLiveness
	Sink() collector.Sink
	// SubgraphFetcher returns the cloud-subgraph read seam the logs collector
	// resolves its CloudResolver and DependencyChecker inputs through. In
	// production this is the SAME ingest sink the collect sink wraps, so the
	// fetch rides the per-call login-routed picker while the admission wrapper
	// stays in front of every WriteResult. Returns nil when the client was
	// constructed without an ingest sink (router-less / headless fixture) — the
	// logs collector surfaces a loud error on nil rather than degrading to a
	// collect with no cloud enrichment.
	SubgraphFetcher() CloudSubgraphFetcher
	RootDir() string
	// UsageAnalyzer returns the client-side agent-flow analyzer (the pure-Go
	// transcriptanalytics engine over the local transcript parquet cache) the
	// analyze_usage intercept dispatches through. Returns nil in router-less /
	// headless test fixtures (and if the cache root is unresolvable) —
	// InterceptAnalyzeUsage nil-checks and renders the cold-cache --seed hint.
	// The analyzer needs no router/network; it reads the local cache only.
	UsageAnalyzer() UsageAnalyzerAPI
	GraphTypeCRUD() GraphTypeCRUDAPI
	Embedder() embed.BinaryEmbedder
	BackendResolver() BackendResolver
	GraphCaller() GraphCaller
	LocalGraphCaller() GraphCaller
	// SegmentManager returns the SAME *segmentdist.Manager the client holds (one
	// instance — duplicate engines would double memory and miss the producer's
	// loaded segments). The read Manager is constructed UNCONDITIONALLY whenever a
	// router is present (wireRuntimesBackground, independent of the LLM pipeline),
	// so an offline daemon (--no-llm-pipeline, or no embedder/summarizer) still
	// serves BM25 over existing segments. Returns nil ONLY for a router-less /
	// headless client; the search arms loud-error on that nil — there is NO server
	// search fallback (that path is retired).
	SegmentManager() SegmentSearcher
	// SegmentVectorResolver returns the SAME *segmentdist.Manager as the by-id
	// stored-vector read seam the mode:"similar" claim resolves its query vector
	// through. Returns nil under the same condition as SegmentManager (pipeline not
	// wired) — the similar-mode claim loud-errors on nil rather than silently
	// falling through to a server text search.
	SegmentVectorResolver() SegmentVectorResolver
	// SegmentShipper returns the SAME *segmentdist.Manager as a build-concurrent/
	// ship-once SHIP surface for the rebuild_segments driver. Returns nil when the
	// pipeline was not wired (same condition as SegmentManager) — the driver errors
	// "pipeline not wired" on nil.
	SegmentShipper() SegmentShipper
	// SegmentPruner returns the SAME *segmentdist.Manager (via the client_segment.go
	// adapter) as the one-shot manage(prune-cache) orphaned-L2-reclaim surface.
	// Returns nil when the segment manager was not constructed (router-less / headless
	// client) — the handler's nil-guard surfaces a not-ready error rather than
	// dereferencing.
	SegmentPruner() SegmentPruner
	// SegmentCacheDropper returns the SAME *segmentdist.Manager (via the
	// client_segment_dropcache.go adapter) as the whole-graph L2 teardown surface
	// manage(drop_graph) drives after the server-side drop succeeds. Returns nil
	// when the segment manager was not constructed (router-less / headless client)
	// — and drop_graph treats nil as "local cache not inspected", NOT as an error:
	// the graph really is gone server-side, so reporting a failure because this
	// client had no segment engine would send an operator hunting a drop that
	// succeeded.
	SegmentCacheDropper() SegmentCacheDropper
	// SegmentDeleter returns the SAME *segmentdist.Manager (via the client_segment.go
	// adapter) as the seam that carries a delete into the shipped segment corpus.
	// Returns nil when the segment manager was not constructed (router-less /
	// headless client); callers skip the re-emit on nil, which is the same
	// best-effort disposition they give a failure.
	SegmentDeleter() SegmentDeleter
	// SegmentCoverage returns the SAME *segmentdist.Manager as the read seam the
	// manage(status) segment-coverage column reads segment-covered doc counts
	// through. Returns nil when the pipeline was not wired (same condition as
	// SegmentManager) — the column renders a placeholder on nil rather than failing.
	SegmentCoverage() SegmentCoverageReader
	// PipelineScanner returns the login-routed PipelineScan+Execute wire seam the
	// rebuild_segments driver pages the segment_rebuild scan through. Returns nil
	// when no router is wired (degraded headless mode) — the driver errors
	// "pipeline not wired" on nil.
	PipelineScanner() PipelineScanner
	// ClearHealLatch clears the per-(graphType, name) auto-heal breaker latch — the
	// manual rebuild_segments re-arm. handleClientRebuildSegments calls it from the
	// SUCCESS branch keyed on scanned>0: an operator asking for a rebuild that actually
	// scanned nodes re-arms the automatic heal so it resumes after a manual
	// intervention. Satisfied by *bootstrap.client (over its healBreaker). A no-op on a
	// non-latched graph, and nil-safe: the *client method guards its own breaker, and a
	// test fake can implement it as a plain no-op recorder.
	ClearHealLatch(gt kgtypes.GraphType, name string)
	// ReflectionForcer returns the on-demand full-corpus reflection backstop lever
	// (thoughts(propagate, force_full:true) drives it). Returns the live
	// *clientthought.PropagationLoop, or nil when the reflection loop is not running
	// in this process (--no-propagation-runtime, or a router-less test fixture) —
	// handlePropagateClient surfaces a loud "reflection loop not running" error on
	// nil rather than silently falling through to the incremental path.
	ReflectionForcer() ReflectionForcer
	// SimilarityForcer returns the on-demand topic-similarity lever
	// (thoughts(propagate, similarity:true) drives it). Returns nil when the
	// reflection loop is not running in this process (same condition as
	// ReflectionForcer) — handlePropagateClient surfaces a loud error on nil.
	SimilarityForcer() SimilarityForcer
	// BlindSpotProvider returns the read seam query(mode:blind_spots) serves the
	// loop's cached faceted report through (GetBlindSpots, O(1)). Returns the live
	// *clientthought.PropagationLoop, or nil when the reflection loop is not running
	// in this process (--no-propagation-runtime, or a router-less test fixture) —
	// handleReflectBlindSpots renders a "reflection loop not running" message on nil
	// rather than recomputing.
	BlindSpotProvider() BlindSpotProvider
	// ClusterProvider returns the read seam query(mode:personality) and
	// query(mode:summary) serve the loop's cached clusters + personality profile
	// through (GetClustersCached, O(1)). Returns the live *clientthought.
	// PropagationLoop, or nil when the reflection loop is not running in this process
	// (--no-propagation-runtime, or a router-less test fixture) — the handlers render
	// a "reflection loop not running" message on nil rather than recomputing.
	ClusterProvider() ClusterProvider
	// TensionsProvider returns the read seam query(mode:tensions) serves the loop's
	// cached tension reports through (GetTensions, O(1)). Returns the live
	// *clientthought.PropagationLoop, or nil when the reflection loop is not running
	// in this process (same condition as ClusterProvider) — handleReflectTensions
	// renders a "reflection loop not running" message on nil rather than recomputing.
	TensionsProvider() TensionsProvider
	// PropReady / PipelineReady report whether the corresponding
	// background-wiring stage has completed (Bind-first startup: the daemon binds the HTTP
	// MCP listener first, then wires the propagation / pipeline runtimes
	// in a background goroutine). The runtime-dependent intercept guards consult
	// these to distinguish the wiring window — emit "daemon still starting:
	// <subsystem> not ready" — from a permanent boot degrade (the accessor returns
	// nil after the flag is set). False while the subsystem has not finished
	// wiring; a true result happens-after the wired handle is published, so a
	// guard that sees Ready()==true may safely read the accessor. The engine ops
	// (query / search-BM25 / mutate) have no runtime dependency and consult none
	// of these — they serve immediately after bind.
	PropReady() bool
	PipelineReady() bool
}
