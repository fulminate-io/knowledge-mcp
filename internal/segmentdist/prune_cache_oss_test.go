// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestPruneCache_OSSLocalLiveSet proves the decouple-#4 local orphan reclaim: over an
// OSS-local-source Manager, PruneCache derives the live set from L2 (no server
// round-trip), removes an orphan .seg not in the resident/L2 live set, and leaves the
// live segments untouched. There is no source and no network leg on this path at
// all, so "zero server RPC" is structural rather than counted.
func TestPruneCache_OSSLocalLiveSet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := t.TempDir()
	mgr := closeOnCleanup(t, NewManager(base, 0))

	// Warm a live HNSW segment into L2 (zero server RPC — local ship).
	require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, "pruneRepo", hnswVecDocs(40)))
	require.NoError(t, mgr.Flush(ctx, kgtypes.GraphCode, "pruneRepo"))

	hnswDir := graphCacheDirFor(base, kgtypes.GraphCode, "pruneRepo", hnsw.New().Name())
	liveBefore := liveSegPaths(t, hnswDir)
	require.NotEmpty(t, liveBefore, "the flush wrote a live .seg to the L2 cache dir")

	// Plant a junk orphan AFTER construction (not in the memoized cache index).
	orphanPath, _ := plantOrphan(t, hnswDir, "orphan-oss", 321)

	rep, err := mgr.PruneCache(ctx, []PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: "pruneRepo"}}, true)
	require.NoError(t, err)
	require.Equal(t, 1, rep.Removed, "exactly the planted orphan is removed")

	_, statErr := os.Stat(orphanPath)
	require.True(t, os.IsNotExist(statErr), "the orphan .seg was unlinked")
	for _, p := range liveBefore {
		_, statErr := os.Stat(p)
		require.NoError(t, statErr, "a live segment survives the prune")
	}
	// The live segment is still on disk under its own name.
	require.FileExists(t, filepath.Join(hnswDir, filepath.Base(liveBefore[0])))
}
