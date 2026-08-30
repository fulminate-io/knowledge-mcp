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

	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "reclaim"}
	ic := newInstrumentedCache(newDiskSegmentCache(dir, 0, adviceRandom))
	dm := newDistManager[mockQuery, mockStats](newMockEngine(t), ic, target, "")
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

// TestReclaimMergedLeavesResidencyUntouched pins that reclaimMerged is purely L2 disk
// reclamation: it reclaims the superseded constituent's FILE and does not touch the
// resident-tracking map.
//
// THE SHIP-BOOKKEEPING HALF OF THIS TEST WAS REMOVED, not lost. It also asserted that
// reclaimMerged left the two ship-bookkeeping maps alone; both are deleted
// with the rail, so there is no bookkeeping left to leave alone and the assertion has
// no referent. The RESIDENCY half survives untouched and is the half that still
// matters: resident drives eviction accounting and the residency budget, so a reclaim
// that silently dropped an entry would make a live segment invisible to both.
func TestReclaimMergedLeavesResidencyUntouched(t *testing.T) {
	t.Parallel()

	dm, _ := newReclaimManager(t, t.TempDir())

	// Seed non-empty residency so a stray mutation would be observable. Without this
	// the "still resident" assertion would pass against an empty map.
	dm.resMu.Lock()
	dm.resident["constituent-1"] = residentSeg{mappedBytes: 8, format: "mock", generation: 3}
	dm.resident["unrelated-seg"] = residentSeg{mappedBytes: 4, format: "mock", generation: 3}
	dm.resMu.Unlock()

	require.NoError(t, dm.cache.Put("constituent-1", []byte("c1-bytes")))
	dm.reclaimMerged(searchengine.MergeResult{
		Removed: []searchengine.SegmentID{"constituent-1"},
		Merged:  searchengine.SegmentBlob{ID: "merged-xyz", Bytes: []byte("m")},
	})

	dm.resMu.Lock()
	_, stillResident := dm.resident["constituent-1"]
	_, unrelatedKept := dm.resident["unrelated-seg"]
	residentLen := len(dm.resident)
	dm.resMu.Unlock()

	require.True(t, stillResident, "reclaimMerged must NOT delete the reclaimed id from resident")
	require.True(t, unrelatedKept, "nor an unrelated entry")
	require.Equal(t, 2, residentLen, "the residency map size is unchanged")

	// KNOWN-POSITIVE: the reclaim DID act on disk, so "resident untouched" is a real
	// restraint rather than the observation that reclaimMerged did nothing at all.
	_, ok := dm.cache.Get("constituent-1")
	require.False(t, ok, "the superseded constituent's L2 file IS reclaimed")
	_, ok = dm.cache.Get("merged-xyz")
	require.True(t, ok, "and the merged blob is present on disk")
}
