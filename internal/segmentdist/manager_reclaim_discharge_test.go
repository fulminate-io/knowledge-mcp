// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_reclaim_discharge_test.go is the gate on the merge reclaim's abort being
// RECOVERABLE rather than PERMANENT.
//
// WHAT CHANGED AND WHAT DID NOT. reclaimMerged still ABORTS on a failed Put — not one
// constituent is removed when the consolidated blob did not land, which is the
// crash-safety property TestReclaimAbortsWhenTheMergedBlobCannotBePersisted pins and
// which this file does not touch. What changed is that the abort now RETAINS the
// merge's supersession obligation (the merged bytes plus the ids they supersede) and
// discharges it on a later consumer touch, instead of discarding it and leaving the
// constituents on disk for the life of the process.
//
// THE TESTS BELOW DRIVE THE PRODUCTION TOUCH. The discharge is reached from
// Manager.Search, beside the mapping-repair drain it is modeled on, so a test that
// called drainReclaimPending directly would prove the method and not the wiring.
type reclaimDischargeFixture struct {
	mgr    *Manager
	gt     kgtypes.GraphType
	name   string
	hdm    *distManager[[]byte, struct{}]
	ic     *instrumentedCache
	victim searchengine.Document
	// serving is the pool's serving set AFTER the aborted delete, read with no
	// re-import in between; stale is what the abort left on disk beside it.
	serving map[searchengine.SegmentID]struct{}
	stale   map[searchengine.SegmentID]struct{}
}

// abortedReclaimDischargeFixture drives a delete and the DRAIN that re-emits its vector
// partition, with that drain's merge reclaim ABORTING, and returns the pool, its
// instrumented cache, and the ids the abort stranded.
//
// THE DRAIN IS THE DRIVER BECAUSE THE DELETE NO LONGER IS: a delete's vector leg is a
// live-bit kill that consolidates nothing, so the pass that can abort a reclaim in this
// pool is the deferred re-emit.
//
// IT ASSERTS THE ABORT RATHER THAN ASSUMING IT, exactly as abortedReclaimPool does, and
// through the pool's own abort mark for the same reason: the drain logs an aborted
// reclaim rather than returning it, so a nil from the drain says nothing either way. An
// injection that missed strands nothing, under which every assertion downstream is about
// an empty set. It keeps its own copy rather than widening that helper because it
// additionally needs the instrumented cache, which is what makes "was anything removed"
// observable.
func abortedReclaimDischargeFixture(t *testing.T, name string) reclaimDischargeFixture {
	t.Helper()

	ctx := context.Background()
	mgr, gt, nm, hdm, ic, victim := deleteRetryFixture(t, name)

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, nm, []searchengine.ExternalID{victim.ID}),
		"FIXTURE PRECONDITION: the delete itself must be clean — it writes no vector blob")

	// EXACTLY ONE PUT FAILS AND IT IS THE RECLAIM'S — the merge hook Puts the
	// consolidated blob before persistResident writes anything, so the first Put of
	// the drain is the reclaim's.
	mark := hdm.reclaimAbortMark()
	ic.failPutUntil = 1
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, nm))
	require.Error(t, hdm.abortedReclaimSince(mark),
		"FIXTURE PRECONDITION: the drain's merge reclaim must have ABORTED — no abort since the mark "+
			"means the injection never fired and no constituent was stranded")
	require.Empty(t, ic.removedSet(),
		"FIXTURE PRECONDITION: the injected failure must have ABORTED the merge reclaim before a "+
			"single constituent was removed — a non-empty removal set means the reclaim completed")

	serving := servingIDs(hdm)
	stale := staleOnDisk(l2HNSWIDs(mgr.cacheDir, nm), serving)
	require.NotEmpty(t, stale,
		"FIXTURE PRECONDITION: the aborted reclaim must have left at least one .seg on disk that the "+
			"pool no longer serves — an empty stale set makes every assertion below vacuous")

	return reclaimDischargeFixture{
		mgr: mgr, gt: gt, name: nm, hdm: hdm, ic: ic, victim: victim,
		serving: serving, stale: stale,
	}
}

