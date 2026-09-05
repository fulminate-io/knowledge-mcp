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
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// prune_cache_reclaim_abort_test.go MEASURES the convergence promise the rest of
// this package used to make about an aborted merge reclaim, and pins the answer.
//
// THE PROMISE, quoted from the two comments that carried it before this file
// existed: "the L2 copies of anything it dropped are reaped by PruneCache"
// (manager_bucket.go) and "referenced by no layer; PruneCache reaps them"
// (manager_publish_resident.go). An aborted reclaim leaves a pre-merge constituent
// in L2 beside the blob that superseded it (manager_reclaim.go's Put-failure
// abort), and every report of that state was bounded by this promise.
//
// THE PROMISE WAS FALSE FOR THAT BLOB, AND THIS FILE IS WHY WE KNOW. It was previously
// only DERIVED from a code read; the tests below drive the state and read the outcome.
// PruneCache computed its live set by force-loading the pool from the SAME L2 index it
// then diffs against, so every id that index held was in the live set by construction
// and could never be classified an orphan.
//
// THESE TESTS NOW PIN THE REPAIR, and they are the SAME tests: each assertion was
// written so that fixing the derivation would turn it red rather than leave it quietly
// satisfied, and that is exactly what happened. The repair is not in this command. A
// prune can only classify what the stored corpus records, and the corpus now records
// supersession — a consolidated blob names the constituents it replaced and the cohort
// it was published with (searchengine/supersession.go) — so the force-load declines a
// superseded constituent, the live set is genuinely narrower than the stored set, and
// what used to survive every prune is reaped by one. The two other seams that landed
// with it are the recoverable reclaim (manager_reclaim_discharge.go) and the delete's
// tombstone seal (manager_bucket_delete_seal.go); each closes a different half, and the
// three are separable — this file's fixture drives the import half.
//
// WHAT DID NOT CHANGE is the direction a naive repair breaks, below.
//
// AND NARROWING THE LIVE SET IS NOT THE FIX, which is the other half of what was
// measured. TestPruneCacheColdStartStillLoadsTheWholeCorpus holds the direction a
// naive repair breaks: for a pool that may be cold-started, "live" is definitionally
// "what a cold start would import", and an engine-resident-only live set condemns a
// prior corpus the moment a process writes before it first searches.

// servingIDs is the pool's CURRENT serving set: the ids search resolves against
// right now, read with NO re-import in between.
//
// IT IS DELIBERATELY NOT forceCompleteLiveSet. That helper force-loads before
// exporting, which is the very step under measurement here; using it to establish a
// baseline would define the stale blob into the live set before any assertion ran.
func servingIDs[Q, S any](dm *distManager[Q, S]) map[searchengine.SegmentID]struct{} {
	out := make(map[searchengine.SegmentID]struct{})
	for _, b := range dm.engine.Export() {
		out[b.ID] = struct{}{}
	}
	return out
}

// staleOnDisk is the difference a correct prune would compute: every .seg id under
// the pool's L2 root that the pool is NOT currently serving.
func staleOnDisk(
	onDisk, serving map[searchengine.SegmentID]struct{},
) map[searchengine.SegmentID]struct{} {
	out := make(map[searchengine.SegmentID]struct{})
	for id := range onDisk {
		if _, live := serving[id]; !live {
			out[id] = struct{}{}
		}
	}
	return out
}

