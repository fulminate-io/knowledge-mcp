// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// vecContentDocs builds n docs carrying BOTH a deterministic 32-byte vector (for
// the HNSW engine) and a content field (for the BM25 engine), so the same corpus
// drives either format. ids are zero-padded for stable ordering.
func vecContentDocs(n int) []searchengine.Document {
	return vecContentDocsSeed(n, 0)
}

// vecContentDocsSeed is vecContentDocs with an id+vector offset so two corpora
// built with distinct seeds never share an id OR a vector (needed when a test adds
// a second batch to an engine that already holds the base corpus).
func vecContentDocsSeed(n, seed int) []searchengine.Document {
	docs := make([]searchengine.Document, n)
	for i := range docs {
		idx := seed + i
		vec := make([]byte, 32)
		for b := range vec {
			vec[b] = byte((idx*31 + b*7 + seed*13) % 251)
		}
		id := fmt.Sprintf("doc-%05d", idx)
		docs[i] = searchengine.Document{
			ID:     id,
			Vector: vec,
			Fields: map[string]string{searchengine.FieldContent: "alpha beta " + id},
		}
	}
	return docs
}

// consolidatedHNSWBlob builds a REAL, decodable HNSW segment over the surviving
// docs — the blob a merge of those docs would have produced, carrying its own
// distinct content hash. Its own engine keeps merge off so the survivors land in
// exactly one segment.
func consolidatedHNSWBlob(t *testing.T, survivors []searchengine.Document) searchengine.SegmentBlob {
	t.Helper()
	eng := searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{
		MinSegmentDocs:     len(survivors),
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
	})
	defer eng.Close()
	require.NoError(t, eng.Add(survivors))
	require.NoError(t, eng.Flush())
	blobs := eng.Export()
	require.Len(t, blobs, 1, "the survivors consolidate into exactly one HNSW segment")
	return blobs[0]
}

// consolidatedBM25Blob is the BM25 counterpart of consolidatedHNSWBlob.
func consolidatedBM25Blob(t *testing.T, survivors []searchengine.Document) searchengine.SegmentBlob {
	t.Helper()
	eng := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
		MinSegmentDocs:     len(survivors),
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
	})
	defer eng.Close()
	require.NoError(t, eng.Add(survivors))
	require.NoError(t, eng.Flush())
	blobs := eng.Export()
	require.Len(t, blobs, 1, "the survivors consolidate into exactly one BM25 segment")
	return blobs[0]
}

// applyMerge reproduces, on a factory-built engine, the state a completed merge
// leaves behind: the consolidated segment becomes resident, the superseded
// constituents leave the searchable set, and the reclaim handler runs over L2.
//
// The engines this package builds have the automatic merge triggers disarmed, so
// no background merge can supply that occasion any more. This drives the same two
// effects doMerge produces — the set swap (Import + Unload here, one CAS
// withReplaced there) followed by the OnMerge handler — through the existing
// exported surface, keeping these tests on a REAL Manager-built engine. Driving
// reclaimMerged with a synthetic MergeResult is the established idiom in this
// package (reclaim_crash_test.go, manager_reclaim_test.go); the set swap is
// paired with it because reclaimMerged is L2-only and every assertion about the
// post-merge RESIDENT set would otherwise be observing a set no merge touched.
//
// NOT AN IDIOM TO COPY INTO PRODUCTION: Import-then-Unload is TWO CAS operations,
// so between them the consolidated segment and the segments it supersedes are
// both live — a transient window in which a concurrent reader would see the same
// document twice. It is safe here only because these tests have no concurrent
// reader. Production code must perform the swap in ONE CAS.
func applyMerge[Q, S any](
	t *testing.T, dm *distManager[Q, S], removed []searchengine.SegmentID, merged searchengine.SegmentBlob,
) {
	t.Helper()
	require.NoError(t, dm.engine.Import([]searchengine.SegmentBlob{merged}, nil))
	dm.engine.Unload(removed)
	dm.reclaimMerged(searchengine.MergeResult{Removed: removed, Merged: merged})
}

