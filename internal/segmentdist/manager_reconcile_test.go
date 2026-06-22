// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// emptyFetchService wraps fakeSegmentService but serves an EMPTY Fetch — the
// server reports the corpus metas via ListDelta (with a real DocCount) yet hands
// back no blobs, so a load() imports NOTHING despite the shipped denominator being
// non-zero. This models the post-restart incident: the server holds the full
// shipped HNSW corpus (List shows it, the doc-count sums above the floor) but the
// live in-memory engine stays empty after load — the collapse the resident-vs-
// shipped probe must catch and segmentPoolDegenerate (server-vs-server) cannot.
type emptyFetchService struct {
	*fakeSegmentService
}

func (f *emptyFetchService) Fetch(
	_ context.Context, _ *connect.Request[knowledgev1.FetchRequest],
) (*connect.Response[knowledgev1.FetchResponse], error) {
	return connect.NewResponse(&knowledgev1.FetchResponse{}), nil
}

// newReconcileHarness stands up an h2c server behind the given handler and returns
// a GraphClient pointed at it (satisfies segmentCaller).
func newReconcileHarness(t *testing.T, svc knowledgev1connect.SegmentServiceHandler) *graphclient.GraphClient {
	t.Helper()
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewSegmentServiceHandler(svc)
	mux.Handle(path, hdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)
	return graphclient.NewGraphClientForURL(srv.URL)
}

// shipHNSWMetas ships HNSW-format blobs carrying the given doc counts to target so
// the server's ListDelta surfaces them (the shipped denominator). The bytes are a
// placeholder — the reconcile tests that need a degenerate load pair this with the
// emptyFetchService so the blobs never decode.
func shipHNSWMetas(t *testing.T, gc *graphclient.GraphClient, target *knowledgev1.GraphSelector, docCounts ...int) {
	t.Helper()
	blobs := make([]*knowledgev1.SegmentBlobProto, 0, len(docCounts))
	for i, dc := range docCounts {
		blobs = append(blobs, &knowledgev1.SegmentBlobProto{
			Id: target.GetRepo() + "-h" + string(rune('A'+i)), Format: hnsw.New().Name(),
			DocCount: int32(dc), Bytes: []byte("seg"),
		})
	}
	_, err := gc.Ship(context.Background(), &knowledgev1.ShipRequest{Target: target, Blobs: blobs})
	require.NoError(t, err)
}

// TestReconcileResidentDegenerate_ColdHeals proves the probe does NOT false-positive
// on a graph whose lazy load would heal: a full HNSW corpus shipped to the server, a
// fresh (cold, resident=0) Manager — ReconcileResidentDegenerate load()s the corpus
// cache-first → resident >= floor → degenerate=false (no rebuild needed).
func TestReconcileResidentDegenerate_ColdHeals(t *testing.T) {
	_, gc := newSegmentHarness(t)
	ctx := context.Background()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "coldRepo"}

	// Ship a real HNSW corpus (1024 docs == one sealed segment) via a producer
	// Manager pointed at the same server.
	producer := NewManager(gc, t.TempDir(), 0)
	require.NoError(t, producer.AddAndShip(ctx, kgtypes.GraphCode, "coldRepo", hnswVecDocs(1024)))
	require.GreaterOrEqual(t, serverHNSWDocCount(t, gc, target), residentBackstopFloor)

	// A FRESH consumer Manager starts cold (resident 0). The probe load()s first.
	consumer := NewManager(gc, t.TempDir(), 0)
	require.Equal(t, 0, consumer.ResidentDocCount(kgtypes.GraphCode, "coldRepo"),
		"the cold consumer has not loaded yet")

	degenerate, err := consumer.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "coldRepo")
	require.NoError(t, err)
	require.False(t, degenerate, "a cold graph that lazy-load heals is NOT flagged degenerate")
	require.GreaterOrEqual(t, consumer.ResidentDocCount(kgtypes.GraphCode, "coldRepo"), residentBackstopFloor,
		"the cache-first load made the corpus resident")
}

// TestReconcileResidentDegenerate_Incident drives the post-restart degenerate state:
// the server holds the full shipped HNSW corpus (DocCount >= floor) but Fetch serves
// nothing, so load imports zero → live resident stays empty after load while shipped
// is non-zero → degenerate=true. This is the incident shape (server intact, live pool
// empty) — the case the read-side recoverIfDegenerate would also catch, but here on
// the startup/periodic edge with no Search.
func TestReconcileResidentDegenerate_Incident(t *testing.T) {
	svc := &emptyFetchService{fakeSegmentService: newFakeSegmentService()}
	gc := newReconcileHarness(t, svc)
	ctx := context.Background()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "incidentRepo"}

	// Server holds a full corpus (128 docs across two HNSW segments, >> floor 64) but
	// the empty Fetch means a load imports nothing.
	shipHNSWMetas(t, gc, target, 64, 64)

	mgr := NewManager(gc, t.TempDir(), 0)
	degenerate, err := mgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "incidentRepo")
	require.NoError(t, err)
	require.True(t, degenerate,
		"server holds the corpus but the live engine stays empty after load → degenerate")
	require.Equal(t, 0, mgr.ResidentDocCount(kgtypes.GraphCode, "incidentRepo"),
		"the live resident pool is empty (the masked collapse)")
}

