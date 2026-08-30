// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestStageRebuildPartitionStagesWithoutShipping is THE CATCHER for the staging entry
// point that replaced the Add-only and seal-only quartet: a partition handed to a reset
// must reach NEITHER serving engine, must ship and publish nothing, and must stay
// separate from the partitions around it.
//
// EVERY HALF IS A DEFECT THIS ARC CLOSED. A partition that reached a serving engine
// polluted the corpus being replaced one bucket at a time, and the finalize then
// published the union of every layer that engine had held — measured live as three bm25
// blobs where one was correct. A partition that shipped would reintroduce the per-group
// ship this design removed. And partitions that merged into one staged entry would mix
// two buckets' membership into a single segment, which is the mixing the retired seal
// existed to prevent and which staging must go on preventing.
func TestStageRebuildPartitionStagesWithoutShipping(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const n = 500 // far below DefaultMinSegmentDocs (1024)
	require.Less(t, n, searchengine.DefaultMinSegmentDocs, "the fixture must be sub-threshold")

	t.Run("neither serving engine is touched, and nothing ships", func(t *testing.T) {
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		gt, name := kgtypes.GraphCode, "staging-untouched"

		require.NoError(t, mgr.StageRebuildPartition(ctx, gt, name, hnswVecDocs(n), vecContentDocs(n)))

		require.Empty(t, mgr.managerFor(gt, name).engine.Export(),
			"a staged partition must NOT reach the serving HNSW engine — that is the pollution staging exists to stop")
		require.Empty(t, mgr.bm25ManagerFor(gt, name).engine.Export(),
			"nor the serving BM25 engine, which is where the field corpus accumulated its extra layers")
		require.Empty(t, mgr.managerFor(gt, name).cache.Keys(),
			"staging must not PERSIST — the finalize is what writes, and staging that wrote early would\n\t\t\tmake a partial layer durable")
		require.Empty(t, mgr.bm25ManagerFor(gt, name).cache.Keys(),
			"nor persist the field share")
	})

	t.Run("one call carries both formats' share", func(t *testing.T) {
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		gt, name := kgtypes.GraphCode, "staging-both-formats"

		require.NoError(t, mgr.StageRebuildPartition(ctx, gt, name, hnswVecDocs(n), vecContentDocs(n)))

		// THE SINGLE CALL IS THE CORRECTNESS DEVICE: a caller cannot stage the vector
		// share and forget the field share, which is how the field corpus came to lag.
		staged := mgr.takeRebuildWork(gt, name)
		require.Len(t, staged.hnsw, 1, "one entry per staging call, per format")
		require.Len(t, staged.bm25, 1, "and the field share of the same call is staged alongside it")
		require.Len(t, staged.hnsw[0].Docs, n, "carrying every vector document it was given")
		require.Len(t, staged.bm25[0].Docs, n, "and every field document")
	})

	t.Run("successive partitions stay separate", func(t *testing.T) {
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		gt, name := kgtypes.GraphCode, "staging-successive"

		// Two sub-threshold groups staged in turn: two staged partitions, which the
		// finalize builds into two segments. Coalescing them here would mix two buckets'
		// membership into one segment — the failure the retired per-group seal guarded.
		require.NoError(t, mgr.StageRebuildPartition(ctx, gt, name, vecContentDocsSeed(10, 0), nil))
		require.NoError(t, mgr.StageRebuildPartition(ctx, gt, name, vecContentDocsSeed(10, 1000), nil))

		staged := mgr.takeRebuildWork(gt, name)
		require.Len(t, staged.hnsw, 2, "each staged group is its own partition")
		require.NotEqual(t, staged.hnsw[0].Bucket, staged.hnsw[1].Bucket,
			"and they carry distinct partition indices, or the finalize would build them as one")
		require.Empty(t, mgr.managerFor(gt, name).cache.Keys(), "still nothing persisted from staging alone")
	})
}

