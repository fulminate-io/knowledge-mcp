// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// residencyPool ships a corpus of one-doc segments from a throwaway engine and
// returns a SECOND manager that has LOADED it — the "resident pool" every residency
// test starts from. It returns the loaded manager, its engine and the shared source
// view (for the fetch counters and the Fetch fault hooks).
//
// One segment per content string: newMockEngine sets MinSegmentDocs=1, so each Add
// seals its own segment and the pool has a discrete, countable resident set.
func residencyPool(t *testing.T, name string, contents ...string) (
	*distManager[mockQuery, mockStats],
	*searchengine.SegmentedIndex[mockQuery, mockStats],
	*fakeSegmentSource,
) {
	t.Helper()
	_, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: name}
	ctx := context.Background()

	shipEng := newMockEngine(t)
	for i, c := range contents {
		require.NoError(t, shipEng.Add([]searchengine.Document{doc(fmt.Sprintf("d%d", i+1), c)}))
	}
	shipMgr, _ := buildManager(shipEng, gc, target, t.TempDir())
	_, err := shipMgr.ship(ctx, shipMgr.locallyShipped)
	require.NoError(t, err)

	loadEng := newMockEngine(t)
	loadMgr, view := buildManager(loadEng, gc, target, t.TempDir())
	require.NoError(t, loadMgr.load(ctx))
	return loadMgr, loadEng, view
}

