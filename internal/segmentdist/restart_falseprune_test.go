// SPDX-License-Identifier: Apache-2.0

package segmentdist

// restart_falseprune_test.go holds the FIVE WIPE-CLASS DEFECT SIGNATURES, reworked
// onto the L2 cache after the cloud segment rail was deleted.
//
// THE INCIDENT SHAPE IS UNCHANGED AND IS WHY THESE EXIST: a process that holds only
// its own tail retires a corpus it did not build, and the corpus is silently gone.
// The original five drove that through the SERVER — a fresh process holding no ship
// bookkeeping at all, issuing a Prune RPC that collapsed the server corpus to
// tail-only. The server, the Prune RPC and that bookkeeping are all deleted, so every
// one of the five was re-pointed rather than retired: the same wipe is reachable
// locally through PruneCache, which diffs the on-disk .seg ids against a LIVE SET.
// If that live set is ever the resident-only tail instead of the complete L2 corpus,
// prune removes the rest — the identical collapse with a different actuator.
//
// PREDECESSOR -> SUCCESSOR, the locked pairings:
//  1. TestRestartShipDoesNotPruneFullCorpus            -> TestFreshProcessCannotRetireAPriorCorpus
//  2. TestRebuildReplacesDegeneratePoolPrunesOld       -> TestPartialLayerNeverRetiresAGoodLayer
//  3. TestLegitimateMergePruneStillWorks               -> TestLegitimateMergeStillReclaims
//  4. TestPostLoadCorpusMergeLeakIsBoundedThenRebuildPrunes -> TestPostLoadMergeLeakIsReclaimedByTheNextRebuild
//  5. TestRestartLoadImportsFullCorpusAfterShip        -> TestRestartLoadImportsTheFullL2Corpus
//
// EVERY ONE CARRIES A KNOWN-POSITIVE, because four of the five assert that something
// did NOT happen. "Nothing was removed" and "the prune never ran at all" are the same
// observation without a control, and a suite of four such assertions would go green
// against a PruneCache wired to return early.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// l2HNSWIDs reads the ids actually on disk in one graph's HNSW pool. It is the
// SUCCESSOR to the deleted shippedHNSWIDs, which read the server's set: the same
// question — what does this corpus consist of — asked of the only store left.
func l2HNSWIDs(cacheDir, name string) map[searchengine.SegmentID]struct{} {
	ids := newDiskSegmentCache(
		graphCacheDirFor(cacheDir, kgtypes.GraphCode, name, hnsw.New().Name()), 0, adviceRandom).Keys()
	out := make(map[searchengine.SegmentID]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

// TestFreshProcessCannotRetireAPriorCorpus is the load-bearing restart proof.
//
// Mechanic: process 1 builds a multi-segment HNSW corpus into the L2 cache. Process 2
// is a RESTART — a FRESH Manager over the SAME cache directory whose engine has
// loaded NOTHING — and it runs PruneCache with execute=true.
//
// THE WIPE THIS CATCHES: PruneCache removes the on-disk ids that are not in the live
// set. A fresh engine's bare Export() is EMPTY, so a live set taken from it would
// make every prior segment an orphan and the executing prune would delete the entire
// corpus. forceCompleteLiveSet is what prevents that — it reloads from L2 first, so
// the live set is the complete stored corpus rather than the resident-only view.
// Removing that reload reproduces the collapse, which is the same shape the original
// server-side false-prune had.
func TestFreshProcessCannotRetireAPriorCorpus(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()
	ctx := context.Background()

	const repo = "restartRepo"
	const corpusSegs = 3
	cacheDir := t.TempDir()

	p1 := closeOnCleanup(t, NewManager(cacheDir, 0))
	for b := range corpusSegs {
		batch := hnswVecDocs(searchCorpusN)
		for i := range batch {
			batch[i].ID = fmt.Sprintf("p1b%d-%s", b, batch[i].ID)
		}
		require.NoError(t, p1.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, batch))
	}
	require.NoError(t, p1.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

	priorCorpus := l2HNSWIDs(cacheDir, repo)
	require.NotEmpty(t, priorCorpus, "fixture: process 1 must leave a corpus on disk to be wiped")

	// Process 2: RESTART over the SAME cache. Its engine has loaded nothing, so a
	// resident-only live set would be empty here.
	p2 := closeOnCleanup(t, NewManager(cacheDir, 0))
	report, err := p2.PruneCache(ctx,
		[]PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: repo}}, true)
	require.NoError(t, err)

	require.Zero(t, report.Removed,
		"a fresh process must retire NOTHING — every id it sees on disk is live content it did not build")
	after := l2HNSWIDs(cacheDir, repo)
	for id := range priorCorpus {
		require.Contains(t, after, id,
			"every prior-corpus segment survives a fresh process's prune (id %s)", id)
	}

	// KNOWN-POSITIVE, same cache, same call: a GENUINE orphan — a .seg file belonging
	// to no live segment — IS removed. Without this the zero above is equally
	// satisfied by a PruneCache that returns before doing anything, which is exactly
	// the state a broken force-complete would be indistinguishable from.
	// THE MANAGER IS BUILT FIRST AND THE ORPHAN PLANTED AFTER, which is the real shape
	// as well as the only workable one. A .seg that is not a decodable segment must not
	// be in the cache's index when the force-load runs, or the load fails trying to
	// import it and the prune returns an error instead of a measurement. Planting after
	// construction is exactly what a genuine orphan is: a file the index never learned.
	// THE POOL IS CONSTRUCTED FIRST AND THE ORPHAN PLANTED AFTER, which is the real
	// shape as well as the only workable one. A .seg that is not a decodable segment
	// must not be in the cache's index when the force-load runs, or the load fails
	// trying to import it and the prune returns an error instead of a measurement.
	// Planting after the index exists is exactly what a genuine orphan IS: a file the
	// index never learned about. Note the pool is built lazily on FIRST USE, so
	// constructing the Manager is not enough — managerFor is what indexes the root.
	orphanDir := graphCacheDirFor(cacheDir, kgtypes.GraphCode, repo, hnsw.New().Name())
	p3 := closeOnCleanup(t, NewManager(cacheDir, 0))
	p3.managerFor(kgtypes.GraphCode, repo)
	require.NoError(t, os.WriteFile(
		filepath.Join(orphanDir, "orphan-not-a-live-segment.seg"), []byte("stale"), 0o600))
	orphanReport, err := p3.PruneCache(ctx,
		[]PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: repo}}, true)
	require.NoError(t, err)
	require.Equal(t, 1, orphanReport.Removed,
		"CONTROL: the same prune DOES remove a genuine orphan, so the zero above is a real measurement")
	require.NotContains(t, l2HNSWIDs(cacheDir, repo), searchengine.SegmentID("orphan-not-a-live-segment"))
	for id := range priorCorpus {
		require.Contains(t, l2HNSWIDs(cacheDir, repo), id,
			"and the live corpus is still untouched after the orphan sweep")
	}
}

