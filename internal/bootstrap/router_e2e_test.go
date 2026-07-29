// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
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
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// fakeAuthStore is the e2e test's keychain stand-in. Mirrors the testStore
// fixture shape in cmd/knowledge/internal/auth/state_test.go::fakeAuthStore
// (kept inline here so this bootstrap test does not depend on auth's test
// fixtures). Set/Delete are concurrency-safe so the mid-session-login
// subtest can flip state from outside the AuthState's IsLoggedIn caller.
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

// staticTokenSource is a non-refreshing TokenSource for the cloud-routed
// subtests. Mirrors the router_test.go staticTokenSource shape.
type staticTokenSource struct{ tok string }

func (s staticTokenSource) Token(_ context.Context) (string, auth.PermissionSet, error) {
	return s.tok, nil, nil
}

// countingEngine is an EngineService handler that records hits per RPC kind
// on atomic counters. Each e2e fixture stands up two of these (local +
// cloud) so each subtest asserts which backend serviced a given call. The
// canned ExecuteResponse carries one search hit so the dispatcher's render
// pipeline produces real LLM-facing output for the body assertions.
type countingEngine struct {
	execute atomic.Int32
}

func (e *countingEngine) Execute(
	_ context.Context,
	_ *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	e.execute.Add(1)
	return connect.NewResponse(&knowledgev1.ExecuteResponse{
		SearchResults: []*knowledgev1.HydratedResult{
			{Score: 0.9, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit"}},
		},
	}), nil
}

func (e *countingEngine) Stats(
	context.Context, *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	return connect.NewResponse(&knowledgev1.StatsResponse{}), nil
}

func (e *countingEngine) MetadataStats(
	context.Context, *connect.Request[knowledgev1.MetadataStatsRequest],
) (*connect.Response[knowledgev1.MetadataStatsResponse], error) {
	return connect.NewResponse(&knowledgev1.MetadataStatsResponse{}), nil
}

func (e *countingEngine) Index(
	context.Context, *connect.Request[knowledgev1.IndexRequest],
) (*connect.Response[knowledgev1.IndexResponse], error) {
	return connect.NewResponse(&knowledgev1.IndexResponse{}), nil
}

func (e *countingEngine) Hive(
	context.Context, *connect.Request[knowledgev1.HiveRequest],
) (*connect.Response[knowledgev1.HiveResponse], error) {
	return connect.NewResponse(&knowledgev1.HiveResponse{}), nil
}

func (e *countingEngine) PipelineScan(
	context.Context, *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	return connect.NewResponse(&knowledgev1.PipelineScanResponse{}), nil
}

func (e *countingEngine) PipelineGenPoll(
	context.Context, *connect.Request[knowledgev1.PipelineGenPollRequest],
) (*connect.Response[knowledgev1.PipelineGenPollResponse], error) {
	return connect.NewResponse(&knowledgev1.PipelineGenPollResponse{}), nil
}

func (e *countingEngine) CorpusDelta(
	context.Context, *connect.Request[knowledgev1.CorpusDeltaRequest],
) (*connect.Response[knowledgev1.CorpusDeltaResponse], error) {
	return connect.NewResponse(&knowledgev1.CorpusDeltaResponse{}), nil
}

func (e *countingEngine) ExportGraph(
	context.Context, *connect.Request[knowledgev1.ExportGraphRequest],
) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	return connect.NewResponse(&knowledgev1.ExportGraphResponse{}), nil
}

func (e *countingEngine) OverwriteGraph(
	context.Context, *connect.Request[knowledgev1.OverwriteGraphRequest],
) (*connect.Response[knowledgev1.OverwriteGraphResponse], error) {
	return connect.NewResponse(&knowledgev1.OverwriteGraphResponse{}), nil
}

// startCountingEngine stands up an h2c httptest.Server in front of a
// countingEngine handler. Returns the server URL and the engine pointer so
// the subtest reads the hit counter. Mirrors graphclient/router_test.go's
// startCountingEngine, copied locally so the bootstrap test does not depend
// on graphclient's test fixtures.
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

