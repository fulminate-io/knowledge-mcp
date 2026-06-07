// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// fakeAuthStore is the inline Store fake the router tests use to flip auth
// state synchronously. Mirrors cmd/knowledge/internal/auth/state_test.go
// fakeAuthStore but stays here so the graphclient package doesn't depend
// on auth's test fixtures.
type fakeAuthStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newFakeAuthStore() *fakeAuthStore {
	return &fakeAuthStore{data: make(map[string]string)}
}

func (s *fakeAuthStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return "", auth.ErrNotFound
	}
	return v, nil
}

func (s *fakeAuthStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *fakeAuthStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; !ok {
		return auth.ErrNotFound
	}
	delete(s.data, key)
	return nil
}

// countingEngine is an EngineService handler that counts hits per RPC kind
// and replies with a minimal, valid response. Each test fixture stands up
// two of these (local + cloud) so we can assert which backend serviced a
// given call.
type countingEngine struct {
	execute       atomic.Int32
	index         atomic.Int32
	metadataStats atomic.Int32
	exportGraph   atomic.Int32
	stats         atomic.Int32
	pipelineScan  atomic.Int32
}

func (e *countingEngine) Execute(
	_ context.Context,
	_ *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	e.execute.Add(1)
	return connect.NewResponse(&knowledgev1.ExecuteResponse{}), nil
}

func (e *countingEngine) Stats(
	_ context.Context,
	_ *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	e.stats.Add(1)
	return connect.NewResponse(&knowledgev1.StatsResponse{}), nil
}

func (e *countingEngine) MetadataStats(
	_ context.Context,
	_ *connect.Request[knowledgev1.MetadataStatsRequest],
) (*connect.Response[knowledgev1.MetadataStatsResponse], error) {
	e.metadataStats.Add(1)
	return connect.NewResponse(&knowledgev1.MetadataStatsResponse{}), nil
}

func (e *countingEngine) Index(
	_ context.Context,
	_ *connect.Request[knowledgev1.IndexRequest],
) (*connect.Response[knowledgev1.IndexResponse], error) {
	e.index.Add(1)
	return connect.NewResponse(&knowledgev1.IndexResponse{}), nil
}

func (e *countingEngine) PipelineScan(
	_ context.Context,
	_ *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	e.pipelineScan.Add(1)
	return connect.NewResponse(&knowledgev1.PipelineScanResponse{}), nil
}

func (e *countingEngine) ExportGraph(_ context.Context, _ *connect.Request[knowledgev1.ExportGraphRequest]) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	e.exportGraph.Add(1)
	return connect.NewResponse(&knowledgev1.ExportGraphResponse{}), nil
}

func (e *countingEngine) OverwriteGraph(_ context.Context, _ *connect.Request[knowledgev1.OverwriteGraphRequest]) (*connect.Response[knowledgev1.OverwriteGraphResponse], error) {
	return connect.NewResponse(&knowledgev1.OverwriteGraphResponse{}), nil
}

// startCountingEngine stands up an h2c httptest.Server in front of a
// countingEngine handler. Returns the server URL and the engine pointer
// so tests can read hit counters.
func startCountingEngine(t *testing.T) (string, *countingEngine) {
	t.Helper()
	eng := &countingEngine{}
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(path, hdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)
	return srv.URL, eng
}

// staticTokenSource is a non-refreshing token source for tests that need
// AuthState=true but do not exercise the 401 retry path.
type staticTokenSource struct{ tok string }

func (s staticTokenSource) Token(_ context.Context) (string, auth.PermissionSet, error) {
	return s.tok, nil, nil
}

// TestRouter_LocalOnly_NoAuth: inline fakeAuthStore (no token) + local hit
// counter → Execute routes local.
func TestRouter_LocalOnly_NoAuth(t *testing.T) {
	localURL, localEng := startCountingEngine(t)
	localGC := NewGraphClientForURL(localURL)
	store := newFakeAuthStore() // empty → not logged in
	as := auth.NewAuthState(store, time.Hour)
	r := NewRouter(localGC, "http://cloud.invalid", staticTokenSource{}, as)

	_, err := r.Execute(context.Background(), &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), localEng.execute.Load(), "local should have received the Execute")
}

