// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// segment_reconcile_clientkit_test.go holds the reconcile fixtures' CLIENT BUILDERS,
// split out of segment_reconcile_test.go to keep both files under the size cap. The
// four wrappers narrow to one constructor: each exposes one more of the pieces its
// callers need (the backend, the cache dir, a supplied backend) and delegates.

// buildReconcileClient wires a *client over an EngineService h2c server + a
// fakeSegBackend (GCS-agent control plane). The consumer Manager runs on the cloud
// GCS segment source (logged-in + WithSegmentTransport). The returned engine handle
// lets a test seed scan pages + read the per-graph PipelineScan count. Presence /
// coverage metas are seeded onto the backend via shipHNSW (manifest seed); a
// manifest with NO backing GCS objects loads to an empty resident pool (the
// post-restart collapse the degenerate-rebuild tests model — the GCS analog of the
// old empty-Fetch service).
func buildReconcileClient(t *testing.T, codeRepos ...string) (*client, *reconcileEngine) {
	t.Helper()
	return buildReconcileClientWith(t, 0, codeRepos...)
}

// buildReconcileClientWith exposes the embedded knob (BinaryVectorCount Stats
// serves) so segmentPoolDegenerate — which gates the heal closure's
// load-first/rebuild decision — can be ARMED (nonzero). A test seeds a real
// decodable corpus by shipping through a producer Manager wired to the SAME backend
// (see shipRealCorpus); a manifest-only seed (shipHNSW) models the empty-load
// collapse.
func buildReconcileClientWith(
	t *testing.T, embedded int32, codeRepos ...string,
) (*client, *reconcileEngine) {
	t.Helper()
	c, eng, _ := buildReconcileClientWithSeg(t, embedded, codeRepos...)
	return c, eng
}

// buildReconcileClientWithSeg is buildReconcileClientWith that ALSO returns the
// *fakeSegBackend, so a test can flip its failReadAfterN knob to model a server that
// answers the cheap presence probe but then times out on the heal probe's load() —
// driving healNeedsRebuild into the ReconcileResidentDegenerate probe-error arm.
func buildReconcileClientWithSeg(
	t *testing.T, embedded int32, codeRepos ...string,
) (*client, *reconcileEngine, *fakeSegBackend) {
	t.Helper()
	c, eng, seg, _ := buildReconcileClientWithSegDir(t, embedded, codeRepos...)
	return c, eng, seg
}

// buildReconcileClientWithSegDir is buildReconcileClientWithSeg that ALSO returns
// the client's L2 cache base dir, so a test can warm the on-disk L2 cache through a
// SEPARATE producer Manager rooted at the SAME dir (the daemon-restart shape: a
// prior run warmed the disk, then a fresh consumer Manager imports from it L2-first
// while the server is down).
func buildReconcileClientWithSegDir(
	t *testing.T, embedded int32, codeRepos ...string,
) (*client, *reconcileEngine, *fakeSegBackend, string) {
	t.Helper()
	return buildReconcileClientOnBackend(t, newFakeSegBackend(t), embedded, codeRepos...)
}

// buildReconcileClientOnBackend is buildReconcileClientWithSegDir over a SUPPLIED
// backend, so a caller can point a fresh client at a backend that already holds a
// corpus rather than paying to build one. Everything else — engine, server, auth,
// cache dir — is still constructed per caller.
func buildReconcileClientOnBackend(
	t *testing.T, backend *fakeSegBackend, embedded int32, codeRepos ...string,
) (*client, *reconcileEngine, *fakeSegBackend, string) {
	t.Helper()
	eng := &reconcileEngine{
		countingEngine: &countingEngine{},
		namesByType:    map[string][]string{string(kgtypes.GraphCode): codeRepos},
		embedded:       embedded,
		scanItems:      map[string][]*knowledgev1.PipelineScanItem{},
		scanCalls:      map[string]int{},
		deltaScanCalls: map[string]int{},
	}

	mux := http.NewServeMux()
	engPath, engHdlr := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(engPath, engHdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)

	local := graphclient.NewGraphClientForURL(srv.URL)
	// Logged IN by design: the login gate selects the OSS-local L2-only segment
	// source for a not-logged-in caller. The reconcile fixtures exercise the
	// SERVER/cloud reconcile path (ReconcileResidentDegenerate over a shipped corpus)
	// — the logged-in regime — so the fixture seeds a keychain refresh token AND a
	// segment transport to keep the manager on the GCS source. The OSS-local heal path
	// is exercised by buildOSSHealClient.
	store := newFakeAuthStore()
	require.NoError(t, store.Set(context.Background(), auth.KeyRefreshToken, "frt-stub"))
	authState := auth.NewAuthState(store, time.Minute)
	router := graphclient.NewRouter(local, srv.URL, staticTokenSource{tok: "tok"}, authState)

	dir := t.TempDir()
	c := &client{
		local:      local,
		router:     router,
		authState:  authState,
		segmentMgr: segmentdist.NewManager(router, dir, 0, segmentdist.WithSegmentTransport(backend.transportBuilder())),
	}
	return c, eng, backend, dir
}
