// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// coverageStatsEngine is an EngineServiceHandler that serves a configurable
// BinaryVectorCount from Stats — the "embedded count" denominator the
// coverage-ratio heal probe (segmentPoolDegenerate → tools.GraphEmbeddedCount)
// compares segment-covered docs against. It embeds countingEngine (same package)
// to inherit every other RPC stub and overrides only Stats.
type coverageStatsEngine struct {
	*countingEngine
	embedded int32
}

func (e *coverageStatsEngine) Stats(
	context.Context, *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	return connect.NewResponse(&knowledgev1.StatsResponse{
		GraphStats: &knowledgev1.GraphStats{BinaryVectorCount: e.embedded},
	}), nil
}

// buildHealProbeClient stands up an EngineService (Stats, embedded count) h2c server
// and a fakeSegBackend (the GCS-agent control plane), wires a logged-IN *Router +
// a segmentMgr WithSegmentTransport(backend) so the manager runs on the cloud GCS
// segment source, and builds a *client whose segmentMgr AND GraphCaller() route as
// in production CLOUD wiring. Shipped metas are seeded onto the GCS backend via
// shipHNSW (manifest seed), so ShippedManifestSnapshot's manifest/read returns them.
//
// LOGGED IN by design: the login gate selects the OSS-local L2-only source for a
// not-logged-in caller. These probes exercise the SERVER/cloud segment path
// (ShippedManifestSnapshot → GCS manifest/read, the coverage ratio) — the logged-in
// regime — so the fixture seeds a keychain refresh token AND a segment transport to
// keep the manager on the GCS source. The OSS-local heal path is exercised
// separately by buildOSSHealClient.
func buildHealProbeClient(t *testing.T, embedded int32) (*client, *fakeSegBackend) {
	t.Helper()
	backend := newFakeSegBackend(t)
	eng := &coverageStatsEngine{countingEngine: &countingEngine{}, embedded: embedded}

	mux := http.NewServeMux()
	engPath, engHdlr := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(engPath, engHdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)

	local := graphclient.NewGraphClientForURL(srv.URL)
	store := newFakeAuthStore()
	require.NoError(t, store.Set(opCtx(), auth.KeyRefreshToken, "frt-stub")) // logged IN → cloud GCS source
	authState := auth.NewAuthState(store, time.Minute)
	router := graphclient.NewRouter(local, srv.URL, staticTokenSource{tok: "tok"}, authState)

	c := &client{
		local:      local,
		router:     router,
		authState:  authState,
		segmentMgr: segmentdist.NewManager(router, t.TempDir(), 0, segmentdist.WithSegmentTransport(backend.transportBuilder())),
	}
	return c, backend
}

// shipHNSW seeds the heal probe's GCS backend with an HNSW-format manifest carrying
// the given live doc_counts (a doc_count of 0 is the pre-doc_count-plumbing blob
// that drives the conservative-unknown signal). It seeds the manifest directly so
// the probe's ShippedManifestSnapshot manifest/read returns the metas.
func shipHNSW(t *testing.T, backend *fakeSegBackend, repo string, docCounts ...int) {
	t.Helper()
	shipHNSWFor(t, backend, kgtypes.GraphCode, repo, docCounts...)
}

// shipHNSWFor is the graph-type-aware shipHNSW: it seeds the manifest for (gt, name)
// on the GCS backend keyed by graphType/name (matching the gcsSegmentSource's
// manifest key), letting a reconcile test seed a degenerate corpus for a NON-code
// embeddable builtin. shipHNSW is the code-graph shorthand that delegates here.
func shipHNSWFor(t *testing.T, backend *fakeSegBackend, gt kgtypes.GraphType, name string, docCounts ...int) {
	t.Helper()
	digests := make([]segManifestDigest, 0, len(docCounts))
	for i, dc := range docCounts {
		digests = append(digests, segManifestDigest{
			ContentHash: name + "-h" + string(rune('A'+i)),
			DocCount:    dc,
		})
	}
	backend.seedManifest(string(gt), name, hnsw.New().Name(), digests)
}

