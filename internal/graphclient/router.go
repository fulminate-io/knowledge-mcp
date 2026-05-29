// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"errors"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
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
// Local() accessor: callers that MUST bypass routing (sync push,
// post-collect linker, post-collect postpopulate — they always read+write
// the local graph) call Router.Local() and operate on the bare local
// *GraphClient. May return nil for cloud-first users without a local
// install; those callsites must nil-check and surface a "no local server"
// error appropriately.
type Router struct {
	local       *GraphClient
	cloudURL    string
	tokenSource auth.TokenSource
	authState   *auth.AuthState

	mu    sync.Mutex
	cloud *GraphClient
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
	return &Router{
		local:       local,
		cloudURL:    cloudURL,
		tokenSource: tokenSource,
		authState:   authState,
	}
}

// Local returns the local *GraphClient backing the router, or nil for a
// cloud-first install. Callers that always-route-local (sync push,
// post-collect linker, post-collect postpopulate) use this accessor; the
// LocalGraphCaller() shape on bootstrap *client exposes it to tools/.
func (r *Router) Local() *GraphClient {
	return r.local
}

// pick returns the *GraphClient that should service this call.
//   - AuthState=true  → cloud (built lazily on first auth-true call).
//   - AuthState=false → local (when non-nil).
//   - Neither         → ErrNoBackend.
//
// pick must be called per-RPC; forwarders MUST NOT cache the *GraphClient
// across calls or the mid-session login swap would not land.
func (r *Router) pick(ctx context.Context) (*GraphClient, error) {
	if r.authState != nil && r.authState.IsLoggedIn(ctx) {
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
func (r *Router) Execute(
	ctx context.Context,
	req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	gc, err := r.pick(ctx)
	if err != nil {
		return nil, err
	}
	return gc.Execute(ctx, req)
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
// In the v1 flow, sync push explicitly routes through LocalGraphCaller()
// (the bare local *GraphClient natively implements Exporter), so this
// forwarder typically only fires when an unanticipated future caller
// reaches ExportGraph via the routed GraphCaller(). Mirrors
// (*GraphClient).ExportGraph (client.go:166) for completeness.
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
