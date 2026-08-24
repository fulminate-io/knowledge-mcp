// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"context"
	"runtime"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// mappedCorpus seals a mock segment from docs and returns its content id and
// encoded bytes — the shape the cache stores and the mapping serves.
func mappedCorpus(t *testing.T, docs ...searchengine.Document) (searchengine.SegmentID, []byte) {
	t.Helper()
	e := newMockEngine(t)
	defer e.Close()
	require.NoError(t, e.Add(docs))
	blobs := e.Export()
	require.Len(t, blobs, 1)
	return blobs[0].ID, blobs[0].Bytes
}

// searchIDs sorts the ids a search returned, so comparisons are order-stable.
func searchIDs(hits []searchengine.Hit) []searchengine.ExternalID {
	ids := make([]searchengine.ExternalID, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	sort.Strings(ids)
	return ids
}

// TestMappedImportSearchParity proves the results a caller sees are identical
// whether a blob arrives as a heap copy or as a mapping. Everything else in this
// phase is lifetime plumbing; this is the check that the plumbing did not change
// what the index answers.
func TestMappedImportSearchParity(t *testing.T) {
	docs := []searchengine.Document{
		doc("a", "alpha shared"), doc("b", "beta shared"), doc("c", "gamma shared"),
	}
	id, blobBytes := mappedCorpus(t, docs...)

	dir := t.TempDir()
	cache := newDiskSegmentCache(dir, 0, adviceRandom)
	cache.Put(id, blobBytes)

	// Heap arm: the bytes exactly as Get returns them.
	heapBytes, ok := cache.Get(id)
	require.True(t, ok)
	heapEngine := newMockEngine(t)
	defer heapEngine.Close()
	require.NoError(t, heapEngine.Import(
		[]searchengine.SegmentBlob{{ID: id, Bytes: heapBytes}}, nil))

	// Mapped arm: the same bytes as a mapping.
	data, release, ok, err := cache.GetMapped(id)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, bytes.Equal(heapBytes, data), "the mapping must serve the same bytes the heap read does")
	mappedEngine := newMockEngine(t)
	defer mappedEngine.Close()
	require.NoError(t, mappedEngine.Import(
		[]searchengine.SegmentBlob{{ID: id, Bytes: data, Release: release}}, nil))

	matched := 0
	for _, term := range []string{"alpha", "beta", "gamma", "shared"} {
		want := searchIDs(heapEngine.Search(mockQuery{term: term}, 10))
		got := searchIDs(mappedEngine.Search(mockQuery{term: term}, 10))
		require.Equal(t, want, got, "term %q", term)
		matched += len(want)
	}
	require.Positive(t, matched, "no term matched anything — comparing empty result sets proves nothing")
	runtime.KeepAlive(mappedEngine)
}

