// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/backends/provider"
	"github.com/fulminate-io/knowledge-mcp/internal/cli"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/dream"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/graphtypecrud"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/workercrud"
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
	rootDir string // display-only — preserved so existing --root invocations stay accepted
	port    int    // TCP port the server listens on
	version string // binary version (reported in MCP initialize)
	// local is the connect-go client to the LOCAL graph server (127.0.0.1).
	// Replaces the prior `client` field as part of the routing rework. The
	// genuinely always-local callers (sync push, sync list) reach it via the
	// LocalGraphCaller()/Router.Local() accessors. The post-collect linker and
	// postpopulate do NOT read this directly — they follow the data via the
	// login-routed GraphCaller (cloud when logged in). May be nil for a
	// cloud-first user with no install — router handles dispatch for everyone
	// else.
	local *graphclient.GraphClient
	// router is the routing layer. Per-call dispatches to local or
	// cloud based on the live auth state cached in authState. Built by
	// constructClient; tests that build *client directly leave router nil
	// (the GraphCaller() accessor returns nil in that case, preserving the
	// pre-rewrite short-circuit contract).
	router *graphclient.Router
	// authState backs the routing decision in router. Held on *client so
	// the e2e test in Phase 4 can inspect / flip it via auth.NewAuthState
	// inputs.
	authState *auth.AuthState

	mcpClient *graphclient.MCPClient // MCP dispatch client (built by the serve daemon, daemon.go)
	sink      collector.Sink         // remote upload sink for client-side collection
	// runtime is the client-side dream.Runner. Wired in buildClient (daemon.go)
	// via wireWorkerRuntime; nil in test harnesses that build *client
	// directly. Phase H narrows the WorkerRuntime() accessor to a
	// tools.WorkerRuntimeAPI interface — for now the field stays concrete.
	runtime *dream.Runner

	// workerReady / propReady / pipelineReady are the per-subsystem readiness
	// flags that distinguish the background-wiring window (Bind-first startup: the daemon
	// binds the HTTP MCP listener first, then wires worker/propagation/pipeline
	// runtimes in a background goroutine) from a permanent boot degrade. Each is
	// Stored true at the END of its wiring stage in wireRuntimesBackground
	// (daemon.go) — whether that stage wired a live runtime or degraded to nil —
	// so the intercept guards can emit "daemon still starting: <subsystem> not
	// ready" during the window and fall through to the existing nil-accessor
	// degrade error once the flag is set. Zero value (false) is correct for a
	// *client built directly in a test harness: it reports "not ready" until
	// explicitly marked, matching the existing nil-accessor degrade contract.
	// The atomic Store also provides the happens-before edge that safely
	// publishes the subsystem handle written immediately before it (see the
	// mark*Ready call sites in wireRuntimesBackground).
	workerReady   atomic.Bool
	propReady     atomic.Bool
	pipelineReady atomic.Bool

	// wireCtx is the cancelable ctx the background wiring goroutine
	// (wireRuntimesBackground) runs under; runServe passes it when launching the
	// goroutine. wireCancel cancels it so an in-flight wire stage and the
	// propagation/pipeline loops unwind promptly on shutdown (bind-first startup). wireDone
	// is closed by wireRuntimesBackground when the background chain finishes (even
	// on an early degrade/return), so the cleanup closure can bounded-join it
	// before draining. All nil in a test harness that builds *client directly and
	// never launches the background wiring.
	wireCtx    context.Context
	wireCancel context.CancelFunc
	wireDone   chan struct{}

	// workerCRUD / graphTypeCRUD are the client-side CRUD clients used by
	// InterceptWorker and InterceptGraphType. Both are wired in
	// constructClient against the login-aware c.router (Execute routes
	// per-call to cloud when logged in / local otherwise) so a cloud-only
	// daemon serves worker + graph-type CRUD from cloud instead of dialing
	// :15022; nil in test harnesses that build *client directly, where the
	// WorkerCRUD() / GraphTypeCRUD() accessors return an untyped nil
	// interface so the intercept nil-check fires.
	workerCRUD    *workercrud.Client
	graphTypeCRUD *graphtypecrud.Client

	// embedder is the client-side BinaryEmbedder used by InterceptSearch /
	// InterceptQuery to embed query text on the client side so the
	// server's compositor short-circuits its own embed call (Phase 4.5).
	// Built in buildClient via llmproviders.BuildEmbedder after config
	// load. nil when no voyage_api_key is configured — search falls
	// back to BM25-only via the server-side nil-embedder path.
	embedder embed.BinaryEmbedder

	// pipeline is the client-side LLM pipeline (summary + embed worker
	// pools + per-graph collectors + background graph-refresh goroutine)
	// constructed by wirePipelineRuntime. nil when --no-llm-pipeline is
	// set OR config provides neither summarizer nor embedder. The deferred
	// p.Stop call in buildClient's cleanup closure (daemon.go) handles nil safely.
	pipeline *pipeline.Pipeline

	// segmentMgr is the per-graph client-hosted BM25+HNSW segment owner. ONE
	// instance shared between the PRODUCER side (the pipeline ships segments
	// into it at embed writeback — AttachSegmentManager) and the CONSUMER side
	// (the search intercepts query it via SegmentManager()). Constructed in
	// wirePipelineRuntime alongside the pipeline; nil when the pipeline was not
	// wired. Holding ONE instance is load-bearing: a second Manager would build
	// duplicate engines (double memory) and miss the producer's loaded segments.
	segmentMgr *segmentdist.Manager

	// claimRegistry + banSet are the client-side hive monitor state, created in
	// constructClient and shared (SAME instance) with the daemon Monitor:
	// claimRegistry maps MCP session → its work claims (InterceptHive Binds on
	// claim / Clears on ack; the Monitor renews them); banSet holds the
	// harness-id ban keys + the Mcp→harness resolver the Monitor populates and
	// the InterceptHive gate consults. Both nil in test harnesses that build
	// *client directly; their accessors + methods are nil-safe.
	claimRegistry *hivemonitor.Registry
	banSet        *hivemonitor.BanSet

	// propLoop is the client-side reflective-surface goroutine that
	// hourly re-detects thought clusters and propagates valence /
	// magnitude through the graph. Wired in buildClient (daemon.go) via
	// wirePropagationRuntime; nil when --no-propagation-runtime is set
	// OR construction failed at boot. The deferred Stop call in
	// buildClient's cleanup closure handles nil safely (Stop is nil-safe). Holds
	// the Execute-only thought.Caller (passed c.router) via NewPropagationLoop
	// — no store-shaped wrapper — so propagation routes cloud-when-logged-in.
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
	// resolver. Constructed lazily on first RepoResolver()
	// call via repoResolverOnce so test harnesses that build *client
	// without a GraphClient don't trip on the nil-graph path. The
	// resolver's own sync.Once gates the code-graph catalog read, so
	// one resolver-per-session is exactly what we want.
	repoResolverOnce sync.Once
	repoResolver     *tools.RepoResolver
}

