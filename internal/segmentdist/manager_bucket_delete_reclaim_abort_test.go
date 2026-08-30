// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_bucket_delete_reclaim_abort_test.go is the gate on the delete re-emit's
// OTHER L2 write — the one the bounded write retry does not cover.
//
// THE DELETE RE-EMIT WRITES L2 TWICE, and only the second write is the retry's.
// replaceBucketGroups' group swap fires the engine's merge hook, which is
// reclaimMerged: it Puts the consolidated blob and only then removes the
// constituents that blob replaces. A failed Put ABORTS that reclaim before a single
// constituent is removed — deliberate, and pinned by
// TestReclaimAbortsWhenTheMergedBlobCannotBePersisted — and the abort is logged,
// never returned. persistResident then runs as the second write, and with the
// bounded retry armed it can succeed where the reclaim's single attempt failed.
//
// WHAT THAT INTERLEAVING USED TO REPORT AND WHAT IT REPORTED AFTER THE RETRY. Before
// the retry the same transient disk error hit persistResident's single attempt too,
// so the delete returned an error and the tools layer appended its not-durable
// qualifier. After the retry the write recovers, DeleteFromBuckets returns nil, and
// the caller is told the removal is durable while the PRE-DELETE constituent sits on
// disk beside the freshly written post-delete blob.
//
// WHAT THE CONSEQUENCE IS NOW, stated precisely because it has NARROWED and the
// qualifier reads as if it had not. That corpus used to be imported whole by a fresh
// process, resurrecting the deleted id; the post-delete blob now records what it
// superseded, so a cold import DECLINES the constituent and the deleted document stays
// gone. What is left is a stored blob nothing serves — disk cost until the retained
// reclaim obligation discharges on the next consumer touch — which is still worth
// reporting and is no longer a data defect.
//
// SO THIS FILE IS ABOUT REPORTING, NOT ABOUT THE RECLAIM'S SEMANTICS. The abort
// stays an abort, the constituents stay on disk; what changes is that a delete whose
// re-emit left that state says so.
func TestDeleteSurfacesAnAbortedMergeReclaim(t *testing.T) {
	t.Parallel()

	t.Run("an aborted reclaim during a delete reaches the caller", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		mgr, gt, name, fdm, ic, docs := deleteFieldRetryFixture(t, "reclaimabort", deleteFixtureN)
		victim := docs[0]

		// EXACTLY ONE PUT FAILS, AND IT IS THE RECLAIM'S. The delete's first write
		// through this cache is reclaimMerged's consolidated-blob Put; every write
		// after it — persistResident's, and any retry of it — succeeds. That is the
		// precise interleaving the bounded retry made silent: a disk error that hits
		// the reclaim and has cleared by the time the re-emit's own write runs.
		ic.failPutUntil = 1

		err := mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID})

		// THE FIXTURE ASSERTION FIRST, because everything below is meaningless if the
		// reclaim did not actually abort. A reclaim that ran to completion removes the
		// constituents it superseded; an aborted one removes nothing at all.
		require.Empty(t, ic.removedSet(),
			"FIXTURE PRECONDITION: the injected failure must have ABORTED the merge reclaim — a "+
				"non-empty removal set means the reclaim's Put succeeded and this test is measuring "+
				"a different interleaving")

		require.Error(t, err,
			"a delete whose re-emit aborted a merge reclaim must NOT report clean success — "+
				"DeleteFromBuckets' error is the only channel the not-durable qualifier travels on, "+
				"so a nil here is the caller being told a resurrectable delete was durable")
		require.ErrorIs(t, err, errInjectedPutFailure,
			"and it must carry the DISK's own error, so the qualifier names the real cause rather "+
				"than one this path invented")
		require.ErrorContains(t, err, "merge reclaim ABORTED",
			"the report must name WHAT failed — a bare disk error reads as a failed re-emit write, "+
				"which is a different state with a different remedy")
		require.ErrorContains(t, err, "NOT removed",
			"and it must name WHAT STAYS RESIDENT, because that is the whole consequence: the "+
				"pre-delete constituents are still on disk")

		// THE PHYSICAL STATE THE REPORT IS ABOUT, asserted rather than assumed: the
		// re-emit's own write DID land (so this is a recovered write, not a failed one)
		// and the un-reclaimed constituent is still on disk.
		requireResidentSetBackedByL2(t, fdm, ic)
		require.NotEmpty(t, staleOnDisk(l2BM25IDs(mgr.cacheDir, name), servingIDs(fdm)),
			"the un-reclaimed pre-delete constituent must still be ON DISK — that is what the report "+
				"is about, and what the caller is warned to expect")

		// AND IT NO LONGER RESURRECTS THE DELETED ID, which is a narrowing of what this
		// report means rather than a reason to stop making it. The qualifier used to
		// cover a documented resurrection: a fresh engine imported the constituent and
		// resolved the deleted document again. Two separate mechanisms now prevent that,
		// one per format: the post-delete FIELD blob records what it superseded, so a
		// cold import declines the stranded constituent; and the VECTOR blob — which this
		// delete deliberately did not rewrite at all — is masked by the durable tombstone
		// record the delete sealed. What is left is stored blobs nothing serves, which is
		// disk cost and not a data defect.
		require.False(t, residentInFreshEngine(t, ctx, mgr.cacheDir, gt, name, victim),
			"a FRESH engine over this corpus must not resolve the deleted id")
	})

	t.Run("known-negative: a clean delete carries no qualifier", func(t *testing.T) {
		t.Parallel()

		// WITHOUT THIS LEG the test above is satisfied by an implementation that
		// reports an aborted reclaim on every delete, which would put a false
		// not-durable warning in front of every caller in the product.
		ctx := context.Background()
		mgr, gt, name, fdm, ic, docs := deleteFieldRetryFixture(t, "reclaimclean", deleteFixtureN)
		victim := docs[0]

		require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}),
			"a delete whose every write lands must report clean success")

		// AND THE INSTRUMENT CAN READ THE OTHER VALUE. The failing leg's emptiness
		// assertion is only evidence of an abort if a completed reclaim is observably
		// non-empty here.
		require.NotEmpty(t, ic.removedSet(),
			"CONTROL: a clean reclaim DOES remove the constituents it superseded, so the empty "+
				"removal set in the leg above is measuring the abort rather than an inert fixture")

		requireResidentSetBackedByL2(t, fdm, ic)
		require.False(t, residentInFreshEngine(t, ctx, mgr.cacheDir, gt, name, victim),
			"CONTROL: a clean delete leaves no copy of the id on disk, so the resurrection asserted "+
				"in the leg above is the abort's doing")
	})

	t.Run("the scoping: a non-delete re-emit keeps today's behaviour", func(t *testing.T) {
		t.Parallel()

		// WHAT THIS PINS IS THE SCOPE, NOT A VIRTUE. ReplaceBucket is the exported
		// re-emit, driven with an ADD shape by callers outside this package whose
		// failure model this change did not examine, so it keeps logAbortedReclaimOnly:
		// an aborted reclaim there stays the ERROR log it has always been and does not
		// become the caller's error. Without this leg the scoping is an unverified
		// claim — an implementation that surfaced the abort on EVERY path passes every
		// other assertion in this file, because no other fixture drives a non-delete
		// re-emit into an abort at all.
		ctx := context.Background()
		mgr, gt, name, _, ic, victim := deleteRetryFixture(t, "reclaimscope")

		ic.failPutUntil = 1

		require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, []searchengine.ExternalID{victim.ID}, nil),
			"the exported re-emit must return exactly what it returned before this change — a "+
				"non-nil here means the delete path's report reached a caller that was never scoped "+
				"for it")

		// AND THE ABORT REALLY HAPPENED, or the nil above is the ordinary clean path
		// and this leg pins nothing at all.
		require.Empty(t, ic.removedSet(),
			"FIXTURE PRECONDITION: the injected failure must have ABORTED this re-emit's reclaim — "+
				"a non-empty removal set means nothing was suppressed and the assertion above is vacuous")
	})
}
