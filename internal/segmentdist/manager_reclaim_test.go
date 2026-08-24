// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// newReclaimManager wires a mock-engine distManager around an instrumentedCache so
// reclaimMerged can be driven directly with synthetic MergeResults and its cache
// op order inspected. The cache wraps a real *diskSegmentCache rooted at dir.
func newReclaimManager(t *testing.T, dir string) (*distManager[mockQuery, mockStats], *instrumentedCache) {
	t.Helper()
	_, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "reclaim"}
	gc.target = target
	ic := newInstrumentedCache(newDiskSegmentCache(dir, 0, adviceRandom))
	dm := newDistManager[mockQuery, mockStats](newMockEngine(t), gc, ic, target, "")
	return dm, ic
}

// TestReclaimMergedPutBeforeRemove pins the crash-safe ordering: the merged blob's
// Put op index must precede EVERY Remove op index, and the merged id ends up on
// disk while every removed id is gone.
func TestReclaimMergedPutBeforeRemove(t *testing.T) {
	t.Parallel()

	dm, ic := newReclaimManager(t, t.TempDir())

	// Seed the two constituents on disk (the pre-merge state).
	dm.cache.Put("constituent-1", []byte("c1-bytes"))
	dm.cache.Put("constituent-2", []byte("c2-bytes"))

	dm.reclaimMerged(searchengine.MergeResult{
		Removed: []searchengine.SegmentID{"constituent-1", "constituent-2"},
		Merged:  searchengine.SegmentBlob{ID: "merged-xyz", Bytes: []byte("merged-bytes")},
	})

	putIdx := ic.firstIndex("put", "merged-xyz")
	require.GreaterOrEqual(t, putIdx, 0, "the merged blob must be Put")
	// EVERY remove op (regardless of id) must come AFTER the merged Put.
	for i, op := range ic.opLog() {
		if op.kind == "remove" {
			require.Greater(t, i, putIdx,
				"Remove op at %d (%s) must come AFTER the merged Put at %d", i, op.id, putIdx)
		}
	}

	// Disk end-state: merged present, both constituents gone.
	_, ok := dm.cache.Get("merged-xyz")
	require.True(t, ok, "merged blob must be present on disk after reclaim")
	for _, id := range []string{"constituent-1", "constituent-2"} {
		_, ok := dm.cache.Get(id)
		require.False(t, ok, "superseded constituent %s must be removed from disk", id)
	}
}

// TestReclaimMergedEmptyIDSkipsRemoves pins the guard: an empty Merged.ID (doMerge
// failed to encode the consolidated blob) skips the ENTIRE reclaim — no Put, no
// Remove — so the constituents survive (a Remove without a durable Put would be a
// fresh false-prune).
func TestReclaimMergedEmptyIDSkipsRemoves(t *testing.T) {
	t.Parallel()

	dm, ic := newReclaimManager(t, t.TempDir())
	dm.cache.Put("constituent-1", []byte("c1-bytes"))

	dm.reclaimMerged(searchengine.MergeResult{
		Removed: []searchengine.SegmentID{"constituent-1"},
		Merged:  searchengine.SegmentBlob{ID: "", Bytes: nil},
	})

	// No reclaim op should have fired beyond the seeding Put.
	require.Equal(t, -1, ic.firstIndex("remove", ""), "empty Merged.ID must fire ZERO Remove ops")
	_, ok := dm.cache.Get("constituent-1")
	require.True(t, ok, "constituent must survive when Merged.ID is empty (no Remove without a Put)")
}

// TestReclaimMergedNoRemovedIsPutOnly pins that a MergeResult with a merged blob
// but an empty Removed set Puts the merged blob and removes nothing.
func TestReclaimMergedNoRemovedIsPutOnly(t *testing.T) {
	t.Parallel()

	dm, ic := newReclaimManager(t, t.TempDir())

	dm.reclaimMerged(searchengine.MergeResult{
		Removed: nil,
		Merged:  searchengine.SegmentBlob{ID: "merged-only", Bytes: []byte("m")},
	})

	require.GreaterOrEqual(t, ic.firstIndex("put", "merged-only"), 0, "merged blob must be Put")
	require.Equal(t, -1, ic.firstIndex("remove", ""), "no Removed ids → ZERO Remove ops")
	_, ok := dm.cache.Get("merged-only")
	require.True(t, ok, "merged blob present on disk")
}

// TestReclaimMergedLeavesBookkeepingUntouched pins that reclaimMerged is purely L2
// disk reclamation: dm.shippedIDs / dm.locallyShipped / dm.resident are NOT
// mutated (the ship/reconcile paths own that bookkeeping, against Export()).
func TestReclaimMergedLeavesBookkeepingUntouched(t *testing.T) {
	t.Parallel()

	dm, _ := newReclaimManager(t, t.TempDir())

	// Seed non-empty bookkeeping so a stray mutation would be observable.
	dm.shipMu.Lock()
	dm.shippedIDs["constituent-1"] = struct{}{}
	dm.shippedIDs["other-shipped"] = struct{}{}
	dm.locallyShipped["constituent-1"] = struct{}{}
	dm.shipMu.Unlock()
	dm.resMu.Lock()
	dm.resident["constituent-1"] = residentSeg{mappedBytes: 8, format: "mock", generation: 3}
	dm.resMu.Unlock()

	dm.cache.Put("constituent-1", []byte("c1-bytes"))
	dm.reclaimMerged(searchengine.MergeResult{
		Removed: []searchengine.SegmentID{"constituent-1"},
		Merged:  searchengine.SegmentBlob{ID: "merged-xyz", Bytes: []byte("m")},
	})

	dm.shipMu.Lock()
	_, stillShipped := dm.shippedIDs["constituent-1"]
	_, stillLocal := dm.locallyShipped["constituent-1"]
	_, otherKept := dm.shippedIDs["other-shipped"]
	shippedLen := len(dm.shippedIDs)
	dm.shipMu.Unlock()
	dm.resMu.Lock()
	_, stillResident := dm.resident["constituent-1"]
	dm.resMu.Unlock()

	require.True(t, stillShipped, "reclaimMerged must NOT delete from shippedIDs")
	require.True(t, stillLocal, "reclaimMerged must NOT delete from locallyShipped")
	require.True(t, otherKept, "unrelated shippedIDs entries untouched")
	require.Equal(t, 2, shippedLen, "shippedIDs size unchanged")
	require.True(t, stillResident, "reclaimMerged must NOT delete from resident")

	// But the disk cache WAS reclaimed.
	_, ok := dm.cache.Get("constituent-1")
	require.False(t, ok, "the superseded constituent's L2 file is reclaimed")
	_, ok = dm.cache.Get("merged-xyz")
	require.True(t, ok, "the merged blob is present on disk")
}
