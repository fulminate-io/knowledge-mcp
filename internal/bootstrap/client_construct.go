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
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// client_construct.go holds constructClient (the *client builder) and its two
// test seams. Split out of client.go to keep that file under the file-length cap.

// newAuthStoreFn is the credential Store constructor used by constructClient.
// Tests override it to inject an in-memory store (e.g. a logged-in fake) and
// avoid touching the real platform keychain. Mirrors the cli.go newStoreFn
// seam. Production callers leave the default in place so auth.OpenStore runs,
// which prefers the platform keychain and falls back to the
// ~/.knowledge/credentials file only when the keychain is provably
// unreachable.
var newAuthStoreFn = auth.OpenStore

// startKeepaliveFn is a method-expression seam over
// (*graphclient.GraphClient).StartKeepalive so tests can observe whether
// constructClient launched the keepalive goroutine without driving a real
// loopback dial. Production callers leave the default in place. Gating the call
// on not-LoggedIn (a logged-in daemon routes cloud and must never probe :15022)
// is what this seam lets a test assert.
var startKeepaliveFn = (*graphclient.GraphClient).StartKeepalive

// selectAuthSources resolves the auth Store, TokenSource, and the machineAuth
// cloud-selection bool that constructClient threads into the Router. It is the
// single place the three Router.pick (router.go:173) inputs are decided.
//
// Four mutually exclusive paths, in precedence order:
//   - --no-auth (fail-closed local-only floor): forces BOTH cloud triggers off
//     in one chokepoint. machineAuth is false WITHOUT consulting f.AuthToken (a
//     present --auth-token / KNOWLEDGE_AUTH_TOKEN cannot re-enable cloud), and
//     the keychain is replaced with noopAuthStore{} (Get→ErrNotFound) so
//     AuthState.IsLoggedIn==false even with a live `knowledge login` refresh
//     token present. tokenSource is the zero-IO StaticTokenSource{} (empty
//     bearer) — never the OAuth source, so no keychain refresh is attempted.
//     Result: no routed op can reach a fulminate.io host regardless of
//     credentials. (The cloud endpoint is still passed UNCHANGED by the caller;
//     --no-auth is a capability reduction, never a host override.)
//   - machine token present (--auth-token / KNOWLEDGE_AUTH_TOKEN): a zero-IO
//     StaticTokenSource bearing the caller-supplied opaque token, machineAuth
//     true. No browser login, no keychain refresh. Permissions are left nil —
//     the token is opaque to the client and the backend enforces its scopes.
//     It outranks the read-only lever below: a caller who supplied their own
//     credential is not reaching for the operator's.
//   - read-only credential lever engaged (auth.CredentialStoreReadOnlyEnv): the
//     read-only source over the session the owning process published,
//     machineAuth false. Never the OAuth source — refreshing rotates the
//     refresh token, and persisting the replacement is precisely what the
//     lever forbids, so a refresh here could only end by stranding every other
//     process on a consumed credential. Such a process must use an existing
//     session or fail, never re-authenticate.
//   - no machine token: the interactive keychain OAuth source, machineAuth
//     false. Cloud selection then follows the keychain login state.
//
// Store construction errors (ErrNotImplementedOS on Windows, or any transient
// failure) are non-fatal: degrade to noopAuthStore{} so AuthState reports
// IsLoggedIn==false and the Router falls through to local.
func selectAuthSources(f Config) (auth.Store, auth.TokenSource, bool) {
	if f.NoAuth {
		return noopAuthStore{}, auth.StaticTokenSource{}, false
	}
	authStore, storeErr := newAuthStoreFn()
	if storeErr != nil {
		authStore = noopAuthStore{}
	}
	if f.AuthToken != "" {
		return authStore, auth.StaticTokenSource{AccessToken: f.AuthToken}, true
	}
	if auth.CredentialStoreIsReadOnly() {
		return authStore, auth.NewReadOnlyTokenSource(authStore), false
	}
	tokenSource := auth.NewOAuthTokenSource(
		authStore,
		cli.CloudEndpoint,
		cli.AllowedAuthHosts(),
	)
	return authStore, tokenSource, false
}

