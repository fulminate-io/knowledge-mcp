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
	t.Parallel()

	ctx := context.Background()
	base := t.TempDir()
	_, gc := newSegmentHarness(t)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, base, 0, withSegmentSource(gc)))

	// Half a threshold keeps each graph inside a SINGLE partition through its tick
	// (the tick counts the incoming window alongside the resident set), so each of
	// the four engines holds exactly one segment for the isolation comparison.
	const seal = searchengine.DefaultMinSegmentDocs / 2
	gt := kgtypes.GraphCode
	hnswFmt, bm25Fmt := hnsw.New().Name(), bm25.New().Name()

	// Seal + ship one segment into each of the four (graph,format) engines. One tick
	// per graph drains BOTH of that graph's formats.
	docsA := vecContentDocs(seal)
	docsB := vecContentDocsSeed(seal, 100000)
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, "graphA", docsA))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, "graphA", docsA))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, "graphA"))
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, "graphB", docsB))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, "graphB", docsB))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, "graphB"))

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
	aResident := aHnsw.engine.Export()
	require.Len(t, aResident, 1, "A/hnsw holds one sealed segment before the merge")

	// Merge A's HNSW engine ONLY. The engines this Manager builds have the automatic
	// triggers disarmed, so the occasion is applied directly; what it reclaims, and
	// where, is exactly what this test measures.
	aDead := seal/3 + 1
	for i := range aDead {
		aHnsw.engine.Delete(docsA[i].ID)
	}
	applyMerge(t, aHnsw, []searchengine.SegmentID{aResident[0].ID}, consolidatedHNSWBlob(t, docsA[aDead:]))

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