// buildE2EClient builds a minimal *client with the routing layer
// wired: optional local *GraphClient, cloudURL, fakeAuthStore-backed
// AuthState. Bypasses constructClient deliberately — constructClient also
// fires DefaultSinkFactory mutation, StartKeepalive (goroutine), and
// workerCRUD wiring, none of which are needed to exercise the dispatcher's
// router-routing path. The test seam is the same engineDispatch closure
// production hits.
func buildE2EClient(local *graphclient.GraphClient, cloudURL string, store auth.Store, ttl time.Duration) *client {
	authState := auth.NewAuthState(store, ttl)
	router := graphclient.NewRouter(local, cloudURL, staticTokenSource{tok: "tok-cloud"}, authState)
	return &client{
		local:     local,
		router:    router,
		authState: authState,
	}
}

// buildE2EClientMachineAuth mirrors buildE2EClient but wires the Router via
// NewRouterWithMachineAuth(..., machineAuth=true) — the exact constructor shape
// constructClient uses for a machine-token client. The cloudURL is an injectable
// test stub here ONLY because production pins it to the canonical endpoint;
// the wiring (StaticTokenSource bearer + machineAuth selection) is identical.
func buildE2EClientMachineAuth(local *graphclient.GraphClient, cloudURL string, store auth.Store) *client {
	authState := auth.NewAuthState(store, time.Hour)
	router := graphclient.NewRouterWithMachineAuth(
		local, cloudURL, auth.StaticTokenSource{AccessToken: "machine-tok"}, authState, true)
	return &client{
		local:     local,
		router:    router,
		authState: authState,
	}
}

