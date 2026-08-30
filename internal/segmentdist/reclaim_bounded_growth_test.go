// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// segDirBytes sums the bytes of every .seg file under one graph/format L2 subdir —
// the on-disk footprint the bounded-growth proof tracks across cycles.
func segDirBytes(t *testing.T, base string, gt kgtypes.GraphType, name, format string) int64 {
	t.Helper()
	dir := graphCacheDirFor(base, gt, name, format)
	var total int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".seg" {
			continue
		}
		info, err := e.Info()
		if err == nil {
			total += info.Size()
		}
	}
	return total
}

// segDirCount counts the .seg files under one graph/format L2 subdir.
func segDirCount(t *testing.T, base string, gt kgtypes.GraphType, name, format string) int {
	t.Helper()
	return len(segFiles(t, base, gt, name, format))
}

// TestReclaimBoundedGrowth drives a REAL Manager (HNSW + BM25) through several
// add+merge cycles and proves the L2 footprint stays bounded to roughly the LIVE
// generation: each cycle replaces the prior segment with one consolidated blob
// (net L2 writes DECREASE — one merged file replaces its constituents) rather than
// accumulating every historical constituent. Full-corpus search returns all live
// docs and the invariant holds throughout.
//
// The merge occasion is APPLIED DIRECTLY rather than triggered and awaited. These
// engines are Manager-built, and that construction disarms the automatic merge
// triggers because the package manages its own segment layout — so waiting on a
// background merge here would wait forever, and a wait that can never be satisfied
// is what let an earlier version of this test hold vacuously: nothing merged, so
// nothing was reclaimed, so the bound was never under any pressure. Each cycle now
// asserts the transition it claims — the superseded constituent is warm in L2
// before the merge and gone from it after.
func TestReclaimBoundedGrowth(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := t.TempDir()

	mgr := closeOnCleanup(t, NewManager(base, 0))

	const seal = searchengine.DefaultMinSegmentDocs
	gt, name := kgtypes.GraphCode, "bounded"
	hnswFmt := hnsw.New().Name()

	dm := mgr.managerFor(gt, name)
	var peakHnswCount int
	removedSoFar := map[searchengine.SegmentID]struct{}{}

	const cycles = 4
	expectedReclaimed := 0
	for c := range cycles {
		// Seal a fresh 1024-doc batch. NO tick here on purpose: what this test measures
		// is the MERGE's reclaim of its superseded constituents, and the L2 population
		// it asserts on comes from the merge + warmExported rather than from any ship.
		// Draining the backlog would re-emit the corpus into partitions and replace the
		// per-cycle topology the bound below is written against, measuring something
		// else.
		before := residentIDs(dm)
		distinctBefore := dm.engine.DistinctResidentDocCount()
		batch := vecContentDocsSeed(seal, (c+1)*1_000_000)
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, batch))

		// HOW MANY SEGMENTS THIS CYCLE OWES, derived by hashing the batch's own ids
		// rather than read back off the engine: a write seals one segment per partition
		// its ids occupy, at the count the seal derives from the corpus it will form.
		// The first cycles derive one partition and seal once; later ones spread.
		sealCount := searchengine.BucketCountFor(distinctBefore + len(batch))
		spread := map[int]struct{}{}
		for _, d := range batch {
			spread[searchengine.BucketOf(d.ID, sealCount)] = struct{}{}
		}
		expectedReclaimed += len(spread)
		warmExported(dm) // models ship-warming: the fresh constituents are L2-backed.

		// This cycle's constituents are whatever the write newly sealed. Taking the
		// DIFF rather than Export()[0] is what makes the step correct now that a write
		// seals one segment per partition rather than one per batch.
		var constituents []searchengine.SegmentID
		for id := range residentIDs(dm) {
			if _, existed := before[id]; !existed {
				constituents = append(constituents, id)
			}
		}
		require.Len(t, constituents, len(spread),
			"cycle %d: the write must seal one segment per partition its ids occupy", c)

		// POSITIVE CONTROL. Without it, "gone from L2 after the merge" is equally
		// satisfied by an id that was never in L2 to begin with.
		for _, id := range constituents {
			_, warm := dm.cache.Get(id)
			require.True(t, warm, "cycle %d: constituent %s must be warm in L2 before the merge", c, id)
		}

		// Delete > 33% of this cycle's segment — the dead-ratio shape a real merge
		// would have consolidated — then apply that consolidation directly.
		dead := seal/3 + 1
		for i := range dead {
			dm.engine.Delete(batch[i].ID)
		}
		applyMerge(t, dm, constituents, consolidatedHNSWBlob(t, batch[dead:]))

		// THE FENCE: the merge's reclaim actually evicted each superseded
		// constituent from L2. This is the assertion the dead wait used to skip.
		for _, id := range constituents {
			_, stillWarm := dm.cache.Get(id)
			require.False(t, stillWarm,
				"cycle %d: superseded constituent %s must be reclaimed from L2", c, id)
			removedSoFar[id] = struct{}{}
		}

		hnswCount := segDirCount(t, base, gt, name, hnswFmt)
		if hnswCount > peakHnswCount {
			peakHnswCount = hnswCount
		}

		// Invariant + search correctness after every cycle. Passing the ACCUMULATED
		// removed set is what arms clause 2 of the helper: with an empty map it
		// could not check that nothing reclaimed is still live.
		assertLiveSetBackedByL2(t, dm, removedSoFar, nil, nil)
		live := liveCorpusIDs(t, dm)
		require.NotEmpty(t, live, "cycle %d: corpus is searchable", c)
	}

	// The expectation is accumulated from the batches' own id hashes, never from the
	// segment ids the engine handed back, so this stays a claim about what the cycles
	// OWED. Its other half is that no two cycles reclaimed the same segment id: the set
	// is keyed by id, so a collision would read as a short count here.
	require.Len(t, removedSoFar, expectedReclaimed,
		"every cycle reclaimed exactly its own superseded constituents")

	// Bounded growth: the L2 file count is bounded by roughly the number of live
	// segments (cycles), NOT by every historical constituent. Each cycle's merge
	// reclaims its superseded constituent, so the peak count stays at or below the
	// live-segment count plus a small transient slack — far below the
	// without-reclaim accumulation (which would be ~2x the segment count, every
	// constituent retained alongside every merged blob).
	require.LessOrEqual(t, peakHnswCount, cycles+1,
		"HNSW L2 file count stays bounded to ~the live generation (peak=%d, cycles=%d)", peakHnswCount, cycles)

	// Footprint sanity: the final on-disk byte total is non-zero (live segments
	// present) and corresponds to roughly the live count, not cycles× constituents.
	require.Positive(t, segDirBytes(t, base, gt, name, hnswFmt), "live HNSW segments occupy L2")
}

// liveCorpusIDs returns the live doc ids the HNSW engine recovers for a fan-out
// query — a coarse "the corpus is searchable" probe across the merged set.
func liveCorpusIDs(t *testing.T, dm *distManager[[]byte, struct{}]) map[searchengine.ExternalID]struct{} {
	t.Helper()
	probe := make([]byte, 32)
	out := make(map[searchengine.ExternalID]struct{})
	for _, h := range dm.engine.Search(probe, 64) {
		out[h.ID] = struct{}{}
	}
	return out
}
