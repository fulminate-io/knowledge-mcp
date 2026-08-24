// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"math/rand/v2"
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

	_, gc := newSegmentHarness(t)
	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))

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
	t.Parallel()

	_, gc := newSegmentHarness(t)
	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))

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

// TestPruneCacheSubset proves liveSetSubsetOfList0 returns false when the computed
// live set contains an id absent from the server's List(0) — the subset-abort guard.
func TestPruneCacheSubset(t *testing.T) {
	t.Parallel()

	_, gc := newSegmentHarness(t)
	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	seedShipped(t, ctx, mgr, kgtypes.GraphCode, "subsetRepo", hnswVecDocs(1024))
	dm := mgr.managerFor(kgtypes.GraphCode, "subsetRepo")

	// The genuinely-shipped live set IS a subset of List(0).
	live, err := mgr.completeHNSWLiveSet(ctx, kgtypes.GraphCode, "subsetRepo")
	require.NoError(t, err)
	ok, err := dm.liveSetSubsetOfList0(ctx, live)
	require.NoError(t, err)
	require.True(t, ok, "the genuinely-shipped live set is a subset of List(0)")

	// Inject a phantom id never shipped to the server → live ⊄ List(0).
	live["phantom-never-shipped"] = struct{}{}
	ok, err = dm.liveSetSubsetOfList0(ctx, live)
	require.NoError(t, err)
	require.False(t, ok, "a live set with an unshipped id is NOT a subset of List(0)")
}

// TestPruneCacheDriverDryRun proves execute=false reports the planted orphan + its
// bytes and DELETES NOTHING (the .seg still exists after).
func TestPruneCacheDriverDryRun(t *testing.T) {
	t.Parallel()

	_, gc := newSegmentHarness(t)
	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
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
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	_ = svc
	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))

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

	_, gc := newSegmentHarness(t)
	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
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

// TestPruneCacheList0Abort proves the subset-abort SKIPS a pool whose computed live
// set is not a subset of List(0): nothing is removed for that pool even with a planted
// orphan present.
//
// DEFENSIVE GUARD (post-determinism note): the live⊄List(0) condition this test
// exercises no longer arises from build non-determinism. The HNSW builder is
// deterministic, so a writer's freshly-built segment hashes to the same id the server
// would compute — a resident id is normally always present on the server. The
// subset-abort still matters as a SAFETY guard for the genuine cases where a resident
// id is legitimately absent from List(0): a segment imported from another source/writer
// that this server never received, or a server that lost a blob. We construct that
// condition ON PURPOSE below by importing a segment built from a GENUINELY DIFFERENT
// corpus (distinct vectors + ids) that was never shipped here — an honest injection of
// a foreign id, not a reliance on two builds of the same data diverging.
func TestPruneCacheList0Abort(t *testing.T) {
	t.Parallel()

	_, gc := newSegmentHarness(t)
	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	seedShipped(t, ctx, mgr, kgtypes.GraphCode, "abortRepo", hnswVecDocs(1024))

	hnswDir := graphCacheDirFor(mgr.cacheDir, kgtypes.GraphCode, "abortRepo", hnsw.New().Name())
	orphanPath, _ := plantOrphan(t, hnswDir, "orphan-abort", 222)

	// Inject a genuinely foreign id absent from List(0): a sealed blob built from a
	// DISTINCT corpus (foreignVecDocs — different vectors + ids), never shipped to this
	// server. Import is additive (idempotent by id), so the foreign id stays resident →
	// live ⊄ List(0) → subset-abort fires.
	foreign := newForeignSealedBlob(t)
	dm := mgr.managerFor(kgtypes.GraphCode, "abortRepo")
	require.NoError(t, dm.engine.Import([]searchengine.SegmentBlob{foreign}, nil))

	rep, err := mgr.PruneCache(ctx, []PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: "abortRepo"}}, true)
	require.NoError(t, err)
	require.Equal(t, 0, rep.Removed, "an aborted pool removes NOTHING")

	hPool := poolReport(rep, hnsw.New().Name())
	require.NotNil(t, hPool)
	require.True(t, hPool.Aborted, "the HNSW pool is Aborted by the List(0) subset guard")
	require.NotEmpty(t, hPool.AbortReason)

	_, statErr := os.Stat(orphanPath)
	require.NoError(t, statErr, "an aborted pool must NOT unlink even a genuine orphan")
}

// TestPruneCacheNoOpEmptyGraph proves a never-shipped graph (no live set, no on-disk
// segments) is a clean no-op.
func TestPruneCacheNoOpEmptyGraph(t *testing.T) {
	t.Parallel()

	_, gc := newSegmentHarness(t)
	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))

	rep, err := mgr.PruneCache(ctx, []PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: "emptyRepo"}}, true)
	require.NoError(t, err)
	require.Equal(t, 0, rep.Removed)
	require.EqualValues(t, 0, rep.RemovedBytes)
	for _, g := range rep.Graphs {
		require.Empty(t, g.Orphans, "no orphans for an empty graph")
		require.False(t, g.Aborted, "an empty (subset-trivially-true) pool is not aborted")
	}
}

// TestPruneCacheLiveSearchAfterPrune is the end-to-end no-false-prune proof: after an
// execute=true prune over a graph with a planted orphan, a live Search returns the
// full shipped corpus (every live segment still searchable).
func TestPruneCacheLiveSearchAfterPrune(t *testing.T) {
	t.Parallel()

	_, gc := newSegmentHarness(t)
	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))

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

// newForeignSealedBlob builds a sealed HNSW segment in a throwaway engine and returns
// its blob — an id that exists nowhere on the test server, used to force a live set
// that is not a subset of List(0). The corpus is a DISTINCT vector set (different
// seed than hnswVecDocs) so the deterministic builder produces a different content
// hash than any resident segment — otherwise the blob would hash identically to the
// embed engine's own segment and Import would dedup it away, leaving no phantom.
func newForeignSealedBlob(t *testing.T) searchengine.SegmentBlob {
	t.Helper()
	eng := closeOnCleanup(t, searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{}))
	require.NoError(t, eng.Add(foreignVecDocs(1024)))
	require.NoError(t, eng.Flush())
	exported := eng.Export()
	require.Len(t, exported, 1)
	return exported[0]
}

// foreignVecDocs builds 1024 docs whose vectors AND ids are disjoint from
// hnswVecDocs — a genuinely foreign corpus so its sealed segment's content hash is
// absent from the server's List(0).
func foreignVecDocs(n int) []searchengine.Document {
	rng := rand.New(rand.NewPCG(0xF0E1, 0xD2C3))
	docs := make([]searchengine.Document, n)
	for i := range docs {
		v := make([]byte, 32)
		for j := range v {
			v[j] = byte(rng.UintN(256))
		}
		docs[i] = searchengine.Document{ID: fmt.Sprintf("foreign%d", i), Vector: v}
	}
	return docs
}