// TestRouterE2E_FourStates_PlusSwapAndSyncAndUnreachable exercises the
// dispatcher path END-TO-END through c.engineDispatch (the same
// closure production threads MCP tool calls through). The 7 subtests:
//
//  1. NoLocal + NoAuth — engineDispatch surfaces ErrNoBackend rendered as the
//     install-or-login message (no "engine:" / "connect:" leak).
//  2. LocalOnly + NoAuth — engineDispatch lands on the local httptest.
//  3. Local + Auth — engineDispatch lands on the cloud httptest.
//  4. NoLocal + Auth — engineDispatch lands on the cloud httptest.
//  5. MidSessionLogin — ttl=1ms, first dispatch routes local; flip token in
//     store; sleep > ttl; second dispatch routes cloud.
//  6. LocalWithAuth + SyncStaysLocal — drive a call through c.LocalGraphCaller()
//     (the sync push / post-collect carve) and confirm it lands on local
//     even while authState reports logged-in.
//  7. LocalUnreachable + NoAuth — local *GraphClient pointed at a reserved
//     port (always-refused). engineDispatch surfaces the rendered
//     "local server unreachable" message (the renderEngineError Branch 2
//     transport-unreachable path), not a raw "connect:" / "dial tcp" leak.
func TestRouterE2E_FourStates_PlusSwapAndSyncAndUnreachable(t *testing.T) {
	ctx := opCtx()
	searchArgs := json.RawMessage(`{"query":"x","graph":"knowledge"}`)

	t.Run("NoLocal_NoAuth_RendersInstallOrLogin", func(t *testing.T) {
		store := newFakeAuthStore() // empty → not logged in
		c := buildE2EClient(nil, "http://cloud.invalid", store, time.Hour)

		out, err := c.engineDispatch(ctx, "search", searchArgs)
		require.NoError(t, err, "the no-backend case is rendered as an error result, not a returned error")
		require.True(t, out.IsError, "no-backend surfaces as an error tool result")
		body := out.Content[0].Text
		assert.Contains(t, body, "no backend available")
		assert.Contains(t, body, "knowledge install")
		assert.Contains(t, body, "knowledge login")
		assert.NotContains(t, body, "engine:", "no leaked engine: prefix")
		assert.NotContains(t, body, "connect:", "no leaked connect: prefix")
	})

	t.Run("LocalOnly_NoAuth_RoutesLocal", func(t *testing.T) {
		localURL, localEng := startCountingEngine(t)
		cloudURL, cloudEng := startCountingEngine(t)
		localGC := graphclient.NewGraphClientForURL(localURL)
		store := newFakeAuthStore() // empty
		c := buildE2EClient(localGC, cloudURL, store, time.Hour)

		_, err := c.engineDispatch(ctx, "search", searchArgs)
		require.NoError(t, err)
		assert.Equal(t, int32(1), localEng.execute.Load(), "logged-out + local present routes local")
		assert.Equal(t, int32(0), cloudEng.execute.Load(), "cloud must not be hit when logged out")
	})

	t.Run("Local_Auth_RoutesCloud", func(t *testing.T) {
		localURL, localEng := startCountingEngine(t)
		cloudURL, cloudEng := startCountingEngine(t)
		localGC := graphclient.NewGraphClientForURL(localURL)
		store := newFakeAuthStore()
		require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-stub"))
		c := buildE2EClient(localGC, cloudURL, store, time.Hour)

		_, err := c.engineDispatch(ctx, "search", searchArgs)
		require.NoError(t, err)
		assert.Equal(t, int32(0), localEng.execute.Load(), "local must not be hit when logged in")
		assert.Equal(t, int32(1), cloudEng.execute.Load(), "logged-in + local present routes cloud")
	})

	t.Run("NoLocal_Auth_RoutesCloud", func(t *testing.T) {
		cloudURL, cloudEng := startCountingEngine(t)
		store := newFakeAuthStore()
		require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-stub"))
		c := buildE2EClient(nil, cloudURL, store, time.Hour)

		_, err := c.engineDispatch(ctx, "search", searchArgs)
		require.NoError(t, err)
		assert.Equal(t, int32(1), cloudEng.execute.Load(), "cloud-first logged-in routes cloud")
	})

	t.Run("MidSessionLoginSwap_LocalToCloudAfterTTL", func(t *testing.T) {
		localURL, localEng := startCountingEngine(t)
		cloudURL, cloudEng := startCountingEngine(t)
		localGC := graphclient.NewGraphClientForURL(localURL)
		store := newFakeAuthStore() // start logged out
		c := buildE2EClient(localGC, cloudURL, store, time.Millisecond)

		// First dispatch: routes local (logged out).
		_, err := c.engineDispatch(ctx, "search", searchArgs)
		require.NoError(t, err)
		assert.Equal(t, int32(1), localEng.execute.Load(), "first dispatch routes local")
		assert.Equal(t, int32(0), cloudEng.execute.Load())

		// User runs `knowledge login` mid-MCP-session — refresh token appears.
		require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-fresh"))
		// Wait past the AuthState TTL so the cached "logged out" expires.
		time.Sleep(50 * time.Millisecond)

		// Second dispatch: AuthState re-checks, sees the token, routes cloud.
		_, err = c.engineDispatch(ctx, "search", searchArgs)
		require.NoError(t, err)
		assert.Equal(t, int32(1), localEng.execute.Load(), "local count must not advance after login")
		assert.Equal(t, int32(1), cloudEng.execute.Load(), "second dispatch routes cloud after TTL expiry")
	})

	t.Run("LocalWithAuth_SyncStaysLocal", func(t *testing.T) {
		// Carve: sync push, post-collect linker, post-collect
		// postpopulate explicitly read+write the LOCAL graph (the
		// destination is cloud via auth.Transport for sync push, and the
		// local-only graph for the other two). They use
		// deps.LocalGraphCaller() instead of deps.GraphCaller() so the
		// router never sends them cloudward even when logged in.
		// This subtest proves the LocalGraphCaller() accessor stays local
		// when AuthState=true, mirroring the carve done in
		// tools/intercept_sync.go::exporterSeam.
		localURL, localEng := startCountingEngine(t)
		cloudURL, cloudEng := startCountingEngine(t)
		localGC := graphclient.NewGraphClientForURL(localURL)
		store := newFakeAuthStore()
		require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-stub"))
		c := buildE2EClient(localGC, cloudURL, store, time.Hour)

		// Sanity: the routed GraphCaller would route cloud here.
		// LocalGraphCaller MUST NOT.
		lgc := c.LocalGraphCaller()
		require.NotNil(t, lgc, "LocalGraphCaller must be non-nil when local is wired")
		_, err := lgc.Execute(ctx, &knowledgev1.ExecuteRequest{})
		require.NoError(t, err)
		assert.Equal(t, int32(1), localEng.execute.Load(), "LocalGraphCaller routes local even when AuthState=true")
		assert.Equal(t, int32(0), cloudEng.execute.Load(), "LocalGraphCaller never routes cloud")
	})

	t.Run("LocalUnreachable_NoAuth_RendersRestartHint", func(t *testing.T) {
		// Reserved port 1 — refused on darwin/linux. The Router picks local
		// (AuthState=false + local non-nil), the dial fails, the dispatcher
		// renders the actionable "local server unreachable" message via
		// renderEngineError's Branch 2 (transport-unreachable).
		localGC := graphclient.NewGraphClientForURL("http://127.0.0.1:1")
		store := newFakeAuthStore() // empty → not logged in
		c := buildE2EClient(localGC, "http://cloud.invalid", store, time.Hour)

		out, err := c.engineDispatch(ctx, "search", searchArgs)
		require.NoError(t, err, "the unreachable case is rendered, not returned")
		require.True(t, out.IsError)
		body := out.Content[0].Text
		assert.Contains(t, body, "local server unreachable")
		assert.Contains(t, body, "knowledge start")
		assert.Contains(t, body, "knowledge login")
		assert.NotContains(t, body, "engine:", "no leaked engine: prefix")
		assert.NotContains(t, body, "dial tcp", "no leaked dial tcp leak")
		assert.NotContains(t, body, "connection refused", "no leaked connect: connection refused leak")
	})
}