// bucketFixtureDocs builds n docs and returns them plus the bucket count they
// derive, failing the test if that count is not the expected one — a fixture that
// drifts onto a power-of-two boundary would re-emit every bucket and quietly
// invalidate the delta assertions below.
func bucketFixtureDocs(t *testing.T, n, wantBuckets int) []searchengine.Document {
	t.Helper()
	docs := vecContentDocs(n)
	got := searchengine.BucketCountFor(n)
	require.Equal(t, wantBuckets, got, "fixture of %d docs derives %d buckets, want %d", n, got, wantBuckets)
	return docs
}

// docsInDistinctBuckets picks want docs that each land in a different bucket, so a
// call can touch exactly that many partitions.
func docsInDistinctBuckets(t *testing.T, docs []searchengine.Document, bucketCount, want int) []searchengine.Document {
	t.Helper()
	seen := make(map[int]struct{}, want)
	out := make([]searchengine.Document, 0, want)
	for _, d := range docs {
		b := searchengine.BucketOf(d.ID, bucketCount)
		if _, dup := seen[b]; dup {
			continue
		}
		seen[b] = struct{}{}
		out = append(out, d)
		if len(out) == want {
			return out
		}
	}
	t.Fatalf("could not find %d docs in distinct buckets", want)
	return nil
}

// TestManagerReplaceBucketPublishesCompleteManifest proves a partial re-emit
// publishes the WHOLE resident corpus, not just the partitions it rewrote.
//
// This is the property that keeps a delta re-emit safe: the manifest is derived
// from the resident set, so the partitions nobody touched remain referenced and
// the server cannot reference-count them away. If a re-emit published only what it
// rebuilt, the six untouched partitions would be reaped and the corpus would
// silently shrink to the two that were rewritten.
// TestManagerReplaceBucketPublishesCompleteManifest WAS DELETED HERE. It asserted a
// bucket replacement published a manifest referencing the COMPLETE derived bucket set
// rather than a partial one. The manifest is deleted, so "what was published" has no
// referent. The completeness property survives on a different operand and is covered:
// the rebuild cardinality gate compares the DERIVED bucket count against the count the
// engine actually holds (TestRebuildCardinalityShortfallIsReported, tools package),
// and TestPartialLayerNeverRetiresAGoodLayer covers the partial-layer direction.
// TestManagerReplaceBucketShipsOncePerCall WAS DELETED HERE. Its subject was RPC
// BATCHING: a ReplaceBucket call touching N partitions had to issue exactly one Ship
// and one PublishManifest rather than N of each. Batching is a property of a network
// call, and there is no network call — writes go straight to the local cache, one
// blob at a time, and counting them would measure the corpus rather than the batching
// this test existed to pin. The mechanism is ABSENT, so no successor is owed. What
// ReplaceBucket still does durably is covered by
// TestEmbedDrainCoalescesMergesOntoReconcileTick below, which asserts the tick makes
// the serving set durable in L2.

// rewritten returns copies of docs that keep their ids but carry DIFFERENT content,
// which is what a re-embed produces: the node is the same, its vector is new.
//
// The distinction matters to any test that counts ships. A rebuild is convergent,
// so re-emitting byte-identical documents republishes the same segment ids and the
// ship diff is empty — correct behavior, and indistinguishable from a broken ship
// path if a fixture re-writes documents unchanged and then asserts a ship happened.
func rewritten(docs []searchengine.Document) []searchengine.Document {
	out := make([]searchengine.Document, len(docs))
	for i, d := range docs {
		vec := make([]byte, len(d.Vector))
		copy(vec, d.Vector)
		if len(vec) > 0 {
			vec[0] ^= 0xFF
		}
		out[i] = searchengine.Document{ID: d.ID, Vector: vec, Fields: d.Fields}
	}
	return out
}

// residentIDs is the set of segment ids currently resident on an engine.
func residentIDs[Q, S any](dm *distManager[Q, S]) map[searchengine.SegmentID]struct{} {
	out := make(map[searchengine.SegmentID]struct{})
	for _, b := range dm.engine.Export() {
		out[b.ID] = struct{}{}
	}
	return out
}