// abortedReclaimPool drives a delete and then the DRAIN that re-emits its vector
// partition, with that drain's merge reclaim ABORTING, and returns the manager plus the
// ids the abort stranded in the HNSW L2 pool.
//
// THE DRAIN IS THE DRIVER BECAUSE THE DELETE NO LONGER IS. A delete's vector leg is a
// live-bit kill that writes no blob and consolidates nothing, so it can strand no vector
// constituent; what re-emits that partition — and therefore what can abort a reclaim in
// this pool — is the deferred drain. The stranded state this file is about is unchanged;
// only which pass produces it moved.
//
// THE ABORT IS ASSERTED, NOT ASSUMED, and it is asserted through the pool's own abort
// mark rather than through a returned error: the drain logs an aborted reclaim rather
// than surfacing it (only the delete path arms surfaceAbortedReclaim), so a nil return
// from the drain says nothing either way. An injection that missed strands nothing at
// all, under which every assertion downstream is about an empty set.
func abortedReclaimPool(t *testing.T, name string) (
	*Manager, kgtypes.GraphType, string, searchengine.Document,
	map[searchengine.SegmentID]struct{}, map[searchengine.SegmentID]struct{},
) {
	t.Helper()

	ctx := context.Background()
	mgr, gt, nm, hdm, ic, victim := deleteRetryFixture(t, name)

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, nm, []searchengine.ExternalID{victim.ID}),
		"FIXTURE PRECONDITION: the delete itself must be clean — it writes no vector blob, so any "+
			"error here is a different fault than the one this fixture injects")

	// EXACTLY ONE PUT FAILS AND IT IS THE RECLAIM'S — the same injection idiom
	// TestDeleteSurfacesAnAbortedMergeReclaim uses, so this file measures the state
	// that test pins rather than a state of its own invention.
	mark := hdm.reclaimAbortMark()
	ic.failPutUntil = 1
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, nm))
	require.Error(t, hdm.abortedReclaimSince(mark),
		"FIXTURE PRECONDITION: the drain's merge reclaim must have ABORTED — no abort since the mark "+
			"means the injection never fired and no constituent was stranded")
	require.Empty(t, ic.removedSet(),
		"FIXTURE PRECONDITION: the injected failure must have ABORTED the merge reclaim — a non-empty "+
			"removal set means the reclaim completed and this fixture strands nothing")

	serving := servingIDs(hdm)
	require.NotEmpty(t, serving, "FIXTURE PRECONDITION: the pool must still serve the post-delete corpus")
	stale := staleOnDisk(l2HNSWIDs(mgr.cacheDir, nm), serving)
	require.NotEmpty(t, stale,
		"FIXTURE PRECONDITION: the aborted reclaim must have left at least one .seg on disk that the "+
			"pool no longer serves — an empty stale set makes every assertion below vacuous")

	return mgr, gt, nm, victim, serving, stale
}

// TestPruneCacheLiveSetExcludesWhatTheStoredBlobsSupersede states the MECHANISM as it
// now stands, and it is the assertion the other tests in this file are consequences of.
//
// WHAT IT USED TO SAY, and why the change is the fix rather than a weakening: the live
// set was EXACTLY the pool's L2 index — a diff of the cache against the cache, under
// which no stored blob could ever be classified an orphan. The vacuity was not in how
// this file computes a diff but in what the stored corpus recorded: an id and a payload
// and nothing that said "superseded by". The blob format now carries that record
// (searchengine/supersession.go), so the force-load DECLINES a constituent the newer
// blob names, and the live set is the stored index minus what the stored index itself
// says is dead.
func TestPruneCacheLiveSetExcludesWhatTheStoredBlobsSupersede(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr, gt, name, _, serving, stale := abortedReclaimPool(t, "pruneabortliveset")

	live, err := mgr.completeHNSWLiveSet(ctx, gt, name)
	require.NoError(t, err)

	onDisk := l2HNSWIDs(mgr.cacheDir, name)
	wantLive := make(map[searchengine.SegmentID]struct{}, len(onDisk))
	for id := range onDisk {
		if _, dead := stale[id]; !dead {
			wantLive[id] = struct{}{}
		}
	}
	require.Equal(t, wantLive, live,
		"the live set the prune diffs against is the pool's L2 index MINUS the constituents its own "+
			"stored blobs record as superseded")

	// AND THE DIFFERENCE IS NOT TRIVIAL, which is the whole point: a stored blob CAN
	// now be classified an orphan. Without this the equality above would be equally
	// satisfied by a pool with nothing stale on disk, which measures nothing.
	require.Less(t, len(live), len(onDisk),
		"CONTROL: the live set is strictly smaller than the stored set, so the diff has something to "+
			"find rather than agreeing with itself by construction")
	for id := range stale {
		require.NotContains(t, live, id,
			"the stranded constituent %s is NOT in the live set the prune diffs against", id)
	}
	for id := range serving {
		require.Contains(t, live, id,
			"CONTROL: every segment the pool actually serves (%s) IS live — the narrowing drops only "+
				"what a stored record supersedes", id)
	}
}