// TestReconcileResidentDegenerate_PartialL2HealsViaServerReimport drives the C5
// case the OLD load()-guarded reconcile could NOT heal: the server holds the FULL
// HNSW corpus and CAN Fetch it, but the consumer already ran a load() that imported
// only a sub-floor PARTIAL set (l2Loaded latched true). A bare dm.load(ctx) would
// short-circuit on the l2Loaded once-guard and re-import nothing, leaving resident
// below floor — flagging degenerate and forcing the expensive rebuild. The cheap
// server re-import (recoverIfDegenerate: importedGen.Store(0) + loadFromServer)
// restores coverage instead.
//
// RED on current source: load() short-circuits, resident stays < floor, shipped >=
// floor → degenerate=true. GREEN after the recoverIfDegenerate re-wire: the cheap
// re-import pulls the full corpus, resident >= floor → degenerate=false.
func TestReconcileResidentDegenerate_PartialL2HealsViaServerReimport(t *testing.T) {
	_, gc := newSegmentHarness(t)
	ctx := context.Background()

	// Server holds a real, Fetch-able full HNSW corpus (>> floor) shipped by a
	// producer Manager pointed at the same server.
	producer := NewManager(gc, t.TempDir(), 0)
	require.NoError(t, producer.AddAndShip(ctx, kgtypes.GraphCode, "partialHealRepo", hnswVecDocs(1024)))

	// Consumer Manager. Grab the SAME dm ReconcileResidentDegenerate will use, then
	// force the partial-L2-already-loaded state: import a sub-floor segment, latch
	// l2Loaded=true, and bump importedGen so a plain load() would short-circuit and
	// re-import nothing.
	consumer := NewManager(gc, t.TempDir(), 0)
	dm := consumer.managerFor(kgtypes.GraphCode, "partialHealRepo")

	partial := buildHNSWSegment(t, vecContentDocsSeed(10, 5000)) // one 10-doc segment (< floor 64)
	partialBlobs := make([]searchengine.SegmentBlob, 0, len(partial))
	for _, b := range partial {
		partialBlobs = append(partialBlobs, blobFromProto(b))
	}
	require.NoError(t, dm.engine.Import(partialBlobs, nil))
	dm.recordResident(partialBlobs)
	dm.l2Loaded.Store(true)   // a prior load already ran (partial L2).
	dm.importedGen.Store(999) // floor advanced past the corpus — plain load() no-ops.
	require.Less(t, dm.engine.ResidentDocCount(), residentBackstopFloor,
		"the consumer is below floor after the partial import")

	degenerate, err := consumer.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "partialHealRepo")
	require.NoError(t, err)
	require.False(t, degenerate,
		"the cheap server re-import must restore coverage, not flag degenerate for a rebuild")
	require.GreaterOrEqual(t, dm.engine.ResidentDocCount(), residentBackstopFloor,
		"resident >= floor after the cheap re-import pulled the full server corpus")
}

// TestReconcileResidentDegenerate_Disarms pins the conservative-unknown and
// sub-floor disarms, mirroring recoverIfDegenerate's so the reconcile never storms a
// migrating fleet or churns a tiny graph.
func TestReconcileResidentDegenerate_Disarms(t *testing.T) {
	ctx := context.Background()

	t.Run("pre-doc_count blob disarms (DocCount==0)", func(t *testing.T) {
		svc := &emptyFetchService{fakeSegmentService: newFakeSegmentService()}
		gc := newReconcileHarness(t, svc)
		target := &knowledgev1.GraphSelector{Graph: "code", Repo: "unknownRepo"}
		// A shipped HNSW meta with DocCount==0 (old pre-doc_count blob) → denominator
		// untrustworthy → disarm.
		shipHNSWMetas(t, gc, target, 0)
		mgr := NewManager(gc, t.TempDir(), 0)
		degenerate, err := mgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "unknownRepo")
		require.NoError(t, err)
		require.False(t, degenerate, "a pre-doc_count blob disarms the ratio (conservative-unknown)")
	})

	t.Run("sub-floor corpus disarms", func(t *testing.T) {
		svc := &emptyFetchService{fakeSegmentService: newFakeSegmentService()}
		gc := newReconcileHarness(t, svc)
		target := &knowledgev1.GraphSelector{Graph: "code", Repo: "tinyRepo"}
		// Shipped corpus of 4 docs (< floor 64) → too small for the ratio → disarm.
		shipHNSWMetas(t, gc, target, 4)
		mgr := NewManager(gc, t.TempDir(), 0)
		degenerate, err := mgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "tinyRepo")
		require.NoError(t, err)
		require.False(t, degenerate, "a sub-floor shipped corpus disarms the ratio (tiny-graph no-flap)")
	})
}

// serverHNSWDocCount sums the HNSW-format shipped doc counts on the server for one
// graph via a throwaway source — the shipped denominator the probe reads.
func serverHNSWDocCount(t *testing.T, gc *graphclient.GraphClient, target *knowledgev1.GraphSelector) int {
	t.Helper()
	src := newRPCSegmentSource(gc, target, "", context.Background())
	metas, err := src.List(context.Background(), 0)
	require.NoError(t, err)
	total := 0
	hnswName := hnsw.New().Name()
	for _, m := range metas {
		if m.Format == hnswName {
			total += m.DocCount
		}
	}
	return total
}
