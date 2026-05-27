// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/backends/provider"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/dream"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/workercrud"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// toolSchema is the client-local wire shape built from the client-owned tool
// catalog (tools.AllToolSchemas). Kept deliberately minimal — same three
// fields exposed to MCP tools/list. InputSchema is raw JSON bytes (JSON Schema
// draft-07) so the client doesn't re-parse or re-validate the schema on every
// tools/list call.
type toolSchema struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// client is the MCP stdio client state. Fields are all client-side only —
// no graph store, no tool handler, no propagation loop (those live in the
// server binary).
type client struct {
	rootDir   string                   // display-only — preserved so existing --root invocations stay accepted
	port      int                      // TCP port the server listens on
	version   string                   // binary version (reported in MCP initialize)
	client    *graphclient.GraphClient // connect-go client to the graph server
	mcpClient *graphclient.MCPClient   // MCP stdio loop (built in mcp.go)
	sink      collector.Sink           // remote upload sink for client-side collection
	// runtime is the client-side dream.Runner. Wired in mcp.go::runMCPMode
	// via wireWorkerRuntime; nil in test harnesses that build *client
	// directly. Phase H narrows the WorkerRuntime() accessor to a
	// tools.WorkerRuntimeAPI interface — for now the field stays concrete.
	runtime *dream.Runner

	// workerCRUD is the client-side wire-loopback CRUD client used by
	// InterceptWorker's list/create/update/delete branches. Wired in
	// constructClient against the same *graphclient.GraphClient; nil in
	// test harnesses that build *client directly. The WorkerCRUD()
	// accessor returns an untyped nil interface in that case so the
	// intercept nil-check fires.
	workerCRUD *workercrud.Client

	// embedder is the client-side BinaryEmbedder used by InterceptSearch /
	// InterceptQuery to embed query text on the client side so the
	// server's compositor short-circuits its own embed call (Phase 4.5).
	// Built in runMCPMode via llmproviders.BuildEmbedder after config
	// load. nil when no voyage_api_key is configured — search falls
	// back to BM25-only via the server-side nil-embedder path.
	embedder embed.BinaryEmbedder

	// pipeline is the client-side LLM pipeline (summary + embed worker
	// pools + per-graph collectors + background graph-refresh goroutine)
	// constructed by wirePipelineRuntime. nil when --no-llm-pipeline is
	// set OR config provides neither summarizer nor embedder. The deferred
	// p.Stop call in runMCPMode handles nil safely.
	pipeline *pipeline.Pipeline

	// propLoop is the client-side reflective-surface goroutine that
	// hourly re-detects thought clusters and propagates valence /
	// magnitude through the graph. Wired in mcp.go::runMCPMode via
	// wirePropagationRuntime; nil when --no-propagation-runtime is set
	// OR construction failed at boot. The deferred Stop call in
	// runMCPMode handles nil safely (Stop is nil-safe). Per BCN4 v2
	// Phase 6 / T1: holds *graphclient.GraphClient directly via
	// NewPropagationLoop — no store-shaped wrapper.
	propLoop *clientthought.PropagationLoop

	// Tool-schema cache: built once by loadSchemas on the first
	// tools/list request from the client-owned catalog
	// (tools.AllToolSchemas), then reused for the rest of the process.
	// schemaMu guards the cache fields. The catalog is built from static
	// local literals and never fails, so the build is effectively a
	// sync.Once — schemaDone latching true on the first call is correct;
	// there is no transient-failure retry path to preserve.
	schemaMu   sync.Mutex
	schemas    []toolSchema
	schemaDone bool

	// repoResolver is the client-side cwd → code-graph-name resolver
	// (FUL-241 Phase 2). Constructed lazily on first RepoResolver()
	// call via repoResolverOnce so test harnesses that build *client
	// without a GraphClient don't trip on the nil-graph path. The
	// resolver's own sync.Once gates the code-graph catalog read, so
	// one resolver-per-session is exactly what we want.
	repoResolverOnce sync.Once
	repoResolver     *tools.RepoResolver
}

// GraphClient returns the connect-go client to the graph server. Satisfies
// tools.ClientDeps so the internal/tools package can reach liveness +
// Status RPCs without importing the concrete *client type (which would
// create an import cycle back into cmd/knowledge).
func (c *client) GraphClient() *graphclient.GraphClient { return c.client }

// PipelineMetrics returns a snapshot of the client-side LLM pipeline
// counters (summary + embed queue depth, running workers, cumulative
// successes / terminal failures). Satisfies the optional
// tools.pipelineMetricser interface read by handleServerStatus —
// without this overlay, manage(status) would print zeros for the
// pipeline counters because the server-side StatusResponse leaves
// those proto fields unset (post-BCN5 the pipeline moved client-side
// but the status overlay was never wired through).
//
// ok=false when pipeline is nil (--no-llm-pipeline, or neither
// summarizer nor embedder configured at boot); callers render that
// case as "(pipeline disabled)" so an operator can distinguish
// "queue empty" from "pipeline never wired."
func (c *client) PipelineMetrics() (pipeline.Metrics, bool) {
	if c.pipeline == nil {
		return pipeline.Metrics{}, false
	}
	return c.pipeline.Metrics(), true
}

