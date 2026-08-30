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

// TestManagerAddAndMarkDirtySealsOneBlob wires a production Manager over the HNSW
// format and a counting caller, marks enough docs dirty to seal exactly one
// segment, and asserts the write path itself ships NOTHING — durability is the
// reconcile tick's job now. The tick then re-emits the graph's partitions and
// ships them in exactly one Ship RPC. A second ship() with no intervening Add
// ships NOTHING (Export-diff no-op for the already-shipped content hash).
// Criterion: Phase 3 Step 1.
func TestManagerAddAndMarkDirtySealsOneBlob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// MinSegmentDocs default is 1024; seal exactly one segment with 1024 docs.
	const n = 1024
	docs := hnswVecDocs(n)

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, "repoHNSW", docs))
	require.Empty(t, mgr.managerFor(kgtypes.GraphCode, "repoHNSW").cache.Keys(),
		"the write path force-seals but never PERSISTS — durability is the reconcile tick's job")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "repoHNSW"))
	require.NotEmpty(t, mgr.managerFor(kgtypes.GraphCode, "repoHNSW").cache.Keys(),
		"the reconcile tick is what writes the sealed partitions to L2")

	// After the tick the resident set is PARTITION-shaped, and how many partitions
	// that is falls out of the corpus size rather than out of anything this test
	// controls. Segment SHAPE is not the subject here, so assert COVERAGE: every
	// shipped blob carries the HNSW format and together they index exactly the
	// embedded ids.
	dm := mgr.managerFor(kgtypes.GraphCode, "repoHNSW")
	exported := dm.engine.Export()
	require.NotEmpty(t, exported, "the tick leaves the corpus resident as partitions")
	covered := 0
	for _, blob := range exported {
		require.Equal(t, hnsw.New().Name(), blob.Format, "shipped blob is tagged with the versioned HNSW format name")
		seg, err := hnsw.New().Decode(blob.Bytes)
		require.NoError(t, err)
		covered += len(seg.IDs())
	}
	require.Equal(t, n, covered, "the resident partitions index exactly the embedded ids")

	// THE EXPORT-DIFF NO-OP ASSERTION WAS REMOVED HERE, not lost. It re-ran ship()
	// with no intervening Add and asserted zero new Ship RPCs and zero new blobs —
	// a property of the ship diff against the shipped-id set, and the ship leg, that
	// set and the RPC are all gone. Its surviving half is that re-writing identical
	// content-hash blobs adds nothing, which TestSegmentDistributionE2E asserts
	// directly against the cache. What this test still owns is the SEAL: one
	// AddAndMarkDirty pass seals exactly the partitions the embedded ids require.
}

// TestManagerFlushSealsSubThresholdTail is the steady-state searchability +
// bounded-segment proof: a graph with FEWER than MinSegmentDocs (1024) embeddable
// nodes, written incrementally, holds an unsealed sub-threshold tail that seals
// ZERO segments and is therefore unsearchable. The quiescence Flush force-seals
// that tail into exactly ONE searchable segment and ships it — and a redundant
// re-flush is a cheap no-op (no segment-count blowup).
//
// The buffered half is built DIRECTLY ON THE ENGINE. That state is no longer
// reachable through the Manager write surface at all: the write path force-seals
// every batch, so no Manager call can leave a tail buffered across it. The engine
// is the only remaining constructor of the precondition Flush exists to resolve,
// and Flush's contract stays live for the migration/one-shot path.
func TestManagerFlushSealsSubThresholdTail(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// 500 < MinSegmentDocs(1024): the incremental backlog never seals a segment.
	const n = 500
	docs := hnswVecDocs(n)
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	dm := mgr.managerFor(kgtypes.GraphCode, "smallRepo")
	require.NoError(t, dm.engine.Add(docs))
	require.Empty(t, dm.engine.Export(), "a sub-1024 incremental backlog seals ZERO segments — unsearchable")
	require.Empty(t, dm.cache.Keys(), "an unsealed sub-threshold backlog PERSISTS NOTHING")

	// Quiescence Flush: force-seal the tail. It becomes exactly ONE searchable
	// segment indexing all the ids, shipped in exactly one Ship RPC carrying one blob.
	require.NoError(t, mgr.Flush(ctx, kgtypes.GraphCode, "smallRepo"))

	exported := dm.engine.Export()
	require.Len(t, exported, 1, "Flush seals the sub-threshold tail into exactly ONE segment")
	require.Equal(t, hnsw.New().Name(), exported[0].Format, "the sealed tail is an hnsw segment")
	seg, err := hnsw.New().Decode(exported[0].Bytes)
	require.NoError(t, err)
	require.Len(t, seg.IDs(), n, "the one sealed segment indexes every embedded id — searchable")

	require.Len(t, dm.cache.Keys(), 1,
		"Flush persists the sealed tail as exactly ONE blob — no blowup")

	// Bounded: a redundant re-Flush on an already-drained buffer is a cheap no-op —
	// the segment count stays at ONE and no new Ship RPC fires.
	require.NoError(t, mgr.Flush(ctx, kgtypes.GraphCode, "smallRepo"))
	require.Len(t, dm.engine.Export(), 1, "re-Flush does not multiply segments")
	require.Len(t, dm.cache.Keys(), 1, "re-Flush persists nothing new (no blowup)")
}

