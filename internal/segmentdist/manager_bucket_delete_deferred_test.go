// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_bucket_delete_deferred_test.go is the gate on the delete's TWO SCHEDULES:
// what the caller observes when DeleteFromBuckets returns, and what is still owed
// afterwards. The vector partitions are no longer rebuilt on the caller's goroutine, so
// every property here is about proving the removal is complete WITHOUT that rebuild
// rather than because of it.

// deferredDeleteFixture seeds a corpus in both formats and drains it to L2, returning a
// manager whose vector pool holds exactly that corpus.
func deferredDeleteFixture(t *testing.T, name string, n int) (*Manager, string, kgtypes.GraphType, []searchengine.Document) {
	t.Helper()

	ctx := context.Background()
	gt := kgtypes.GraphCode
	dir := t.TempDir()

	mgr := closeOnCleanup(t, NewManager(dir, 0))
	docs := bothFormatDocs(n, "defdel-"+name+"-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
	require.Equal(t, n, mgr.managerFor(gt, name).engine.ResidentDocCount(),
		"FIXTURE PRECONDITION: the vector corpus must hold the whole fixture, or the delete below has no partition to have deferred")

	return mgr, dir, gt, docs
}

// countGroupRebuildsForFormat counts one format's partition-rebuild records for a graph.
//
// IT COUNTS RATHER THAN REQUIRING EXACTLY ONE, because the assertion it serves is that
// the count is ZERO for the vector format — which diagRecord's require.Len(1) cannot
// express — and because the paired known-positive on the field format needs the same
// instrument for the comparison to mean anything. The graph identity of a code graph is
// spelled into repo= and its name= is empty; matching on name= finds nothing and reads
// as "the instrument never fired".
func countGroupRebuildsForFormat(logged, graphName, format string) int {
	n := 0
	for line := range strings.SplitSeq(logged, "\n") {
		if strings.Contains(line, `msg="segmentdist: group_rebuild"`) &&
			strings.Contains(line, "repo="+graphName) &&
			strings.Contains(line, "format="+format) {
			n++
		}
	}
	return n
}

// requireDeleteDefersTheVectorRebuild runs one delete with the log captured and asserts
// the split schedule at the seam: no vector partition rebuild on the caller's goroutine,
// and — the same-run known positive — the field rebuild that DID run. Without the
// positive leg a handler that captured nothing is indistinguishable from a delete that
// re-emitted nothing.
func requireDeleteDefersTheVectorRebuild(
	t *testing.T, mgr *Manager, gt kgtypes.GraphType, name string, ids []searchengine.ExternalID,
) {
	t.Helper()

	logged := captureDrainLog(t, func() {
		require.NoError(t, mgr.DeleteFromBuckets(context.Background(), gt, name, ids))
	})
	require.Positive(t, countGroupRebuildsForFormat(logged, name, "bm25v2"),
		"CONTROL: the field corpus's partition rebuild still runs inline on the delete, so a zero here "+
			"means the log capture saw nothing at all and the vector assertion below is vacuous\nfull log:\n%s", logged)
	require.Zero(t, countGroupRebuildsForFormat(logged, name, "hnswv3"),
		"the delete must not rebuild a vector partition on the caller's own goroutine — that "+
			"reconstruction is what this deferral takes off the user's path\nfull log:\n%s", logged)
}

// TestDeleteRemovesResultsFromSearchWithoutAnHNSWReEmit is the user-visible contract:
// the document is gone from search the moment the call returns, and it got there
// WITHOUT the partition reconstruction that used to dominate a delete's service time.
//
// The two halves are what make the change safe rather than merely faster. Absence alone
// would be satisfied by the old inline re-emit; absence of the rebuild alone would be
// satisfied by a delete that did nothing at all.
func TestDeleteRemovesResultsFromSearchWithoutAnHNSWReEmit(t *testing.T) {
	requireMeasurementRun(t)
	const name = "deferred-delete-search"
	mgr, _, gt, docs := deferredDeleteFixture(t, name, twoPartitionFixtureN)
	victim := docs[0]

	before := searchHitIDs(t, mgr, gt, name, victim.Vector, 10)
	require.Contains(t, before, victim.ID,
		"PRECONDITION: the victim must be searchable before the delete, or its later absence proves nothing")

	requireDeleteDefersTheVectorRebuild(t, mgr, gt, name, []searchengine.ExternalID{victim.ID})

	require.NotContains(t, searchHitIDs(t, mgr, gt, name, victim.Vector, 10), victim.ID,
		"a deleted document must be out of the searchable set the moment the delete returns — the "+
			"live-bit kill is what the caller's contract needs, and it is synchronous")
}

// TestDeletionIsDurableAcrossReloadBeforeAnyReEmit is the property the whole deferral
// rests on: the removal survives a restart even though the blob on disk STILL CARRIES
// the document, because the durable tombstone mask seeds it dead at import.
//
// THE SECOND ASSERTION IS WHAT MAKES THE FIRST NON-VACUOUS. A test that only checked the
// document was absent after reload would pass just as well against the old inline
// re-emit, where the blob no longer carried it at all — which is precisely the state
// deferral removes. Asserting that the pre-delete blob is unchanged on disk, and that a
// fresh engine's PHYSICAL count still includes the victim while its LIVE count does not,
// pins the mask as the thing doing the work.
func TestDeletionIsDurableAcrossReloadBeforeAnyReEmit(t *testing.T) {
	requireMeasurementRun(t)
	ctx := context.Background()
	const name = "deferred-delete-reload"
	mgr, dir, gt, docs := deferredDeleteFixture(t, name, twoPartitionFixtureN)
	victim := docs[0]

	beforeBlobs := l2HNSWIDs(dir, name)
	require.NotEmpty(t, beforeBlobs, "PRECONDITION: the corpus must have reached L2, or there is no blob to reload from")

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))
	// DELIBERATELY NO DRAIN. The deferred re-emit is what would eventually rewrite these
	// blobs; this test is about the window BEFORE it runs.

	require.Equal(t, beforeBlobs, l2HNSWIDs(dir, name),
		"the delete must not have rewritten the vector blobs — if it did, this test is measuring the "+
			"inline re-emit rather than the deferral, and the durability below proves nothing about the mask")

	physical, live := freshEngineCounts(t, ctx, dir, gt, name)
	require.Equal(t, twoPartitionFixtureN, physical,
		"the blob a FRESH process imports still physically carries the deleted document, because its "+
			"partition has not been re-emitted yet")
	require.Equal(t, twoPartitionFixtureN-1, live,
		"and it is nevertheless dead on arrival: the durable tombstone record seeds it at import, which "+
			"is the only thing standing between a deferred delete and a resurrection")

	fresh := closeOnCleanup(t, NewManager(dir, 0))
	require.NotContains(t, searchHitIDs(t, fresh, gt, name, victim.Vector, 10), victim.ID,
		"and a fresh process must not serve it")
}