// TestPruneCacheReapsAnUnreclaimedMergeConstituent is the reap half: an executing prune
// now removes the stranded constituent from disk.
//
// IT USED TO ASSERT THE OPPOSITE, and the survival it pinned was the measured defect:
// the constituent was in the live set by construction, so it could never be an orphan.
// What changed is not this command — it still reaps exactly what its diff classifies —
// but the stored corpus, which now says what supersedes what.
func TestPruneCacheReapsAnUnreclaimedMergeConstituent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr, gt, name, _, serving, stale := abortedReclaimPool(t, "pruneabortreap")

	// A GENUINE ORPHAN IS PLANTED IN THE SAME POOL AND SWEPT BY THE SAME CALL. This
	// is the known-positive without which "the constituent survived" is equally
	// satisfied by a prune that did nothing at all — and an inert prune and a prune
	// whose live set swallowed the constituent are indistinguishable from the outside.
	// It is planted AFTER the pool's cache index was built, which is what a genuine
	// orphan IS: a file the index never learned about.
	hnswDir := graphCacheDirFor(mgr.cacheDir, gt, name, hnsw.New().Name())
	orphanPath, _ := plantOrphan(t, hnswDir, "orphan-beside-the-stranded-constituent", 128)

	rep, err := mgr.PruneCache(ctx, []PruneCacheTarget{{GraphType: gt, Name: name}}, true)
	require.NoError(t, err)

	pool := poolReport(rep, hnsw.New().Name())
	require.NotNil(t, pool, "the HNSW pool must be reported at all")
	require.False(t, pool.Aborted,
		"the pool must be pruned rather than refused: its live set is non-empty, so a refusal here "+
			"would mean this test is measuring the empty-live-set guard instead")

	wantReaped := []searchengine.SegmentID{"orphan-beside-the-stranded-constituent"}
	for id := range stale {
		wantReaped = append(wantReaped, id)
	}
	require.ElementsMatch(t, wantReaped, pool.Orphans,
		"the prune reaps BOTH classes and only those: the .seg its index never learned about — the "+
			"known-positive without which a reap of nothing would read the same — and the stranded "+
			"constituent the stored corpus now records as superseded")
	_, statErr := os.Stat(orphanPath)
	require.True(t, os.IsNotExist(statErr), "and the planted orphan is really unlinked")

	after := l2HNSWIDs(mgr.cacheDir, name)
	for id := range stale {
		require.NotContains(t, after, id,
			"the stranded constituent %s is referenced by no layer the pool serves and no longer "+
				"survives an EXECUTING prune", id)
	}
	for id := range serving {
		require.Contains(t, after, id,
			"CONTROL: the blob the pool actually serves (%s) survives — the prune reaped the stale "+
				"blob without wiping the pool", id)
	}
}