// TestManagerRoutesPerGraph asserts two distinct graphs get distinct engines and
// independent ship state.
func TestManagerRoutesPerGraph(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	docsA := hnswVecDocs(1024)
	docsB := hnswVecDocs(1024)

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	seedShipped(t, ctx, mgr, kgtypes.GraphCode, "repoA", docsA)
	seedShipped(t, ctx, mgr, kgtypes.GraphKnowledge, "kg", docsB)

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

// TestManagerAddAndMarkDirtyFieldsSealsBM25Blob is Phase 3 Step 2's criterion on
// the new vehicle: the field write path builds BM25 segments from field-bearing
// Documents through the BM25 engine but ships nothing, and the reconcile tick then
// ships the re-emitted partitions in exactly one Ship RPC with one bm25-format
// blob. An empty-diff re-ship (no intervening Add) is a no-op.
func TestManagerAddAndMarkDirtyFieldsSealsBM25Blob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// MinSegmentDocs default is 1024; seal exactly one segment with 1024 docs.
	const n = 1024
	docs := bm25FieldDocs(n)

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	// BOTH READS NAME THE GRAPH THAT WAS WRITTEN. They used to name code/"repoBM25"
	// while the writes went to knowledge/"kgBM25", so the before-assertion was empty
	// for a graph nothing had touched — trivially true — and the after-assertion could
	// only ever fail. A per-graph cache read has to name the same graph as the write
	// or it is measuring a different directory.
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, kgtypes.GraphKnowledge, "kgBM25", docs))
	require.Empty(t, mgr.bm25ManagerFor(kgtypes.GraphKnowledge, "kgBM25").cache.Keys(),
		"the field write path force-seals but never PERSISTS")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphKnowledge, "kgBM25"))
	require.NotEmpty(t, mgr.bm25ManagerFor(kgtypes.GraphKnowledge, "kgBM25").cache.Keys(),
		"the reconcile tick is what writes the sealed BM25 blob to L2")

	// Segment shape is not the subject — assert COVERAGE over the re-emitted
	// partitions: every shipped blob is bm25-tagged and together they index exactly
	// the embedded ids.
	dm := mgr.bm25ManagerFor(kgtypes.GraphKnowledge, "kgBM25")
	exported := dm.engine.Export()
	require.NotEmpty(t, exported, "the tick leaves the corpus resident as partitions")
	covered := 0
	for _, blob := range exported {
		require.Equal(t, bm25.New().Name(), blob.Format, "shipped blob is tagged with this engine's format")
		seg, err := bm25.New().Decode(blob.Bytes)
		require.NoError(t, err)
		covered += len(seg.IDs())
	}
	require.Equal(t, n, covered, "the resident partitions index exactly the embedded ids")

	// THE EMPTY-DIFF RE-SHIP ASSERTION WAS REMOVED HERE, for the same reason as its
	// HNSW twin above: it measured the ship diff against the shipped-id set, and that whole
	// mechanism is deleted. The determinism it rested on — BM25 Build producing a
	// byte-identical segment, hence an identical content hash — is still true and is
	// what makes the cache-level idempotency in TestSegmentDistributionE2E hold. What
	// this test still owns is that one field-Add seals a BM25 blob.
}