// LocalLiveness returns the LOCAL graph client as a liveness-only view
// (Healthy + Status, no Execute carrier). Satisfies tools.ClientDeps so the
// internal/tools package can reach the local daemon's liveness + Status RPCs
// for manage(status) without being able to pull a graph-write off the bare
// local client — graph reads/writes route via GraphCaller() (the Router).
// *graphclient.GraphClient satisfies tools.LocalLiveness structurally.
func (c *client) LocalLiveness() tools.LocalLiveness { return c.local }

// PipelineMetrics returns a snapshot of the client-side LLM pipeline
// counters (summary + embed queue depth, running workers, cumulative
// successes / terminal failures). Satisfies the optional
// tools.pipelineMetricser interface read by handleServerStatus —
// without this overlay, manage(status) would print zeros for the
// pipeline counters because the server-side StatusResponse leaves
// those proto fields unset (the pipeline moved client-side
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

// CloudStatusInfo reports whether the user is logged in to Fulminate Cloud
// and the cloud host to surface in manage(status). Satisfies the optional
// tools.cloudStatusInfo interface read by handleServerStatus: when logged
// in, status reports the CLOUD graph via the routed Stats RPC instead of
// the local daemon. c.authState backs the same routing decision the Router
// uses; IsLoggedIn is the canonical 5s-TTL login signal. The nil-guard
// matches the degraded-test-fixture tolerance the other accessors carry
// (e.g. GraphCaller returns nil when router==nil).
func (c *client) CloudStatusInfo() (bool, string) {
	if c.authState == nil {
		return false, cli.CloudEndpoint
	}
	return c.authState.IsLoggedIn(context.Background()), cli.CloudEndpoint
}