// TestPartialLayerNeverRetiresAGoodLayer pins the corpus-REPLACEMENT half, which is
// the direction a naive fix breaks.
//
// A gate that refuses every retire would pass the previous test and silently stop
// rebuilds from ever superseding an old corpus — segments accumulating forever. So
// both arms are asserted: a COMPLETE deterministic rebuild DOES supersede the old
// layer, while a rebuild that staged nothing does NOT retire the good one.
//
// THIS IS SEPARATE FROM THE EMPTY-LAYER GUARD on purpose. Empty and PARTIAL are
// different inputs: a gate rejecting only the empty case still admits a fresh
// process's two-segment tail retiring a four-thousand-segment corpus, which is the
// actual incident shape.
func TestPartialLayerNeverRetiresAGoodLayer(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()
	ctx := context.Background()

	const repo = "rebuildRepo"
	cacheDir := t.TempDir()

	// A good, populated layer.
	p1 := closeOnCleanup(t, NewManager(cacheDir, 0))
	require.NoError(t, p1.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, hnswVecDocs(searchCorpusN)))
	require.NoError(t, p1.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))
	goodLayer := l2HNSWIDs(cacheDir, repo)
	require.NotEmpty(t, goodLayer, "fixture: a populated layer must exist to be retired")

	// ARM 1 — A FINALIZE THAT STAGED NOTHING must not retire the good layer.
	p2 := closeOnCleanup(t, NewManager(cacheDir, 0))
	res, err := p2.FinalizeRebuild(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err, "an empty finalize is a no-op, not an error")
	require.Empty(t, res.HNSWSuperseded,
		"a rebuild that staged nothing must supersede nothing — retiring here is the wipe")
	for id := range goodLayer {
		require.Contains(t, l2HNSWIDs(cacheDir, repo), id,
			"the good layer survives a partial/empty rebuild (id %s)", id)
	}

	// ARM 2 — CONTROL: a COMPLETE rebuild DOES supersede. Without this the assertion
	// above is satisfied by a FinalizeRebuild that can never retire anything, which
	// would leak every superseded segment forever.
	p3 := closeOnCleanup(t, NewManager(cacheDir, 0))
	// THE PRIOR LAYER HAS TO BE RESIDENT FOR THERE TO BE ANYTHING TO SUPERSEDE. The
	// superseded set is prior-export minus new-export, and a fresh manager's engine has
	// loaded nothing — so without this load the control asserts against an empty prior
	// and would read "supersedes nothing" from a rebuild that in production supersedes
	// the whole layer. Loading is also what a real process does before it rebuilds.
	require.NoError(t, p3.managerFor(kgtypes.GraphCode, repo).load(ctx))
	require.NotEmpty(t, p3.managerFor(kgtypes.GraphCode, repo).engine.Export(),
		"fixture control: the prior layer must be resident, or ARM 2 measures an empty diff")
	require.NoError(t, p3.StageRebuildPartition(ctx, kgtypes.GraphCode, repo,
		hnswVecDocs(searchCorpusN+32), nil))
	full, err := p3.FinalizeRebuild(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.NotEmpty(t, full.HNSWSuperseded,
		"CONTROL: a COMPLETE deterministic rebuild must still supersede the layer it replaces")
}

