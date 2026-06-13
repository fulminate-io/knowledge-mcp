// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// vecDocs builds n searchengine.Documents with deterministic 32-byte vectors.
func hnswVecDocs(n int) []searchengine.Document {
	rng := rand.New(rand.NewPCG(0xABCD, 0xEF01))
	docs := make([]searchengine.Document, n)
	for i := range docs {
		v := make([]byte, 32)
		for j := range v {
			v[j] = byte(rng.UintN(256))
		}
		docs[i] = searchengine.Document{ID: fmt.Sprintf("n%d", i), Vector: v}
	}
	return docs
}

// TestManagerAddAndShipSealsOneBlob wires a production Manager over the HNSW
// format and a counting caller, AddAndShips enough docs to seal exactly one
// segment, and asserts exactly one Ship RPC carrying one hnsw-format blob fires.
// A second AddAndShip with the same docs ships NOTHING (Export-diff no-op for the
// already-shipped content hash). Criterion: Phase 3 Step 1.
func TestManagerAddAndShipSealsOneBlob(t *testing.T) {
	_, gc := newSegmentHarness(t)
	cc := &countingCaller{inner: gc}
	ctx := context.Background()

	// MinSegmentDocs default is 1024; seal exactly one segment with 1024 docs.
	docs := hnswVecDocs(1024)

	mgr := NewManager(cc, t.TempDir(), 0)

	require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "repoHNSW", docs))
	require.Equal(t, int64(1), cc.shipCalls.Load(), "AddAndShip sealing one segment fires exactly one Ship RPC")
	require.Equal(t, int64(1), cc.shipBlobs.Load(), "the Ship carries exactly one segment blob")

	// The shipped blob must be tagged with the HNSW format and decode back to a
	// segment indexing exactly the embedded ids.
	dm := mgr.managerFor(kgtypes.GraphCode, "repoHNSW")
	exported := dm.engine.Export()
	require.Len(t, exported, 1)
	require.Equal(t, "hnsw", exported[0].Format, "shipped blob is tagged hnsw")
	seg, err := hnsw.New().Decode(exported[0].Bytes)
	require.NoError(t, err)
	require.Len(t, seg.IDs(), 1024, "decoded segment indexes exactly the embedded ids")

	// Export-diff no-op: a second ship() with NO intervening Add re-exports the
	// SAME sealed segment (same content hash), so the diff against shippedIDs is
	// empty → ZERO new Ship RPC, ZERO new blobs. This is the manager.go ship-diff
	// guarantee. (Note: re-ADDING the same docs would seal a NEW segment with a
	// DIFFERENT content hash — HNSW graph topology is non-deterministic, randomLevel
	// — so doc-level idempotency does NOT hold for HNSW the way it does for the
	// deterministic mock format; the segment-level diff no-op is the true property.)
	beforeShips := cc.shipCalls.Load()
	beforeBlobs := cc.shipBlobs.Load()
	_, shipErr := dm.ship(ctx, dm.locallyShipped)
	require.NoError(t, shipErr)
	require.Equal(t, beforeShips, cc.shipCalls.Load(), "re-ship without Add issues ZERO new Ship RPCs")
	require.Equal(t, beforeBlobs, cc.shipBlobs.Load(), "re-ship without Add sends ZERO new blobs")
}

