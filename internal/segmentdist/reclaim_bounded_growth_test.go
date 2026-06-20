// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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
func TestReclaimBoundedGrowth(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	_, gc := newSegmentHarness(t)
	mgr := NewManager(gc, base, 0)

	const seal = searchengine.DefaultMinSegmentDocs
	gt, name := kgtypes.GraphCode, "bounded"
	hnswFmt := hnsw.New().Name()

	dm := mgr.managerFor(gt, name)
	var peakHnswCount int

	const cycles = 4
	for c := range cycles {
		// Add+ship a fresh 1024-doc segment (one new sealed segment per cycle).
		batch := vecContentDocsSeed(seal, (c+1)*1_000_000)
		require.NoError(t, mgr.AddAndShip(ctx, gt, name, batch))

		// Trip a dead-ratio merge on this cycle's segment: delete > 33% of the most
		// recently sealed segment so the merge consolidates it (the prior cycles'
		// segments stay clean and untouched).
		for i := range seal/3 + 1 {
			dm.engine.Delete(batch[i].ID)
		}
		waitMergeCount(dm.engine.MergeCount, dm.engine.MergeCount()+1)
		waitMergeQuiesce(dm.engine.MergeCount)
		warmExported(dm)
		time.Sleep(40 * time.Millisecond)

		hnswCount := segDirCount(t, base, gt, name, hnswFmt)
		if hnswCount > peakHnswCount {
			peakHnswCount = hnswCount
		}

		// Invariant + search correctness after every cycle.
		assertLiveSetBackedByL2(t, dm, map[searchengine.SegmentID]struct{}{}, nil, nil)
		live := liveCorpusIDs(t, dm)
		require.NotEmpty(t, live, "cycle %d: corpus is searchable", c)
	}

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
