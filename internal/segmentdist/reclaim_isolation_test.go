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
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// segFiles returns the set of .seg filenames (content-hash ids) under one graph's
// per-format L2 cache subdir, the disk-derived view the Manager.NewManager path
// uses to observe reclamation (no instrumented seam exists on that path — T4).
func segFiles(t *testing.T, base string, gt kgtypes.GraphType, name, format string) map[string]struct{} {
	t.Helper()
	dir := graphCacheDirFor(base, gt, name, format)
	out := make(map[string]struct{})
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out // missing dir = empty set
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".seg" {
			out[e.Name()[:len(e.Name())-len(".seg")]] = struct{}{}
		}
	}
	return out
}

// TestReclaimMultiGraphIsolation drives a merge on graph A's HNSW engine through
// the REAL Manager and asserts the reclamation is scoped to A's HNSW L2 subdir:
// A/bm25, B/hnsw, B/bm25 are all untouched. Observed via on-disk .seg state per
// graphCacheDirFor subdir (no seam on the Manager path).
func TestReclaimMultiGraphIsolation(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	_, gc := newSegmentHarness(t)
	mgr := NewManager(gc, base, 0)

	const seal = searchengine.DefaultMinSegmentDocs
	gt := kgtypes.GraphCode
	hnswFmt, bm25Fmt := hnsw.New().Name(), bm25.New().Name()

	// Seal + ship one 1024-doc segment into each of the four (graph,format) engines.
	docsA := vecContentDocs(seal)
	docsB := vecContentDocsSeed(seal, 100000)
	require.NoError(t, mgr.AddAndShip(ctx, gt, "graphA", docsA))
	require.NoError(t, mgr.AddAndShipFields(ctx, gt, "graphA", docsA))
	require.NoError(t, mgr.AddAndShip(ctx, gt, "graphB", docsB))
	require.NoError(t, mgr.AddAndShipFields(ctx, gt, "graphB", docsB))

	// Snapshot the three engines that must stay untouched.
	beforeABm25 := segFiles(t, base, gt, "graphA", bm25Fmt)
	beforeBHnsw := segFiles(t, base, gt, "graphB", hnswFmt)
	beforeBBm25 := segFiles(t, base, gt, "graphB", bm25Fmt)
	require.NotEmpty(t, beforeABm25)
	require.NotEmpty(t, beforeBHnsw)
	require.NotEmpty(t, beforeBBm25)

	aHnsw := mgr.managerFor(gt, "graphA")
	beforeAHnsw := segFiles(t, base, gt, "graphA", hnswFmt)
	require.NotEmpty(t, beforeAHnsw)

	// Trigger a dead-ratio merge on A's HNSW engine ONLY.
	for i := range seal/3 + 1 {
		aHnsw.engine.Delete(docsA[i].ID)
	}
	require.GreaterOrEqual(t, waitMergeCount(aHnsw.engine.MergeCount, 1), uint64(1), "A/hnsw merge must fire")
	waitMergeQuiesce(aHnsw.engine.MergeCount)
	// Let the lock-free post-CAS reclaim settle on disk.
	time.Sleep(80 * time.Millisecond)

	// A/hnsw: the superseded constituent's .seg file is gone; a NEW merged .seg
	// replaced it.
	afterAHnsw := segFiles(t, base, gt, "graphA", hnswFmt)
	require.NotEqual(t, beforeAHnsw, afterAHnsw, "A/hnsw L2 set changed (constituent reclaimed, merged added)")

	// The other three engines' L2 subdirs are byte-for-byte untouched.
	require.Equal(t, beforeABm25, segFiles(t, base, gt, "graphA", bm25Fmt), "A/bm25 L2 untouched")
	require.Equal(t, beforeBHnsw, segFiles(t, base, gt, "graphB", hnswFmt), "B/hnsw L2 untouched")
	require.Equal(t, beforeBBm25, segFiles(t, base, gt, "graphB", bm25Fmt), "B/bm25 L2 untouched")

	// Per-engine invariant holds (disk-derived removed set; clause-2 vacuous on the
	// Manager path — pass empty removed, observe L2 backing only).
	assertLiveSetBackedByL2(t, aHnsw, map[searchengine.SegmentID]struct{}{}, nil, nil)
	assertLiveSetBackedByL2(t, mgr.managerFor(gt, "graphB"), map[searchengine.SegmentID]struct{}{}, nil, nil)
	assertLiveSetBackedByL2(t, mgr.bm25ManagerFor(gt, "graphA"), map[searchengine.SegmentID]struct{}{}, nil, nil)
	assertLiveSetBackedByL2(t, mgr.bm25ManagerFor(gt, "graphB"), map[searchengine.SegmentID]struct{}{}, nil, nil)
}
