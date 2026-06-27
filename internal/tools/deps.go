// SPDX-License-Identifier: Apache-2.0

// Package tools holds the client-side MCP tool intercepts for the knowledge
// stdio binary: the manage(status), collect, and backend-write-through
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
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// SegmentSearcher is the narrow consumer-side seam the search intercepts use to
// query the client-hosted BM25+HNSW segment engines. *segmentdist.Manager
// satisfies it (Manager.Search). Declared here as an interface — not the
// concrete type — so the search arms reach the engine without tools importing
// segmentdist's full surface, and so tests can inject a fake Manager that
// asserts the arm drove the CLIENT engine instead of dispatching a server
// search. Returns RRF-fused ranked Hits (ID + fused score) for hydration.
type SegmentSearcher interface {
	Search(ctx context.Context, gt kgtypes.GraphType, name, queryText string, queryVec []byte, k int) ([]searchengine.Hit, error)
}

// SegmentVectorResolver is the narrow consumer-side seam the mode:"similar" search
// claim uses to resolve a node's STORED query vector from the client-local HNSW
// segments by external id. *segmentdist.Manager satisfies it (Manager.VectorByID).
// Kept DELIBERATELY SEPARATE from SegmentSearcher — not folded into it — so the
// ~15 Search-only test doubles (fakeSegmentSearcher, recallFakeSearcher,
// fanOutSegmentSearcher, every SegmentManager() stub) compile unchanged; a narrow
// per-purpose seam over the same concrete is the established deps.go pattern
// (SegmentShipper, PipelineScanner, ReflectionForcer). The (ok=false, err=nil)
// tuple separates absent-id (node not embedded yet → caller loud-errors) from a
// load failure (err!=nil).
type SegmentVectorResolver interface {
	VectorByID(ctx context.Context, gt kgtypes.GraphType, name, externalID string) ([]byte, bool, error)
}

