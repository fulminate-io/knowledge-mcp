// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
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
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptanalytics"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptsync"
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
	rootDir    string // project root (--root); the ast intercept walks source files under it locally
	rootDirSet bool   // whether --root was explicitly set (vs the "." default) — gates the ast walk-root fail-loud guard
	port       int    // TCP port the server listens on
	version    string // binary version (reported in MCP initialize)
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
	// cloudTokenSource is the SINGLE process-wide cloud credential source,
	// resolved once by selectAuthSources and shared by the Router AND the
	// segment/sync/transcript control-plane transports (via
	// buildCloudSyncTransport). Retaining the one source — instead of each
	// consumer minting a fresh cold keychain OAuth source — means one warm
	// cache/refresh cycle per token lifetime, and lets the resolved
	// Config.AuthToken (flag/config, not os.Getenv) reach every cloud
	// transport so a machine-authed headless daemon never silently falls
	// back to the keyring. nil in test harnesses that build *client directly.
	cloudTokenSource auth.TokenSource

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

	// freshness is the activity hook's state: the account freshness watermark
	// last observed on a response, plus the cool-off window that bounds how
	// often observed movement may wake the pipeline. Value, not pointer — the
	// zero value is usable, so a test-built *client needs no extra wiring.
	// See client_freshness.go.
	freshness freshnessTrigger

	// segmentMgr is the per-graph client-hosted BM25+HNSW segment owner. ONE
	// instance shared between the PRODUCER side (the pipeline ships segments
	// into it at embed writeback — AttachSegmentManager) and the CONSUMER side
	// (the search intercepts query it via SegmentManager()). Constructed
	// UNCONDITIONALLY in wireRuntimesBackground (ensureSegmentManager, router-gated,
	// BEFORE wirePipelineRuntime) so the READ path serves BM25 over existing
	// segments even offline (--no-llm-pipeline, or no embedder/summarizer); nil ONLY
	// for a router-less / headless client. The embedder/LLM is required only for
	// index UPDATES, which stay in the pipeline-gated body. Holding ONE instance is
	// load-bearing: a second Manager would build duplicate engines (double memory)
	// and miss the producer's loaded segments.
	segmentMgr  *segmentdist.Manager
	healBreaker segmentHealBreaker // per-(graphType,name) auto-heal circuit breaker; zero value usable — see segment_heal_breaker.go

	// deltaHorizon is the tombstone-delta consumer's per-graph read progress: the
	// server-served horizon the last successful consume read up to, so the next one
	// reads only what changed after it. It is deliberately PROCESS-LOCAL and never
	// persisted — a second durable horizon could advance past a window, which is the
	// exact hazard the rebuild watermark is careful about. A restart re-seeds each
	// entry from that durable watermark, costing one re-read and never a missed
	// delete. Guarded by deltaHorizonMu because the periodic loop and the
	// nudge-woken pass both reach it. Created LAZILY on first write so a *client
	// built directly by a test harness — which is how most of this package's tests
	// construct one — needs no extra wiring.
	deltaHorizonMu sync.Mutex
	deltaHorizon   map[segmentGraphRef]int64

	// Coverage-repair arm state, PROCESS-LOCAL for the same reason deltaHorizon is:
	// the worst a restart costs is one repair pass per gapped graph, never a missed
	// repair. It deliberately does NOT extend the durable rebuild-state record —
	// that record is written WHOLESALE by two callers, so a third field would be
	// clobbered by whichever wrote last.
	//
	//   segmentRepairResidue     — per-graph structural gap the last pass settled at.
	//                              The trigger's operands can never converge on their
	//                              own (the embedded denominator counts tombstoned and
	//                              embed-failed nodes the rebuild scan excludes), so
	//                              the arm calibrates against what it observed instead
	//                              of re-firing forever.
	//   segmentRepairFailures    — consecutive failed passes per graph, feeding the
	//                              ARM-LOCAL disarm. Repair failures deliberately never
	//                              reach the shared heal breaker, which would disarm the
	//                              graph's whole auto-heal.
	//   segmentRepairCursor      — rotating round-robin position, so one reconcile tick
	//                              costs at most one full-corpus scan.
	//   segmentRepairSeen        — graphs offered the slot so far in THIS pass; compared
	//                              against the cursor to pick the tick's graph.
	//   segmentRepairTickClaimed — whether this pass has already granted its slot.
	//
	// ALL of them are guarded by segmentRepairMu, the counters and the flag alike: the
	// boot-delay reconcile goroutine and the periodic ticker can both be inside a pass.
	segmentRepairMu          sync.Mutex
	segmentRepairResidue     map[segmentGraphRef]int
	segmentRepairFailures    map[segmentGraphRef]int
	segmentRepairCursor      int
	segmentRepairSeen        int
	segmentRepairTickClaimed bool
	// segmentBackstopSeeded records the graphs whose DECLINED-graph seed this process
	// has already attempted, so a graph whose record write keeps failing does not
	// re-issue the horizon probe on every rotation forever. Guarded by the same mutex.
	segmentBackstopSeeded map[segmentGraphRef]struct{}

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

	// collectRuntime is the standing, daemon-lifetime runtime that owns detached
	// collect goroutines and tracks per-target run status. Constructed
	// EARLY and unconditionally in constructClient (it has zero dependencies —
	// no router/pipeline), so it is always available by the time a collect runs
	// behind the PipelineReady gate. The collect intercept races a run against
	// its 60s DetachAfter; drainOnShutdown Stops it. Never nil for a
	// constructClient-built client; the accessors nil-guard for direct test
	// fixtures.
	collectRuntime *tools.CollectRuntime

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

	// usageAnalyzer is the lazily-constructed client-side agent-flow analyzer
	// (embedded DuckDB over the local transcript parquet cache) the analyze_usage
	// intercept dispatches through. Built once on first use under usageAnalyzerMu;
	// it needs no router/network (reads the local cache only).
	usageAnalyzerMu   sync.Mutex
	usageAnalyzer     *transcriptanalytics.Service
	usageAnalyzerDone bool

	// transcriptHealth tracks the health of the background hourly transcript-upload
	// loop (success/failure counters, last-success timestamp, consecutive-failure
	// streak). Constructed in wireRuntimesBackground immediately before the loops are
	// spawned and Record-ed on every tick; read by the TranscriptUploadHealth()
	// accessor that the manage(status) surface overlays. nil in test-built clients
	// (like the other runtime handles) — the accessor nil-guards.
	transcriptHealth *transcriptsync.UploadHealthTracker
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