// residentIDsOf snapshots a pool's resident segment ids in sorted order.
func residentIDsOf(m *distManager[mockQuery, mockStats]) []searchengine.SegmentID {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	ids := make([]searchengine.SegmentID, 0, len(m.resident))
	for id := range m.resident {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// evictedIDsOf snapshots a pool's strict-reload id set.
func evictedIDsOf(m *distManager[mockQuery, mockStats]) []searchengine.SegmentID {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	return append([]searchengine.SegmentID(nil), m.evictedIDs...)
}

// TestEvictResidentThenSearchReturnsIdenticalHits is ticket constraint 6's
// before/after search-result equality on an evict/reload cycle: an evicted pool must
// be INDISTINGUISHABLE to a searcher from a never-loaded one.
func TestEvictResidentThenSearchReturnsIdenticalHits(t *testing.T) {
	t.Parallel()

	mgr, eng, _ := residencyPool(t, "evictidentical", "alpha", "alpha", "alpha")
	before := eng.Search(mockQuery{term: "alpha"}, 10)
	require.Len(t, before, 3, "the loaded pool serves every doc")

	freed, ok := mgr.evictResident()
	require.True(t, ok, "a pool whose every id is in L2 must evict")
	require.Positive(t, freed, "eviction must report the blob bytes it released")
	require.True(t, mgr.isEvicted())
	require.Empty(t, eng.Search(mockQuery{term: "alpha"}, 10),
		"the evicted engine holds nothing until it is re-materialized")

	require.NoError(t, mgr.load(context.Background()))
	after := eng.Search(mockQuery{term: "alpha"}, 10)
	require.Equal(t, before, after, "the re-materialized pool must return the SAME hits in the SAME order")
	require.False(t, mgr.isEvicted(), "a materialized pool is no longer latched evicted")
}

// TestEvictDeclinesWhenAResidentIDIsAbsentFromL2 covers the re-materializability
// gate in BOTH directions. The known-positive control is not decoration: without it
// an evictResident that declined unconditionally would satisfy the decline half.
func TestEvictDeclinesWhenAResidentIDIsAbsentFromL2(t *testing.T) {
	t.Parallel()

	t.Run("missing_l2_blob_declines", func(t *testing.T) {
		mgr, eng, _ := residencyPool(t, "evictdecline", "alpha", "alpha", "alpha")
		ids := residentIDsOf(mgr)
		require.Len(t, ids, 3)
		mgr.cache.Remove(ids[0])

		freed, ok := mgr.evictResident()
		require.False(t, ok, "a pool that cannot be restored from L2 must not be evicted at all")
		require.Zero(t, freed)
		require.False(t, mgr.isEvicted(), "a declined eviction latches nothing")
		require.Len(t, eng.Search(mockQuery{term: "alpha"}, 10), 3,
			"the engine must still hold every segment after a declined eviction")
	})

	t.Run("intact_l2_evicts", func(t *testing.T) {
		mgr, eng, _ := residencyPool(t, "evictcontrol", "alpha", "alpha", "alpha")
		freed, ok := mgr.evictResident()
		require.True(t, ok, "with L2 intact the same gate must ACT — otherwise the decline above proves nothing")
		require.Positive(t, freed)
		require.True(t, mgr.isEvicted())
		require.Empty(t, eng.Search(mockQuery{term: "alpha"}, 10))
	})
}

// TestStrictReloadErrorsRatherThanServingAShortList pins the two DIRECTIONS of one
// property: the re-materialization of an evicted pool is STRICT over the exact set
// eviction unloaded.
//
//   - missing_blob_errors — a blob that genuinely cannot be restored makes the
//     re-materialization FAIL LOUDLY rather than serving fewer hits. Under the
//     tolerant reload this passes silently with a short list, which is the miss the
//     strictness exists to prevent.
//   - merge_reclaim_rewrites_the_evicted_set — a merge completing AFTER the eviction
//     supersedes ids the strict set still names, and reclaimMerged rewrites the set
//     so the reload succeeds IN FULL off the merged blob.
//
// The pair is the point: missing_blob_errors is what stops the rewrite from being
// implemented as "swallow every missing id".
func TestStrictReloadErrorsRatherThanServingAShortList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("missing_blob_errors", func(t *testing.T) {
		mgr, eng, view := residencyPool(t, "strictmissing", "alpha", "alpha", "alpha")
		_, ok := mgr.evictResident()
		require.True(t, ok)

		ids := evictedIDsOf(mgr)
		require.Len(t, ids, 3, "the strict set names exactly what was unloaded")
		// The blob is gone from L2 AND unavailable from the source: it genuinely
		// cannot be restored.
		mgr.cache.Remove(ids[0])
		view.rejectFetch = func([]searchengine.SegmentID) error {
			return fmt.Errorf("segment %s is unavailable", ids[0])
		}

		err := mgr.load(ctx)
		require.Error(t, err, "a re-materialization that cannot complete must ERROR, never serve a short list")
		require.Empty(t, eng.Search(mockQuery{term: "alpha"}, 10),
			"a failed re-materialization must publish NO partial results")
		require.True(t, mgr.isEvicted(), "a failed re-materialization leaves the latch set")
	})

	t.Run("merge_reclaim_rewrites_the_evicted_set", func(t *testing.T) {
		mgr, eng, view := residencyPool(t, "strictmerge", "alpha", "alpha", "alpha")
		_, ok := mgr.evictResident()
		require.True(t, ok)

		superseded := evictedIDsOf(mgr)
		require.Len(t, superseded, 3)

		// A merge that was already in flight when the pool was evicted now completes,
		// consolidating all three constituents into one durable blob.
		seg, err := mockFormat{}.Build([]searchengine.Document{
			doc("d1", "alpha"), doc("d2", "alpha"), doc("d3", "alpha"),
		})
		require.NoError(t, err)
		mergedBytes, err := seg.Encode()
		require.NoError(t, err)
		const mergedID = "merged-after-eviction-0001"

		// The superseded constituents are genuinely unrecoverable once the merge has
		// reclaimed them — which is precisely why a strict reload still naming them
		// would hard-error on data that is perfectly intact.
		gone := make(map[searchengine.SegmentID]struct{}, len(superseded))
		for _, id := range superseded {
			gone[id] = struct{}{}
		}
		view.rejectFetch = func(req []searchengine.SegmentID) error {
			for _, id := range req {
				if _, dead := gone[id]; dead {
					return fmt.Errorf("segment %s was reclaimed by the merge", id)
				}
			}
			return nil
		}

		mgr.reclaimMerged(searchengine.MergeResult{
			Removed: superseded,
			Merged:  searchengine.SegmentBlob{ID: mergedID, Bytes: mergedBytes},
		})
		require.Equal(t, []searchengine.SegmentID{mergedID}, evictedIDsOf(mgr),
			"the rewrite drops the superseded ids and adds the merged one")
		require.True(t, mgr.isEvicted(), "the rewrite does NOT clear the latch — that is markMaterialized's job")

		fetchesBefore := view.fetchCalls.Load()
		require.NoError(t, mgr.load(ctx), "the rewritten strict set restores the pool in full")
		require.Len(t, eng.Search(mockQuery{term: "alpha"}, 10), 3,
			"every doc is back, served from the merged blob")
		require.Equal(t, fetchesBefore, view.fetchCalls.Load(),
			"the merged blob was Put to L2, so the reload pays ZERO network")
		require.False(t, mgr.isEvicted())
	})
}

// TestSearchRematerializesAnEvictedPool drives the REAL consumer entry point —
// Manager.Search, over both formats of a real graph — across an evict/reload cycle.
//
// Three assertions, and the third is the named catcher: (a) the hits are identical,
// (b) neither pool is latched evicted afterwards, and (c) the consumer-search touch
// stamp ADVANCED. (c) is what goes red if the touch stamp lands in the doc comment
// but not in searchPoolArms' code.
func TestSearchRematerializesAnEvictedPool(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphKnowledge, "searchRematerializes"

	_, gc := newSegmentHarness(t)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))

	docs := bothFormatDocs(8, "remat-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	before, err := mgr.Search(ctx, gt, name, "common", docs[0].Vector, 5)
	require.NoError(t, err)
	require.NotEmpty(t, before, "PRECONDITION: the corpus must be searchable before it is evicted")

	dm, bm := mgr.managerFor(gt, name), mgr.bm25ManagerFor(gt, name)
	touchBefore := dm.lastSearchTouch()
	require.Positive(t, touchBefore, "a completed search stamps the consumer touch")

	_, hnswOK := dm.evictResident()
	_, bm25OK := bm.evictResident()
	require.True(t, hnswOK, "the HNSW pool must be evictable")
	require.True(t, bm25OK, "the BM25 pool must be evictable")
	require.True(t, mgr.PoolEvicted(gt, name))

	after, err := mgr.Search(ctx, gt, name, "common", docs[0].Vector, 5)
	require.NoError(t, err)
	require.Equal(t, before, after,
		"an evicted pool must be indistinguishable to a searcher from a never-evicted one")
	require.False(t, mgr.PoolEvicted(gt, name), "the search re-materialized both pools")
	require.Greater(t, dm.lastSearchTouch(), touchBefore, "the second search advanced the touch stamp")
}