// TestRouter_Auth_RoutesCloud: inline fakeAuthStore seeded with refresh
// token + StaticTokenSource → Execute routes cloud.
func TestRouter_Auth_RoutesCloud(t *testing.T) {
	localURL, localEng := startCountingEngine(t)
	cloudURL, cloudEng := startCountingEngine(t)
	localGC := NewGraphClientForURL(localURL)

	store := newFakeAuthStore()
	require.NoError(t, store.Set(context.Background(), auth.KeyRefreshToken, "frt-stub"))
	as := auth.NewAuthState(store, time.Hour) // long TTL — single fresh check
	r := NewRouter(localGC, cloudURL, staticTokenSource{tok: "tok-cloud"}, as)

	_, err := r.Execute(context.Background(), &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(0), localEng.execute.Load(), "local should not have been called")
	assert.Equal(t, int32(1), cloudEng.execute.Load(), "cloud should have serviced the call")
}

// TestRouter_NoLocalNoAuth_ReturnsErrNoBackend: local=nil + AuthState=false
// → Execute returns ErrNoBackend with a message containing both 'knowledge
// install' and 'knowledge login'.
func TestRouter_NoLocalNoAuth_ReturnsErrNoBackend(t *testing.T) {
	store := newFakeAuthStore() // empty
	as := auth.NewAuthState(store, time.Hour)
	r := NewRouter(nil, "http://cloud.invalid", staticTokenSource{}, as)

	_, err := r.Execute(context.Background(), &knowledgev1.ExecuteRequest{})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoBackend, "must wrap ErrNoBackend")
	msg := err.Error()
	assert.Contains(t, msg, "knowledge install")
	assert.Contains(t, msg, "knowledge login")
}

// TestRouter_NoLocal_Auth_RoutesCloud: local=nil + AuthState=true → Execute
// hits cloud (cloud-first user, no install).
func TestRouter_NoLocal_Auth_RoutesCloud(t *testing.T) {
	cloudURL, cloudEng := startCountingEngine(t)
	store := newFakeAuthStore()
	require.NoError(t, store.Set(context.Background(), auth.KeyRefreshToken, "frt-stub"))
	as := auth.NewAuthState(store, time.Hour)
	r := NewRouter(nil, cloudURL, staticTokenSource{tok: "tok-cloud"}, as)

	_, err := r.Execute(context.Background(), &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), cloudEng.execute.Load())
}

// TestRouter_DownLocal_Auth_RoutesCloud: a wired-but-DOWN local + AuthState=true
// → Execute (and the IngestService/Backend picker) route cloud with the local
// counter at 0 — fall-through reaches cloud WITHOUT a healthy local server.
func TestRouter_DownLocal_Auth_RoutesCloud(t *testing.T) {
	cloudURL, cloudEng := startCountingEngine(t)
	localGC := newUnhealthyLocalClient(t)
	store := newFakeAuthStore()
	require.NoError(t, store.Set(context.Background(), auth.KeyRefreshToken, "frt-stub"))
	as := auth.NewAuthState(store, time.Hour)
	r := NewRouter(localGC, cloudURL, staticTokenSource{tok: "tok-cloud"}, as)

	_, err := r.Execute(context.Background(), &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), cloudEng.execute.Load(), "cloud serviced Execute despite the down local")

	// The collect path picks the same routed target (not the down local).
	picked, perr := r.Backend(context.Background())
	require.NoError(t, perr)
	assert.NotSame(t, localGC, picked, "collect routing must not select the down local")
	ingest, ierr := r.IngestClient(context.Background())
	require.NoError(t, ierr)
	assert.NotNil(t, ingest, "IngestClient resolves a routed target with no healthy local")
}

