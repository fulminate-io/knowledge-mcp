// SPDX-License-Identifier: Apache-2.0

package segmentdist

// manager_residency_quarantine_test.go covers what a QUARANTINE does to a pool's
// residency bookkeeping, which is a different question from the residency
// mechanics its sibling file tests: those ask when a cold pool may be evicted and
// how it comes back, and this one asks what happens when one of its members has
// been deliberately withdrawn and is never coming back.

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestQuarantinedSegmentDoesNotPinOrBreakThePool is F4, driven rather than
// argued. It covers BOTH interleavings the review named, because they are one
// fact seen at two moments.
//
// THE ENGINE-SIDE WITHDRAWAL DOES NOT REACH EITHER OF THEM, which is what I got
// wrong before reading these two gates. WithdrawSegment removes the id from the
// engine's PUBLISHED SET; the eviction candidate walk reads m.resident, this
// manager's own map, and the strict reload replays m.evictedIDs. Neither is
// touched by an engine-side swap.
//
// LEG (a): the re-materializability gate asks L2 for every resident id, and
// quarantine has just dropped that id's index entry — so the gate counts it
// missing and DECLINES the eviction. Forever: nothing is ever going to put those
// bytes back, so the pool is pinned in memory by one bad segment.
//
// LEG (b): evictedIDs is replayed with tolerateMisses=false, whose contract is
// that a miss is unrecoverable. Correct for a lost id, wrong for one deliberately
// withdrawn — every reload attempt hard-fails and the pool is unsearchable until
// a restart.
//
// WITHDRAWN IS NOT MISSING. Both legs are the gates looking for something nobody
// intends to supply, and forgetQuarantined is what stops them looking.
func TestQuarantinedSegmentDoesNotPinOrBreakThePool(t *testing.T) {
	mgr, eng := residencyPool(t, "f4pool", "alpha", "bravo", "charlie")

	mgr.resMu.Lock()
	residentIDs := make([]searchengine.SegmentID, 0, len(mgr.resident))
	for id := range mgr.resident {
		residentIDs = append(residentIDs, id)
	}
	mgr.resMu.Unlock()
	require.Len(t, residentIDs, 3, "precondition: three discrete resident segments")
	slices.Sort(residentIDs)
	corrupt := residentIDs[0]

	// THE DISPOSITION EXACTLY AS OnCorruptSegment TAKES IT: the engine withdraws
	// the segment from its published set, the cache moves the file aside and drops
	// its index entry, and the manager forgets it from its own residency
	// bookkeeping. The third call is the one under test.
	eng.WithdrawSegment(corrupt)
	// The quarantine is reached through the concrete disk cache: the manager holds
	// the narrower segmentL2Cache interface, which deliberately does not carry a
	// withdrawal verb, and the production callback closes over the concrete cache
	// for exactly that reason.
	disk, isDisk := mgr.cache.(*diskSegmentCache)
	require.True(t, isDisk, "this test asserts against the disk cache's quarantine specifically")
	require.NoError(t, disk.Quarantine(corrupt, errStub{}))
	mgr.forgetQuarantined(corrupt)

	// LEG (a): the pool must still be evictable.
	_, ok := mgr.evictResident()
	require.True(t, ok,
		"a pool holding one quarantined segment must still be evictable: the gate asks L2 for every resident id, "+
			"and a withdrawn id it can never satisfy declines the eviction forever and pins the pool in memory")

	mgr.resMu.Lock()
	evicted := append([]searchengine.SegmentID(nil), mgr.evictedIDs...)
	mgr.resMu.Unlock()
	require.NotContains(t, evicted, corrupt,
		"the withdrawn id must not be recorded for replay, or the strict reload below is asked for bytes nobody will supply")
	require.Len(t, evicted, 2, "the two healthy segments are what was unloaded")

	// LEG (b): the strict reload must re-materialize the survivors rather than
	// hard-fail on the withdrawn one.
	require.NoError(t, mgr.reload(evicted, false),
		"the strict reload must re-materialize the pool without the withdrawn segment; "+
			"replaying it would hard-fail every attempt and leave the pool unsearchable until a restart")

	// KNOWN-POSITIVE FOR LEG (b), because the assertion above passes for two
	// different reasons and only one of them is the fix: the reload succeeds
	// because the withdrawn id is ABSENT from the replay set, not because the
	// strict reload tolerates it. Putting it back proves the second half — a
	// withdrawn id left in evictedIDs hard-fails every attempt, which is the
	// unsearchable-pool leg. Without this arm, a change that silently began
	// tolerating misses would keep the test green and retire the guarantee.
	err := mgr.reload(append(evicted, corrupt), false)
	require.Error(t, err,
		"a withdrawn id replayed under the strict reload must still be unrecoverable — that contract is what makes removing it from the set the fix")
	require.ErrorContains(t, err, "absent from the L2 cache")
}

// errStub is a stand-in corruption reason; Quarantine only logs it.
type errStub struct{}

func (errStub) Error() string { return "test: stored bytes are unreadable" }

