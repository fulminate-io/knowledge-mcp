// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestPruneCacheRefusesAnEmptyLiveSet is the corpus-wipe property at its SECOND
// reachable site.
//
// WHY IT IS REQUIRED WORK IN THIS CHANGE rather than a hardening afterthought:
// prunePoolReport classifies every on-disk id absent from `live` as an orphan and,
// under execute, unlinks it. So an EMPTY live set unlinks EVERY .seg in the pool.
// That was previously unreachable only because a caller-supplied subset check
// against the source's List(0) covered it — a check deleted with that rail, because
// locally List(0) IS the cache the live set was loaded from and the comparison would
// be the cache against itself. Removing that arm without putting this in its place
// would have made a whole-pool wipe reachable on the one destructive path this
// ticket makes universal.
//
// THREE LEGS, and the third is what stops the first from being satisfiable by a
// function that refuses everything:
//
//	(1) EMPTY LIVE SET over a POPULATED pool  -> REFUSED, nothing removed.
//	(2) The refusal is REPORTED, with a reason naming the empty live set. An
//	    unreported refusal is indistinguishable from a pool that had nothing to
//	    prune: both render zero orphans, and an operator reads "nothing to do" where
//	    the truth is "declined to act".
//	(3) KNOWN-POSITIVE: a NON-empty live set over the same pool still prunes the
//	    genuine orphan. Without this leg a prunePoolReport that refused every pool
//	    would pass legs (1) and (2).
func TestPruneCacheRefusesAnEmptyLiveSet(t *testing.T) {
	t.Parallel()

	const (
		liveID   = searchengine.SegmentID("seg-live")
		orphanID = searchengine.SegmentID("seg-orphan")
	)

	newPool := func(t *testing.T) (*Manager, string, string) {
		t.Helper()
		cacheDir := t.TempDir()
		mgr := closeOnCleanup(t, NewManager(cacheDir, 0))
		const name = "pruneRefusalRepo"
		plantBlob(t, cacheDir, name, hnsw.New().Name(), liveID, []byte("live-bytes"))
		plantBlob(t, cacheDir, name, hnsw.New().Name(), orphanID, []byte("orphan-bytes"))
		return mgr, cacheDir, name
	}

	t.Run("an empty live set REFUSES a populated pool and removes nothing", func(t *testing.T) {
		mgr, cacheDir, name := newPool(t)
		dir := graphCacheDirFor(cacheDir, kgtypes.GraphCode, name, hnsw.New().Name())

		var report PruneCacheReport
		target := PruneCacheTarget{GraphType: kgtypes.GraphCode, Name: name}
		require.NoError(t, mgr.prunePoolReport(
			&report, target, hnsw.New().Name(), dir,
			map[searchengine.SegmentID]struct{}{}, true /* execute */))

		require.Equal(t, 0, report.Removed, "an empty live set must remove NOTHING")
		require.Len(t, report.Graphs, 1)
		pool := report.Graphs[0]
		require.True(t, pool.Aborted, "the pool must be reported as refused, not as a pool with nothing to prune")
		require.Contains(t, pool.AbortReason, "empty live set",
			"the reason must NAME the empty live set")
		require.Empty(t, pool.Orphans, "a refused pool classifies nothing as an orphan")

		// THE FILES ARE THE ASSERTION. A report that says "refused" while the unlink
		// already happened would pass every field check above.
		onDisk := onDiskHNSWIDs(t, cacheDir, kgtypes.GraphCode, name)
		require.ElementsMatch(t, []searchengine.SegmentID{liveID, orphanID}, onDisk,
			"both .seg files must survive a refused prune")
	})

	t.Run("KNOWN-POSITIVE: a non-empty live set still prunes the genuine orphan", func(t *testing.T) {
		mgr, cacheDir, name := newPool(t)
		dir := graphCacheDirFor(cacheDir, kgtypes.GraphCode, name, hnsw.New().Name())

		var report PruneCacheReport
		target := PruneCacheTarget{GraphType: kgtypes.GraphCode, Name: name}
		require.NoError(t, mgr.prunePoolReport(
			&report, target, hnsw.New().Name(), dir,
			map[searchengine.SegmentID]struct{}{liveID: {}}, true /* execute */))

		require.Len(t, report.Graphs, 1)
		pool := report.Graphs[0]
		require.False(t, pool.Aborted, "a pool with a real live set is not refused")
		require.Equal(t, []searchengine.SegmentID{orphanID}, pool.Orphans)
		require.Equal(t, 1, report.Removed)

		onDisk := onDiskHNSWIDs(t, cacheDir, kgtypes.GraphCode, name)
		require.Equal(t, []searchengine.SegmentID{liveID}, onDisk,
			"the live blob survives and the orphan is gone — so the refusal above was a refusal, "+
				"not a prune that could never remove anything")
	})
}