// TestRouter_CloudBuiltLazilyOnce: route 100 Execute calls with AuthState=true
// through one Router; cloud client constructor fires exactly ONCE.
//
// We assert this via the cloud server's connection count: NewCloudGraphClient
// installs ONE http.Client per Router, so the cloud httptest server should
// see all requests over the same client (one or a small number of pooled
// connections). We can't intercept NewCloudGraphClient directly without a
// package hook, but the contract we care about is "lazy construction once,
// not per-call" — exercised by sending many calls and asserting the
// Router's cached cloud pointer is non-nil after the first call and stays
// referentially stable.
func TestRouter_CloudBuiltLazilyOnce(t *testing.T) {
	cloudURL, cloudEng := startCountingEngine(t)
	store := newFakeAuthStore()
	require.NoError(t, store.Set(context.Background(), auth.KeyRefreshToken, "frt-stub"))
	as := auth.NewAuthState(store, time.Hour)
	r := NewRouter(nil, cloudURL, staticTokenSource{tok: "tok"}, as)

	// Sanity: pre-call, the cached cloud pointer is nil.
	r.mu.Lock()
	require.Nil(t, r.cloud, "cloud should not be built before first call")
	r.mu.Unlock()

	const N = 100
	for range N {
		_, err := r.Execute(context.Background(), &knowledgev1.ExecuteRequest{})
		require.NoError(t, err)
	}
	assert.Equal(t, int32(N), cloudEng.execute.Load())

	// After N calls, the cached pointer must be set, and identical to itself
	// across reads (proves the constructor didn't fire per-call).
	r.mu.Lock()
	first := r.cloud
	r.mu.Unlock()
	require.NotNil(t, first, "cloud should be cached after first call")

	for range 10 {
		_, err := r.Execute(context.Background(), &knowledgev1.ExecuteRequest{})
		require.NoError(t, err)
		r.mu.Lock()
		assert.Same(t, first, r.cloud, "cloud pointer must remain stable across calls (constructor fired once)")
		r.mu.Unlock()
	}
}

// TestRouter_Local_ReturnsLocalClient: Router.Local() returns the same
// *GraphClient pointer passed at construction, OR nil when nil was passed.
func TestRouter_Local_ReturnsLocalClient(t *testing.T) {
	localURL, _ := startCountingEngine(t)
	localGC := NewGraphClientForURL(localURL)
	store := newFakeAuthStore()
	as := auth.NewAuthState(store, time.Hour)

	r := NewRouter(localGC, "http://cloud.invalid", staticTokenSource{}, as)
	assert.Same(t, localGC, r.Local(), "Local() must return the same *GraphClient pointer passed at construction")

	rNil := NewRouter(nil, "http://cloud.invalid", staticTokenSource{}, as)
	assert.Nil(t, rNil.Local(), "Local() must return nil when constructed with nil")
}

// TestRouter_MidSessionLoginSwap: inline fakeAuthStore + ttl=1ms → local→cloud
// swap on token Set.
func TestRouter_MidSessionLoginSwap(t *testing.T) {
	localURL, localEng := startCountingEngine(t)
	cloudURL, cloudEng := startCountingEngine(t)
	localGC := NewGraphClientForURL(localURL)
	store := newFakeAuthStore() // empty initially
	as := auth.NewAuthState(store, time.Millisecond)
	r := NewRouter(localGC, cloudURL, staticTokenSource{tok: "tok"}, as)

	// First call: not logged in → local.
	_, err := r.Execute(context.Background(), &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), localEng.execute.Load())
	assert.Equal(t, int32(0), cloudEng.execute.Load())

	// User runs `knowledge login` → keychain populated.
	require.NoError(t, store.Set(context.Background(), auth.KeyRefreshToken, "frt-fresh"))
	time.Sleep(50 * time.Millisecond) // > ttl

	// Next call: logged in → cloud.
	_, err = r.Execute(context.Background(), &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), localEng.execute.Load(), "local count must not advance")
	assert.Equal(t, int32(1), cloudEng.execute.Load(), "cloud must have serviced the second call")
}