// waitForMerge polls until the engine's merge counter shows at least one merge has
// fired, and FAILS the test when the 30s deadline elapses instead of returning
// quietly. The threshold is "at least one merge": every caller drives a single
// occasion on a freshly built engine.
//
// A wait that can never be satisfied — an engine whose automatic merge triggers are
// disarmed — must be an immediate red, not a silent 30-second tax. Returning the
// current count on timeout is what let TestReclaimBoundedGrowth burn four dead 30s
// waits while still passing. The deadline is generous so a real HNSW/BM25 merge of a
// 1024-doc segment completes even under -race instrumentation (which slows
// Build/Merge several-fold). Only tests that build their own engines with merge
// ARMED may call it; the Manager-built engines here drive the occasion through
// applyMerge instead.
func waitForMerge(t *testing.T, mergeCount func() uint64, what string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if mergeCount() >= 1 {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("%s: no merge ever fired within 30s (merge count still %d) — "+
		"the engine's merge trigger is disarmed or the occasion never arrived", what, mergeCount())
}

// TestManagerMergeReclaim drives a REAL embed engine (both HNSW via managerFor and
// BM25 via bm25ManagerFor) through a merge and asserts the reclaim handler clears
// the superseded constituent from the live L2 cache: pre-merge the constituent is
// warm (it was shipped), post-merge it is gone and the consolidated segment is
// present. This proves the embed path's reclaim behavior end-to-end on an engine
// built through Manager construction.
//
// The merge occasion is applied directly (applyMerge) rather than provoked by a
// dead-ratio wait: these engines are built with the automatic triggers disarmed,
// so waiting on one would wait forever. What the reclaim DOES is unchanged and
// asserted below; that a real background trigger reaches the hook is covered by
// TestMergeReclaimHappyPath, which builds its engines with merge armed.
func TestManagerMergeReclaim(t *testing.T) {
	t.Parallel()

	// Every batch seals now, so the corpus size no longer has to clear a threshold.
	// It is chosen instead to keep the graph inside a SINGLE partition through the
	// tick, so the reclaim still has exactly one constituent to work on: the tick
	// counts the incoming window alongside the resident set, so a half-threshold
	// corpus derives one partition.
	const seal = searchengine.DefaultMinSegmentDocs / 2
	docs := vecContentDocs(seal)
	ctx := context.Background()

	t.Run("hnsw", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		gt, name := kgtypes.GraphCode, "mergereclaim-hnsw"

		// The write force-seals the batch and the tick ships the re-emitted partition,
		// warming its L2 file.
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
		dm := mgr.managerFor(gt, name)
		pre := dm.engine.Export()
		require.Len(t, pre, 1, "one sealed segment expected")
		constituentID := pre[0].ID
		_, warm := dm.cache.Get(constituentID)
		require.True(t, warm, "shipped constituent must be warm in L2 before the merge")

		// Delete > 33% of the segment, then consolidate the survivors — the shape a
		// dead-ratio merge produced when the trigger was armed.
		dead := seal/3 + 1
		for i := range dead {
			dm.engine.Delete(docs[i].ID)
		}
		applyMerge(t, dm, []searchengine.SegmentID{constituentID}, consolidatedHNSWBlob(t, docs[dead:]))

		mergedID := assertReclaimed(t, dm, constituentID)
		_, mergedWarm := dm.cache.Get(mergedID)
		require.True(t, mergedWarm, "consolidated segment must be warm in L2 after merge")
	})

	t.Run("bm25", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		gt, name := kgtypes.GraphCode, "mergereclaim-bm25"

		require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
		dm := mgr.bm25ManagerFor(gt, name)
		pre := dm.engine.Export()
		require.Len(t, pre, 1, "one sealed BM25 segment expected")
		constituentID := pre[0].ID
		_, warm := dm.cache.Get(constituentID)
		require.True(t, warm, "shipped BM25 constituent must be warm in L2 before the merge")

		dead := seal/3 + 1
		for i := range dead {
			dm.engine.Delete(docs[i].ID)
		}
		applyMerge(t, dm, []searchengine.SegmentID{constituentID}, consolidatedBM25Blob(t, docs[dead:]))

		mergedID := assertReclaimed(t, dm, constituentID)
		_, mergedWarm := dm.cache.Get(mergedID)
		require.True(t, mergedWarm, "consolidated BM25 segment must be warm in L2 after merge")
	})
}

// assertReclaimed asserts the superseded constituent is gone from the live L2
// cache and returns the merged segment's id from the post-merge Export. The
// reclaim handler runs synchronously on the calling goroutine, so there is
// nothing to wait for.
func assertReclaimed[Q, S any](t *testing.T, dm *distManager[Q, S], constituentID searchengine.SegmentID) searchengine.SegmentID {
	t.Helper()
	if _, ok := dm.cache.Get(constituentID); ok {
		t.Fatalf("superseded constituent %s was NOT reclaimed from the live L2 cache", constituentID)
	}
	post := dm.engine.Export()
	require.Len(t, post, 1, "exactly one consolidated segment after the merge")
	require.NotEqual(t, constituentID, post[0].ID, "merge must produce a new content hash")
	return post[0].ID
}

// THE TWO-ENGINE HOOK TEST THAT LIVED HERE IS GONE. It asserted the DETERMINISTIC
// rebuild engine carried NO merge hook while the embed engine did — a contrast between
// two engines, and there is one engine per format now. What survives of its claim (the
// hook stays wired at all, which the factory collapse could have dropped) is asserted by
// TestFactoryWiresReclaimHookDespiteDisarm in manager_merge_scoping_test.go rather than
// duplicated here.