// TestAbortedReclaimDischargesOnTheNextConsumerTouch is the seam's headline property:
// the constituents an abort stranded are reclaimed by the next search, without the
// merge that superseded them ever running again.
func TestAbortedReclaimDischargesOnTheNextConsumerTouch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := abortedReclaimDischargeFixture(t, "reclaimdischarge")

	// THE STATE BEFORE THE TOUCH, asserted so the assertions after it are a
	// transition rather than a coincidence: the injected failure is spent (every
	// later Put succeeds) and yet nothing has converged on its own.
	require.Empty(t, f.ic.removedSet(),
		"CONTROL: nothing is reclaimed by the mere passage of the delete — the discharge below is "+
			"the consumer touch's doing")
	before := l2HNSWIDs(f.mgr.cacheDir, f.name)
	for id := range f.stale {
		require.Contains(t, before, id,
			"CONTROL: the stranded constituent %s is on disk before the touch", id)
	}

	// ONE consumer touch, through the production entry point.
	_, err := f.mgr.Search(ctx, f.gt, f.name, "delretry", f.victim.Vector, 5)
	require.NoError(t, err)

	after := l2HNSWIDs(f.mgr.cacheDir, f.name)
	for id := range f.stale {
		require.NotContains(t, after, id,
			"the stranded constituent %s must be reclaimed on the next consumer touch — the abort is "+
				"recoverable, not permanent", id)
	}
	// AND THE CONSOLIDATED BLOB IS DURABLE, which is the half that makes the removal
	// legal at all: post-merge it is the ONLY copy of those constituents' documents,
	// so a discharge that removed them without persisting it would be the false prune
	// the abort exists to prevent.
	for id := range servingIDs(f.hdm) {
		require.Contains(t, after, id,
			"the blob the pool serves (%s) must be on disk after the discharge", id)
	}
	require.NotEmpty(t, after, "and the pool's L2 corpus is not left empty")

	// AND THE POOL IS NOT LEFT SERVING A SEGMENT WITH NO FILE BEHIND IT. The search
	// above ran load() before the drain, so the L2 import re-added the stranded
	// constituent to the ENGINE; a discharge that unlinked the file without unloading
	// the entry would leave the pool serving bytes nothing on disk backs, which breaks
	// the property persistResident establishes and disarms eviction for the whole pool.
	requireResidentSetBackedByL2(t, f.hdm, f.ic)
}

// nthPutIndex returns the op-log index of the nth (1-based) Put of id, or -1.
//
// IT COUNTS RATHER THAN FINDING THE FIRST because an aborted reclaim ALREADY Put the
// merged id once — that call is what failed — so "a Put of the merged blob precedes the
// first Remove" is satisfied by the failed one and would assert nothing about the
// discharge. The SECOND Put is the discharge's own.
func nthPutIndex(ic *instrumentedCache, id searchengine.SegmentID, n int) int {
	seen := 0
	for i, op := range ic.opLog() {
		if op.kind != "put" || op.id != id {
			continue
		}
		seen++
		if seen == n {
			return i
		}
	}
	return -1
}