// ResetPipelineFailedCounters zeroes the session-lifetime failed counters.
// Satisfies the optional tools.pipelineResetter interface called by
// clear_llm_failures after removing on-disk markers.
func (c *client) ResetPipelineFailedCounters() {
	if c.pipeline != nil {
		c.pipeline.ResetFailedCounters()
	}
}

// Sink returns the remote upload sink. Satisfies tools.ClientDeps — the
// collect intercept path streams chunks to the server via this sink rather
// than opening knowledge.bin directly.
func (c *client) Sink() collector.Sink { return c.sink }

// RootDir returns the project root directory passed via --root (defaults to
// ".") so the ast intercept can walk source files locally. Satisfies
// tools.ClientDeps; the server has no repo (remote-server mode) so AST
// parsing must happen on the client where the files live.
func (c *client) RootDir() string { return c.rootDir }

// WorkerRuntime returns the client-side dream runtime so the worker
// trigger / status MCP intercepts (Phase H) can dispatch through it.
// Returns a tools.WorkerRuntimeAPI rather than *dream.Runner so test
// fakes in the tools package can satisfy ClientDeps without
// instantiating a real Runner. *dream.Runner satisfies WorkerRuntimeAPI
// structurally — no adapter needed in production.
//
// May return a nil interface (because c.runtime is a nil *dream.Runner)
// if wireWorkerRuntime degraded at boot. InterceptWorker nil-checks
// before dispatching. Note: a typed-nil *dream.Runner is NOT a nil
// interface value, so the check inside InterceptWorker compares the
// typed pointer indirectly via OnManualTrigger's own nil-receiver guard.
func (c *client) WorkerRuntime() tools.WorkerRuntimeAPI {
	if c.runtime == nil {
		// Return an untyped nil so InterceptWorker's `rt == nil` check
		// fires. Without this, the interface would carry a typed-nil
		// *dream.Runner and the nil-check would pass through.
		return nil
	}
	return c.runtime
}

// WorkerCRUD returns the client-side wire-loopback CRUD client so the
// worker list / create / update / delete MCP intercepts can dispatch
// through it. Mirrors the WorkerRuntime accessor's nil-tolerance —
// returns an untyped nil interface when the *client was constructed
// without one, so InterceptWorker's nil-check fires before any wire
// call.
func (c *client) WorkerCRUD() tools.WorkerCRUDAPI {
	if c.workerCRUD == nil {
		return nil
	}
	return c.workerCRUD
}

// Embedder returns the client-side binary embedder so InterceptSearch /
// InterceptQuery can embed query text on the client side. Returns nil
// (the interface zero) when no voyage_api_key is configured — the search
// intercept falls through to forwarding the original args unchanged,
// and the server's compositor either uses its own embedder (if any)
// or returns BM25-only results.
func (c *client) Embedder() embed.BinaryEmbedder {
	return c.embedder
}

// BackendResolver returns the production backend resolver — delegates to
// cmd/knowledge/internal/backends/provider. Closed-switch order is set
// there; Default returns nil when no backend is configured (intercepts
// fall through to local-only).
func (c *client) BackendResolver() tools.BackendResolver { return providerBackendResolver{} }

// GraphCaller returns the production graph caller — the same
// *graphclient.GraphClient the rest of the stdio client uses. Intercepts
// forward the local portion of a backend-backed call through this
// caller. Returns nil only when the *client was constructed without a
// GraphClient (degraded headless mode); intercepts fail fast in that
// case rather than performing the backend write with no way to persist
// the local-graph mirror.
func (c *client) GraphCaller() tools.GraphCaller {
	if c.client == nil {
		return nil
	}
	return graphClientCaller{gc: c.client}
}

// RepoResolver returns the client-side cwd → code-graph-name resolver.
// Constructed lazily on first call via repoResolverOnce — one resolver
// per MCP session. Returns nil when no GraphCaller is wired (degraded
// headless mode), and InjectRepoIfCodeGraph falls through harmlessly
// in that case.
func (c *client) RepoResolver() *tools.RepoResolver {
	c.repoResolverOnce.Do(func() {
		gc := c.GraphCaller()
		if gc == nil {
			return
		}
		c.repoResolver = tools.NewRepoResolver(gc)
	})
	return c.repoResolver
}

// providerBackendResolver delegates to the production
// cmd/knowledge/internal/backends/provider package. Kept tiny so the
// *client struct can satisfy ClientDeps.BackendResolver() without
// holding a per-instance field.
type providerBackendResolver struct{}

func (providerBackendResolver) Default() backends.Backend {
	return provider.Default()
}
func (providerBackendResolver) ByName(name string) backends.Backend {
	return provider.ByName(name)
}

// graphClientCaller adapts a *graphclient.GraphClient to the narrow
// tools.GraphCaller interface so intercepts can forward tail-calls
// without depending on the concrete graph-client type. The base seam is Execute —
// every intercept read/write rides the Execute carrier, type-asserting this
// concrete value UP to render.Executor / Indexer / Syncer / topologyFetcher as
// needed.
type graphClientCaller struct {
	gc *graphclient.GraphClient
}