// constructClient builds the client state that proxies to the graph server.
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

	// Resolve the three Router.pick (router.go:173) inputs — auth Store,
	// TokenSource, and the machineAuth cloud-selection bool. selectAuthSources
	// is the single decision point; under --no-auth it returns the noop store +
	// empty StaticTokenSource + machineAuth=false, forcing pick() local-only
	// regardless of any credential present (fail-closed). See its doc-comment.
	authStore, tokenSource, machineAuth := selectAuthSources(f)
	authState := auth.NewAuthState(authStore, 0)
	// machineAuth forces cloud selection without keychain involvement: a
	// machine-token client routes every op to cloud and runs with no local
	// server. The endpoint is always the canonical pinned cloud endpoint — never
	// overridden; in-cluster routing is handled by infrastructure, out of scope.
	// Under --no-auth machineAuth is false and authStore is the noop store, so
	// pick() always returns the local *GraphClient.
	router := graphclient.NewRouterWithMachineAuth(tcp, cli.CloudEndpoint, tokenSource, authState, machineAuth)

	c := &client{
		rootDir:    f.RootDir,
		rootDirSet: f.RootDirSet,
		port:       f.Port,
		version:    Version,
		local:      tcp,
		router:     router,
		authState:  authState,
		// Retain the ONE token source selectAuthSources just built for the
		// Router so the segment/sync/transcript control transports share it
		// (via buildCloudSyncTransport) instead of each minting a fresh cold
		// keychain source. This is also what carries the resolved
		// Config.AuthToken (flag/config) into those transports.
		cloudTokenSource: tokenSource,
		// collectRuntime is constructed EARLY and unconditionally (zero
		// dependencies — no router/pipeline) so a detached collect always has a
		// runtime to launch under once it passes the PipelineReady gate.
		collectRuntime: tools.NewCollectRuntime(),
		// workingSet is constructed EARLY and unconditionally for the same
		// reason: it is the gate every background loop consults, and a
		// consumer wired before it existed would read nil (EMPTY) forever.
		workingSet: workingset.New(),
	}
	// Record every direct user interaction with a concrete graph instance. The
	// recorder sits on Router.Execute so every routed call is judged by the same
	// (operation, instance) rule; the Router keeps returning c.router from
	// GraphCaller(), so no type-assertion seam is disturbed.
	c.router.AttachWorkingSet(c.AdmitGraph)
	// Per-call login-aware routing: the sink re-picks the IngestService
	// backend on every CollectChunk/Finalize/FetchCloudSubgraph via the
	// Router (cloud when logged in, local otherwise), so a mid-session
	// `knowledge login` flip routes the next collect to cloud without a
	// restart. Do NOT capture a fixed tcp.IngestClient() here.
	//
	// It is WRAPPED so a collect of any graph family admits the graph it just
	// produced. DefaultSinkFactory below closes over c.sink, so wrapping here
	// covers the handler that forgets opts.Sink too.
	c.sink = admittingSink{
		inner: remote.NewUploadSinkFunc(c.router.IngestClient),
		admit: func(gt kgtypes.GraphType, name string) { c.AdmitGraph(gt, name, "collect") },
	}
	// Wire the graph-type CRUD client through the login-aware Router so a
	// logged-in (cloud-only, no local server) daemon serves graph-type CRUD
	// from cloud instead of dialing :15022. Every CRUD call rides the
	// Router's per-call Execute dispatch (cloud when logged in, local
	// otherwise) so the active backend stays the source of truth for
	// graph-resident NodeGraphTypeDef rows.
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

// buildCloudSyncTransport constructs the sync *auth.Transport over the SHARED
// process-wide cloud token source (c.cloudTokenSource) rather than minting a
// fresh cold keychain source. It is the factory the daemon threads into the
// segment control plane, the sync push/pull intercept, and the transcript
// upload loop so all three present the one warm credential — the exact wrap
// cli.BuildSyncTransport performs, over the retained source. The (*auth.Transport,
// error) shape matches the seam every consumer expects; the error is always nil
// here (construction cannot fail once the source is in hand) but is retained to
// satisfy that shape.
func (c *client) buildCloudSyncTransport() (*auth.Transport, error) {
	return auth.NewSyncTransport(cli.CloudEndpoint, c.cloudTokenSource), nil
}