// TestPersistResidentDoesNotResurrectAQuarantinedSegment is F2 at the manager
// level — the test I labeled "not reproduced, hours of fixture work" when the
// fixture it needs already existed two files away.
//
// THE RESURRECTION. Quarantine drops the segment's L2 index entry but leaves the
// FILE aside as evidence. If the engine still publishes the id, the next
// persistResident exports it, diffs it against an L2 index that no longer has it,
// concludes it is a NEW blob, and writes the corrupt bytes back under the same
// name — passing the content-address check on the way, because this class hashes
// correctly. The quarantine is undone by ordinary bookkeeping, with nothing in
// the logs to say a withdrawal was reversed.
//
// THE TWO ARMS ARE THE CONTROL. Without the withdrawal the write happens; with it
// the diff has nothing to write. Asserting only the second would pass just as
// well against a persist path that had stopped writing anything at all.
func TestPersistResidentDoesNotResurrectAQuarantinedSegment(t *testing.T) {
	run := func(t *testing.T, withdraw bool) int {
		t.Helper()
		mgr, eng := residencyPool(t, "f2pool", "alpha", "bravo")

		mgr.resMu.Lock()
		ids := make([]searchengine.SegmentID, 0, len(mgr.resident))
		for id := range mgr.resident {
			ids = append(ids, id)
		}
		mgr.resMu.Unlock()
		require.Len(t, ids, 2)
		slices.Sort(ids)
		corrupt := ids[0]

		if withdraw {
			eng.WithdrawSegment(corrupt)
		}
		disk, ok := mgr.cache.(*diskSegmentCache)
		require.True(t, ok)
		require.NoError(t, disk.Quarantine(corrupt, errStub{}))
		mgr.forgetQuarantined(corrupt)

		written, err := mgr.persistResident()
		require.NoError(t, err)
		return written
	}

	t.Run("KNOWN-POSITIVE: without the withdrawal the corrupt bytes are written back", func(t *testing.T) {
		require.Equal(t, 1, run(t, false),
			"the engine still lists the quarantined id, so the resident diff sees a blob L2 lacks and re-writes it — "+
				"this is the resurrection, and without observing it the arm below proves nothing")
	})

	t.Run("with the withdrawal there is nothing to write back", func(t *testing.T) {
		require.Zero(t, run(t, true),
			"a withdrawn segment is not exported, so the diff has no blob to resurrect")
	})
}

// gapCache lands a quarantine inside evictResident's lock gap, without any
// production test seam.
//
// THE SEAM IS ALREADY AN INTERFACE METHOD. evictResident snapshots the resident
// ids under resMu, RELEASES the lock, then probes sizeOf for each id — the
// re-materializability gate — and only afterwards re-takes the lock to record the
// replay set. sizeOf is the gate's only probe and it is part of segmentL2Cache,
// so overriding it on an embedded cache puts a hook exactly in the window,
// reached through the real code path rather than a hole cut for the test.
//
// FIRING ON THE LAST PROBE IS DELIBERATE: every id has already been counted
// present, so the gate passes and the eviction proceeds — which is the only
// interleaving where the stale snapshot can reach the assignment.
type gapCache struct {
	*diskSegmentCache
	probes int
	fireOn int
	onFire func()
}

func (g *gapCache) sizeOf(id searchengine.SegmentID) (int64, bool) {
	size, ok := g.diskSegmentCache.sizeOf(id)
	g.probes++
	if g.probes == g.fireOn && g.onFire != nil {
		g.onFire()
	}
	return size, ok
}

// TestEvictResidentDoesNotReplayAQuarantineFromTheLockGap is R3: the withdrawn id
// must not come back through the strict-reload replay set.
//
// evictResident's snapshot is taken under resMu and the lock is released before
// the gate runs. A quarantine landing in that window has already removed its id
// from m.resident — but not from the slice — so assigning the slice verbatim puts
// a withdrawn id back into evictedIDs, and the strict reload then hard-fails on
// bytes nobody intends to supply. Re-filtering the snapshot against the map as it
// is at assignment time is what closes it.
func TestEvictResidentDoesNotReplayAQuarantineFromTheLockGap(t *testing.T) {
	mgr, eng := residencyPool(t, "gappool", "alpha", "bravo", "charlie")

	mgr.resMu.Lock()
	ids := make([]searchengine.SegmentID, 0, len(mgr.resident))
	for id := range mgr.resident {
		ids = append(ids, id)
	}
	mgr.resMu.Unlock()
	require.Len(t, ids, 3)
	slices.Sort(ids)
	corrupt := ids[0]

	disk, ok := mgr.cache.(*diskSegmentCache)
	require.True(t, ok)
	mgr.cache = &gapCache{
		diskSegmentCache: disk,
		fireOn:           len(ids), // the LAST probe: every id already counted present
		onFire: func() {
			eng.WithdrawSegment(corrupt)
			require.NoError(t, disk.Quarantine(corrupt, errStub{}))
			mgr.forgetQuarantined(corrupt)
		},
	}

	_, evicted := mgr.evictResident()
	require.True(t, evicted, "the gate saw every id present, so the eviction must proceed — that is the window under test")

	mgr.resMu.Lock()
	replay := append([]searchengine.SegmentID(nil), mgr.evictedIDs...)
	mgr.resMu.Unlock()

	require.NotContains(t, replay, corrupt,
		"a quarantine that landed in the lock gap must not be replayed: the strict reload would hard-fail on an id "+
			"deliberately withdrawn, re-opening the leg forgetQuarantined closes")
	require.NoError(t, mgr.reload(replay, false),
		"and the filtered replay set must re-materialize cleanly")
}