// TestAPreviewPruneNoLongerResurrectsTheDeletedID is the search half: computing the live
// set no longer puts the deleted document back into the SERVING engine.
//
// IT IS ASSERTED ON THE PREVIEW RUN, deliberately. execute=false is the run an operator
// reaches for to see what a prune WOULD do, and the force-load happens before the execute
// flag is ever consulted — so a read-only inspection that could still mutate the serving
// engine is the exposure this pins. It used to assert the resurrection, which was the
// measured defect: the force-load imported the stranded constituent on top of an engine
// that already held the authoritative post-delete state.
func TestAPreviewPruneNoLongerResurrectsTheDeletedID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr, gt, name, victim, _, _ := abortedReclaimPool(t, "pruneabortsearch")

	_, before := mgr.managerFor(gt, name).engine.VectorByID(victim.ID)
	require.False(t, before,
		"FIXTURE PRECONDITION: the delete removed the id from the SERVING engine, so anything below "+
			"is about the prune putting it back rather than a delete that never landed")

	_, err := mgr.PruneCache(ctx, []PruneCacheTarget{{GraphType: gt, Name: name}}, false)
	require.NoError(t, err)

	_, resident, verr := mgr.VectorByID(ctx, gt, name, victim.ID)
	require.NoError(t, verr)
	require.False(t, resident,
		"a PREVIEW prune must not resurrect the deleted id in the engine it measured — the force-load "+
			"declines the stranded constituent the post-delete blob records as superseded")

	// KNOWN POSITIVE, same run and same instrument: the force-load DID import, so the
	// assertion above is not a read against an engine that loaded nothing.
	_, live, verr := mgr.VectorByID(ctx, gt, name, deleteFixtureNeighborID(name))
	require.NoError(t, verr)
	require.True(t, live,
		"CONTROL: a document the delete did not name still resolves after the same force-load")
}

// deleteRetryFixture names its documents by ordinal, so a neighbor of the victim is
// addressable without threading the whole slice through abortedReclaimPool.
func deleteFixtureNeighborID(name string) string {
	return "delretry-" + name + "-n1"
}

// TestAnOrdinaryReadNoLongerResurrectsAStrandedConstituent is the measurement that put
// the resurrection where it actually belonged, now asserting the repaired behaviour.
//
// IT WAS NEVER THE PRUNE'S DEFECT, and that attribution is the reason this test exists
// separately. The prune force-loads through the same L2 import every consumer touch
// reaches, so the resurrection was reachable with no prune in the picture: the first read
// after the delete ran load()'s PRIMARY branch against a pool whose engine already held
// the authoritative post-delete state, and imported the stranded constituent on top of
// it. A fix aimed at prune_cache.go would have left this path resurrecting — which is
// why the fix is in what the corpus RECORDS, and why this test still measures the plain
// read rather than the prune.
func TestAnOrdinaryReadNoLongerResurrectsAStrandedConstituent(t *testing.T) {
	t.Parallel()

	t.Run("a plain read leaves the deleted id gone", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		mgr, gt, name, victim, serving, _ := abortedReclaimPool(t, "readresurrect")
		hdm := mgr.managerFor(gt, name)

		require.Len(t, serving, 1,
			"FIXTURE PRECONDITION: the engine serves ONLY the post-delete blob, so a count change "+
				"below would be the stranded constituent arriving")
		require.Equal(t, deleteFixtureN-1, hdm.engine.DistinctResidentDocCount(),
			"FIXTURE PRECONDITION: and it serves the post-delete document count")

		_, resident, err := mgr.VectorByID(ctx, gt, name, victim.ID)
		require.NoError(t, err)
		require.False(t, resident,
			"ONE ordinary read must NOT resolve the deleted id — the L2 import declines the stranded "+
				"constituent the post-delete blob records as superseded")
		require.Equal(t, deleteFixtureN-1, hdm.engine.DistinctResidentDocCount(),
			"and the pre-delete corpus does not come back into the serving engine")

		// KNOWN POSITIVE: the import DID run and DID publish. Without it the two
		// assertions above are equally satisfied by a read that resolved nothing.
		_, live, err := mgr.VectorByID(ctx, gt, name, deleteFixtureNeighborID("readresurrect"))
		require.NoError(t, err)
		require.True(t, live, "CONTROL: a document the delete did not name still resolves")
	})

	t.Run("known-negative: a clean delete's read stays clean", func(t *testing.T) {
		t.Parallel()

		// WITHOUT THIS LEG the assertion above is satisfied by a fixture whose delete
		// never removed the document in the first place, and by a VectorByID that
		// resolves any id it is handed.
		ctx := context.Background()
		mgr, gt, name, hdm, ic, victim := deleteRetryFixture(t, "readclean")

		// THE SAME TWO PASSES THE ABORT LEG DRIVES, with nothing injected: the delete
		// kills and seals, and the drain re-emits the vector partition. It is the DRAIN's
		// reclaim that removes constituents, so the clean run has to include it or the
		// control below is comparing a two-pass fixture against a one-pass one.
		require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
		require.NotEmpty(t, ic.removedSet(),
			"CONTROL: a clean drain's reclaim DOES remove the constituents it superseded, so the "+
				"stranded blob above is genuinely the abort's doing")

		_, resident, err := mgr.VectorByID(ctx, gt, name, victim.ID)
		require.NoError(t, err)
		require.False(t, resident,
			"CONTROL: the same read over a pool with nothing stranded does NOT resolve the deleted id")
		require.Equal(t, deleteFixtureN-1, hdm.engine.DistinctResidentDocCount(),
			"and its serving corpus stays at the post-delete count")
	})
}

