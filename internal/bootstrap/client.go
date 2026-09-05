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
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/graphtypecrud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptanalytics"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptsync"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
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

// client is the `knowledge` client state. Fields are all client-side only —
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

	// subgraphFetcher is the INNER ingest sink c.sink wraps, retained so the
	// logs collector's cloud-subgraph read is reached BY NAME through the
	// SubgraphFetcher accessor instead of by downcasting the wrapped sink —
	// which fails the moment anything decorates the sink. ONE instance, shared
	// with c.sink: constructClient hoists the uploader and assigns both, so the
	// fetch and the writes ride the same picker and the same epoch sequence.
	// nil in a test harness that builds *client directly.
	subgraphFetcher *remote.UploadSink

	// propReady / pipelineReady are the per-subsystem readiness
	// flags that distinguish the background-wiring window (Bind-first startup: the daemon
	// binds the HTTP MCP listener first, then wires the propagation/pipeline
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

	// graphTypeCRUD is the client-side CRUD client used by
	// InterceptGraphType. It is wired in constructClient against the
	// login-aware c.router (Execute routes per-call to cloud when logged in /
	// local otherwise) so a cloud-only daemon serves graph-type CRUD from cloud
	// instead of dialing :15022; nil in test harnesses that build *client
	// directly, where the GraphTypeCRUD() accessor returns an untyped nil
	// interface so the intercept nil-check fires.
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

	// serverSegmentStamp reads the per-graph SERVER change stamp the bulk gen poll
	// last sampled — the maximum of the server's vector-write and erasure-append
	// times, in unix nanos — plus whether that graph has been sampled at all.
	// Wired from the pipeline beside it; see fuseCaughtUp, its only consumer.
	//
	// A FUNC FIELD RATHER THAN A DIRECT PIPELINE CALL, following the same injection
	// idiom localPresence and the collect-gate factory use. The operand then comes
	// from whoever holds it, and the predicate stays answerable without standing up
	// a poll loop. NIL MEANS NO READER, which fuseCaughtUp declines on — never
	// treating an absent operand as "caught up".
	serverSegmentStamp func(gt kgtypes.GraphType, name string) (int64, bool)

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

	// startupBalance holds the ONE boot-time balance verdict per segment-bearing
	// graph, so a pool that was already pathological when this daemon started is
	// readable on the status surface and not only in a log line nobody tailed. Zero
	// value usable; process-scoped and never refreshed — see
	// client_segment_balance_startup.go.
	startupBalance startupBalance

	// reaper removes dead vectors server-side when the quiescence-edge balance verdict
	// observes an imbalance the reap can repair. It is a FIELD rather than a direct
	// call so a test can install a counting double for the DEPENDENCY while the
	// ordering logic under test — reap, then RE-READ, then conclude — stays real.
	//
	// NIL IS A REAL STATE: a client whose graph caller carries no Index seam gets no
	// reaper, and the verdict then REPORTS an imbalance instead of concluding one,
	// because an unhealed gap is not evidence of a defect.
	reaper ReapInvoker

	// rebuild repairs a shortfall the reap could not close, by rebuilding the graph's
	// segments from its already-embedded nodes. A FIELD for the reason reaper is one:
	// the verdict's contract is expressed in INVOCATION COUNTS — a gap the reap closes
	// drives ZERO rebuilds, a surviving one drives exactly one — which a package-level
	// call cannot express to a test.
	//
	// NIL IS A REAL STATE: the surviving deficit is then reported and not repaired,
	// which is honest rather than degraded — nothing is silently swallowed.
	rebuild rebuildDriver

	// repairArm runs the BOUNDED repair over a graph whose deficit survived the reap,
	// ahead of the reset rebuild. A FIELD for the reason rebuild is one: the verdict's
	// contract is expressed in INVOCATION COUNTS — a deficit the bounded arm closes
	// drives ONE repair and ZERO rebuilds — which a package-level call cannot express
	// to a test.
	//
	// NIL IS A REAL STATE: the routing is skipped and the surviving deficit goes
	// straight to the reset rebuild, which is exactly what every edge did before this
	// arm was wired. Honest rather than degraded — nothing is silently swallowed.
	repairArm repairDriver

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
	//   segmentRepairTickGranted — whether this pass has already GRANTED its slot. The
	//                              name says GRANTED rather than claimed because the
	//                              rotation offers a graph a turn and spends the grant
	//                              only once one passes every gate that can decline.
	//
	// ALL of them are guarded by segmentRepairMu, the counters and the flag alike: the
	// boot-delay reconcile goroutine and the periodic ticker can both be inside a pass.
	segmentRepairMu          sync.Mutex
	segmentRepairResidue     map[segmentGraphRef]int
	segmentRepairFailures    map[segmentGraphRef]int
	segmentRepairCursor      int
	segmentRepairSeen        int
	segmentRepairTickGranted bool
	// segmentBackstopSeeded records the graphs whose DECLINED-graph seed this process
	// has already attempted, so a graph whose record write keeps failing does not
	// re-issue the horizon probe on every rotation forever. Guarded by the same mutex.
	segmentBackstopSeeded map[segmentGraphRef]struct{}
	// segmentFloorRecovered records the graphs whose UNREADABLE-RETENTION-FLOOR
	// recovery rebuild this process has already attempted. Guarded by the same mutex.
	//
	// THE GATE IS REQUIRED, not cautious. If the state path is UNWRITABLE rather than
	// corrupt, the recovery rebuild runs and publishes and its state write then fails
	// with a WARN and a nil error — so the rebuild reports success, the heal breaker
	// never latches, the record is still unreadable, and the next pass would drive
	// another full-corpus rebuild. Forever. Neither an error check nor the breaker
	// bounds that; only this claim does.
	segmentFloorRecovered map[segmentGraphRef]struct{}

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

	// workingSet is the set of graphs a direct user interaction has admitted in
	// THIS process, and the gate every background loop consults before touching
	// a graph. Constructed EARLY and unconditionally in constructClient (it has
	// zero dependencies) so no wiring order can leave a consumer holding nil.
	// It is nil for a directly-built test fixture, and nil reads as EMPTY —
	// default-deny, never unrestricted.
	workingSet *workingset.Set

	// localPresence answers whether background work may touch this graph on THIS
	// machine. nil means the production default (graphLocallyPresent below), and
	// production never sets it — it exists so a fixture can state presence
	// directly instead of having to plant a repo manifest on the test machine.
	// Same bootstrap-supplied-closure shape as the auto-heal arm's
	// healIfSegmentless.
	localPresence func(gt kgtypes.GraphType, name string) bool

	// presenceSkipMu guards presenceSkipLogged, the set of code graphs already
	// reported as declined for background work. The predicate runs on every
	// reconcile tick and every catalog pass, so the line is latched to once per
	// graph per process — the same edge-triggered shape AdmitGraph uses for its
	// first-admission line.
	presenceSkipMu     sync.Mutex
	presenceSkipLogged map[string]struct{}

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
	// (the pure-Go transcriptanalytics engine over the local transcript parquet
	// cache) the analyze_usage intercept dispatches through. Built once on first
	// use under usageAnalyzerMu; it needs no router/network (reads the local
	// cache only).
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

	// updateHealth tracks the background update checker's state — last check,
	// last install, the failure streak, and the reason a tick took no action.
	// Constructed in maybeStartUpdateCheck immediately before the loops are
	// spawned, so a daemon whose disable gate refused leaves it NIL and the
	// status surface renders no update block at all rather than a healthy zero.
	// The accessor nil-guards, exactly as the transcript one does.
	updateHealth *updateHealthTracker
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