// TestReclaimDischargeKeepsThePutBeforeRemoveOrdering is the crash-safety half, at the
// discharge rather than at the original reclaim.
//
// WHY IT NEEDS ITS OWN FIXTURE. On the delete path the consolidated blob lands anyway —
// persistResident writes the post-delete resident set right after the merge hook — so a
// discharge that removed the constituents WITHOUT persisting anything would still leave
// a recoverable corpus there, and no assertion on that fixture can tell the two apart. A
// merge that is NOT followed by a re-emit write (the background merge the engine runs on
// segment count) is where the Put is the only thing standing between a discharge and the
// whole corpus segment gone, so it is the fixture this property is asserted on.
func TestReclaimDischargeKeepsThePutBeforeRemoveOrdering(t *testing.T) {
	t.Parallel()

	constituents, merged := realMergeBlobs(t)
	docs := vecContentDocs(2)
	res := searchengine.MergeResult{
		Merged:  merged,
		Removed: []searchengine.SegmentID{constituents[0].ID, constituents[1].ID},
	}

	// abortedObligation seeds the constituents, aborts one reclaim over them, and
	// returns the pool holding the retained obligation.
	abortedObligation := func(t *testing.T) (string, *diskSegmentCache, *instrumentedCache, *distManager[mockQuery, mockStats]) {
		t.Helper()
		dir := t.TempDir()
		real := newDiskSegmentCache(dir, 0, adviceRandom)
		for _, b := range constituents {
			require.NoError(t, real.Put(b.ID, b.Bytes),
				"fixture: seeding a constituent must succeed, or the test proves nothing")
		}
		ic := newInstrumentedCache(real)
		ic.failPut = true
		dm := newReclaimDMOverCache(t, ic)

		dm.reclaimMerged(res)
		require.Empty(t, ic.removedSet(),
			"FIXTURE PRECONDITION: the failed Put must have aborted the reclaim before any removal")
		dm.resMu.Lock()
		pending := len(dm.reclaimPending)
		dm.resMu.Unlock()
		require.Equal(t, 1, pending,
			"FIXTURE PRECONDITION: the abort must have RETAINED its supersession obligation — with "+
				"nothing retained there is nothing for the drain below to discharge")
		return dir, real, ic, dm
	}

	t.Run("a_discharge_persists_the_merged_blob_before_it_removes", func(t *testing.T) {
		t.Parallel()
		dir, real, ic, dm := abortedObligation(t)

		ic.failPut = false // the transient disk error clears
		require.NoError(t, dm.drainReclaimPending())

		_, ok := real.Get(merged.ID)
		require.True(t, ok,
			"the discharge must PERSIST the consolidated blob — post-merge it is the only durable copy "+
				"of the constituents' documents, and removing them without it is the false prune the "+
				"abort exists to prevent")
		require.Len(t, ic.removedSet(), 2, "and then reclaim both constituents")

		put := nthPutIndex(ic, merged.ID, 2)
		require.Positive(t, put, "the discharge's own Put of the merged blob must be in the op log")
		require.Less(t, put, ic.firstIndex("remove", ""),
			"and it must precede the FIRST Remove — the crash-safe ordering reclaimMerged states, kept "+
				"by the discharge rather than defeated by hand")

		recovered := reloadCorpusFromDir(t, dir, docs)
		for _, d := range docs {
			require.Contains(t, recovered, d.ID,
				"a restart after the discharge must still recover document %s", d.ID)
		}

		dm.resMu.Lock()
		defer dm.resMu.Unlock()
		require.Empty(t, dm.reclaimPending, "and a discharged obligation is dropped rather than re-driven")
	})

	t.Run("a_discharge_whose_put_fails_removes_NOTHING_and_stays_owed", func(t *testing.T) {
		t.Parallel()
		dir, real, ic, dm := abortedObligation(t)

		// THE DISK IS STILL BAD: failPut stays armed.
		require.Error(t, dm.drainReclaimPending(),
			"a discharge that could not persist the consolidated blob must REPORT the failure rather "+
				"than absorb it")
		require.Empty(t, ic.removedSet(),
			"and it must remove NOTHING — the same abort semantics as the original reclaim, applied to "+
				"the retry")
		for _, b := range constituents {
			_, ok := real.Get(b.ID)
			require.True(t, ok, "constituent %s must still be readable", b.ID)
		}
		recovered := reloadCorpusFromDir(t, dir, docs)
		for _, d := range docs {
			require.Contains(t, recovered, d.ID,
				"and a restart still recovers document %s from the constituents", d.ID)
		}

		dm.resMu.Lock()
		defer dm.resMu.Unlock()
		require.Len(t, dm.reclaimPending, 1,
			"the obligation stays owed, so a later touch can discharge it once the disk recovers")
	})

	t.Run("the_bound_stops_re_arming_and_still_removes_nothing", func(t *testing.T) {
		t.Parallel()
		_, real, ic, dm := abortedObligation(t)

		// A DISK THAT IS NOT COMING BACK. The bound must STOP rather than re-arm
		// forever on a cause a retry cannot clear.
		for i := range reclaimMaxAttempts {
			require.Error(t, dm.drainReclaimPending(), "attempt %d must still report the failure", i+1)
		}
		dm.resMu.Lock()
		pending := len(dm.reclaimPending)
		dm.resMu.Unlock()
		require.Zero(t, pending,
			"after reclaimMaxAttempts failed discharges the obligation is dropped and announced, not "+
				"retried forever")
		require.NoError(t, dm.drainReclaimPending(),
			"CONTROL: and a drain with nothing owed is a no-op rather than a repeated failure")
		require.Empty(t, ic.removedSet(),
			"and the forfeit removed nothing: the constituents stay on disk, which is the state the "+
				"bound announces")
		for _, b := range constituents {
			_, ok := real.Get(b.ID)
			require.True(t, ok, "constituent %s survives the forfeit", b.ID)
		}
	})
}

// TestReclaimDischargeRemovesNothingWithoutAnAbort is the known-negative: a pool whose
// reclaims all landed has no obligation to discharge, so the drain must not remove a
// thing.
//
// WITHOUT IT the test above is satisfied by a drain that removes whatever it finds —
// which on this fixture is indistinguishable from the correct behaviour and, on a pool
// with no abort, is a corpus wipe.
func TestReclaimDischargeRemovesNothingWithoutAnAbort(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr, gt, nm, hdm, ic, victim := deleteRetryFixture(t, "reclaimnoabort")

	// THE SAME TWO PASSES the abort fixture drives, with nothing injected: it is the
	// DRAIN's reclaim that removes constituents, so a clean run has to include it.
	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, nm, []searchengine.ExternalID{victim.ID}))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, nm))
	require.NotEmpty(t, ic.removedSet(),
		"CONTROL: a clean drain's reclaim DOES remove the constituents it superseded, so this pool "+
			"has nothing left owing")

	cleanReclaim := ic.removedSet()
	before := l2HNSWIDs(mgr.cacheDir, nm)

	_, err := mgr.Search(ctx, gt, nm, "delretry", victim.Vector, 5)
	require.NoError(t, err)

	require.Equal(t, cleanReclaim, ic.removedSet(),
		"a pool with no aborted reclaim must have nothing to discharge — the drain removed a segment "+
			"nobody owed")
	require.Equal(t, before, l2HNSWIDs(mgr.cacheDir, nm),
		"and its stored corpus is byte-for-byte the same set after the touch")
	require.NotEmpty(t, servingIDs(hdm), "CONTROL: the pool still serves its corpus")
}