// SegmentShipper is the build-concurrent / ship-once SHIP surface the
// rebuild_segments driver drives. *segmentdist.Manager satisfies it. The method
// set is DELIBERATELY the Add-ONLY + single-finalize shape (there is NO
// AddAndShipDeterministic): the driver builds every full chunk concurrently via
// the Add-ONLY AddDeterministic (HNSW) + AddFields (BM25) — no per-chunk ship —
// then ships exactly ONCE via the single serial FlushDeterministic after the
// concurrent pool joins. That is the fix for the concurrent-ship/reconcilePrune
// data-loss race: a single ship over the fully-published Export can only prune
// genuinely merged-away ids, never a live concurrently-built sibling.
// FlushDeterministic RETURNS the server-pruned (merged-away) ids; the driver
// passes them to InvalidateLocal so the superseded local .seg files are evicted
// rather than orphaning under an unbounded cache.
type SegmentShipper interface {
	AddDeterministic(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error
	AddFields(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error
	FlushDeterministic(ctx context.Context, gt kgtypes.GraphType, name string) ([]searchengine.SegmentID, error)
	InvalidateLocal(gt kgtypes.GraphType, name string, ids []searchengine.SegmentID)
}

// SegmentPruner is the narrow seam the one-shot manage(prune-cache) handler drives
// to reclaim orphaned L2 segment files. *segmentdist.Manager satisfies it (via the
// bootstrap client_segment.go adapter — the ONLY place the tools-local and
// segmentdist-native vocabularies meet). The targets cross this seam as PARALLEL
// slices (graphTypes[i] pairs with names[i]) of already-imported kgtypes.GraphType
// + string — DELIBERATELY not a segmentdist target type — so tools never imports
// segmentdist (the same intra-client decoupling the four sibling segment seams keep:
// this file references *segmentdist.Manager in PROSE only, never in a signature or a
// var _ assertion). execute=false previews (the report carries the would-remove
// orphans, deletes nothing); execute=true unlinks the orphans and fills
// Removed/RemovedBytes.
type SegmentPruner interface {
	PruneCache(ctx context.Context, graphTypes []kgtypes.GraphType, names []string, execute bool) (PruneCacheReport, error)
}

// PruneCacheGraphReport is the tools-local per-(graph, format) prune result — a
// field-identical mirror of segmentdist.PruneCacheGraphReport over already-imported
// types only (kgtypes.GraphType + searchengine.SegmentID). The client_segment.go
// adapter copies it field-for-field across the package boundary. Orphans is the
// would-remove (preview) OR did-remove set; Bytes is the summed .seg FileInfo size;
// Aborted+AbortReason surface a List(0) subset-abort for a SKIPPED pool.
type PruneCacheGraphReport struct {
	GraphType   kgtypes.GraphType
	Name        string
	Format      string
	Orphans     []searchengine.SegmentID
	Bytes       int64
	Aborted     bool
	AbortReason string
}

// PruneCacheReport is the tools-local whole-run result mirroring
// segmentdist.PruneCacheReport: one PruneCacheGraphReport per (graph, format) pool
// plus the EXECUTED totals (Removed count + RemovedBytes), zero on a preview run.
type PruneCacheReport struct {
	Graphs       []PruneCacheGraphReport
	Removed      int
	RemovedBytes int64
}

// SegmentCoverageReader is the narrow read seam the manage(status) segment-coverage
// column uses to read a graph's segment-covered doc count (summed HNSW
// meta.DocCount). *segmentdist.Manager satisfies it (Manager.ShippedSegmentDocCount).
// A narrow per-purpose seam over the same concrete is the established deps.go
// pattern (SegmentSearcher, SegmentShipper, SegmentVectorResolver). The renderer
// consumes only the covered count; anyUnknown (the conservative-unknown signal the
// auto-heal probe reads) is irrelevant to a display column and ignored there.
type SegmentCoverageReader interface {
	ShippedSegmentDocCount(ctx context.Context, gt kgtypes.GraphType, name string) (covered int, anyUnknown bool, err error)
	// ResidentDocCount returns the LIVE in-memory engine resident doc count for one
	// graph — the searchable pool's actual size, distinct from the SERVER's shipped
	// count above. The status column renders both so a collapse (server intact, live
	// pool empty) shows "live 0 of N" instead of being masked behind the shipped
	// figure. Satisfied by *segmentdist.Manager.ResidentDocCount (a single atomic
	// read, no RPC).
	ResidentDocCount(gt kgtypes.GraphType, name string) int
}

// PipelineScanner is the login-routed PipelineScan + Execute wire seam the
// rebuild_segments driver pages the segment_rebuild scan through. GraphCaller
// exposes only Execute and the *graphclient.Router has NO PipelineScan — only the
// bootstrap routedWireClient does — so this is a distinct accessor satisfied by a
// login-routed adapter (per-call cloud-when-logged-in / local-otherwise).
type PipelineScanner interface {
	PipelineScan(ctx context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error)
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// ReflectionForcer is the narrow seam the manual propagate tool uses to drive an
// on-demand full-corpus reflection backstop pass (thoughts(propagate,
// force_full:true)). *clientthought.PropagationLoop satisfies it (ForceFullPass).
// Declared here as an interface — not the concrete loop type — so the tools layer
// reaches the lever without importing the loop's full surface, and so tests inject
// a fake recording the force call. ForceFullPass claims the per-account reflection
// single-flight guard, bypasses the cadence + quiet-skip + incremental scoping, and
// resets the backstop clock on completion; it returns
// clientthought.ErrReflectionInFlight (a benign coalesce, not a failure) when
// another pass already holds the guard.
type ReflectionForcer interface {
	ForceFullPass(ctx context.Context) (clientthought.PropagationResult, error)
}

// BlindSpotProvider is the narrow READ seam the on-demand query(mode:blind_spots)
// handler serves the loop's cached faceted report through. *clientthought.
// PropagationLoop satisfies it (GetBlindSpots). Declared as an interface — not the
// concrete loop type — so the tools layer reads the cache without importing the
// loop's full surface, and so tests inject a fake returning a constructed report.
// GetBlindSpots is O(1) (a p.mu-guarded field read): the handler serves the report
// the background tick already computed and NEVER recomputes on the call path. A
// zero-value report (Computed=false) is the cold sentinel before the first tick —
// the handler renders a not-yet-computed message rather than a synchronous
// recompute.
type BlindSpotProvider interface {
	GetBlindSpots() clientthought.BlindSpotReport
}

// ClusterProvider is the narrow READ seam the on-demand query(mode:personality)
// and query(mode:summary) handlers serve the loop's cached clusters + personality
// profile through. *clientthought.PropagationLoop satisfies it (GetClustersCached).
// Declared as an interface — not the concrete loop type — so the tools layer reads
// the cache without importing the loop's full surface, and so tests inject a fake
// returning constructed clusters. GetClustersCached is O(1) (a p.mu-guarded field
// read): the handler serves the clusters the background tick already detected and
// NEVER recomputes on the call path. The bool is the cold sentinel (false before
// the first tick) — the handler renders a not-yet-computed message rather than a
// synchronous cluster detect.
type ClusterProvider interface {
	GetClustersCached() ([]clientthought.ThoughtCluster, *clientthought.PersonalityProfile, bool)
}

// TensionsProvider is the narrow READ seam the on-demand query(mode:tensions)
// handler serves the loop's cached tension reports through. *clientthought.
// PropagationLoop satisfies it (GetTensions). Declared as an interface — not the
// concrete loop type — so the tools layer reads the cache without importing the
// loop's full surface, and so tests inject a fake returning constructed reports.
// GetTensions is O(1) (a p.mu-guarded field read): the handler serves the reports
// the background tick already computed and NEVER recomputes on the call path. The
// bool is the cold sentinel (false before the first tick) — the handler renders a
// not-yet-computed message rather than a synchronous tension detect.
type TensionsProvider interface {
	GetTensions() ([]clientthought.TensionReport, bool)
}

// SimilarityForcer is the narrow seam the manual propagate tool uses to drive the
// now-ASYNC topic-similarity lever (thoughts(propagate, similarity:true)).
// *clientthought.PropagationLoop satisfies it. Declared as an interface (mirroring
// ReflectionForcer) so the tools layer reaches the lever without the loop's full
// surface and tests inject a fake.
//
// The lever is async: StartSimilarityPass acquires the SAME per-account reflection
// single-flight guard in the trigger path (coalescing onto an in-flight tick →
// started=false, no second concurrent recompute), then runs the whole topic layer
// (drain → centroids → reconcile → merge cascade → summaries → drift → links) on a
// daemon-lifetime goroutine and invokes onComplete with the report — it does NOT
// return the rendered report to the caller. The event seam persists one status
// record per pass: BeginSimilarityEvent creates the status=running event at trigger
// time and FinishSimilarityEvent REPLACES it at completion (re-supplying the FULL
// metadata map — upsert is a whole-node REPLACE). The read methods back the
// similarity_report fetch op. RunSimilarityPass stays on the interface as the worker
// body StartSimilarityPass calls internally.
type SimilarityForcer interface {
	RunSimilarityPass(ctx context.Context, linkThreshold, mergeThreshold float64, densify clientthought.DensifyParams) (clientthought.SimilarityReport, error)
	StartSimilarityPass(linkThreshold, mergeThreshold float64, densify clientthought.DensifyParams, onStarted func(), onComplete clientthought.SimilarityComplete) (started bool)
	BeginSimilarityEvent(ctx context.Context, link, merge float64) (id string, startedAt time.Time, err error)
	FinishSimilarityEvent(ctx context.Context, id string, startedAt time.Time, link, merge float64, status string, durationMs int64, rendered string, headline map[string]string) error
	LatestSimilarityEvent(ctx context.Context) (*knowledgev1.Node, bool)
	LatestCompletedSimilarityEvent(ctx context.Context) (*knowledgev1.Node, bool)
	SimilarityEventByID(ctx context.Context, id string) (*knowledgev1.Node, bool)
}

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
// *graphclient.GraphClient the rest of the stdio client uses; tests inject
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
//   - WorkerRuntime: client-side dream runtime for the worker.trigger /
//     worker.status MCP intercepts. Returns nil when wireWorkerRuntime
//     degraded at boot — InterceptWorker nil-checks before dispatching
//     so the rest of the MCP loop keeps working.
//   - WorkerCRUD: client-side wire-loopback CRUD client for the
//     worker.list / worker.create / worker.update / worker.delete MCP
//     intercepts. Production wires a *workercrud.Client backed by the
//     same GraphClient; tests inject a fake. Returns nil only when the
//     client was constructed without a GraphClient — InterceptWorker
//     nil-checks before dispatching.
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
	RootDir() string
	WorkerRuntime() WorkerRuntimeAPI
	// ClaimRegistry returns the client-side hive claim registry recording
	// which MCP session holds which work claims. InterceptHive Binds on a
	// successful claim and Clears on ack/fail; the daemon Monitor reads it each
	// tick to renew the cloud lease for live claims. Returns nil in router-less
	// test fixtures and degraded headless mode — InterceptHive nil-checks before
	// using it, and the Registry methods are themselves nil-safe.
	ClaimRegistry() *hivemonitor.Registry
	// BanSet returns the client-side hive ban set: the harness-session-id ban
	// keys plus the daemon-populated Mcp-Session-Id→harness-id resolver.
	// InterceptHive consults it to refuse a banned session's hive calls
	// CLIENT-SIDE before they reach the cloud (an unresolved session fails open).
	// Returns nil in router-less test fixtures and degraded headless mode — the
	// gate nil-checks, and the BanSet methods are themselves nil-safe.
	BanSet() *hivemonitor.BanSet
	WorkerCRUD() WorkerCRUDAPI
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
	// WorkerReady / PropReady / PipelineReady report whether the corresponding
	// background-wiring stage has completed (Bind-first startup: the daemon binds the HTTP
	// MCP listener first, then wires the worker / propagation / pipeline runtimes
	// in a background goroutine). The runtime-dependent intercept guards consult
	// these to distinguish the wiring window — emit "daemon still starting:
	// <subsystem> not ready" — from a permanent boot degrade (the accessor returns
	// nil after the flag is set). False while the subsystem has not finished
	// wiring; a true result happens-after the wired handle is published, so a
	// guard that sees Ready()==true may safely read the accessor. The engine ops
	// (query / search-BM25 / mutate) have no runtime dependency and consult none
	// of these — they serve immediately after bind.
	WorkerReady() bool
	PropReady() bool
	PipelineReady() bool
}