// TestMultiIDDeleteDefersAtTheSeam pins that the MULTI-ID shape gets the same treatment
// as the single-id one, at DeleteFromBuckets.
//
// IT IS NAMED FOR WHAT IT DRIVES. It calls DeleteFromBuckets directly and asserts the
// seam's behaviour under a many-id delete; it does NOT exercise manage(prune)'s route to
// that seam, and an earlier name claiming prune coverage overstated it. The ROUTE is
// pinned where it lives, in package tools:
// TestInterceptManage_Prune_WiresPrunedIDsToSegmentDelete asserts a prune hands its
// pruned ids to the deleter, and TestInterceptManage_Prune_NoPrunedIDsSkipsSegmentDelete
// asserts it does not call the deleter when there are none. Those two plus this one are
// what make "prune gets the deferral" true; this one alone is not.
//
// THE MULTI-ID SHAPE IS THE ONE THAT MATTERED. A single-id delete rebuilt one partition;
// a prune naming hundreds of ids dirtied most partitions of the corpus and rebuilt every
// one of them serially, which is where the seconds went.
func TestMultiIDDeleteDefersAtTheSeam(t *testing.T) {
	requireMeasurementRun(t)
	const name = "deferred-multiid"
	mgr, _, gt, docs := deferredDeleteFixture(t, name, twoPartitionFixtureN)

	var victims []searchengine.ExternalID
	for _, d := range docs[:24] {
		victims = append(victims, d.ID)
	}
	bucketCount := searchengine.BucketCountFor(mgr.managerFor(gt, name).engine.DistinctResidentDocCount())
	spanned := map[int]struct{}{}
	for _, id := range victims {
		spanned[searchengine.BucketOf(id, bucketCount)] = struct{}{}
	}
	require.Greater(t, len(spanned), 1,
		"FIXTURE PRECONDITION: a prune-shaped delete must span more than one partition, or it is a "+
			"single-id delete wearing a slice")

	requireDeleteDefersTheVectorRebuild(t, mgr, gt, name, victims)

	for _, d := range docs[:24] {
		require.NotContains(t, searchHitIDs(t, mgr, gt, name, d.Vector, 10), d.ID,
			"every pruned document must leave the searchable set immediately")
	}
}

