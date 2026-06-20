// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
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
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
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

// healSegmentService is a minimal in-memory SegmentServiceHandler for the heal
// probe test: Ship stamps a monotonic generation and stores the blob; ListDelta
// serves the stored metas (carrying doc_count). It only needs Ship + ListDelta
// for the probe (HasShippedSegments / ShippedSegmentDocCount drive ListDelta(0));
// Fetch/Prune are present to satisfy the handler interface and never fire here.
type healSegmentService struct {
	mu        sync.Mutex
	byKey     map[string][]*knowledgev1.SegmentBlobProto
	gen       uint64
	manifests map[string]map[string]map[string]bool // graphKey -> writer+format -> id-set
}

func newHealSegmentService() *healSegmentService {
	return &healSegmentService{
		byKey:     map[string][]*knowledgev1.SegmentBlobProto{},
		manifests: map[string]map[string]map[string]bool{},
	}
}

func (f *healSegmentService) key(t *knowledgev1.GraphSelector) string {
	return t.GetGraph() + ":" + t.GetRepo() + t.GetAccount() + t.GetName()
}

func (f *healSegmentService) Ship(
	_ context.Context, req *connect.Request[knowledgev1.ShipRequest],
) (*connect.Response[knowledgev1.ShipResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(req.Msg.GetTarget())
	for _, b := range req.Msg.GetBlobs() {
		f.gen++
		f.byKey[k] = append(f.byKey[k], &knowledgev1.SegmentBlobProto{
			Id: b.GetId(), Format: b.GetFormat(), Generation: f.gen,
			DocCount: b.GetDocCount(), Bytes: b.GetBytes(),
		})
	}
	return connect.NewResponse(&knowledgev1.ShipResponse{}), nil
}

func (f *healSegmentService) ListDelta(
	_ context.Context, req *connect.Request[knowledgev1.ListDeltaRequest],
) (*connect.Response[knowledgev1.ListDeltaResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(req.Msg.GetTarget())
	var metas []*knowledgev1.SegmentMetaProto
	for _, b := range f.byKey[k] {
		if b.GetGeneration() > req.Msg.GetSinceGen() {
			metas = append(metas, &knowledgev1.SegmentMetaProto{
				Id: b.GetId(), Format: b.GetFormat(), Generation: b.GetGeneration(), DocCount: b.GetDocCount(),
			})
		}
	}
	return connect.NewResponse(&knowledgev1.ListDeltaResponse{Metas: metas}), nil
}

func (f *healSegmentService) Fetch(
	_ context.Context, _ *connect.Request[knowledgev1.FetchRequest],
) (*connect.Response[knowledgev1.FetchResponse], error) {
	return connect.NewResponse(&knowledgev1.FetchResponse{}), nil
}

func (f *healSegmentService) Prune(
	_ context.Context, _ *connect.Request[knowledgev1.PruneRequest],
) (*connect.Response[knowledgev1.PruneResponse], error) {
	return connect.NewResponse(&knowledgev1.PruneResponse{}), nil
}

// Publish swaps this writer's manifest and refcount-GCs blobs no manifest
// references — the registry-model mirror so the coverage-heal probe's publish path
// behaves like the real server.
func (f *healSegmentService) Publish(
	_ context.Context, req *connect.Request[knowledgev1.PublishRequest],
) (*connect.Response[knowledgev1.PublishResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(req.Msg.GetTarget())
	if f.manifests[k] == nil {
		f.manifests[k] = map[string]map[string]bool{}
	}
	mk := req.Msg.GetWriterId() + "\x00" + req.Msg.GetFormat()
	set := map[string]bool{}
	for _, id := range req.Msg.GetIds() {
		set[id] = true
	}
	f.manifests[k][mk] = set

	referenced := map[string]bool{}
	for _, s := range f.manifests[k] {
		for id := range s {
			referenced[id] = true
		}
	}
	kept := f.byKey[k][:0]
	var removed uint64
	for _, b := range f.byKey[k] {
		if referenced[b.GetId()] {
			kept = append(kept, b)
			continue
		}
		removed++
	}
	f.byKey[k] = kept
	return connect.NewResponse(&knowledgev1.PublishResponse{Deleted: removed}), nil
}

// buildHealProbeClient stands up ONE h2c httptest server serving BOTH the
// EngineService (Stats, embedded count) and the SegmentService (ListDelta,
// shipped metas) handlers, wires a logged-out *Router pointed at it, and builds a
// *client whose segmentMgr AND GraphCaller() both route through that one router —
// exactly the production wiring (pipeline.go:166 passes c.router to NewManager;
// GraphCaller() returns c.router). Shipped metas are seeded via shipHNSW through
// the client's own router, so the segment service is internal to the fixture.
func buildHealProbeClient(t *testing.T, embedded int32) *client {
	t.Helper()
	seg := newHealSegmentService()
	eng := &coverageStatsEngine{countingEngine: &countingEngine{}, embedded: embedded}

	mux := http.NewServeMux()
	segPath, segHdlr := knowledgev1connect.NewSegmentServiceHandler(seg)
	mux.Handle(segPath, segHdlr)
	engPath, engHdlr := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(engPath, engHdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)

	local := graphclient.NewGraphClientForURL(srv.URL)
	authState := auth.NewAuthState(newFakeAuthStore(), time.Minute) // logged out → routes local
	router := graphclient.NewRouter(local, srv.URL, staticTokenSource{tok: "tok"}, authState)

	c := &client{
		local:      local,
		router:     router,
		authState:  authState,
		segmentMgr: segmentdist.NewManager(router, t.TempDir(), 0),
	}
	return c
}

// shipHNSW seeds the heal probe's server with HNSW-format segment metas carrying
// the given live doc_counts (a doc_count of 0 is the pre-doc_count-plumbing blob
// that drives the conservative-unknown signal). Ships through the client's own
// router so the metas land where the probe's ListDelta(0) reads them.
func shipHNSW(t *testing.T, c *client, repo string, docCounts ...int) {
	t.Helper()
	shipHNSWFor(t, c, kgtypes.GraphCode, repo, docCounts...)
}

// shipHNSWFor is the graph-type-aware shipHNSW: it addresses the (gt, name)
// instance through graphsel.GraphSelectorFor (so a cloud/cicd graph ships under
// Account and a practice graph under Language, not Repo) — letting a reconcile
// test ship a degenerate corpus for a NON-code embeddable builtin. shipHNSW is the
// code-graph shorthand that delegates here.
func shipHNSWFor(t *testing.T, c *client, gt kgtypes.GraphType, name string, docCounts ...int) {
	t.Helper()
	blobs := make([]*knowledgev1.SegmentBlobProto, 0, len(docCounts))
	for i, dc := range docCounts {
		blobs = append(blobs, &knowledgev1.SegmentBlobProto{
			Id: name + "-h" + string(rune('A'+i)), Format: hnsw.New().Name(), DocCount: int32(dc), Bytes: []byte("seg"),
		})
	}
	_, err := c.router.Ship(context.Background(), &knowledgev1.ShipRequest{
		Target: graphsel.GraphSelectorFor(gt, name, false),
		Blobs:  blobs,
	})
	require.NoError(t, err)
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
	ctx := context.Background()

	t.Run("CASE A degenerate-but-nonzero heals", func(t *testing.T) {
		c := buildHealProbeClient(t, 120)
		shipHNSW(t, c, "repoA", 12) // covered=12, anyUnknown=false
		degenerate, err := c.segmentPoolDegenerate(ctx, kgtypes.GraphCode, "repoA")
		require.NoError(t, err)
		require.True(t, degenerate, "above floor, covered/embedded below ratio → rebuild")
	})

	t.Run("CASE B healthy disarms", func(t *testing.T) {
		c := buildHealProbeClient(t, 60)
		shipHNSW(t, c, "repoB", 58) // covered=58, anyUnknown=false
		degenerate, err := c.segmentPoolDegenerate(ctx, kgtypes.GraphCode, "repoB")
		require.NoError(t, err)
		require.False(t, degenerate, "above floor, covered/embedded above ratio → no rebuild")
	})

	t.Run("CASE C small graph below floor no-flap", func(t *testing.T) {
		c := buildHealProbeClient(t, 10)
		shipHNSW(t, c, "repoC", 1) // covered=1, embedded=10 < floor → ratio never consulted
		degenerate, err := c.segmentPoolDegenerate(ctx, kgtypes.GraphCode, "repoC")
		require.NoError(t, err)
		require.False(t, degenerate, "embedded below floor → ratio path disarmed")
	})

	t.Run("CASE D migration all-zero-doc_count storm guard disarms", func(t *testing.T) {
		c := buildHealProbeClient(t, 60000)
		shipHNSW(t, c, "repoD", 0, 0) // every HNSW meta doc_count==0 → anyUnknown=true
		degenerate, err := c.segmentPoolDegenerate(ctx, kgtypes.GraphCode, "repoD")
		require.NoError(t, err)
		require.False(t, degenerate,
			"anyUnknown (pre-doc_count migration state) DISARMS the ratio despite covered=0 < 0.5*embedded — no fleet rebuild storm")
	})
}
