// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/cli"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/graphtypecrud"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/workercrud"
)

// client_construct.go holds constructClient (the *client builder) and its two
// test seams. Split out of client.go to keep that file under the file-length cap.

// newAuthStoreFn is the keychain Store constructor used by constructClient.
// Tests override it to inject an in-memory store (e.g. a logged-in fake) and
// avoid touching the real platform keychain. Mirrors the cli.go newStoreFn
// seam. Production callers leave the default in place so auth.NewStore runs.
var newAuthStoreFn = auth.NewStore

// startKeepaliveFn is a method-expression seam over
// (*graphclient.GraphClient).StartKeepalive so tests can observe whether
// constructClient launched the keepalive goroutine without driving a real
// loopback dial. Production callers leave the default in place. Gating the call
// on not-LoggedIn (a logged-in daemon routes cloud and must never probe :15022)
// is what this seam lets a test assert.
var startKeepaliveFn = (*graphclient.GraphClient).StartKeepalive

// constructClient builds a stdio client that proxies to the graph server.
// It does NOT open any .bin file and does NOT register key fragments —
// the server binary owns graph storage. The sink is a RemoteUploadSink
// over the IngestService client so local collection streams chunks to the
// server.
//
// Also launches the background keepalive goroutine that surfaces server
// drops to slog between user-triggered tool calls — but ONLY when not logged
// in. A logged-in daemon routes every op to cloud and operates with no local
// knowledge-server, so it must never probe :15022; the keepalive is gated off
// for it. The unary reconnect interceptor redials transparently when the next
// real request lands — keepalive is operator visibility, not recovery.
//
// Router wiring: builds the auth.AuthState backed by the platform keychain
// Store (or a no-op stub on platforms where the keychain is not implemented —
// Windows) and the bare keychain OAuth TokenSource, then wraps the local
// *GraphClient in a *graphclient.Router. Every routed GraphCaller() call
// dispatches per-call on the keychain auth state: cached IsLoggedIn=true →
// cloud; false → local; neither → ErrNoBackend.
func constructClient(f Config) *client {
	dialLocal := f.LocalDialer
	if dialLocal == nil {
		dialLocal = graphclient.NewGraphClient
	}
	tcp := dialLocal(f.Port)

	// Build the auth Store. ErrNotImplementedOS (Windows) is non-fatal:
	// substitute a no-op Store so AuthState always returns false and the
	// Router falls through to local. Other errors are also non-fatal —
	// degrade to no-op so the local path still works.
	authStore, storeErr := newAuthStoreFn()
	if storeErr != nil {
		authStore = noopAuthStore{}
	}
	// TokenSource selection — the coexistence rule for the two auth paths:
	//   - machine token present (--auth-token / KNOWLEDGE_AUTH_TOKEN) → a
	//     zero-IO StaticTokenSource bearing the caller-supplied opaque token.
	//     No browser login, no keychain refresh. Permissions are left nil: the
	//     token is opaque to the client and the backend enforces its scopes.
	//   - no machine token → the interactive keychain OAuth source, which mints
	//     fresh access tokens on demand from the `knowledge login` refresh token.
	machineAuth := f.AuthToken != ""
	var tokenSource auth.TokenSource
	if machineAuth {
		tokenSource = auth.StaticTokenSource{AccessToken: f.AuthToken}
	} else {
		tokenSource = auth.NewOAuthTokenSource(
			authStore,
			cli.CloudEndpoint,
			cli.AllowedAuthHosts(),
		)
	}
	authState := auth.NewAuthState(authStore, 0)
	// machineAuth forces cloud selection without keychain involvement: a
	// machine-token client routes every op to cloud and runs with no local
	// server. The endpoint is always the canonical pinned cloud endpoint — never
	// overridden; in-cluster routing is handled by infrastructure, out of scope.
	router := graphclient.NewRouterWithMachineAuth(tcp, cli.CloudEndpoint, tokenSource, authState, machineAuth)

	c := &client{
		rootDir:   f.RootDir,
		port:      f.Port,
		version:   Version,
		local:     tcp,
		router:    router,
		authState: authState,
		// claimRegistry is created here so ClaimRegistry() (consumed by
		// InterceptHive) and the daemon Monitor (wired in runServe) share the
		// SAME instance — the claims InterceptHive records are the ones the
		// Monitor renews.
		claimRegistry: hivemonitor.NewRegistry(),
		// banSet is created here so the InterceptHive ban gate (via BanSet())
		// and the daemon Monitor (which records mcp→harness resolutions and bans
		// cloud-evicted members) share the SAME instance.
		banSet: hivemonitor.NewBanSet(),
	}
	// Per-call login-aware routing: the sink re-picks the IngestService
	// backend on every CollectChunk/Finalize/FetchCloudSubgraph via the
	// Router (cloud when logged in, local otherwise), so a mid-session
	// `knowledge login` flip routes the next collect to cloud without a
	// restart. Do NOT capture a fixed tcp.IngestClient() here.
	c.sink = remote.NewUploadSinkFunc(c.router.IngestClient)
	// Wire the worker CRUD client through the login-aware Router so a
	// logged-in (cloud-only, no local server) daemon serves worker CRUD
	// from cloud instead of dialing :15022. Every CRUD call rides the
	// Router's per-call Execute dispatch (cloud when logged in, local
	// otherwise) so the active backend stays the source of truth for
	// graph-resident NodeWorker rows.
	c.workerCRUD = workercrud.New(c.router)
	// Same login-aware Router routing for graph-resident NodeGraphTypeDef rows.
	c.graphTypeCRUD = graphtypecrud.New(c.router)
	// Install as the process-wide default factory too so call-sites that
	// don't route through c.sink (e.g. codegraph.Sync in an intercept
	// handler that forgets to set opts.Sink) still get the remote sink.
	collector.DefaultSinkFactory = func() collector.Sink { return c.sink }
	// Periodic local-server drop detection — but ONLY for a not-logged-in
	// client. A logged-in daemon routes every op to cloud and runs with no local
	// knowledge-server, so probing :15022 would only emit escalating ERROR-log
	// spam for a server intentionally not running; gate it off. Routed through
	// the startKeepaliveFn seam so test (b) can assert the gate without a real
	// loopback dial. Test harnesses that build a *client directly (without this
	// constructor) skip this — nothing else in the codebase calls StartKeepalive.
	if !c.router.LoggedIn(context.Background()) {
		startKeepaliveFn(tcp, context.Background())
	}
	return c
}