// Execute is the base GraphCaller seam: it exposes the wrapped *GraphClient's
// engine Execute so the carrier-backed internal wire helpers (PersistBatch,
// render.FetchNode / IterEdges, the project/plan/ticket intercepts, the
// thought/linker/pipeline wire helpers) decode raw ExecuteResponse carriers. The
// helpers type-assert this same concrete value to the render.Executor seam.
func (g graphClientCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return g.gc.Execute(ctx, req)
}

// Index exposes the wrapped *GraphClient's engine Index RPC so the client-side
// manage intercepts (T-GTB3 Phase 7: set_metadata_overrides / delete_branch /
// list_branches / rebuild_bm25 / rebuild_hnsw) can drive the generic lifecycle
// ops without reaching for the concrete *GraphClient. This is the narrow Index
// seam the tools.Indexer type-assert upgrades to — like Execute above, it does
// NOT widen the Call-only tools.GraphCaller interface.
func (g graphClientCaller) Index(ctx context.Context, req *knowledgev1.IndexRequest) (*knowledgev1.IndexResponse, error) {
	return g.gc.Index(ctx, req)
}

// MetadataStats exposes the wrapped *GraphClient's engine MetadataStats RPC so
// the client-side promote_metadata composer (T-GTB6) can read the per-graph
// stats + override carriers off the GraphCaller without reaching for the
// concrete *GraphClient. Like Execute/Index, this is a narrow seam a tools-side
// interface type-asserts for; it does NOT widen the Call-only GraphCaller.
func (g graphClientCaller) MetadataStats(ctx context.Context, req *knowledgev1.MetadataStatsRequest) (*knowledgev1.MetadataStatsResponse, error) {
	return g.gc.MetadataStats(ctx, req)
}

// ExportGraph exposes the wrapped *GraphClient's engine ExportGraph RPC so the
// client-side push orchestration (InterceptSync) can fetch the serialized OSS
// graph bytes off the GraphCaller without reaching for the concrete
// *GraphClient. This is the narrow Exporter seam the tools.Exporter type-assert
// upgrades to — like Execute/Index/MetadataStats/Sync, it does NOT widen the
// Call-only tools.GraphCaller interface.
func (g graphClientCaller) ExportGraph(ctx context.Context, req *knowledgev1.ExportGraphRequest) (*knowledgev1.ExportGraphResponse, error) {
	return g.gc.ExportGraph(ctx, req)
}

// constructClient builds a stdio client that proxies to the graph server.
// It does NOT open any .bin file and does NOT register key fragments —
// the server binary owns graph storage. The sink is a RemoteUploadSink
// over the IngestService client so local collection streams chunks to the
// server.
//
// Also launches the background keepalive goroutine that surfaces server
// drops to slog between user-triggered tool calls. The unary reconnect
// interceptor redials transparently when the next real request lands —
// keepalive is operator visibility, not recovery.
func constructClient(f Config) *client {
	tcp := graphclient.NewGraphClient(f.Port)
	c := &client{
		rootDir: f.RootDir,
		port:    f.Port,
		version: Version,
		client:  tcp,
	}
	c.sink = remote.NewUploadSink(tcp.IngestClient())
	// Wire the worker CRUD client. Same GraphClient as everything else
	// — every CRUD call goes back through the wire-loopback transport
	// so the server stays the source of truth for graph-resident
	// NodeWorker rows.
	c.workerCRUD = workercrud.New(tcp)
	// Install as the process-wide default factory too so call-sites that
	// don't route through c.sink (e.g. codegraph.Sync in an intercept
	// handler that forgets to set opts.Sink) still get the remote sink.
	collector.DefaultSinkFactory = func() collector.Sink { return c.sink }
	// Production-only: the stdio MCP client needs periodic drop detection.
	// Test harnesses that build a *client directly (without this
	// constructor) skip this — nothing else in the codebase calls
	// StartKeepalive.
	tcp.StartKeepalive(context.Background())
	return c
}

// loadSchemas returns the client-owned full tool-schema set, built once from
// tools.AllToolSchemas on the first call and served from an in-process cache
// thereafter. The MCP tool catalog is client-owned: a static set of local schema
// literals, so the build never fails; the error return is retained only so the
// handleToolsList caller's shape stays unchanged (it is always nil).
func (c *client) loadSchemas(_ context.Context) ([]toolSchema, error) {
	c.schemaMu.Lock()
	defer c.schemaMu.Unlock()

	if c.schemaDone {
		return c.schemas, nil
	}

	defs := tools.AllToolSchemas()
	out := make([]toolSchema, 0, len(defs))
	for _, def := range defs {
		schemaJSON, err := json.Marshal(def.InputSchema)
		if err != nil {
			// Static literals — marshal cannot fail in practice. Surface
			// the error rather than caching a partial catalog.
			return nil, fmt.Errorf("marshal client-side tool schema %q: %w", def.Name, err)
		}
		out = append(out, toolSchema{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: schemaJSON,
		})
	}

	c.schemas = out
	c.schemaDone = true
	return c.schemas, nil
}
