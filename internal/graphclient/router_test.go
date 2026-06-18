// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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

// TestRouter_MachineAuth_RoutesCloudWithoutLogin proves the headless
// machine-auth path: a Router built with machineAuth=true routes to cloud
// EVEN WITH an empty keychain (never logged in). This is the selection rule
// for a client started with a machine bearer token — it runs cloud-only with
// no keychain involvement.
func TestRouter_MachineAuth_RoutesCloudWithoutLogin(t *testing.T) {
	localURL, localEng := startCountingEngine(t)
	cloudURL, cloudEng := startCountingEngine(t)
	localGC := NewGraphClientForURL(localURL)
	store := newFakeAuthStore() // empty → NOT logged in
	as := auth.NewAuthState(store, time.Hour)
	r := NewRouterWithMachineAuth(localGC, cloudURL, staticTokenSource{tok: "machine-tok"}, as, true)

	_, err := r.Execute(context.Background(), &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(0), localEng.execute.Load(), "machine-auth must not route to local")
	assert.Equal(t, int32(1), cloudEng.execute.Load(), "machine-auth routes cloud without keychain login")
	assert.True(t, r.LoggedIn(context.Background()), "machine-auth must report LoggedIn==true (cloud-only, no local)")
}

// TestRouter_MachineAuthFalse_UnchangedSelection is the regression guard that
// the machineAuth OR did not change the default: machineAuth=false over an
// empty (not-logged-in) store still routes local, exactly as before.
func TestRouter_MachineAuthFalse_UnchangedSelection(t *testing.T) {
	localURL, localEng := startCountingEngine(t)
	cloudURL, cloudEng := startCountingEngine(t)
	localGC := NewGraphClientForURL(localURL)
	store := newFakeAuthStore() // empty → not logged in
	as := auth.NewAuthState(store, time.Hour)
	r := NewRouterWithMachineAuth(localGC, cloudURL, staticTokenSource{}, as, false)

	_, err := r.Execute(context.Background(), &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), localEng.execute.Load(), "machineAuth=false + empty store still routes local")
	assert.Equal(t, int32(0), cloudEng.execute.Load(), "cloud must not be hit without auth")
	assert.False(t, r.LoggedIn(context.Background()), "machineAuth=false + empty store reports LoggedIn==false")
}
