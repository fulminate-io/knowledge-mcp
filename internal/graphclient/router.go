// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"errors"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// ErrNoBackend is the sentinel returned by Router methods when neither a
// local server nor an authenticated cloud session is available. The engine
// dispatcher (cmd/knowledge/internal/engine/dispatch.go) renders this case
// into an actionable LLM-facing message ("run `knowledge install` or
// `knowledge login`") rather than the raw error string.
var ErrNoBackend = errors.New("graphclient: no backend available — run `knowledge install` to start the local server or `knowledge login` for Fulminate Cloud")

// Router dispatches EngineService RPCs (Execute, Index, MetadataStats,
// ExportGraph, Stats) to either a local *GraphClient on 127.0.0.1 or a
// lazily-constructed cloud *GraphClient, per the live auth state cached in
// the embedded *auth.AuthState.
//
// Per-call dispatch contract: every forwarder method calls pick(ctx) at
// invocation time (not at construction time, not at type-assertion time).
// A user can `knowledge login` mid-MCP-session and the next forwarder call
// (after the AuthState TTL expires) routes to cloud without a process restart.
//
// Lazy cloud construction: the cloud *GraphClient is built once under mu
// the first time pick() sees AuthState=true, then reused for every
// subsequent cloud-routed call. The constructor reads tokenSource on
// every RPC (via bearerRoundTripper.RoundTrip), so token refreshes take
// effect transparently.
//
// Zero local + zero auth: ErrNoBackend is returned without dispatching.
//
// Local() accessor: the three callers that MUST bypass routing call
// Router.Local() and operate on the bare local *GraphClient — sync push (source
// bytes always come from the local graph), sync list, and sync pull (the
// OverwriteGraph apply target is always the local .bin). The post-collect linker
// and postpopulate are NOT local-only; they route through the cloud-aware
// GraphCaller. May return nil for cloud-first users without a local install;
// those callsites must nil-check and surface a "no local server" error
// appropriately.
type Router struct {
	local       *GraphClient
	cloudURL    string
	tokenSource auth.TokenSource
	authState   *auth.AuthState

	// machineAuth, when true, forces cloud selection unconditionally —
	// independent of the keychain auth state. It is set when the client was
	// started with a machine bearer token (the headless, non-interactive auth
	// path): such a client routes every op to cloud and runs with no local
	// server, exactly like a logged-in interactive client, but without any
	// keychain involvement. Reached only via NewRouterWithMachineAuth.
	machineAuth bool

	mu    sync.Mutex
	cloud *GraphClient
	// admitGraph records a user interaction with a concrete graph instance into
	// the working set. Installed by AttachWorkingSet; nil until then, and a nil
	// admitter records nothing. See router_admission.go.
	admitGraph func(gt kgtypes.GraphType, name, reason string)
}

// CloseIdleConnections releases the pooled connections of BOTH clients the Router
// can hold: the local one it was constructed with, and the cloud one it builds
// lazily on first cloud routing.
//
// THE LAZY CLOUD CLIENT IS THE REASON THIS EXISTS. A caller holding only the local
// *GraphClient it passed to NewRouter cannot reach the cloud client at all — the
// Router mints it internally under mu — so closing the local client leaves the
// cloud connection pooled and, with it, whatever server is serving it. In the
// daemon that is correct and irrelevant; in a test binary that constructs routers
// per test it pins an HTTP/2 serve goroutine per router for the life of the
// process.
//
// Safe on a nil Router and safe to call repeatedly.
//
// It carries GraphClient.CloseIdleConnections's limitation with it: a pool-level
// release reaches only the connections the transport already calls idle. Close
// below is the release for a router a caller is finished with.
func (r *Router) CloseIdleConnections() {
	if r == nil {
		return
	}
	r.local.CloseIdleConnections()
	r.mu.Lock()
	cloud := r.cloud
	r.mu.Unlock()
	cloud.CloseIdleConnections()
}

// Close tears down the connections of BOTH clients the Router can hold, whether
// their transports consider them idle or not — the router-shaped counterpart of
// GraphClient.Close, and for the same reason: a caller discarding a router wants
// its connections gone as a fact rather than as the outcome of a race with the
// transport's own stream bookkeeping.
//
// Safe on a nil Router, safe to call repeatedly, and safe to call on a router
// that never routed to cloud (the lazy client is nil, and Close is nil-safe).
func (r *Router) Close() {
	if r == nil {
		return
	}
	r.local.Close()
	r.mu.Lock()
	cloud := r.cloud
	r.mu.Unlock()
	cloud.Close()
}

// NewRouter wires a Router. local may be nil (cloud-first user with no
// local server install). cloudURL is the Fulminate API base URL (no
// trailing slash). tokenSource and authState must be non-nil — pass
// auth.StaticTokenSource{} + a never-logged-in AuthState in degraded
// configurations rather than nil.
func NewRouter(
	local *GraphClient,
	cloudURL string,
	tokenSource auth.TokenSource,
	authState *auth.AuthState,
) *Router {
	return NewRouterWithMachineAuth(local, cloudURL, tokenSource, authState, false)
}

