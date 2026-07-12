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

// waitMergeCount polls until the engine's MergeCount reaches at least want, or the
// deadline elapses, returning the final count. The deadline is generous so a real
// HNSW/BM25 merge of a 1024-doc segment completes even under -race instrumentation
// (which slows Build/Merge several-fold).
func waitMergeCount(mergeCount func() uint64, want uint64) uint64 {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if mergeCount() >= want {
			return mergeCount()
		}
		time.Sleep(3 * time.Millisecond)
	}
	return mergeCount()
}

// TestManagerMergeReclaim drives a REAL embed engine (both HNSW via managerFor and
// BM25 via bm25ManagerFor) through a production-threshold dead-ratio merge and
// asserts the wired OnMerge hook reclaims the superseded constituent from the live
// L2 cache: pre-merge the constituent is warm (it was shipped), post-merge it is
// gone and the consolidated segment is present. This proves the embed path's
// auto-reclaim wiring end-to-end through Manager construction.
func TestManagerMergeReclaim(t *testing.T) {
	const seal = searchengine.DefaultMinSegmentDocs // 1024 → one Add seals one segment
	docs := vecContentDocs(seal)
	ctx := context.Background()

	t.Run("hnsw", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		gt, name := kgtypes.GraphCode, "mergereclaim-hnsw"

		// AddAndShip seals + ships one 1024-doc segment, warming its L2 file.
		require.NoError(t, mgr.AddAndShip(ctx, gt, name, docs))
		dm := mgr.managerFor(gt, name)
		pre := dm.engine.Export()
		require.Len(t, pre, 1, "one sealed segment expected")
		constituentID := pre[0].ID
		_, warm := dm.cache.Get(constituentID)
		require.True(t, warm, "shipped constituent must be warm in L2 before the merge")

		// Delete > 33% of the segment → dead-ratio merge fires.
		for i := range seal/3 + 1 {
			dm.engine.Delete(docs[i].ID)
		}
		require.GreaterOrEqual(t, waitMergeCount(dm.engine.MergeCount, 1), uint64(1),
			"dead-ratio merge must fire on the embed HNSW engine")

		mergedID := pollReclaimed(t, dm, constituentID)
		_, mergedWarm := dm.cache.Get(mergedID)
		require.True(t, mergedWarm, "consolidated segment must be warm in L2 after merge")
	})

	t.Run("bm25", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		gt, name := kgtypes.GraphCode, "mergereclaim-bm25"

		require.NoError(t, mgr.AddAndShipFields(ctx, gt, name, docs))
		dm := mgr.bm25ManagerFor(gt, name)
		pre := dm.engine.Export()
		require.Len(t, pre, 1, "one sealed BM25 segment expected")
		constituentID := pre[0].ID
		_, warm := dm.cache.Get(constituentID)
		require.True(t, warm, "shipped BM25 constituent must be warm in L2 before the merge")

		for i := range seal/3 + 1 {
			dm.engine.Delete(docs[i].ID)
		}
		require.GreaterOrEqual(t, waitMergeCount(dm.engine.MergeCount, 1), uint64(1),
			"dead-ratio merge must fire on the embed BM25 engine")

		mergedID := pollReclaimed(t, dm, constituentID)
		_, mergedWarm := dm.cache.Get(mergedID)
		require.True(t, mergedWarm, "consolidated BM25 segment must be warm in L2 after merge")
	})
}

// pollReclaimed waits until the superseded constituent is gone from the live L2
// cache (the OnMerge hook fires just after mergeCnt.Add) and returns the merged
// segment's id from the post-merge Export.
func pollReclaimed[Q, S any](t *testing.T, dm *distManager[Q, S], constituentID searchengine.SegmentID) searchengine.SegmentID {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := dm.cache.Get(constituentID); !ok {
			break
		}
		time.Sleep(3 * time.Millisecond)
	}
	if _, ok := dm.cache.Get(constituentID); ok {
		t.Fatalf("superseded constituent %s was NOT reclaimed from the live L2 cache", constituentID)
	}
	post := dm.engine.Export()
	require.Len(t, post, 1, "exactly one consolidated segment after the merge")
	require.NotEqual(t, constituentID, post[0].ID, "merge must produce a new content hash")
	return post[0].ID
}

// TestDetManagerNoAutoReclaim proves the deterministic rebuild engine (detManagers
// via AddDeterministic) carries NO OnMerge hook: a dead-ratio merge on that engine
// does NOT remove the superseded constituent from its L2 cache (the det path
// reclaims via the ROLE-A FlushDeterministic→InvalidateLocal channel instead).
func TestDetManagerNoAutoReclaim(t *testing.T) {
	const seal = searchengine.DefaultMinSegmentDocs
	docs := vecContentDocs(seal)
	ctx := context.Background()

	_, gc := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	gt, name := kgtypes.GraphCode, "noreclaim-det"

	// Seal one deterministic segment (Add-only; no ship on this path).
	require.NoError(t, mgr.AddDeterministic(ctx, gt, name, docs))
	dm := mgr.hnswManagerFor(mgr.detManagers, hnsw.NewDeterministic(), gt, name, false)
	pre := dm.engine.Export()
	require.Len(t, pre, 1, "one sealed deterministic segment expected")
	constituentID := pre[0].ID

	// Warm the constituent into the det L2 cache by hand (the det path warms it via
	// FlushDeterministic in production; here we seed it directly so a stray
	// auto-reclaim would be observable as a Remove).
	dm.cache.Put(constituentID, pre[0].Bytes)
	_, warm := dm.cache.Get(constituentID)
	require.True(t, warm, "constituent seeded into det L2 cache")

	for i := range seal/3 + 1 {
		dm.engine.Delete(docs[i].ID)
	}
	require.GreaterOrEqual(t, waitMergeCount(dm.engine.MergeCount, 1), uint64(1),
		"dead-ratio merge must still fire on the det engine (only the reclaim hook differs)")

	// Give any (erroneous) OnMerge ample time to run, then assert the constituent
	// is STILL present — the det engine must not auto-reclaim.
	time.Sleep(150 * time.Millisecond)
	_, stillWarm := dm.cache.Get(constituentID)
	require.True(t, stillWarm,
		"det engine must NOT auto-reclaim: constituent %s must survive in L2 (nil OnMerge)", constituentID)
}