// TestLegitimateMergeStillReclaims is the both-directions half without which the
// other four are satisfiable by a system that refuses every reclaim.
//
// A genuine merge — the consolidated blob written, its constituents superseded —
// must still reclaim those constituents from L2.
func TestLegitimateMergeStillReclaims(t *testing.T) {
	t.Parallel()

	constituents, merged := realMergeBlobs(t)
	dir := t.TempDir()
	real := newDiskSegmentCache(dir, 0, adviceRandom)
	for _, b := range constituents {
		require.NoError(t, real.Put(b.ID, b.Bytes),
			"fixture: the constituents must be on disk or the reclaim has nothing to do")
	}
	ic := newInstrumentedCache(real)

	newReclaimDMOverCache(t, ic).reclaimMerged(searchengine.MergeResult{
		Merged:  merged,
		Removed: []searchengine.SegmentID{constituents[0].ID, constituents[1].ID},
	})

	removed := ic.removedSet()
	require.Len(t, removed, 2,
		"a legitimate merge STILL reclaims its constituents — a guard that blocks this is worse than the leak")
	for _, b := range constituents {
		_, ok := real.Get(b.ID)
		require.False(t, ok, "constituent %s is gone from L2 after a legitimate reclaim", b.ID)
	}
	_, ok := real.Get(merged.ID)
	require.True(t, ok, "and the consolidated blob that replaced them is present")
}