// TestConstructClient_Coexistence proves the two auth paths coexist end-to-end:
// the headless machine-token path selects cloud with NO keychain, and the
// interactive (keychain login) path is unchanged by the feature. The cloud
// endpoint is the canonical pinned cli.CloudEndpoint in every case — there is
// no override to vary; the cloud-reaching subtest injects a stub cloudURL only
// through the test seam, with the production wiring shape preserved.
func TestConstructClient_Coexistence(t *testing.T) {
	ctx := opCtx()

	// withConstructClientSeams overrides the keychain store + keepalive package
	// vars so constructClient never touches the real keychain or dials loopback,
	// then builds a *client via the production constructor. Mirrors
	// client_keepalive_gate_test.go.
	withConstructClientSeams := func(t *testing.T, cfg Config, store auth.Store) *client {
		t.Helper()
		cfg.LocalDialer = func(int) *graphclient.GraphClient {
			return graphclient.NewGraphClientForURL("http://local.invalid")
		}
		origStore := newAuthStoreFn
		newAuthStoreFn = func() (auth.Store, error) { return store, nil }
		t.Cleanup(func() { newAuthStoreFn = origStore })
		origKeepalive := startKeepaliveFn
		startKeepaliveFn = func(_ *graphclient.GraphClient, _ context.Context) {}
		t.Cleanup(func() { startKeepaliveFn = origKeepalive })
		c := constructClient(cfg)
		require.NotNil(t, c)
		return c
	}

	t.Run("MachineToken_SelectsCloud_NoKeychain", func(t *testing.T) {
		// Through the real constructor: a machine-token Config over an empty
		// keychain reports LoggedIn==true (cloud selected without any login).
		c := withConstructClientSeams(t, Config{AuthToken: "machine-tok"}, newFakeAuthStore())
		assert.True(t, c.router.LoggedIn(ctx),
			"machine-token config must select cloud (LoggedIn==true) over an empty keychain")
	})

	t.Run("MachineToken_ExecuteReachesCloudStub", func(t *testing.T) {
		// The wiring constructClient produces (StaticTokenSource bearer +
		// machineAuth) routed against a cloud stub: Execute lands on cloud, the
		// local engine stays at 0, with an empty keychain.
		localURL, localEng := startCountingEngine(t)
		cloudURL, cloudEng := startCountingEngine(t)
		localGC := graphclient.NewGraphClientForURL(localURL)
		c := buildE2EClientMachineAuth(localGC, cloudURL, newFakeAuthStore()) // empty keychain

		searchArgs := json.RawMessage(`{"query":"x","graph":"knowledge"}`)
		_, err := c.engineDispatch(ctx, "search", searchArgs)
		require.NoError(t, err)
		assert.Equal(t, int32(0), localEng.execute.Load(), "machine-auth must not route to local")
		assert.Equal(t, int32(1), cloudEng.execute.Load(), "machine-auth Execute reaches the cloud stub with no keychain")
	})

	t.Run("NoToken_SeededKeychain_SelectsCloud", func(t *testing.T) {
		// Interactive path unchanged: no machine token, keychain seeded with a
		// refresh token → LoggedIn==true (WorkOS selection retained).
		store := newFakeAuthStore()
		require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-stub"))
		c := withConstructClientSeams(t, Config{}, store)
		assert.True(t, c.router.LoggedIn(ctx),
			"no-token config with a seeded keychain must follow the unchanged login selection (LoggedIn==true)")
	})

	t.Run("NoToken_EmptyKeychain_SelectsLocal", func(t *testing.T) {
		// Interactive path unchanged: no machine token, empty keychain →
		// LoggedIn==false (routes local).
		c := withConstructClientSeams(t, Config{}, newFakeAuthStore())
		assert.False(t, c.router.LoggedIn(ctx),
			"no-token config with an empty keychain selects local (LoggedIn==false)")
	})
}