// TestEvictionFreesHeap is ticket constraint 4's measurement: it proves eviction
// RELEASES the decoded segments rather than shuffling a reference, and it RECORDS
// the reload cost paid to get them back.
//
// THE HONEST LIMITATION, stated rather than papered over: a MemStats delta at test
// scale demonstrates the release; it does NOT predict the production figure, because
// production pools are two to three orders of magnitude larger and the daemon's
// allocator behaviour under live load differs. The production number is the
// real-corpus acceptance step's job, and that step MEASURES it rather than
// extrapolating this one.
//
// The reload duration is LOGGED, never asserted against a threshold: a wall-clock
// gate in a package that already runs for over a minute is a flake generator, and
// this number's job is to be recorded.
//
// It does NOT call t.Parallel: a heap measurement sharing the process with
// concurrently-running tests measures them too.
func TestEvictionFreesHeap(t *testing.T) {
	ctx := context.Background()
	gt, name := kgtypes.GraphKnowledge, "evictionFreesHeap"

	_, gc := newSegmentHarness(t)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))

	docs := heapFixtureDocs(600)
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
	_, err := mgr.Search(ctx, gt, name, "alpha", docs[0].Vector, 5)
	require.NoError(t, err)

	hdm, bdm := mgr.managerFor(gt, name), mgr.bm25ManagerFor(gt, name)
	residentHeapBytes := hdm.residentBytes() + bdm.residentBytes()
	require.Positive(t, residentHeapBytes, "PRECONDITION: the pools must hold modeled resident heap to free")

	// KNOWN-NEGATIVE CONTROL for the delta below, and it is what makes that delta a
	// measurement rather than a coincidence: the SAME GC/read pair around NO eviction
	// at all. The fixture build allocates heavily, so an ambient collection could
	// otherwise supply the whole "release" and the real assertion would pass without
	// eviction contributing anything.
	//
	// THE CONTROL SETTLES THE HEAP FIRST, for the same reason the eviction reading
	// below takes two GCs: a single collection leaves the previous phase's garbage
	// partially uncollected. Reading idleBefore after one GC measures the tail of
	// the fixture build rather than ambient movement, and on a warm binary — a
	// repeated -count run, where earlier iterations left the heap busy — that tail
	// exceeds the modeled resident heap and fails the control while the product
	// under test is behaving correctly.
	var idleBefore, idleAfter runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&idleBefore)
	runtime.GC()
	runtime.ReadMemStats(&idleAfter)
	idle := int64(idleBefore.HeapAlloc) - int64(idleAfter.HeapAlloc)
	t.Logf("idle control: heap moved %d bytes across a GC pair with no eviction", idle)
	require.Less(t, idle, residentHeapBytes,
		"ambient collection alone must not account for the modeled resident heap — otherwise the delta below proves nothing")

	// TWO FORCED GCs, one before each reading, because Go's heap accounting only
	// reflects a release AFTER collection — a single GC is the classic false-red.
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	hnswFreed, hnswOK := hdm.evictResident()
	bm25Freed, bm25OK := bdm.evictResident()
	// NAMED CATCHER: without these an implementation that always DECLINES produces a
	// stable-heap reading that a careless delta assertion reads as a pass.
	require.True(t, hnswOK, "the HNSW pool must actually evict")
	require.True(t, bm25OK, "the BM25 pool must actually evict")
	freed := hnswFreed + bm25Freed
	require.Equal(t, residentHeapBytes, freed, "the reported free must be the modeled resident heap")

	runtime.GC()
	runtime.ReadMemStats(&after)
	released := int64(before.HeapAlloc) - int64(after.HeapAlloc)

	// THE FLOOR IS THE MEMBERS-DERIVED COMPONENT, AND THE PAYLOAD TERM IS
	// DELIBERATELY EXCLUDED FROM IT.
	//
	// The retired form asserted released >= freed, justified by "the decoded form
	// is no smaller than the blob". That justification does not survive the
	// mapped formats: a mapped payload's bytes are page cache, so evicting it
	// returns almost no Go heap, and once BOTH formats are mapped an assertion
	// anchored on the full modeled total would fail on entirely correct work.
	//
	// What eviction genuinely returns is what the ENGINE built per segment and
	// dropped: the membership index and the liveness bitset. That is the honest
	// floor, and it holds whether or not a payload is mapped.
	//
	// The expectation is DERIVED FROM THE FIXTURE, not read back from the code
	// under test — computing it with the production model would be an identity
	// check that passes just as happily with the model wrong. Per pool: 600 ids
	// of the form "heap-00000" (10 bytes) at the engine's modeled per-member
	// overhead of 48 bytes, plus a liveness bitset of ceil(600/64)=10 words at 8
	// bytes. Two pools index the same fixture.
	const (
		fixtureDocs        = 600
		fixtureIDBytes     = 10 // len("heap-00000")
		memberOverhead     = 48 // mirrors searchengine's memberEntryOverheadBytes
		bitsetWordsPerPool = 10 // ceil(600/64)
		poolsInFixture     = 2
	)
	membersFloor := int64(poolsInFixture) *
		(fixtureDocs*(fixtureIDBytes+memberOverhead) + bitsetWordsPerPool*8)

	t.Logf("eviction freed %d modeled heap bytes; heap released %d bytes; members floor %d",
		freed, released, membersFloor)
	require.GreaterOrEqual(t, released, membersFloor,
		"evicting both pools must release at least the membership index and liveness bitsets (%d bytes); the payload term is excluded because a mapped payload's bytes are page cache, not heap",
		membersFloor)

	start := time.Now()
	require.NoError(t, hdm.load(ctx))
	require.NoError(t, bdm.load(ctx))
	t.Logf("reload from L2 took %s", time.Since(start))

	// Keep the manager reachable across the readings so its retained heap is counted
	// rather than collected as dead.
	runtime.KeepAlive(mgr)
}