// ResetPipelineFailedCounters zeroes the session-lifetime failed counters.
// Satisfies the optional tools.pipelineResetter interface called by
// clear_llm_failures after removing on-disk markers.
func (c *client) ResetPipelineFailedCounters() {
	if c.pipeline != nil {
		c.pipeline.ResetFailedCounters()
	}
}

// WakePipeline nudges every LLM-pipeline collector to re-scan promptly.
// Satisfies the optional tools.pipelineWaker interface the collect intercept
// calls after a successful collect, so a freshly-collected graph that had
// idle-backed-off its scan cadence discovers the new nodes within one base tick
// instead of waiting out the hour-long idle ceiling.
func (c *client) WakePipeline() {
	if c.pipeline != nil {
		c.pipeline.WakeAll()
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

// WorkerCRUD returns the client-side wire-loopback CRUD client for the worker
// intercepts (nil-tolerant; see the workerCRUD/graphTypeCRUD field comment).
func (c *client) WorkerCRUD() tools.WorkerCRUDAPI {
	if c.workerCRUD == nil {
		return nil
	}
	return c.workerCRUD
}

// GraphTypeCRUD mirrors WorkerCRUD for the graph_type intercepts (nil-tolerant).
func (c *client) GraphTypeCRUD() tools.GraphTypeCRUDAPI {
	if c.graphTypeCRUD == nil {
		return nil
	}
	return c.graphTypeCRUD
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

// GraphCaller returns the production graph caller — the
// routing layer. The returned *Router dispatches per-call to either the
// local *GraphClient (default / logged out) or a lazily-built cloud
// *GraphClient (when AuthState reports IsLoggedIn=true), surfacing
// ErrNoBackend when neither is reachable. Intercepts forward the local
// portion of a backend-backed call through this caller. Returns nil
// only when the *client was constructed without a router (zero-value
// test fixture); intercepts fail fast in that case rather than
// performing the backend write with no way to persist the local-graph
// mirror.
func (c *client) GraphCaller() tools.GraphCaller {
	if c.router == nil {
		return nil
	}
	return c.router
}

// LocalGraphCaller returns a GraphCaller that ALWAYS targets the local server,
// bypassing routing. Only the three local-only callers use it: sync push (source
// bytes come from the LOCAL graph; destination is cloud), sync list, and sync
// pull (the OverwriteGraph apply target is the LOCAL .bin). The post-collect
// linker and postpopulate are NOT local-only (cloud-routed GraphCaller). Returns
// nil when the *client has no local GraphClient (cloud-first user, no install);
// callers' nil-guards surface the degraded-mode error. The wrapper satisfies
// Indexer / Exporter / Overwriter / metadataStatsCaller / statsRPC.
func (c *client) LocalGraphCaller() tools.GraphCaller {
	if c.local == nil {
		return nil
	}
	return graphClientCaller{gc: c.local}
}

// noopAuthStore is a fallback Store implementation used when auth.NewStore()
// returns ErrNotImplementedOS (Windows) or any other transient construction
// failure. Get always returns ErrNotFound so AuthState reports
// IsLoggedIn=false; Set/Delete are silent no-ops. The router falls through
// to the local *GraphClient unconditionally when this store backs the
// AuthState, preserving the prior unauthenticated behavior on those
// platforms.
type noopAuthStore struct{}

func (noopAuthStore) Get(context.Context, string) (string, error) {
	return "", auth.ErrNotFound
}
func (noopAuthStore) Set(context.Context, string, string) error { return nil }
func (noopAuthStore) Delete(context.Context, string) error      { return nil }

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