// TestManagerFlushSealsSubThresholdTail is the steady-state searchability +
// bounded-segment proof: a graph with FEWER than MinSegmentDocs (1024) embeddable
// nodes, written incrementally, seals ZERO segments via AddAndShip (it just
// buffers the sub-threshold tail). The quiescence Flush force-seals that tail into
// exactly ONE searchable segment and ships it — and a redundant re-flush is a
// cheap no-op (no segment-count blowup).
func TestManagerFlushSealsSubThresholdTail(t *testing.T) {
	_, gc := newSegmentHarness(t)
	cc := &countingCaller{inner: gc}
	ctx := context.Background()

	// 500 < MinSegmentDocs(1024): the incremental backlog never seals a segment.
	const n = 500
	docs := hnswVecDocs(n)
	mgr := NewManager(cc, t.TempDir(), 0)

	require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "smallRepo", docs))
	dm := mgr.managerFor(kgtypes.GraphCode, "smallRepo")
	require.Empty(t, dm.engine.Export(), "a sub-1024 incremental backlog seals ZERO segments — unsearchable")
	require.Equal(t, int64(0), cc.shipCalls.Load(), "AddAndShip ships NOTHING for a sub-threshold backlog")

	// Quiescence Flush: force-seal the tail. It becomes exactly ONE searchable
	// segment indexing all the ids, shipped in exactly one Ship RPC carrying one blob.
	require.NoError(t, mgr.Flush(ctx, kgtypes.GraphCode, "smallRepo"))

	exported := dm.engine.Export()
	require.Len(t, exported, 1, "Flush seals the sub-threshold tail into exactly ONE segment")
	require.Equal(t, "hnsw", exported[0].Format, "the sealed tail is an hnsw segment")
	seg, err := hnsw.New().Decode(exported[0].Bytes)
	require.NoError(t, err)
	require.Len(t, seg.IDs(), n, "the one sealed segment indexes every embedded id — searchable")

	require.Equal(t, int64(1), cc.shipCalls.Load(), "Flush ships the sealed tail in exactly one Ship RPC")
	require.Equal(t, int64(1), cc.shipBlobs.Load(), "the Ship carries exactly one segment blob — no blowup")

	// Bounded: a redundant re-Flush on an already-drained buffer is a cheap no-op —
	// the segment count stays at ONE and no new Ship RPC fires.
	require.NoError(t, mgr.Flush(ctx, kgtypes.GraphCode, "smallRepo"))
	require.Len(t, dm.engine.Export(), 1, "re-Flush does not multiply segments")
	require.Equal(t, int64(1), cc.shipCalls.Load(), "re-Flush issues ZERO new Ship RPCs (no blowup)")
}

// TestManagerRoutesPerGraph asserts two distinct graphs get distinct engines and
// independent ship state.
func TestManagerRoutesPerGraph(t *testing.T) {
	_, gc := newSegmentHarness(t)
	cc := &countingCaller{inner: gc}
	ctx := context.Background()

	docsA := hnswVecDocs(1024)
	docsB := hnswVecDocs(1024)

	mgr := NewManager(cc, t.TempDir(), 0)
	require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "repoA", docsA))
	require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphKnowledge, "kg", docsB))

	dmA := mgr.managerFor(kgtypes.GraphCode, "repoA")
	dmB := mgr.managerFor(kgtypes.GraphKnowledge, "kg")
	require.NotSame(t, dmA, dmB, "distinct graphs get distinct managers")

	// Each manager's target selector routed the instance name to the right field.
	require.Equal(t, "repoA", dmA.target.GetRepo())
	require.Equal(t, "kg", dmB.target.GetName())
	require.Equal(t, "code", dmA.target.GetGraph())
	require.Equal(t, string(kgtypes.GraphKnowledge), dmB.target.GetGraph())
}

// bm25FieldDocs builds n field-bearing Documents (no Vector) for the BM25 engine.
func bm25FieldDocs(n int) []searchengine.Document {
	docs := make([]searchengine.Document, n)
	for i := range docs {
		docs[i] = searchengine.Document{
			ID: fmt.Sprintf("n%d", i),
			Fields: map[string]string{
				searchengine.FieldSymbolName: fmt.Sprintf("uniqueterm%d", i),
				searchengine.FieldSummary:    fmt.Sprintf("shared corpus body token%d common", i),
			},
		}
	}
	return docs
}

// TestManagerAddAndShipFieldsSealsBM25Blob is Phase 3 Step 2's criterion:
// AddAndShipFields builds + ships BM25 segments from field-bearing Documents
// through the BM25 engine; a sealed-segment ship fires exactly one Ship RPC with
// one bm25-format blob, and an empty-diff re-ship (no intervening Add) is a no-op.
func TestManagerAddAndShipFieldsSealsBM25Blob(t *testing.T) {
	_, gc := newSegmentHarness(t)
	cc := &countingCaller{inner: gc}
	ctx := context.Background()

	// MinSegmentDocs default is 1024; seal exactly one segment with 1024 docs.
	docs := bm25FieldDocs(1024)

	mgr := NewManager(cc, t.TempDir(), 0)

	require.NoError(t, mgr.AddAndShipFields(ctx, kgtypes.GraphKnowledge, "kgBM25", docs))
	require.Equal(t, int64(1), cc.shipCalls.Load(), "AddAndShipFields sealing one segment fires exactly one Ship RPC")
	require.Equal(t, int64(1), cc.shipBlobs.Load(), "the Ship carries exactly one segment blob")

	// The shipped blob is tagged bm25 and decodes back to a segment indexing the ids.
	dm := mgr.bm25ManagerFor(kgtypes.GraphKnowledge, "kgBM25")
	exported := dm.engine.Export()
	require.Len(t, exported, 1)
	require.Equal(t, "bm25", exported[0].Format, "shipped blob is tagged bm25")
	seg, err := bm25.New().Decode(exported[0].Bytes)
	require.NoError(t, err)
	require.Len(t, seg.IDs(), 1024, "decoded segment indexes exactly the embedded ids")

	// Empty-diff re-ship: a second ship() with NO intervening Add re-exports the SAME
	// sealed segment (BM25 Build is deterministic, so the content hash is identical),
	// so the diff against shippedIDs is empty → ZERO new Ship RPC, ZERO new blobs.
	beforeShips := cc.shipCalls.Load()
	beforeBlobs := cc.shipBlobs.Load()
	_, shipErr := dm.ship(ctx, dm.locallyShipped)
	require.NoError(t, shipErr)
	require.Equal(t, beforeShips, cc.shipCalls.Load(), "re-ship without Add issues ZERO new Ship RPCs")
	require.Equal(t, beforeBlobs, cc.shipBlobs.Load(), "re-ship without Add sends ZERO new blobs")
}

