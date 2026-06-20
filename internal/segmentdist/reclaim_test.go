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
func TestMergeReclaimHappyPath(t *testing.T) {
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

		require.GreaterOrEqual(t, waitMergeCount(dm.engine.MergeCount, 1), uint64(1), "count-driven merge must fire")
		waitMergeQuiesce(dm.engine.MergeCount)
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

		require.GreaterOrEqual(t, waitMergeCount(dm.engine.MergeCount, 1), uint64(1), "count-driven merge must fire")
		waitMergeQuiesce(dm.engine.MergeCount)
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
	require.GreaterOrEqual(t, waitMergeCount(dm.engine.MergeCount, 1), uint64(1), "dead-ratio merge must fire")
	waitMergeQuiesce(dm.engine.MergeCount)
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

	// Quiesce window with NO writes: the merger ticks but finds no eligible target.
	waitMergeQuiesce(dm.engine.MergeCount)

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

// waitMergeQuiesce waits until the merge count stays stable across a quiescence
// window, so async merges have finished before the test inspects final state.
func waitMergeQuiesce(mergeCount func() uint64) {
	waitMergeQuiesceWindow(mergeCount, 120*time.Millisecond)
}

// waitMergeQuiesceWindow is waitMergeQuiesce with a caller-chosen stable window.
// The property fuzz net passes a tighter window (its merges are tiny multi-doc
// segments) so it can run many distinct streams within the pre-commit budget,
// while the default callers keep the conservative 120ms window.
func waitMergeQuiesceWindow(mergeCount func() uint64, stableFor time.Duration) {
	last := mergeCount()
	stableStart := time.Now()
	// Generous overall cap so a slow -race-instrumented merge is not mistaken for
	// quiescence before it lands.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		cur := mergeCount()
		if cur != last {
			last = cur
			stableStart = time.Now()
			continue
		}
		if time.Since(stableStart) >= stableFor {
			return
		}
	}
}