// heapFixtureDocs builds n documents carrying a 32-byte vector and a body large
// enough that the decoded pools dominate test-harness noise in a heap reading.
func heapFixtureDocs(n int) []searchengine.Document {
	body := strings.Repeat("alpha beta gamma delta epsilon zeta eta theta ", 48)
	docs := make([]searchengine.Document, n)
	for i := range docs {
		vec := make([]byte, 32)
		for b := range vec {
			vec[b] = byte((i*31 + b*7) % 251)
		}
		docs[i] = searchengine.Document{
			ID:     fmt.Sprintf("heap-%05d", i),
			Vector: vec,
			Fields: map[string]string{
				searchengine.FieldSymbolName: fmt.Sprintf("heapterm%d", i),
				searchengine.FieldSummary:    fmt.Sprintf("%s doc%d", body, i),
			},
		}
	}
	return docs
}

// TestForceLoadClearsTheEvictedLatch is markMaterialized's forceCompleteLiveSet
// catcher, and no other test can see it: forceCompleteLiveSet re-imports the pool
// WITHOUT going through load(), so a census over load() call sites misses it. Omit
// the clear there and an operator's prune-cache leaves a fully-resident pool latched
// evicted forever.
func TestForceLoadClearsTheEvictedLatch(t *testing.T) {
	t.Parallel()

	mgr, _, _ := residencyPool(t, "forceloadlatch", "alpha", "alpha", "alpha")
	_, ok := mgr.evictResident()
	require.True(t, ok)
	require.True(t, mgr.isEvicted())
	require.Zero(t, mgr.residentBytes(), "an evicted pool accounts for no resident bytes")

	ids, err := mgr.forceCompleteLiveSet(context.Background())
	require.NoError(t, err)
	require.Len(t, ids, 3, "the force-load rebuilds the COMPLETE live set")

	require.False(t, mgr.isEvicted(), "a force-loaded pool is fully resident and must not stay latched evicted")
	require.Positive(t, mgr.residentBytes(), "its bytes are back in the residency accounting")
}
