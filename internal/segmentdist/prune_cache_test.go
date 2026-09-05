// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// plantOrphan writes a junk <id>.seg into the per-(graph,format) L2 cache dir AFTER
// the Manager + its diskSegmentCache already exist (so scanExisting has run and the
// id is NOT in the in-memory index). This is the T2-1 regression shape: the
// index-gated diskSegmentCache.Remove would silently no-op this file; only a direct
// os.Remove unlinks it. Returns the absolute path + byte size.
func plantOrphan(t *testing.T, dir, id string, size int) (string, int64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o750))
	path := filepath.Join(dir, id+".seg")
	junk := make([]byte, size)
	for i := range junk {
		junk[i] = byte('x')
	}
	require.NoError(t, os.WriteFile(path, junk, 0o600))
	return path, int64(size)
}

// poolReport finds the per-(graph,format) report for the given format, or nil.
func poolReport(rep PruneCacheReport, format string) *PruneCacheGraphReport {
	for i := range rep.Graphs {
		if rep.Graphs[i].Format == format {
			return &rep.Graphs[i]
		}
	}
	return nil
}

// TestPruneCacheForceLoad proves forceCompleteLiveSet makes Export() COMPLETE even
// after a live segment was Unloaded (resident-only Export would miss it).
func TestPruneCacheForceLoad(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	// 512 keeps the graph inside a SINGLE partition through the tick (the tick counts
	// the incoming window alongside the resident set), so the unload/force-load
	// round trip below has exactly one segment to reason about.
	require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, "forceRepo", hnswVecDocs(512)))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "forceRepo"))
	dm := mgr.managerFor(kgtypes.GraphCode, "forceRepo")
	require.Len(t, dm.engine.Export(), 1, "one shipped segment after the tick")

	// load() to populate the resident-tracking map (recordResident runs in load/reload,
	// NOT in Add/ship), so the eviction has a segment to drop. load is idempotent
	// by segment id, so re-listing this process's own shipped tail is harmless.
	require.NoError(t, dm.load(ctx))

	// Drop the live segment from resident: a bare Export() now misses it.
	unloaded := dm.evictAllResidentForTest()
	require.NotEmpty(t, unloaded, "the eviction drops the resident segment")
	require.Empty(t, dm.engine.Export(), "resident-only Export is now empty (the false-prune trap)")

	// forceCompleteLiveSet resets the load floor + reloads → Export complete again.
	ids, err := dm.forceCompleteLiveSet(ctx)
	require.NoError(t, err)
	require.Len(t, ids, 1, "force-load restores the unloaded-but-live segment into the live set")
}

// TestPruneCacheCoversTheRebuiltLayer proves a blob a RESET rebuild just published is in
// the HNSW live set, so PruneCache can never reap the layer the graph is being served
// from.
//
// THE MECHANISM CHANGED UNDER THIS TEST AND THE CLAIM DID NOT, which is why it is
// rewritten rather than retired. It used to prove completeHNSWLiveSet UNIONED a second,
// deterministic engine's export, because the rebuild wrote that engine and the embed
// engine could not see it. The reset now finalizes at the SERVING engine — there is no
// second export to union — so the union is what has become uninteresting, not the
// property. What must stay true is what an operator depends on: freshly rebuilt content
// is live, never an orphan.
func TestPruneCacheCoversTheRebuiltLayer(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	// Stage one partition, then the single finalize that builds, ships and swaps it in.
	// BOTH formats are staged: Swapped is the AND of the two legs, so staging only the
	// vector share would leave the field leg reporting not-swapped and the assertion
	// below would fail for a reason that has nothing to do with the live set.
	corpus := vecContentDocs(1024)
	require.NoError(t, mgr.StageRebuildPartition(ctx, kgtypes.GraphCode, "detRepo", corpus, corpus))
	res, err := mgr.FinalizeRebuild(ctx, kgtypes.GraphCode, "detRepo")
	require.NoError(t, err)
	require.True(t, res.Swapped, "the reset's publish must LAND, or the live set below is about nothing")

	export := mgr.managerFor(kgtypes.GraphCode, "detRepo").engine.Export()
	require.Len(t, export, 1, "the serving engine holds the one partition this run built")
	builtID := export[0].ID

	live, err := mgr.completeHNSWLiveSet(ctx, kgtypes.GraphCode, "detRepo")
	require.NoError(t, err)
	_, present := live[builtID]
	require.True(t, present, "the rebuilt blob IS in the HNSW live set — PruneCache must never treat it as an orphan")
}