// replacedCount reports how many segments present before are gone after — the
// observable signature of a re-emit, since rebuilding a partition retires the
// segment that held it.
func replacedCount(before, after map[searchengine.SegmentID]struct{}) int {
	n := 0
	for id := range before {
		if _, survived := after[id]; !survived {
			n++
		}
	}
	return n
}

// TestEmbedDrainCoalescesMergesOntoReconcileTick is THE CATCHER for the coalescing
// premise: a write batch must not rebuild anything, and the deferred tick must
// rebuild at most once per partition the batch touched.
//
// If the write path re-emitted inline it would rebuild a partition per batch, and
// because ids are hash distributed a single batch touches most partitions of a
// corpus — which is the cost this whole design exists to avoid. Leg 1 fails
// immediately if that regression is reintroduced.
//
// TWO LEGS OVER ONE SEEDED CORPUS, because the two halves need opposite inputs.
// Leg 1 keeps the realistic write window (a full embed batch), which is what makes
// its no-rebuild assertion meaningful. That same window is useless for the tick
// BOUND: a batch that wide dirties every partition, so the bound equals the
// resident count and is satisfied by any behavior at all. Leg 2 therefore drives a
// deliberately NARROW batch pinned to three partitions, which is the only shape
// where "at most one rebuild per dirty partition" can fail.
func TestEmbedDrainCoalescesMergesOntoReconcileTick(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const corpus = 6144 // derives 8 buckets, clear of a doubling boundary
	const buckets = 8
	const drain = 100 // EmbedBatchSizeOrDefault
	require.Equal(t, buckets, searchengine.BucketCountFor(corpus), "layout count")

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "coalesce-tick"

	docs := bucketFixtureDocs(t, corpus, buckets)
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, docs))
	dm := mgr.managerFor(gt, name)

	beforeDrain := residentIDs(dm)
	require.Len(t, beforeDrain, buckets, "the seed lands one segment per partition")
	keysBefore := len(mgr.managerFor(gt, name).cache.Keys())

	// LEG 1 — REALISTIC WRITE. A drain re-writes the first `drain` documents with
	// NEW content — the shape a re-embed carries, where a node keeps its id and gets
	// a fresh vector. Re-writing them unchanged would encode to the same bytes and
	// ship nothing, because the rebuild is convergent.
	batch := rewritten(docs[:drain])
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, batch))

	// Nothing rebuilt, nothing shipped. Every seed segment survives and the only
	// additions are the sealed tails.
	//
	// THE EXPECTED TAIL COUNT IS THE BATCH'S OWN PARTITION SPREAD, derived here by
	// hashing the batch's ids rather than read back off the engine, so it states what
	// the write path OWES rather than restating what it did. A write seals one segment
	// per partition its ids occupy: a batch-wide seal would produce one segment
	// spanning every partition, and a delete arriving before the tick would then close
	// over all of them.
	tailPartitions := map[int]struct{}{}
	for _, d := range batch {
		tailPartitions[searchengine.BucketOf(d.ID, buckets)] = struct{}{}
	}
	afterDrain := residentIDs(dm)
	require.Zero(t, replacedCount(beforeDrain, afterDrain),
		"a drain must not rebuild any partition — it only seals tails")
	require.Len(t, afterDrain, buckets+len(tailPartitions),
		"the drain adds exactly one tail segment per partition the batch touches (%d partitions)",
		len(tailPartitions))
	require.Len(t, mgr.managerFor(gt, name).cache.Keys(), keysBefore, "a drain must not PERSIST")

	// Clear leg 1's dirty window so leg 2 starts from one segment per partition.
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
	require.Len(t, residentIDs(dm), buckets, "leg 1's tick retires the tail")
	require.Subset(t, mgr.managerFor(gt, name).cache.Keys(), residentIDs(dm),
		"leg 1's tick makes the serving set DURABLE — every resident id is in L2")

	// LEG 2 — BOUNDED TICK. Drawn from BEYOND leg 1's window: those documents still
	// hold their seed content, so rewriting them genuinely changes their partitions.
	// Drawing from docs[:drain] instead would re-apply leg 1's edit, the rebuild
	// would be convergent, and the tick would replace only the tail.
	subset := rewritten(docsInDistinctBuckets(t, docs[drain:], buckets, 3))
	dirtyBuckets := make(map[int]struct{}, len(subset))
	for _, d := range subset {
		dirtyBuckets[searchengine.BucketOf(d.ID, buckets)] = struct{}{}
	}
	require.Len(t, dirtyBuckets, 3, "the subset is pinned to exactly three partitions")

	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, subset))
	beforeTick := residentIDs(dm)
	// The subset is pinned to three partitions and the write seals one tail per
	// partition, so the tail count IS the dirty-partition count.
	require.Len(t, beforeTick, buckets+len(dirtyBuckets),
		"the narrow drain adds exactly one tail segment per dirty partition")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
	afterTick := residentIDs(dm)
	rebuilt := replacedCount(beforeTick, afterTick)
	// THE CEILING IS TWO PER DIRTY PARTITION: one rebuilt partition segment, plus the
	// one tail that partition's writes sealed. It was one-per-partition-plus-one when a
	// batch sealed a single tail for the whole window.
	bound := 2 * len(dirtyBuckets)
	t.Logf("LEG2 dirtyBuckets=%d bound=%d rebuilt=%d ceiling=%d",
		len(dirtyBuckets), bound, rebuilt, len(beforeTick))
	require.LessOrEqual(t, rebuilt, bound,
		"the tick rebuilds at most one segment per dirty partition (plus retiring that partition's tail)")
	require.Positive(t, rebuilt, "the tick must actually re-emit the dirty partitions")
	require.Len(t, afterTick, buckets, "the tail segment is retired by the tick")
	require.Subset(t, mgr.managerFor(gt, name).cache.Keys(), afterTick,
		"leg 2's tick likewise persists what it left resident")
}