// NewRouterWithMachineAuth wires a Router that forces cloud selection when
// machineAuth is true, bypassing the keychain auth state entirely. This is the
// constructor for the headless machine-bearer path: the client presents a
// caller-supplied token and routes every op to cloud with no local server and
// no interactive login.
//
// cloudURL stays the canonical build-tag-pinned cloud endpoint at every call
// site — this constructor exists ONLY to carry the machine-token selection
// signal, never to vary the endpoint. The endpoint is not overridable;
// in-cluster routing is handled by infrastructure, out of this client's scope.
//
// The same local/cloudURL/tokenSource/authState contract as NewRouter applies:
// local may be nil; tokenSource and authState must be non-nil.
func NewRouterWithMachineAuth(
	local *GraphClient,
	cloudURL string,
	tokenSource auth.TokenSource,
	authState *auth.AuthState,
	machineAuth bool,
) *Router {
	return &Router{
		local:       local,
		cloudURL:    cloudURL,
		tokenSource: tokenSource,
		authState:   authState,
		machineAuth: machineAuth,
	}
}

// Local returns the local *GraphClient backing the router, or nil for a
// cloud-first install. The three always-route-local callers (sync push, sync
// list, and sync pull's OverwriteGraph apply) use this accessor; the
// LocalGraphCaller() shape on bootstrap *client exposes it to tools/. The
// post-collect linker and postpopulate route through the cloud-aware
// GraphCaller, not this accessor.
func (r *Router) Local() *GraphClient {
	return r.local
}

// IngestClient is the per-call-routed IngestService picker. It resolves the
// backend *GraphClient via pick(ctx) — cloud when logged-in, local
// otherwise — and returns that backend's IngestServiceClient (client.go:88).
// The collect UploadSink invokes this per CollectChunk/Finalize/FetchCloudSubgraph
// so a mid-session login flip re-routes the next chunk without a restart.
// Mirrors the Execute/Stats forwarder shape, but returns the IngestService
// client rather than driving an RPC (the sink owns the CollectChunk flow).
func (r *Router) IngestClient(ctx context.Context) (knowledgev1connect.IngestServiceClient, error) {
	gc, err := r.pick(ctx)
	if err != nil {
		return nil, err
	}
	return gc.IngestClient(), nil
}

// LoggedIn reports the live auth state (cloud vs local routing). The client-side
// LLM pipeline's refreshOnce compares this across ticks to detect a login flip
// and force a full collector rebind (resetting the per-collector dirty-gen
// caches so a survivor graphKey re-scans the new backend from gen 0). Mirrors
// the cloud-routing decision pick(ctx) makes internally.
//
// Returns true when EITHER a machine bearer token was supplied at construction
// (headless auth — always cloud) OR the keychain reports a live login
// (KeyRefreshToken presence, set by the user-run `knowledge login` CLI). The
// machine-auth signal is fixed at construction; the keychain signal is live.
func (r *Router) LoggedIn(ctx context.Context) bool {
	return r.machineAuth || (r.authState != nil && r.authState.IsLoggedIn(ctx))
}

// Backend is the per-call-routed concrete-backend resolver. It returns the
// *GraphClient that should service this call — cloud when logged-in (built
// lazily via ensureCloud), local otherwise, ErrNoBackend when neither — by
// delegating to the same pick(ctx) the EngineService forwarders use. The
// client-side LLM pipeline holds a routedWireClient over this so each scan +
// writeback binds the CURRENT backend; the per-graph collector resolves one
// concrete backend here at construction and stamps it on every emitted work
// item. *GraphClient satisfies pipeline.WireClient (PipelineScan + Execute),
// so the result is usable as a WireClient directly. Re-picks per call so a
// mid-session login flip re-routes the next scan/write without a restart.
func (r *Router) Backend(ctx context.Context) (*GraphClient, error) {
	return r.pick(ctx)
}

// pick returns the *GraphClient that should service this call.
//   - machine-auth OR AuthState=true → cloud (built lazily on first cloud call).
//   - otherwise, local non-nil        → local.
//   - neither                         → ErrNoBackend.
//
// Cloud selection fires when EITHER a machine bearer token was supplied at
// construction (headless auth — fixed) OR the keychain reports a live login
// (set by `knowledge login`).
//
// pick must be called per-RPC; forwarders MUST NOT cache the *GraphClient
// across calls or the mid-session login swap would not land.
func (r *Router) pick(ctx context.Context) (*GraphClient, error) {
	if r.machineAuth || (r.authState != nil && r.authState.IsLoggedIn(ctx)) {
		return r.ensureCloud(), nil
	}
	if r.local != nil {
		return r.local, nil
	}
	return nil, ErrNoBackend
}