// TestPruneCacheOnDisk covers listOnDiskSegIDs: missing dir => empty+nil; a dir with
// .seg files returns their ids + FileInfo sizes (and skips non-.seg).
func TestPruneCacheOnDisk(t *testing.T) {
	t.Parallel()

	// Missing dir => empty, nil error.
	segs, err := listOnDiskSegIDs(filepath.Join(t.TempDir(), "nope"))
	require.NoError(t, err)
	require.Empty(t, segs)

	// Two .seg files + one non-.seg (must be skipped).
	dir := t.TempDir()
	plantOrphan(t, dir, "aaaa", 10)
	plantOrphan(t, dir, "bbbb", 20)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notaseg.txt"), []byte("x"), 0o600))

	segs, err = listOnDiskSegIDs(dir)
	require.NoError(t, err)
	require.Len(t, segs, 2, "only .seg files are returned")
	byID := map[string]int64{}
	for _, s := range segs {
		byID[s.id] = s.bytes
	}
	require.EqualValues(t, 10, byID["aaaa"])
	require.EqualValues(t, 20, byID["bbbb"])
}

// TestPruneCacheSubset WAS DELETED HERE. It proved the subset predicate returned
// false when the computed live set held an id absent from the SERVER's listing — a
// cross-check of the local live set against a second, independent authority.
//
// NO SUCCESSOR IS OWED, and re-pointing it would have been actively wrong. The live
// set is now DERIVED from L2 by forceCompleteLiveSet, so "is the live set a subset of
// L2" compares the cache against itself. That is the mirrors-are-not-cross-checks
// shape this ticket forbids as compensation: a second reading of one authority is not
// a second authority. The guard the subset check backed — never unlink an id that is
// still live — is now carried by forceCompleteLiveSet itself and by the five
// signatures in restart_falseprune_test.go.

// TestPruneCacheDriverDryRun proves execute=false reports the planted orphan + its
// bytes and DELETES NOTHING (the .seg still exists after).
func TestPruneCacheDriverDryRun(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	seedShipped(t, ctx, mgr, kgtypes.GraphCode, "dryRepo", hnswVecDocs(1024))

	hnswDir := graphCacheDirFor(mgr.cacheDir, kgtypes.GraphCode, "dryRepo", hnsw.New().Name())
	orphanPath, orphanBytes := plantOrphan(t, hnswDir, "orphan-dryrun", 777)

	rep, err := mgr.PruneCache(ctx, []PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: "dryRepo"}}, false)
	require.NoError(t, err)
	require.Equal(t, 0, rep.Removed, "dry run removes nothing")
	require.EqualValues(t, 0, rep.RemovedBytes)

	pool := poolReport(rep, hnsw.New().Name())
	require.NotNil(t, pool)
	require.Contains(t, pool.Orphans, "orphan-dryrun", "dry run REPORTS the orphan")
	require.Equal(t, orphanBytes, pool.Bytes, "dry run reports the orphan bytes")

	_, statErr := os.Stat(orphanPath)
	require.NoError(t, statErr, "dry run must NOT delete the .seg")
}

