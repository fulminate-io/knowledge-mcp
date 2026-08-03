// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// newEmptyFetchHarness returns a shared server fake and a view over it whose Fetch
// serves EMPTY (no blobs, no error) even when the server holds the ids — the
// server reports the corpus metas via List (with a real DocCount) yet a load()
// imports NOTHING. This models the post-restart incident: the server holds the full
// shipped HNSW corpus (List shows it, the doc-count sums above the floor) but the
// live in-memory engine stays empty after load — the collapse the resident-vs-
// shipped probe must catch and segmentPoolDegenerate (server-vs-server) cannot.
func newEmptyFetchHarness(t *testing.T) *fakeSegmentSource {
	t.Helper()
	view := newSharedServerFake().viewFor(&knowledgev1.GraphSelector{}, "")
	view.emptyFetch = true
	return view
}

// shipHNSWMetas ships HNSW-format blobs carrying the given doc counts to target via
// the view so the server's List surfaces them (the shipped denominator). The bytes
// are a placeholder — the reconcile tests that need a degenerate load pair this with
// the empty-Fetch view so the blobs never decode.
func shipHNSWMetas(t *testing.T, view *fakeSegmentSource, target *knowledgev1.GraphSelector, docCounts ...int) {
	t.Helper()
	blobs := make([]*knowledgev1.SegmentBlobProto, 0, len(docCounts))
	for i, dc := range docCounts {
		blobs = append(blobs, &knowledgev1.SegmentBlobProto{
			Id: target.GetRepo() + "-h" + string(rune('A'+i)), Format: hnsw.New().Name(),
			DocCount: int32(dc), Bytes: []byte("seg"),
		})
	}
	view.server.ship(target, "", blobs)
}

// TestReconcileResidentDegenerate_ColdHeals proves the probe does NOT false-positive
// on a graph whose lazy load would heal: a full HNSW corpus shipped to the server, a
// fresh (cold, resident=0) Manager — ReconcileResidentDegenerate load()s the corpus
// cache-first → resident >= floor → degenerate=false (no rebuild needed).
func TestReconcileResidentDegenerate_ColdHeals(t *testing.T) {
	t.Parallel()

	_, gc := newSegmentHarness(t)
	ctx := context.Background()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "coldRepo"}

	// Ship a real HNSW corpus (1024 docs == one sealed segment) via a producer
	// Manager pointed at the same server.
	producer := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	seedShipped(t, ctx, producer, kgtypes.GraphCode, "coldRepo", hnswVecDocs(1024))
	require.GreaterOrEqual(t, serverHNSWDocCount(t, gc, target), residentBackstopFloor)

	// A FRESH consumer Manager starts cold (resident 0). The probe load()s first.
	consumer := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
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
	t.Parallel()

	gc := newEmptyFetchHarness(t)
	ctx := context.Background()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "incidentRepo"}

	// Server holds a full corpus (128 docs across two HNSW segments, >> floor 64) but
	// the empty Fetch means a load imports nothing.
	shipHNSWMetas(t, gc, target, 64, 64)

	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
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
	t.Parallel()

	_, gc := newSegmentHarness(t)
	ctx := context.Background()

	// Server holds a real, Fetch-able full HNSW corpus (>> floor) shipped by a
	// producer Manager pointed at the same server.
	producer := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	require.NoError(t, producer.AddAndMarkDirty(ctx, kgtypes.GraphCode, "partialHealRepo", hnswVecDocs(1024)))
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "partialHealRepo"))

	// Consumer Manager. Grab the SAME dm ReconcileResidentDegenerate will use, then
	// force the partial-L2-already-loaded state: import a sub-floor segment, latch
	// l2Loaded=true, and bump importedGen so a plain load() would short-circuit and
	// re-import nothing.
	consumer := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
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
	t.Parallel()

	ctx := context.Background()

	t.Run("pre-doc_count blob disarms (DocCount==0)", func(t *testing.T) {
		gc := newEmptyFetchHarness(t)
		target := &knowledgev1.GraphSelector{Graph: "code", Repo: "unknownRepo"}
		// A shipped HNSW meta with DocCount==0 (old pre-doc_count blob) → denominator
		// untrustworthy → disarm.
		shipHNSWMetas(t, gc, target, 0)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		degenerate, err := mgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "unknownRepo")
		require.NoError(t, err)
		require.False(t, degenerate, "a pre-doc_count blob disarms the ratio (conservative-unknown)")
	})

	t.Run("sub-floor corpus disarms", func(t *testing.T) {
		gc := newEmptyFetchHarness(t)
		target := &knowledgev1.GraphSelector{Graph: "code", Repo: "tinyRepo"}
		// Shipped corpus of 4 docs (< floor 64) → too small for the ratio → disarm.
		shipHNSWMetas(t, gc, target, 4)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		degenerate, err := mgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "tinyRepo")
		require.NoError(t, err)
		require.False(t, degenerate, "a sub-floor shipped corpus disarms the ratio (tiny-graph no-flap)")
	})
}

// serverHNSWDocCount sums the HNSW-format shipped doc counts on the shared server
// for one graph — the shipped denominator the probe reads. It reads the server the
// view is bound to directly (List(0) over that target).
func serverHNSWDocCount(t *testing.T, view *fakeSegmentSource, target *knowledgev1.GraphSelector) int {
	t.Helper()
	metas := view.server.listDelta(target, "", 0)
	total := 0
	hnswName := hnsw.New().Name()
	for _, m := range metas {
		if m.GetFormat() == hnswName {
			total += int(m.GetDocCount())
		}
	}
	return total
}