// TestPruneCacheColdStartStillLoadsTheWholeCorpus is the direction a naive repair
// breaks, and it is why the reap above cannot be delivered by narrowing the live set.
//
// THE WRITE-BEFORE-FIRST-SEARCH ORDER IS THE ORDINARY ONE — a collect writes into a
// graph long before anything searches it — and in that order the engine's resident
// set is this process's own new segment alone while L2 holds the whole prior corpus.
// A live set taken from the engine there names one segment and condemns every other
// stored blob, which is TestFreshProcessCannotRetireAPriorCorpus's incident shape
// reached through the write path instead of the read path.
func TestPruneCacheColdStartStillLoadsTheWholeCorpus(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	const repo = "coldStartPrune"
	cacheDir := t.TempDir()

	p1 := closeOnCleanup(t, NewManager(cacheDir, 0))
	require.NoError(t, p1.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, hnswVecDocs(searchCorpusN)))
	require.NoError(t, p1.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))
	prior := l2HNSWIDs(cacheDir, repo)
	require.NotEmpty(t, prior, "FIXTURE PRECONDITION: a stored corpus must exist to be condemned")

	// A RESTART THAT WRITES BEFORE IT SEARCHES.
	p2 := closeOnCleanup(t, NewManager(cacheDir, 0))
	require.NoError(t, p2.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, hnswVecDocs(8)))
	dm := p2.managerFor(kgtypes.GraphCode, repo)

	// THE HAZARD, MEASURED RATHER THAN ASSERTED AS A RISK: what an engine-resident-only
	// live set would condemn here.
	engineOnly := servingIDs(dm)
	require.NotEmpty(t, staleOnDisk(prior, engineOnly),
		"an engine-resident-only live set would condemn stored corpus this process did not build — "+
			"which is why the force-load cannot simply be narrowed to Export()")

	// AND THE REAL LIVE SET COVERS ALL OF IT.
	live, err := p2.completeHNSWLiveSet(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	for id := range prior {
		require.Contains(t, live, id,
			"every prior-corpus segment (%s) must be in the live set of a process that wrote before it "+
				"searched", id)
	}

	rep, err := p2.PruneCache(ctx, []PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: repo}}, true)
	require.NoError(t, err)
	require.Zero(t, rep.Removed, "and nothing of that corpus is retired")
	for id := range prior {
		require.FileExists(t, filepath.Join(
			graphCacheDirFor(cacheDir, kgtypes.GraphCode, repo, hnsw.New().Name()), id+".seg"),
			"the .seg for prior-corpus segment %s survives", id)
	}
}
