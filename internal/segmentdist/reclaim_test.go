// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// warmExported writes every currently-sealed segment into the (instrumented) L2
// cache, modeling the ship-warming that puts a shipped constituent on disk before
// a later background merge supersedes it. Returns the set of ids warmed.
func warmExported[Q, S any](dm *distManager[Q, S]) map[searchengine.SegmentID]struct{} {
	warmed := make(map[searchengine.SegmentID]struct{})
	for _, b := range dm.engine.Export() {
		dm.cache.Put(b.ID, b.Bytes)
		warmed[b.ID] = struct{}{}
	}
	return warmed
}

// TestMergeReclaimHappyPath drives a count-driven background merge on a real engine
// (HNSW and BM25) and asserts the wired reclaim hook: the consolidated blob is
// present in L2, every superseded constituent is gone, the invariant holds, and
// full-corpus search still returns every added doc.
//
// THIS IS THE END-TO-END TRIGGER-TO-HOOK SENTINEL, and it is the only one left.
// It builds its engines through the reclaim testkit with merge ARMED, so a real
// background trigger reaches Options.OnMerge here. The engines a Manager builds
// have those triggers disarmed, so the Manager-level reclaim tests apply the merge
// occasion directly instead and assert only what the reclaim DOES. Do not
// "restore" a MergeCount assertion to those tests: on a Manager-built engine the
// count cannot move, and this test is where that link is kept instead.
func TestMergeReclaimHappyPath(t *testing.T) {
	t.Parallel()

	const nSeg = 8
	docs := vecContentDocs(nSeg)

	t.Run("hnsw", func(t *testing.T) {
		// countTarget 4 < 8 segments → count-driven merge fires.
		dm, ic := buildHNSWReclaimManager(t, kgtypes.GraphCode, "happy-hnsw", t.TempDir(), 4)
		defer dm.engine.Close()

		for _, d := range docs {
			require.NoError(t, dm.engine.Add([]searchengine.Document{d}))
		}
		// Warm every sealed segment into L2 (models ship-warming). Some may already
		// have been superseded by an async merge; warm whatever is currently sealed.
		warmExported(dm)

		waitForMerge(t, dm.engine.MergeCount, "count-driven merge must fire")
		waitReclaimSettled(t, dm.engine)
		warmExported(dm) // ensure the final consolidated blob(s) are L2-backed

		assertReclaimHappened(t, ic)

		// Invariant clauses 1+2 (every live id L2-backed; no removed id still live).
		assertLiveSetBackedByL2(t, dm, ic.removedSet(), nil, nil)
		// HNSW search correctness: every doc recoverable via self-recall, no leaks.
		recall, leaked := hnswRecallOK(dm, docs, map[searchengine.ExternalID]struct{}{})
		require.False(t, leaked, "no absent doc may leak")
		require.GreaterOrEqual(t, recall, 0.99, "every added doc recoverable post-merge (recall=%.3f)", recall)
	})

	t.Run("bm25", func(t *testing.T) {
		dm, ic := buildBM25ReclaimManager(t, kgtypes.GraphCode, "happy-bm25", t.TempDir(), 4)
		defer dm.engine.Close()

		for _, d := range docs {
			require.NoError(t, dm.engine.Add([]searchengine.Document{d}))
		}
		warmExported(dm)

		waitForMerge(t, dm.engine.MergeCount, "count-driven merge must fire")
		waitReclaimSettled(t, dm.engine)
		warmExported(dm)

		assertReclaimHappened(t, ic)

		// Invariant clauses 1+2 plus clause-3 exact full-corpus search (BM25 enumerable).
		expectLive := idSetExcept(docs, map[searchengine.ExternalID]struct{}{})
		assertLiveSetBackedByL2(t, dm, ic.removedSet(), expectLive, bm25SearchAllLiveIDs(dm, len(docs)))
	})
}

// assertReclaimHappened asserts the reclaim hook actually ran: at least one Put
// (the consolidated blob) and at least one Remove (a superseded constituent), with
// every Remove ordered AFTER the merged Put that anchored it.
func assertReclaimHappened(t *testing.T, ic *instrumentedCache) {
	t.Helper()
	require.NotEmpty(t, ic.removedSet(), "the merge must have reclaimed at least one superseded constituent")
	require.GreaterOrEqual(t, ic.firstIndex("put", ""), 0, "the merged blob must have been Put")
	// Every remove must come after SOME put (crash-safe ordering holds per-reclaim;
	// the first put in the log is a lower bound on the first reclaim's put).
	firstPut := ic.firstIndex("put", "")
	for i, op := range ic.opLog() {
		if op.kind == "remove" {
			require.Greater(t, i, firstPut, "every Remove must follow the merged Put (crash-safe order)")
		}
	}
}