// TestManagerHoldsBothFormatMaps asserts ONE Manager owns BOTH formats per graph:
// the HNSW and BM25 maps are distinct, each format's ManagerFor lazily constructs
// (and memoizes) its own distManager, and the same graph routed through both
// formats yields two distinct engines.
func TestManagerHoldsBothFormatMaps(t *testing.T) {
	_, gc := newSegmentHarness(t)
	mgr := NewManager(gc, t.TempDir(), 0)

	hnswDM := mgr.managerFor(kgtypes.GraphKnowledge, "kg")
	bm25DM := mgr.bm25ManagerFor(kgtypes.GraphKnowledge, "kg")
	require.NotNil(t, hnswDM)
	require.NotNil(t, bm25DM)

	// Lazy memoization: a second call returns the SAME instance for each format.
	require.Same(t, hnswDM, mgr.managerFor(kgtypes.GraphKnowledge, "kg"))
	require.Same(t, bm25DM, mgr.bm25ManagerFor(kgtypes.GraphKnowledge, "kg"))

	// Both maps populated independently under the same graph key.
	require.Len(t, mgr.managers, 1)
	require.Len(t, mgr.bm25Managers, 1)
}

// TestGraphCacheDirsAreFormatDistinct is Phase 3 Step 1's criterion: HNSW and BM25
// L2 caches root under format-distinct directories for the same graph.
func TestGraphCacheDirsAreFormatDistinct(t *testing.T) {
	base := t.TempDir()
	hnswDir := graphCacheDirFor(base, kgtypes.GraphCode, "repo", hnsw.New().Name())
	bm25Dir := graphCacheDirFor(base, kgtypes.GraphCode, "repo", bm25.New().Name())
	require.NotEqual(t, hnswDir, bm25Dir, "HNSW and BM25 caches must root under format-distinct dirs")
	require.Contains(t, hnswDir, "hnsw")
	require.Contains(t, bm25Dir, "bm25")
}

// TestHasShippedSegments is the auto-heal presence-probe criterion: the cheap
// presence probe drives ListDelta(sinceGen=0) through the existing fake
// SegmentService and returns (false,nil) when the server holds no segments,
// (true,nil) when it holds one+. It must NEVER Fetch a blob — the probe is
// presence-only (metas), so the countingCaller's Fetch counter stays at zero.
func TestHasShippedSegments(t *testing.T) {
	_, gc := newSegmentHarness(t)
	cc := &countingCaller{inner: gc}
	ctx := context.Background()
	mgr := NewManager(cc, t.TempDir(), 0)

	// Empty graph: zero metas → (false, nil), and no Fetch.
	has, err := mgr.HasShippedSegments(ctx, kgtypes.GraphCode, "emptyRepo")
	require.NoError(t, err)
	require.False(t, has, "a graph with zero shipped segments probes as absent")
	require.Equal(t, int64(0), cc.fetchCalls.Load(), "the presence probe must NOT Fetch any blob")

	// Ship one blob for a distinct graph, then probe: one+ metas → (true, nil),
	// still no Fetch.
	_, err = gc.Ship(ctx, &knowledgev1.ShipRequest{
		Target: &knowledgev1.GraphSelector{Graph: "code", Repo: "populatedRepo"},
		Blobs: []*knowledgev1.SegmentBlobProto{
			blobToProto(searchengine.SegmentBlob{ID: "s1", Format: "hnsw", Bytes: []byte("seg")}),
		},
	})
	require.NoError(t, err)

	has, err = mgr.HasShippedSegments(ctx, kgtypes.GraphCode, "populatedRepo")
	require.NoError(t, err)
	require.True(t, has, "a graph with one+ shipped segments probes as present")
	require.Equal(t, int64(0), cc.fetchCalls.Load(), "the presence probe must NOT Fetch any blob")
}