// ensureCloud builds the cloud *GraphClient on first use and returns the
// cached pointer thereafter. mu serializes construction; subsequent calls
// take the mu briefly to read the already-built pointer (cheap, but uniform
// with the construction path so the read-after-write fence is correct).
func (r *Router) ensureCloud() *GraphClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cloud == nil {
		r.cloud = NewCloudGraphClient(r.cloudURL, r.tokenSource)
	}
	return r.cloud
}

// Execute is the per-call-routed EngineService.Execute forwarder. Mirrors
// (*GraphClient).Execute (client.go:98) so every tools-side render.Executor
// type-assertion seam succeeds when GraphCaller() returns a *Router.
//
// It is also the working-set admission chokepoint: every routed call passes
// through here, so admission is decided uniformly by (operation, instance)
// rather than per call site. See router_admission.go.
func (r *Router) Execute(
	ctx context.Context,
	req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	// SELECTOR VALIDATION COMES FIRST — before the pick and before the wire
	// call. A target naming a family this binary cannot honor is bad input, so
	// it is refused at the boundary rather than after a backend has answered and
	// its response has been thrown away.
	gt, instance, err := resolveAdmissionTarget(req)
	if err != nil {
		return nil, err
	}
	gc, err := r.pick(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := gc.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	// ADMISSION FOLLOWS A SUCCESSFUL DISPATCH, AND THE ORDERING IS THE POINT.
	// Admission is not an existence check, so recording it BEFORE the call
	// admitted a read of a graph that does not exist exactly as it admitted a
	// read of one that does — and nothing ever aged the member out, so the
	// phantom earned a collector, a gen-poll entry and a scan cadence forever.
	// Enrolling only what the backend actually answered for is the half of the
	// convergence rule that stops a member being created; eviction on a durable
	// not-found is the half that removes one already there.
	r.recordAdmission(ctx, gt, instance)
	return resp, nil
}

// Index is the per-call-routed EngineService.Index forwarder. Mirrors
// (*GraphClient).Index (client.go:137) so the tools.Indexer type assertion
// at intercept_manage_index.go:38 succeeds when GraphCaller() returns a *Router.
func (r *Router) Index(
	ctx context.Context,
	req *knowledgev1.IndexRequest,
) (*knowledgev1.IndexResponse, error) {
	gc, err := r.pick(ctx)
	if err != nil {
		return nil, err
	}
	return gc.Index(ctx, req)
}

// MetadataStats is the per-call-routed EngineService.MetadataStats forwarder.
// Mirrors (*GraphClient).MetadataStats (client.go:124) so the
// metadataStatsCaller type assertion at intercept_manage_promote.go:42
// succeeds when GraphCaller() returns a *Router.
func (r *Router) MetadataStats(
	ctx context.Context,
	req *knowledgev1.MetadataStatsRequest,
) (*knowledgev1.MetadataStatsResponse, error) {
	gc, err := r.pick(ctx)
	if err != nil {
		return nil, err
	}
	return gc.MetadataStats(ctx, req)
}

// ExportGraph is the per-call-routed EngineService.ExportGraph forwarder.
// sync pull fetches its cloud bytes through THIS routed forwarder —
// GraphCaller().ExportGraph routes cloud when logged in via r.pick — then
// applies them locally via OverwriteGraph. sync push instead reads its local
// source bytes through LocalGraphCaller() (the bare local *GraphClient natively
// implements Exporter), bypassing this forwarder. Mirrors
// (*GraphClient).ExportGraph (client.go) for completeness.
func (r *Router) ExportGraph(
	ctx context.Context,
	req *knowledgev1.ExportGraphRequest,
) (*knowledgev1.ExportGraphResponse, error) {
	gc, err := r.pick(ctx)
	if err != nil {
		return nil, err
	}
	return gc.ExportGraph(ctx, req)
}

// FreshnessGen returns the account freshness watermark last observed on a
// response from the backend this call routes to. ErrNoBackend (or a nil backend)
// reads as 0, which the wire contract defines as "no watermark".
//
// It routes through pick(ctx) rather than reading a package-level slot because a
// logged-in client holds BOTH a cloud *GraphClient and a local one (Local()
// serves sync push/pull, :49-56) and their counters belong to different
// accounts. Reading the backend the caller's traffic actually went to is what
// makes a later compare meaningful; a shared cell would flap between two
// accounts' values forever.
func (r *Router) FreshnessGen(ctx context.Context) uint64 {
	be, err := r.pick(ctx)
	if err != nil || be == nil {
		return 0
	}
	return be.FreshnessGen()
}

// Stats is the per-call-routed EngineService.Stats forwarder. Mirrors
// (*GraphClient).Stats (client.go:111) so the statsRPC type assertion
// (intercept_query_cloud_cicd.go:236, intercept_query_stats.go:53, etc.)
// succeeds when GraphCaller() returns a *Router.
func (r *Router) Stats(
	ctx context.Context,
	req *knowledgev1.StatsRequest,
) (*knowledgev1.StatsResponse, error) {
	gc, err := r.pick(ctx)
	if err != nil {
		return nil, err
	}
	return gc.Stats(ctx, req)
}