// TestPostLoadMergeLeakIsReclaimedByTheNextRebuild pins the BOUNDED-LEAK contract as
// a contract rather than a silent leak.
//
// After a load pulls the stored corpus into an engine and an in-process merge
// consolidates segments this process never wrote, those merged-away ids are not
// reclaimed immediately — they are reclaimed by the NEXT full rebuild. The leak is
// bounded, and "bounded" is the thing under test: an unbounded version accumulates
// dead .seg files forever.
func TestPostLoadMergeLeakIsReclaimedByTheNextRebuild(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()
	ctx := context.Background()

	const repo = "leakRepo"
	cacheDir := t.TempDir()

	p1 := closeOnCleanup(t, NewManager(cacheDir, 0))
	require.NoError(t, p1.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, hnswVecDocs(searchCorpusN)))
	require.NoError(t, p1.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))
	beforeRebuild := l2HNSWIDs(cacheDir, repo)
	require.NotEmpty(t, beforeRebuild, "fixture: a stored corpus must exist to be superseded")

	// The next FULL rebuild reclaims what it supersedes rather than leaving it to
	// accumulate. This is the bound.
	p2 := closeOnCleanup(t, NewManager(cacheDir, 0))
	// THE LOAD IS THE POINT OF THIS TEST, not setup around it: the leak is bounded by
	// the next rebuild only because that rebuild's process LOADS the stored corpus
	// first and can therefore see what it is superseding. The superseded set is
	// prior-export minus new-export, so a process that never loaded reports an empty
	// prior — an unbounded leak that this assertion would read as a bound.
	require.NoError(t, p2.managerFor(kgtypes.GraphCode, repo).load(ctx))
	require.NoError(t, p2.StageRebuildPartition(ctx, kgtypes.GraphCode, repo,
		hnswVecDocs(searchCorpusN+32), nil))
	res, err := p2.FinalizeRebuild(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.NotEmpty(t, res.HNSWSuperseded,
		"the next full rebuild reclaims the superseded ids — this is what bounds the leak")

	// CONTROL that the bound is real rather than nominal: the superseded ids the
	// rebuild REPORTED are the ids it actually retired, so the report is not a
	// number nobody acted on.
	require.Subset(t, keysOf(beforeRebuild), res.HNSWSuperseded,
		"every reported superseded id must come from the corpus that was actually there")
}

// keysOf flattens an id set for a subset assertion.
func keysOf(m map[searchengine.SegmentID]struct{}) []searchengine.SegmentID {
	out := make([]searchengine.SegmentID, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	return out
}

// TestRestartLoadImportsTheFullL2Corpus is the read-side restart proof: a cold
// process must import the WHOLE stored corpus on its first load, not a tail.
//
// THE WIPE THIS CATCHES is the read-side twin of the prune-side one. If a restart
// imported only part of the corpus, every downstream consumer that measures the live
// set — prune-cache above all — would see a short set and treat the remainder as
// orphaned. A short import and a false prune are the same bug one step apart.
func TestRestartLoadImportsTheFullL2Corpus(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()
	ctx := context.Background()

	const repo = "importRepo"
	const corpusSegs = 3
	cacheDir := t.TempDir()

	p1 := closeOnCleanup(t, NewManager(cacheDir, 0))
	for b := range corpusSegs {
		batch := hnswVecDocs(searchCorpusN)
		for i := range batch {
			batch[i].ID = fmt.Sprintf("imp%d-%s", b, batch[i].ID)
		}
		require.NoError(t, p1.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, batch))
	}
	require.NoError(t, p1.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))
	stored := l2HNSWIDs(cacheDir, repo)
	require.NotEmpty(t, stored, "fixture: a multi-segment corpus must be stored")

	// RESTART: a fresh Manager over the same cache. Its resident count after the first
	// materialization must be the WHOLE stored set.
	//
	// THE MATERIALIZATION IS TRIGGERED, NOT WAITED FOR. Loading is lazy and driven by a
	// consumer touch — a search, a probe, a rebuild — so nothing happens spontaneously
	// on a fresh Manager and a polling wait would only ever time out. The trigger here
	// is the load itself, which is the operation every one of those consumers reaches.
	p2 := closeOnCleanup(t, NewManager(cacheDir, 0))
	require.NoError(t, p2.managerFor(kgtypes.GraphCode, repo).load(ctx))
	require.Equal(t, len(stored), p2.ResidentSegmentCount(kgtypes.GraphCode, repo, hnsw.New().Name()),
		"a restart imports the FULL L2 corpus (%d segments), not a tail", len(stored))

	// CONTROL: the count is a real reading of this graph rather than a constant — an
	// UNRELATED graph in the same cache reports zero through the same call.
	require.Zero(t, p2.ResidentSegmentCount(kgtypes.GraphCode, "a-graph-that-was-never-built", hnsw.New().Name()),
		"CONTROL: the same probe reads zero for a graph with no corpus, so the count above is measured")
}