// TestMappedSegmentReleasedWhenEntryDropped asserts BOTH halves of the lifetime
// rule: a mapping is released once its entry is unreachable, and NOT before.
//
// The not-before half is the one that matters for correctness. Search reads
// entries off an atomic snapshot with no lock held, so releasing at the moment an
// entry is swapped out would unmap memory a live reader can still be walking.
func TestMappedSegmentReleasedWhenEntryDropped(t *testing.T) {
	id, blobBytes := mappedCorpus(t, doc("a", "alpha"), doc("b", "beta"))
	cache := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
	cache.Put(id, blobBytes)

	var released atomic.Int64
	data, release, ok, err := cache.GetMapped(id)
	require.NoError(t, err)
	require.True(t, ok)

	engine := newMockEngine(t)
	require.NoError(t, engine.Import([]searchengine.SegmentBlob{{
		ID: id, Bytes: data,
		Release: func() { released.Add(1); release() },
	}}, nil))

	// NOT BEFORE: while the entry is resident and searchable, no release.
	require.NotEmpty(t, searchIDs(engine.Search(mockQuery{term: "alpha"}, 10)))
	for range 20 {
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
	require.Zero(t, released.Load(), "the mapping was released while its entry was still resident")

	// AND THEN: drop every reference to the engine and its entry.
	engine.Unload([]searchengine.SegmentID{id})
	engine.Close()
	engine = nil
	_ = engine
	require.True(t, waitForRelease(&released),
		"the mapping was never released after its entry became unreachable — "+
			"the zero above proves nothing if the cleanup never fires at all")
}

// TestMergedMappingOwnedAndReleased covers the path entryFromDecoded alone never
// reaches. Merges publish through newEntry, not Import, so a cleanup hung only on
// the import path would leave every merge's blob heap-resident for the life of
// the process — and because a merge REPLACES its constituents, a progressively
// merged corpus would climb back to whole-corpus residency.
func TestMergedMappingOwnedAndReleased(t *testing.T) {
	dir := t.TempDir()
	dm, ic := newReclaimManager(t, dir)

	id, blobBytes := mappedCorpus(t, doc("m1", "merged one"), doc("m2", "merged two"))
	// The merged entry is PUBLISHED before the reclaim hook runs, exactly as the
	// real path publishes it via newEntry.
	require.NoError(t, dm.engine.Import([]searchengine.SegmentBlob{{ID: id, Bytes: blobBytes}}, nil))
	require.NotEmpty(t, searchIDs(dm.engine.Search(mockQuery{term: "merged"}, 10)))

	dm.reclaimMerged(searchengine.MergeResult{
		Merged: searchengine.SegmentBlob{ID: id, Bytes: blobBytes},
	})

	// The reclaim mapped the merged blob and republished the payload over it.
	require.GreaterOrEqual(t, ic.firstIndex("getmapped", id), 0,
		"reclaimMerged never asked for a mapping, so the merged payload is still heap-backed")
	require.GreaterOrEqual(t, ic.firstIndex("put", id), 0)

	// It is still the same searchable segment afterwards — a remap that lost
	// documents would satisfy the ops assertions above.
	require.Equal(t,
		[]searchengine.ExternalID{"m1", "m2"},
		searchIDs(dm.engine.Search(mockQuery{term: "merged"}, 10)))
}

// TestReloadFailsLoudWhenMappingFails is the unapproved-fallback guard.
//
// When the cache holds an id but cannot MAP it, reload must fail with that
// condition rather than treat it as a miss and read the bytes onto the heap. A
// silent heap lane would hide a broken platform mapping arm on exactly the
// platform CI never runs, while quietly reinstating the memory profile this seam
// exists to remove.
func TestReloadFailsLoudWhenMappingFails(t *testing.T) {
	dir := t.TempDir()
	dm, ic := newReclaimManager(t, dir)
	id, blobBytes := mappedCorpus(t, doc("a", "alpha"))
	dm.cache.Put(id, blobBytes)

	// KNOWN-POSITIVE: with mapping healthy the very same reload succeeds, so the
	// failure below is the injected condition and not a broken fixture.
	require.NoError(t, dm.reload(context.Background(), []searchengine.SegmentID{id}, false))
	require.NotEmpty(t, searchIDs(dm.engine.Search(mockQuery{term: "alpha"}, 10)))

	ic.failMapping = true
	err := dm.reload(context.Background(), []searchengine.SegmentID{id}, false)
	require.Error(t, err, "reload must FAIL when a cached segment cannot be mapped")
	require.ErrorIs(t, err, errInjectedMappingFailure,
		"reload must surface the mapping failure rather than substitute a heap read")

	// It must not have quietly fallen back to the byte-copy read.
	for _, op := range ic.opLog() {
		require.NotEqual(t, "get", op.kind,
			"reload used the heap-copy Get — that is the silent fallback this gate exists to forbid")
	}
}

// waitForRelease drives GC until released reports a non-zero count, or gives up.
// A cleanup runs on a background goroutine some cycles after its object becomes
// unreachable, so a single runtime.GC() is not enough to observe it.
func waitForRelease(released *atomic.Int64) bool {
	for range 50 {
		runtime.GC()
		if released.Load() > 0 {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return released.Load() > 0
}