// TestSegmentPoolDegenerateCoverageProbe is the fails-when-absent heal-decision
// test (Phase 3 Step 4). It drives segmentPoolDegenerate — the rebuild decision
// the auto-heal closure gates on when segments are already present (the closure
// rebuilds iff !HasShippedSegments OR segmentPoolDegenerate) — across the four
// cases the coverage-ratio probe must discriminate, with floor=64 / ratio=0.5.
//
// CASE A (degenerate heals): covered=12, embedded=120 (clears floor, 0.1 < 0.5
//
//	ratio) → degenerate=true. On HEAD's zero-only arm a nonzero-but-degenerate pool
//	would NOT heal; this case is what the coverage lever adds.
//
// CASE B (healthy disarms): covered=58, embedded=60 (above floor, 0.97 ≥ 0.5) →
//
//	degenerate=false.
//
// CASE C (small-graph no-flap): embedded=10 (below floor 64), covered=1 → the
//
//	ratio path is never consulted → degenerate=false.
//
// CASE D (MIGRATION STORM GUARD): anyUnknown=true (every shipped HNSW meta
//
//	doc_count==0, the post-deploy pre-rebuild fleet state), embedded=60000,
//	covered=0 → the conservative-unknown guard DISARMS the ratio (degenerate=false),
//	falling back to the zero-only trigger. Without this guard covered=0 < 0.5*60000
//	would fire a fleet-wide rebuild storm — this is the T2-2 fails-when-absent pin.
func TestSegmentPoolDegenerateCoverageProbe(t *testing.T) {
	ctx := opCtx()

	t.Run("CASE A degenerate-but-nonzero heals", func(t *testing.T) {
		c, backend := buildHealProbeClient(t, 120)
		shipHNSW(t, backend, "repoA", 12) // covered=12, anyUnknown=false
		snap, err := c.segmentMgr.ShippedManifestSnapshot(ctx, kgtypes.GraphCode, "repoA", hnsw.New().Name())
		require.NoError(t, err)
		degenerate, err := c.segmentPoolDegenerate(ctx, kgtypes.GraphCode, "repoA", snap)
		require.NoError(t, err)
		require.True(t, degenerate, "above floor, covered/embedded below ratio → rebuild")
	})

	t.Run("CASE B healthy disarms", func(t *testing.T) {
		c, backend := buildHealProbeClient(t, 60)
		shipHNSW(t, backend, "repoB", 58) // covered=58, anyUnknown=false
		snap, err := c.segmentMgr.ShippedManifestSnapshot(ctx, kgtypes.GraphCode, "repoB", hnsw.New().Name())
		require.NoError(t, err)
		degenerate, err := c.segmentPoolDegenerate(ctx, kgtypes.GraphCode, "repoB", snap)
		require.NoError(t, err)
		require.False(t, degenerate, "above floor, covered/embedded above ratio → no rebuild")
	})

	t.Run("CASE C small graph below floor no-flap", func(t *testing.T) {
		c, backend := buildHealProbeClient(t, 10)
		shipHNSW(t, backend, "repoC", 1) // covered=1, embedded=10 < floor → ratio never consulted
		snap, err := c.segmentMgr.ShippedManifestSnapshot(ctx, kgtypes.GraphCode, "repoC", hnsw.New().Name())
		require.NoError(t, err)
		degenerate, err := c.segmentPoolDegenerate(ctx, kgtypes.GraphCode, "repoC", snap)
		require.NoError(t, err)
		require.False(t, degenerate, "embedded below floor → ratio path disarmed")
	})

	t.Run("CASE D migration all-zero-doc_count storm guard disarms", func(t *testing.T) {
		c, backend := buildHealProbeClient(t, 60000)
		shipHNSW(t, backend, "repoD", 0, 0) // every HNSW meta doc_count==0 → anyUnknown=true
		snap, err := c.segmentMgr.ShippedManifestSnapshot(ctx, kgtypes.GraphCode, "repoD", hnsw.New().Name())
		require.NoError(t, err)
		degenerate, err := c.segmentPoolDegenerate(ctx, kgtypes.GraphCode, "repoD", snap)
		require.NoError(t, err)
		require.False(t, degenerate,
			"anyUnknown (pre-doc_count migration state) DISARMS the ratio despite covered=0 < 0.5*embedded — no fleet rebuild storm")
	})
}