// TestPruneCacheDriverExecute proves execute=true UNLINKS ONLY the orphan (via direct
// os.Remove of a file NOT in the cache index — the T2-1 regression), leaves the live
// .seg untouched, and reports Removed/RemovedBytes. It exercises BOTH formats.
func TestPruneCacheDriverExecute(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	// Ship a real HNSW + BM25 corpus (warms each format's L2 cache with real .seg files).
	seedShipped(t, ctx, mgr, kgtypes.GraphCode, "execRepo", hnswVecDocs(1024))
	seedShippedFields(t, ctx, mgr, kgtypes.GraphCode, "execRepo", bm25FieldDocs(1024))

	hnswDir := graphCacheDirFor(mgr.cacheDir, kgtypes.GraphCode, "execRepo", hnsw.New().Name())
	bm25Dir := graphCacheDirFor(mgr.cacheDir, kgtypes.GraphCode, "execRepo", bm25.New().Name())

	// Capture the live .seg files (must survive). Plant an orphan in EACH format dir
	// AFTER construction+ship (post-scanExisting → not in the in-memory index).
	liveHNSW := liveSegPaths(t, hnswDir)
	liveBM25 := liveSegPaths(t, bm25Dir)
	require.NotEmpty(t, liveHNSW, "a live HNSW .seg exists on disk")
	require.NotEmpty(t, liveBM25, "a live BM25 .seg exists on disk")

	hOrphan, hBytes := plantOrphan(t, hnswDir, "orphan-hnsw", 333)
	bOrphan, bBytes := plantOrphan(t, bm25Dir, "orphan-bm25", 444)

	rep, err := mgr.PruneCache(ctx, []PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: "execRepo"}}, true)
	require.NoError(t, err)

	// Both orphans removed, both pools pruned independently.
	require.Equal(t, 2, rep.Removed, "exactly the two planted orphans (one per format) removed")
	require.Equal(t, hBytes+bBytes, rep.RemovedBytes, "RemovedBytes sums both orphan sizes")

	// The orphans are GONE (the direct-os.Remove proof — would FAIL against index-gated cache.Remove).
	_, hStat := os.Stat(hOrphan)
	require.True(t, os.IsNotExist(hStat), "the planted HNSW orphan .seg is unlinked")
	_, bStat := os.Stat(bOrphan)
	require.True(t, os.IsNotExist(bStat), "the planted BM25 orphan .seg is unlinked")

	// Every live .seg survives.
	for _, p := range append(liveHNSW, liveBM25...) {
		_, statErr := os.Stat(p)
		require.NoError(t, statErr, "live segment %s must survive the prune", p)
	}

	hPool := poolReport(rep, hnsw.New().Name())
	require.NotNil(t, hPool)
	require.Equal(t, []searchengine.SegmentID{"orphan-hnsw"}, hPool.Orphans)
	bPool := poolReport(rep, bm25.New().Name())
	require.NotNil(t, bPool)
	require.Equal(t, []searchengine.SegmentID{"orphan-bm25"}, bPool.Orphans)
}

// TestPruneCacheUnloadedButLiveSurvives proves an unloaded-but-live segment is NEVER
// pruned: drop it from resident, then execute a prune — force-load restores it so it
// is not an orphan and its .seg survives.
func TestPruneCacheUnloadedButLiveSurvives(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	// 512 keeps the graph inside a SINGLE partition through the tick, so exactly one
	// .seg lands on disk to unload and then rescue.
	require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, "unloadRepo", hnswVecDocs(512)))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "unloadRepo"))

	hnswDir := graphCacheDirFor(mgr.cacheDir, kgtypes.GraphCode, "unloadRepo", hnsw.New().Name())
	live := liveSegPaths(t, hnswDir)
	require.Len(t, live, 1, "one live HNSW .seg on disk")

	// Drop the live segment from resident: a resident-only diff would mark it orphaned.
	// load() first so the resident-tracking map is populated (recordResident runs in
	// load/reload, not Add/ship); load is idempotent by id.
	dm := mgr.managerFor(kgtypes.GraphCode, "unloadRepo")
	require.NoError(t, dm.load(ctx))
	require.NotEmpty(t, dm.evictAllResidentForTest())
	require.Empty(t, dm.engine.Export(), "resident-only Export is empty after unload")

	rep, err := mgr.PruneCache(ctx, []PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: "unloadRepo"}}, true)
	require.NoError(t, err)
	require.Equal(t, 0, rep.Removed, "the unloaded-but-live segment is NOT pruned (force-load restored it)")

	_, statErr := os.Stat(live[0])
	require.NoError(t, statErr, "the unloaded-but-live .seg must survive")
}