// TestManagerHoldsBothFormatMaps asserts ONE Manager owns BOTH formats per graph:
// the HNSW and BM25 maps are distinct, each format's ManagerFor lazily constructs
// (and memoizes) its own distManager, and the same graph routed through both
// formats yields two distinct engines.
func TestManagerHoldsBothFormatMaps(t *testing.T) {
	t.Parallel()

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

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
	t.Parallel()

	base := t.TempDir()
	hnswDir := graphCacheDirFor(base, kgtypes.GraphCode, "repo", hnsw.New().Name())
	bm25Dir := graphCacheDirFor(base, kgtypes.GraphCode, "repo", bm25.New().Name())
	require.NotEqual(t, hnswDir, bm25Dir, "HNSW and BM25 caches must root under format-distinct dirs")
	require.Contains(t, hnswDir, hnsw.New().Name())
	require.Contains(t, bm25Dir, bm25.New().Name())
}

// TWO REGISTRY-PROBE TESTS WERE DELETED HERE, each with its successor named.
//
// TestHasShippedSegments drove a List(sinceGen=0) through the source and asserted a
// graph probes present/absent by whether the REGISTRY held metas. There is no
// registry to probe. The surviving presence signal is local and structural —
// resident == 0 against a populated corpus — and it is owned by the bootstrap heal
// tests (segment_local_presence_test.go and segment_oss_heal_test.go), which step 5.4
// keeps for exactly this reason.
//
// TestShippedSegmentDocCount asserted the coverage probe summed HNSW meta.DocCount,
// EXCLUDED BM25 metas to avoid double-counting the same nodes, and flagged
// an unknown-count flag on a zero doc_count. All three rest on deleted machinery: the
// metas came from a server List, the flag is deleted, and ShippedSegmentDocCount now
// routes to a resident read whose signature no longer carries it. The FORMAT-SEPARATION
// half of it survives and is stronger than the filter it replaces: HNSW and BM25 now
// occupy separate cache roots, so BM25 blobs cannot be seen from the HNSW probe at
// all rather than being filtered out of a shared list. That is asserted by
// TestFormatFamiliesAreDisjointOnDisk. The probe itself is covered by the
// manage_status coverage tests, which drive the resident path.

// TestManagerResidentDocCount is the live-resident accessor criterion: after a
// graph seals a segment locally, Manager.ResidentDocCount returns the SAME figure
// the underlying engine's ResidentDocCount reports (the in-memory searchable
// coverage), and a graph that was never added/loaded returns 0.
func TestManagerResidentDocCount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	// Seal exactly one segment (1024 docs == MinSegmentDocs) locally — the write
	// seals it into the engine (resident) and the tick ships it.
	docs := hnswVecDocs(1024)
	seedShipped(t, ctx, mgr, kgtypes.GraphCode, "residentRepo", docs)

	// The Manager accessor matches the engine's own resident snapshot.
	engineResident := mgr.managerFor(kgtypes.GraphCode, "residentRepo").engine.ResidentDocCount()
	require.Equal(t, 1024, engineResident, "the sealed segment's docs are resident in the engine")
	require.Equal(t, engineResident, mgr.ResidentDocCount(kgtypes.GraphCode, "residentRepo"),
		"Manager.ResidentDocCount mirrors the live engine resident count")

	// A never-added (never-searched/loaded) graph lazily constructs an empty engine
	// → zero resident.
	require.Equal(t, 0, mgr.ResidentDocCount(kgtypes.GraphCode, "neverTouchedRepo"),
		"an unloaded graph has zero resident docs")
}

// TestGraphSelectorMapping asserts the per-graph-type field routing mirrors the
// canonical graphTarget mapping.
func TestGraphSelectorMapping(t *testing.T) {
	t.Parallel()

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
