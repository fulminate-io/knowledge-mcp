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

// TestEmptyLayerNeverRetiresAGoodLayer is the corpus-wipe defect signature,
// restated against the mechanism that survives.
//
// IT REPLACES TestPublishSubsetGate/empty_live_set_skips_publish. That subtest drove
// an empty live set into the manifest publish and asserted the publish was skipped,
// because publishing an empty manifest drove a server refcount-GC that reaped the
// whole corpus. The publish is deleted, so the assertion has no mechanism left
// to make — but the PROPERTY it defended is not gone, it MOVED: the destructive act
// is now engine.ReplaceLayer, which retires the entire prior layer in one CAS.
//
// THE PROPERTY: an empty prospective layer must NEVER retire a populated one.
//
// BOTH DIRECTIONS RUN OVER THE SAME GATE, because "returns false" is satisfiable by
// a gate that refuses everything. The populated leg is the known-positive.
func TestEmptyLayerNeverRetiresAGoodLayer(t *testing.T) {
	t.Parallel()

	dm := newDistManagerForWipeGuard(t)

	t.Run("an empty prospective layer is REFUSED", func(t *testing.T) {
		ok, reason := dm.prospectiveLayerOK(nil)

		require.False(t, ok, "an empty prospective layer must not be allowed to replace a populated one")
		require.Equal(t, "empty live set", reason,
			"the refusal must NAME the empty live set — an operator cannot act on a bare false")
	})

	t.Run("a populated prospective layer is ALLOWED", func(t *testing.T) {
		built := []searchengine.SegmentBlob{
			{ID: "seg-a", Bytes: []byte(`[{"id":"d1","content":"alpha"}]`)},
			{ID: "seg-b", Bytes: []byte(`[{"id":"d2","content":"beta"}]`)},
		}

		ok, reason := dm.prospectiveLayerOK(built)

		require.True(t, ok, "a populated layer must be allowed to swap — this is the known-positive")
		require.Empty(t, reason)
	})
}

// TestSupersededBlobsAreEvictedFromL2 pins the dropped-ids computation that
// FinalizeRebuild feeds into InvalidateLocal.
//
// IT ASSERTS ON DISK, NOT ON A CALL COUNT, and that distinction is the whole test. A
// call-count assertion is satisfied by a wiring that calls InvalidateLocal with an
// EMPTY set — which is precisely what a silently-lost superseded computation would
// produce. Nothing else in the suite looks at the .seg files under a graph's cache
// root after a rebuild, so the loss would otherwise be invisible.
func TestSupersededBlobsAreEvictedFromL2(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cacheDir := t.TempDir()
	gt, name := kgtypes.GraphCode, "supersededRepo"
	mgr := closeOnCleanup(t, NewManager(cacheDir, 0))

	// Run A lays down a corpus, so run B has something to supersede.
	stageRebuildRun(t, ctx, mgr, gt, name, vecContentDocs(1024))
	resA, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	require.True(t, resA.Swapped, "run A must land, or run B supersedes nothing")

	beforeIDs := onDiskHNSWIDs(t, cacheDir, gt, name)
	require.NotEmpty(t, beforeIDs, "run A must have written .seg files, or this test is about nothing")

	// Run B replaces the layer with a DIFFERENT corpus, so the content hashes differ
	// and run A's blobs are genuinely superseded rather than re-minted identically.
	stageRebuildRun(t, ctx, mgr, gt, name, vecContentDocsSeed(1024, 7))
	resB, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	require.True(t, resB.Swapped, "run B must land")
	require.NotEmpty(t, resB.HNSWSuperseded,
		"the swap retired run A's layer, so the superseded set must be non-empty — an empty set here "+
			"is exactly the silent loss this test exists to catch")

	mgr.InvalidateLocal(gt, name, resB.HNSWSuperseded)

	afterIDs := onDiskHNSWIDs(t, cacheDir, gt, name)
	after := make(map[searchengine.SegmentID]struct{}, len(afterIDs))
	for _, id := range afterIDs {
		after[id] = struct{}{}
	}
	for _, id := range resB.HNSWSuperseded {
		_, still := after[id]
		require.False(t, still,
			"superseded blob %s is still on disk — the dropped-ids computation did not reach InvalidateLocal", id)
	}
}

// newDistManagerForWipeGuard builds a bare distManager over a real disk cache. The
// gate under test reads only its argument, so no corpus is needed.
func newDistManagerForWipeGuard(t *testing.T) *distManager[mockQuery, mockStats] {
	t.Helper()
	cache := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
	return newDistManager[mockQuery, mockStats](
		newMockEngine(t), cache, graphSelector(kgtypes.GraphCode, "wipeguard"), "")
}

// onDiskHNSWIDs reads the .seg ids physically present under a graph's HNSW cache
// root. It goes to the filesystem deliberately: an in-memory index would answer from
// the same bookkeeping the code under test maintains.
func onDiskHNSWIDs(t *testing.T, cacheDir string, gt kgtypes.GraphType, name string) []searchengine.SegmentID { //nolint:unparam // gt is the intentional named API: it is half the cache-dir key, and these fixtures happen to exercise code graphs
	t.Helper()
	dir := graphCacheDirFor(cacheDir, gt, name, hnsw.New().Name())
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var ids []searchengine.SegmentID
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".seg" {
			continue
		}
		ids = append(ids, e.Name()[:len(e.Name())-len(".seg")])
	}
	return ids
}