// TestPruneCacheList0Abort WAS DELETED HERE. It proved that a live set holding an id
// the SERVER's List(0) did not return aborted the pool: nothing removed, the planted
// orphan left on disk, and the refusal reported rather than silent.
//
// NO SUCCESSOR IS OWED, for the same reason recorded above the deleted
// TestPruneCacheSubset. The subset check compared the local live set against a SECOND,
// independent authority; that authority is gone, and comparing the cache against
// itself is the mirrors-are-not-cross-checks shape this ticket forbids as
// compensation. Its fixtures went with it — a sealed blob built from a deliberately
// foreign corpus existed only to make a resident id that the server had never seen,
// and there is no server for an id to be foreign to.
//
// THE REFUSAL MACHINERY ITSELF SURVIVES and is still asserted: PruneCache still
// reports an Aborted pool with a reason, for the EMPTY-live-set condition, and
// prune_cache_empty_refusal_test.go drives both directions of it.

// TestPruneCacheNoOpEmptyGraph proves a never-shipped graph (no live set, no on-disk
// segments) is a clean no-op.
func TestPruneCacheNoOpEmptyGraph(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	rep, err := mgr.PruneCache(ctx, []PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: "emptyRepo"}}, true)
	require.NoError(t, err)
	require.Equal(t, 0, rep.Removed)
	require.EqualValues(t, 0, rep.RemovedBytes)
	for _, g := range rep.Graphs {
		require.Empty(t, g.Orphans, "no orphans for an empty graph")
		require.False(t, g.Aborted, "a graph with nothing on disk is not a refusal — there is nothing to refuse")
	}
}

// TestPruneCacheLiveSearchAfterPrune is the end-to-end no-false-prune proof: after an
// execute=true prune over a graph with a planted orphan, a live Search returns the
// full shipped corpus (every live segment still searchable).
func TestPruneCacheLiveSearchAfterPrune(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	docs := hnswVecDocs(1024)
	seedShipped(t, ctx, mgr, kgtypes.GraphCode, "searchRepo", docs)

	// Search BEFORE the prune to establish the searchable corpus size.
	before := mgr.managerFor(kgtypes.GraphCode, "searchRepo").engine.ResidentDocCount()
	require.Equal(t, 1024, before, "the full corpus is resident before the prune")

	hnswDir := graphCacheDirFor(mgr.cacheDir, kgtypes.GraphCode, "searchRepo", hnsw.New().Name())
	plantOrphan(t, hnswDir, "orphan-search", 99)

	rep, err := mgr.PruneCache(ctx, []PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: "searchRepo"}}, true)
	require.NoError(t, err)
	require.Equal(t, 1, rep.Removed, "only the planted orphan removed")

	// A live Search over the graph still returns the full shipped corpus — no live
	// segment was false-pruned.
	hits, err := mgr.Search(ctx, kgtypes.GraphCode, "searchRepo", "", docs[0].Vector, 10)
	require.NoError(t, err)
	require.NotEmpty(t, hits, "the graph is still searchable after the prune")

	after := mgr.managerFor(kgtypes.GraphCode, "searchRepo").engine.ResidentDocCount()
	require.Equal(t, 1024, after, "every live doc remains searchable after the prune (no false-prune)")
}

// liveSegPaths returns the absolute paths of every .seg file currently under dir.
func liveSegPaths(t *testing.T, dir string) []string {
	t.Helper()
	segs, err := listOnDiskSegIDs(dir)
	require.NoError(t, err)
	paths := make([]string, 0, len(segs))
	for _, s := range segs {
		paths = append(paths, filepath.Join(dir, s.id+".seg"))
	}
	return paths
}