// TestReclaimContentSharedIdRetained pins that a constituent the merge did NOT
// supersede is retained: a dead-ratio merge consolidates only the DIRTY segment;
// a sibling clean segment stays in Export() and its L2 file is never Removed.
func TestReclaimContentSharedIdRetained(t *testing.T) {
	t.Parallel()

	// High count target so ONLY the dead-ratio trigger fires (not count-driven).
	dm, ic := buildHNSWReclaimManager(t, kgtypes.GraphCode, "shared-id", t.TempDir(), 1<<30)
	defer dm.engine.Close()

	// Segment A: a 4-doc "dirty" segment we will push past the dead ratio.
	dirty := vecContentDocs(4)
	require.NoError(t, dm.engine.Add(dirty))
	// Segment B: a separate single-doc "clean" segment (own Add → own segment).
	cleanDoc := vecContentDocs(1)[0]
	cleanDoc.ID = "clean-keep"
	require.NoError(t, dm.engine.Add([]searchengine.Document{cleanDoc}))

	// Identify the clean segment's id: it is the unique single-doc segment (the
	// 4-doc dirty segment has DocCount 4).
	var cleanID searchengine.SegmentID
	for _, b := range dm.engine.Export() {
		if b.DocCount == 1 {
			cleanID = b.ID
		}
	}
	require.NotEmpty(t, cleanID, "must locate the clean single-doc segment id")
	warmExported(dm)

	// Delete 2 of the 4 dirty docs → 50% dead ≥ 0.33 → only segment A is eligible.
	dm.engine.Delete(dirty[0].ID)
	dm.engine.Delete(dirty[1].ID)
	waitForMerge(t, dm.engine.MergeCount, "dead-ratio merge must fire")
	waitReclaimSettled(t, dm.engine)
	warmExported(dm)

	// The clean segment's id was NOT superseded: still live, never Removed, L2 kept.
	stillLive := false
	for _, b := range dm.engine.Export() {
		if b.ID == cleanID {
			stillLive = true
		}
	}
	require.True(t, stillLive, "the clean segment must remain in Export() (merge did not supersede it)")
	_, removed := ic.removedSet()[cleanID]
	require.False(t, removed, "the clean segment id must NOT be in the reclaim Removed set")
	_, l2 := dm.cache.Get(cleanID)
	require.True(t, l2, "the clean segment's L2 file must be retained")

	assertLiveSetBackedByL2(t, dm, ic.removedSet(), nil, nil)
}

// TestReclaimReadOnlyNoRemoval pins the authoritative-liveness guarantee: a loaded
// corpus with ZERO writes/merges produces ZERO cache.Remove calls and retains
// every segment — age is NEVER the eviction signal. The engine is constructed with
// a high SegmentCountTarget so a >16-segment load does NOT trip a count-driven
// merge (which would legitimately consolidate and test the wrong thing).
func TestReclaimReadOnlyNoRemoval(t *testing.T) {
	t.Parallel()

	// 20 segments (each its own Add) — would exceed the default target of 16, so the
	// high SegmentCountTarget is load-bearing here.
	const nSeg = 20
	dm, ic := buildHNSWReclaimManager(t, kgtypes.GraphCode, "read-only", t.TempDir(), 1<<30)
	defer dm.engine.Close()

	docs := vecContentDocs(nSeg)
	for _, d := range docs {
		require.NoError(t, dm.engine.Add([]searchengine.Document{d}))
	}
	require.GreaterOrEqual(t, len(dm.engine.Export()), nSeg, "each doc seals its own segment (no count-merge)")
	warmExported(dm)
	beforeIDs := exportedIDSet(dm)

	// A BOUNDED OPPORTUNITY WITH NO WRITES: the merger ticks but finds no eligible
	// target. The opportunity is explicit here, and it has to be. This test asserts
	// that the merger DECLINED, and the completion wait below is a terminal-state
	// predicate: on a corpus that has published nothing and is not eligible it holds
	// on its first evaluation, at zero elapsed time. "The merger declined" measured
	// at zero elapsed time is a tautology — it never got a tick. So the merger is
	// given a bounded run of ticks first, and only then is the wait taken.
	//
	// This is not the longer-window fix the ticket rules out: it grants an occasion
	// at a site that has no flake, rather than widening a window at a site that does.
	time.Sleep(readOnlyMergeOpportunity)
	waitReclaimSettled(t, dm.engine)

	require.Equal(t, uint64(0), dm.engine.MergeCount(), "no merge may fire on a read-only corpus")
	require.Empty(t, ic.removedSet(), "a read-only corpus must produce ZERO cache.Remove calls")
	require.Equal(t, beforeIDs, exportedIDSet(dm), "every segment — including old ones — is retained")

	assertLiveSetBackedByL2(t, dm, ic.removedSet(), nil, nil)
}

// exportedIDSet is the set of segment ids currently in the engine's Export().
func exportedIDSet[Q, S any](dm *distManager[Q, S]) map[searchengine.SegmentID]struct{} {
	out := make(map[searchengine.SegmentID]struct{})
	for _, b := range dm.engine.Export() {
		out[b.ID] = struct{}{}
	}
	return out
}

// readOnlyMergeOpportunity is how long TestReclaimReadOnlyNoRemoval lets the
// background merger run before asserting that it declined to merge. It is
// expressed in merger ticks: searchengine's mergeTickInterval is 50ms
// (searchengine/merge.go, unexported), so this is ten ticks — many evaluations of
// the trigger policy, and short enough that a test asserting a negative does not
// become the package's slowest.
const readOnlyMergeOpportunity = 500 * time.Millisecond

// THE QUIESCENCE WINDOW THAT LIVED HERE IS GONE. waitMergeQuiesce and
// waitMergeQuiesceWindow returned once the engine's MERGE COUNTER had not moved
// for a wall-clock window (120ms by default, 40ms for the property fuzz net).
//
// THEY OBSERVED THE WRONG EVENT. That counter increments at the CAS publish, before
// the OnMerge hook that writes the consolidated blob to L2 and removes the segments
// it superseded, so a stable counter meant "nothing new was published recently" and
// never "the reclaim finished". A whole window could elapse inside one hook, and
// the test then inspected a corpus whose live merged segment had no L2 file. Two
// tests in this family failed that way under parallel load, both reporting an empty
// removed set after a counted merge.
//
// WHAT REPLACED THEM: waitReclaimSettled (reclaim_wait_test.go), which waits on
// merge COMPLETION — published merges equal settled merges, and the engine would
// pick no new merge target — and fails the test on its deadline instead of
// returning quietly. A wider window was never the fix; the event was.