// seedShipped drives documents to a DURABLE, published state the way a fixture
// needs: mark them for re-emit, then run the re-emit that ships and publishes.
//
// It is the setup replacement for the retired Add-then-ship entry point. Tests
// whose SUBJECT is when a write ships must not use it — they need to drive the
// write and the re-emit separately so they can observe what happens between.
//
// It takes testing.TB so benchmarks can seed the same way tests do.
func seedShipped(
	t testing.TB, ctx context.Context, mgr *Manager,
	gt kgtypes.GraphType, name string, docs []searchengine.Document,
) {
	t.Helper()
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
}

// seedShippedFields is the field-engine counterpart of seedShipped.
func seedShippedFields(
	t testing.TB, ctx context.Context, mgr *Manager,
	gt kgtypes.GraphType, name string, docs []searchengine.Document,
) {
	t.Helper()
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
}

// TestReEmitRetriesSkippedPublish covers the durability hole a SKIPPED publish
// would otherwise open.
//
// A skip is not an error: the blobs are already on the server, but the manifest
// still names the segments the re-emit retired, so the new content is unreferenced
// and reference-count-collectable. If the tick cleared its backlog anyway, nothing
// would ever republish — the graph has already been re-emitted in memory and the
// tails are gone, so no later write would notice. The backlog must therefore
// survive a skip, and a subsequent tick must re-attempt the publish even with no
// new documents to contribute.
// TestReEmitRetriesSkippedPublish WAS DELETED HERE. Its subject was the
// publish-retry latch: a tick whose manifest publish was SKIPPED had to leave a
// pending-retry bit set so a later tick re-attempted it. There is no publish, no
// manifest and no retry latch, so the mechanism is ABSENT rather than weakened and
// no successor is owed. The durability question it ultimately guarded — did the
// tick's content actually reach the store — is now answered directly against the L2
// cache by the sibling tests in this file.