// TestRouter_IngestClient_RoutesByLogin: Router.IngestClient(ctx) reuses
// pick(ctx), so it returns the LOCAL backend's IngestService client when not
// logged in and the CLOUD backend's when logged in — proving the collect sink
// picker routes by live auth state rather than hardcoding local.
func TestRouter_IngestClient_RoutesByLogin(t *testing.T) {
	localURL, _ := startCountingEngine(t)
	cloudURL, _ := startCountingEngine(t)
	localGC := NewGraphClientForURL(localURL)
	store := newFakeAuthStore() // empty initially → not logged in
	as := auth.NewAuthState(store, time.Millisecond)
	r := NewRouter(localGC, cloudURL, staticTokenSource{tok: "tok"}, as)

	ctx := context.Background()

	// Not logged in → IngestClient resolves the local backend's ingest client.
	ic, err := r.IngestClient(ctx)
	require.NoError(t, err)
	assert.Same(t, localGC.IngestClient(), ic, "IngestClient must return the local backend's IngestService client when not logged in")

	// User runs `knowledge login` → keychain populated; wait past TTL.
	require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-fresh"))
	time.Sleep(50 * time.Millisecond)

	// Logged in → IngestClient resolves the cloud backend's ingest client.
	ic2, err := r.IngestClient(ctx)
	require.NoError(t, err)
	r.mu.Lock()
	cloudGC := r.cloud
	r.mu.Unlock()
	require.NotNil(t, cloudGC, "cloud client must be built after login")
	assert.Same(t, cloudGC.IngestClient(), ic2, "IngestClient must return the cloud backend's IngestService client when logged in")
	assert.NotSame(t, ic, ic2, "the picked ingest client must differ across the login flip")
}

// TestRouter_Backend_RoutesByLogin: Router.Backend(ctx) reuses pick(ctx), so it
// returns the LOCAL *GraphClient when not logged in and the CLOUD *GraphClient
// when logged in, re-picking per call across a mid-session login flip — the
// concrete-backend resolver the pipeline binds per collector.
func TestRouter_Backend_RoutesByLogin(t *testing.T) {
	localURL, _ := startCountingEngine(t)
	cloudURL, _ := startCountingEngine(t)
	localGC := NewGraphClientForURL(localURL)
	store := newFakeAuthStore() // empty initially → not logged in
	as := auth.NewAuthState(store, time.Millisecond)
	r := NewRouter(localGC, cloudURL, staticTokenSource{tok: "tok"}, as)

	ctx := context.Background()

	// Not logged in → Backend resolves the local *GraphClient.
	be, err := r.Backend(ctx)
	require.NoError(t, err)
	assert.Same(t, localGC, be, "Backend must return the local *GraphClient when not logged in")

	// User runs `knowledge login` → keychain populated; wait past TTL.
	require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-fresh"))
	time.Sleep(50 * time.Millisecond)

	// Logged in → Backend resolves the cloud *GraphClient.
	be2, err := r.Backend(ctx)
	require.NoError(t, err)
	r.mu.Lock()
	cloudGC := r.cloud
	r.mu.Unlock()
	require.NotNil(t, cloudGC, "cloud client must be built after login")
	assert.Same(t, cloudGC, be2, "Backend must return the cloud *GraphClient when logged in")
	assert.NotSame(t, be, be2, "the picked backend must differ across the login flip")
}

// TestRouter_Backend_NoBackend: local=nil + not logged in → Backend returns
// ErrNoBackend (the pipeline collector treats this as "no scan this tick").
func TestRouter_Backend_NoBackend(t *testing.T) {
	store := newFakeAuthStore()
	as := auth.NewAuthState(store, time.Hour)
	r := NewRouter(nil, "http://cloud.invalid", staticTokenSource{}, as)

	_, err := r.Backend(context.Background())
	require.ErrorIs(t, err, ErrNoBackend)
}