// TestDeferredDeleteIdsSurviveARebuildThatDidNotEmitTheirPartition is the always-on
// invariant guard, and it stays in the suite permanently.
//
// A deferred delete's only protection between the kill and the re-emit is its entry in
// the durable mask, so an id must not lose that entry on account of work that did not
// reach its partition. This drives the segmentdist end of the seam: a delete spanning
// more partitions than one drain serves, followed by a drain, must leave every id whose
// partition the drain did not publish still masked and still dead on a reload.
//
// ITS COUNTERPART AT THE OTHER END OF THE SEAM IS THE TOOLS-SIDE REGRESSION
// (TestDeltaRebuildKeepsTombstonesOutsideItsOwnPartitions), which pins the rebuild
// driver's trim. The two cannot be merged, and the reason is VISIBILITY rather than
// import direction: the rebuild driver's trim seam is retainTombstones and
// finishRebuild, both UNEXPORTED, so only a test inside package tools can reach them.
//
// NEITHER PACKAGE IMPORTS THE OTHER, and that is load-bearing architecture a reader
// must not unlearn: tools reaches this package only through the interfaces declared in
// its own deps_segments.go (SegmentShipper, SegmentDeleter and their siblings), which
// is what lets the driver be tested against fakes and what keeps the dependency from
// running in either direction.
func TestDeferredDeleteIdsSurviveARebuildThatDidNotEmitTheirPartition(t *testing.T) {
	requireMeasurementRun(t)
	ctx := context.Background()
	const name = "deferred-invariant"
	mgr, dir, gt, docs := deferredDeleteFixture(t, name, budgetFixtureN)

	bucketCount := searchengine.BucketCountFor(mgr.managerFor(gt, name).engine.DistinctResidentDocCount())
	victims := make([]searchengine.ExternalID, 0, 256)
	spanned := map[int]struct{}{}
	for _, d := range docs[:256] {
		victims = append(victims, d.ID)
		spanned[searchengine.BucketOf(d.ID, bucketCount)] = struct{}{}
	}
	require.Greater(t, len(spanned), deferredReEmitPartitionBudget,
		"FIXTURE PRECONDITION: the delete must span more partitions than one drain serves, or nothing "+
			"is left un-emitted and the survival assertion is vacuous")

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, victims))
	require.ElementsMatch(t, victims, persistedMask(t, mgr, gt, name),
		"PRECONDITION: the delete seals every id into the durable record")

	served := mgr.deferredReEmitIDs(gt, name)
	require.NotEmpty(t, served, "PRECONDITION: the drain must have work, or it cannot have failed to reach anything")
	servedSet := map[searchengine.ExternalID]struct{}{}
	for _, id := range served {
		servedSet[id] = struct{}{}
	}
	var unserved []searchengine.ExternalID
	for _, id := range victims {
		if _, in := servedSet[id]; !in {
			unserved = append(unserved, id)
		}
	}
	require.NotEmpty(t, unserved, "PRECONDITION: some ids' partitions must go un-emitted by this drain")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	after := persistedMask(t, mgr, gt, name)
	for _, id := range unserved {
		require.Contains(t, after, id,
			"an id whose partition this drain never emitted must KEEP its mask entry: the blob still "+
				"carries the document, and the entry is the only thing masking it at the next import")
	}

	_, live := freshEngineCounts(t, ctx, dir, gt, name)
	require.Equal(t, budgetFixtureN-len(victims), live,
		"and a fresh process must serve none of the deleted documents — neither the ids this drain "+
			"discharged nor the ids it left masked")
}