// TestShippedSegmentDocCount is the coverage-probe data-source criterion: the
// probe sums HNSW-format meta.DocCount (covered), EXCLUDES BM25 metas (they index
// the same nodes — double-counting), flags anyUnknown only when an HNSW meta has
// DocCount==0, and never Fetches a blob.
func TestShippedSegmentDocCount(t *testing.T) {
	_, gc := newSegmentHarness(t)
	cc := &countingCaller{inner: gc}
	ctx := context.Background()
	mgr := NewManager(cc, t.TempDir(), 0)

	// Graph A: two HNSW segments (doc_count 1000 + 24) + one BM25 segment (doc_count
	// 2048 — must be EXCLUDED). All non-zero HNSW → covered=1024, anyUnknown=false.
	_, err := gc.Ship(ctx, &knowledgev1.ShipRequest{
		Target: &knowledgev1.GraphSelector{Graph: "code", Repo: "covRepo"},
		Blobs: []*knowledgev1.SegmentBlobProto{
			blobToProto(searchengine.SegmentBlob{ID: "h1", Format: "hnsw", DocCount: 1000, Bytes: []byte("a")}),
			blobToProto(searchengine.SegmentBlob{ID: "h2", Format: "hnsw", DocCount: 24, Bytes: []byte("b")}),
			blobToProto(searchengine.SegmentBlob{ID: "b1", Format: "bm25", DocCount: 2048, Bytes: []byte("c")}),
		},
	})
	require.NoError(t, err)

	covered, anyUnknown, err := mgr.ShippedSegmentDocCount(ctx, kgtypes.GraphCode, "covRepo")
	require.NoError(t, err)
	require.Equal(t, 1024, covered, "covered sums HNSW doc_counts only (BM25 excluded — no double-count)")
	require.False(t, anyUnknown, "all HNSW metas have non-zero doc_count → anyUnknown is false")
	require.Equal(t, int64(0), cc.fetchCalls.Load(), "the coverage probe must NOT Fetch any blob")

	// Graph B: one HNSW segment with DocCount==0 (an old pre-doc_count blob) →
	// anyUnknown=true, covered counts only the non-zero HNSW metas.
	_, err = gc.Ship(ctx, &knowledgev1.ShipRequest{
		Target: &knowledgev1.GraphSelector{Graph: "code", Repo: "unknownRepo"},
		Blobs: []*knowledgev1.SegmentBlobProto{
			blobToProto(searchengine.SegmentBlob{ID: "h3", Format: "hnsw", DocCount: 512, Bytes: []byte("d")}),
			blobToProto(searchengine.SegmentBlob{ID: "h4", Format: "hnsw", DocCount: 0, Bytes: []byte("e")}),
		},
	})
	require.NoError(t, err)

	covered, anyUnknown, err = mgr.ShippedSegmentDocCount(ctx, kgtypes.GraphCode, "unknownRepo")
	require.NoError(t, err)
	require.Equal(t, 512, covered, "covered sums only the non-zero HNSW metas")
	require.True(t, anyUnknown, "an HNSW meta with doc_count==0 sets anyUnknown (conservative-unknown signal)")
}

// TestGraphSelectorMapping asserts the per-graph-type field routing mirrors the
// canonical graphTarget mapping.
func TestGraphSelectorMapping(t *testing.T) {
	cases := []struct {
		gt   kgtypes.GraphType
		name string
		want *knowledgev1.GraphSelector
	}{
		{kgtypes.GraphCode, "r", &knowledgev1.GraphSelector{Graph: "code", Repo: "r"}},
		{kgtypes.GraphCloud, "acct", &knowledgev1.GraphSelector{Graph: "cloud", Account: "acct"}},
		{kgtypes.GraphCICD, "acct", &knowledgev1.GraphSelector{Graph: "cicd", Account: "acct"}},
		{kgtypes.GraphKnowledge, "kg", &knowledgev1.GraphSelector{Graph: "knowledge", Name: "kg"}},
	}
	for _, tc := range cases {
		got := graphSelector(tc.gt, tc.name)
		require.Equal(t, tc.want.GetGraph(), got.GetGraph())
		require.Equal(t, tc.want.GetRepo(), got.GetRepo())
		require.Equal(t, tc.want.GetAccount(), got.GetAccount())
		require.Equal(t, tc.want.GetName(), got.GetName())
	}
}