// TestRouter_Forwarders_RouteAtCallTime — table test across all 5 forwarder
// methods proving per-call pick() dispatch. AuthState=false first; flip to
// logged in and assert each method routes to cloud thereafter.
//
// Reviewer T1 regression guard: if a forwarder closed over a constructed-time
// backend rather than calling r.pick(ctx), the mid-session swap would not
// land and one server would receive all calls.
func TestRouter_Forwarders_RouteAtCallTime(t *testing.T) {
	localURL, localEng := startCountingEngine(t)
	cloudURL, cloudEng := startCountingEngine(t)
	localGC := NewGraphClientForURL(localURL)
	store := newFakeAuthStore()
	as := auth.NewAuthState(store, time.Millisecond)
	r := NewRouter(localGC, cloudURL, staticTokenSource{tok: "tok"}, as)

	ctx := context.Background()

	// Per-forwarder (name → invoke → readers).
	type forwarder struct {
		name        string
		call        func() error
		localGetter func() int32
		cloudGetter func() int32
	}
	fws := []forwarder{
		{
			name: "Execute",
			call: func() error {
				_, err := r.Execute(ctx, &knowledgev1.ExecuteRequest{})
				return err
			},
			localGetter: func() int32 { return localEng.execute.Load() },
			cloudGetter: func() int32 { return cloudEng.execute.Load() },
		},
		{
			name: "Index",
			call: func() error {
				_, err := r.Index(ctx, &knowledgev1.IndexRequest{})
				return err
			},
			localGetter: func() int32 { return localEng.index.Load() },
			cloudGetter: func() int32 { return cloudEng.index.Load() },
		},
		{
			name: "MetadataStats",
			call: func() error {
				_, err := r.MetadataStats(ctx, &knowledgev1.MetadataStatsRequest{})
				return err
			},
			localGetter: func() int32 { return localEng.metadataStats.Load() },
			cloudGetter: func() int32 { return cloudEng.metadataStats.Load() },
		},
		{
			name: "ExportGraph",
			call: func() error {
				_, err := r.ExportGraph(ctx, &knowledgev1.ExportGraphRequest{})
				return err
			},
			localGetter: func() int32 { return localEng.exportGraph.Load() },
			cloudGetter: func() int32 { return cloudEng.exportGraph.Load() },
		},
		{
			name: "Stats",
			call: func() error {
				_, err := r.Stats(ctx, &knowledgev1.StatsRequest{})
				return err
			},
			localGetter: func() int32 { return localEng.stats.Load() },
			cloudGetter: func() int32 { return cloudEng.stats.Load() },
		},
	}

	// Phase 1: AuthState=false → every forwarder routes to local.
	for _, f := range fws {
		t.Run(f.name+"_local_under_no_auth", func(t *testing.T) {
			before := f.localGetter()
			require.NoError(t, f.call())
			assert.Equal(t, before+1, f.localGetter(), "%s: local should advance", f.name)
			assert.Equal(t, int32(0), f.cloudGetter(), "%s: cloud should stay at 0", f.name)
		})
	}

	// Login: populate the keychain, wait past TTL so the next IsLoggedIn re-reads.
	require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-fresh"))
	time.Sleep(50 * time.Millisecond)

	// Phase 2: AuthState=true → every forwarder routes to cloud. Local stays
	// at the prior count (1 each from Phase 1).
	for _, f := range fws {
		t.Run(f.name+"_cloud_under_auth", func(t *testing.T) {
			prevLocal := f.localGetter()
			before := f.cloudGetter()
			require.NoError(t, f.call())
			assert.Equal(t, before+1, f.cloudGetter(), "%s: cloud should advance", f.name)
			assert.Equal(t, prevLocal, f.localGetter(), "%s: local count must NOT advance after auth flip", f.name)
		})
	}
}
