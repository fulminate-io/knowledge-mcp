// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// plantCacheFile writes a file of exactly size bytes at path, creating parents.
// Planting artifacts directly (rather than shipping a real corpus through
// seedShipped) is deliberate: DropGraphCache asserts only over graphCacheDirFor's
// layout, so binding the test to the ship path would couple a filesystem-cleanup
// test to the engine machinery it has nothing to do with.
func plantCacheFile(t *testing.T, path string, size int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, make([]byte, size), 0o600))
}

// TestDropGraphCache_RemovesEveryFormatDir proves one call sweeps the graph out of
// EVERY format directory found at the cache root — hnsw, bm25 and the reserved
// rebuildstate record — and reports the true file count and byte total.
//
// The decoy is the point of the test: dropRepo@branch sits beside dropRepo under
// the same format/graphType prefix, and it MUST survive. Without it an
// implementation that walked <format>/<graphType>/ matching by prefix would pass
// while silently eating every branch overlay and quarantine sibling of the graph
// an operator asked to drop.
func TestDropGraphCache_RemovesEveryFormatDir(t *testing.T) {
	t.Parallel()

	mgr := NewManager(loginStateStub{}, t.TempDir(), 0)

	hnswDir := graphCacheDirFor(mgr.cacheDir, kgtypes.GraphCode, "dropRepo", hnsw.New().Name())
	bm25Dir := graphCacheDirFor(mgr.cacheDir, kgtypes.GraphCode, "dropRepo", "bm25")
	statePath := rebuildStatePathFor(mgr.cacheDir, kgtypes.GraphCode, "dropRepo")
	decoyDir := graphCacheDirFor(mgr.cacheDir, kgtypes.GraphCode, "dropRepo@branch", hnsw.New().Name())

	// Distinct sizes so Files and Bytes cannot both be satisfied by one number.
	plantCacheFile(t, filepath.Join(hnswDir, "aaa.seg"), 100)
	plantCacheFile(t, filepath.Join(hnswDir, "bbb.seg"), 250)
	plantCacheFile(t, filepath.Join(bm25Dir, "ccc.seg"), 30)
	plantCacheFile(t, statePath, 7)
	plantCacheFile(t, filepath.Join(decoyDir, "survivor.seg"), 999)

	report, err := mgr.DropGraphCache(kgtypes.GraphCode, "dropRepo")
	require.NoError(t, err)

	assert.NoDirExists(t, hnswDir, "the hnsw dir for the dropped graph is gone")
	assert.NoDirExists(t, bm25Dir, "the bm25 dir for the dropped graph is gone")
	assert.NoFileExists(t, statePath, "the rebuild-state record for the dropped graph is gone")
	assert.FileExists(t, filepath.Join(decoyDir, "survivor.seg"),
		"a sibling whose name merely PREFIXES the dropped graph must survive")

	assert.Equal(t, 4, report.Files, "every planted file for the graph is counted")
	assert.Equal(t, int64(100+250+30+7), report.Bytes, "the byte total is the summed sizes")
	assert.ElementsMatch(t, []string{hnsw.New().Name(), "bm25", rebuildStateFormat}, report.Formats,
		"all three format dirs are reported")
}

// TestDropGraphCache_NeverLoadedGraphIsCleanNoOp is the ticket's never-loaded
// acceptance: dropping a graph that was never cached locally is a clean zero, not
// an error, and it touches nothing else in the cache.
func TestDropGraphCache_NeverLoadedGraphIsCleanNoOp(t *testing.T) {
	t.Parallel()

	mgr := NewManager(loginStateStub{}, t.TempDir(), 0)

	otherDir := graphCacheDirFor(mgr.cacheDir, kgtypes.GraphCode, "keepRepo", hnsw.New().Name())
	plantCacheFile(t, filepath.Join(otherDir, "keep.seg"), 42)

	report, err := mgr.DropGraphCache(kgtypes.GraphCode, "neverLoaded")
	require.NoError(t, err, "a graph with no local cache is not an error")

	assert.Equal(t, 0, report.Files)
	assert.Equal(t, int64(0), report.Bytes)
	assert.Empty(t, report.Formats)
	assert.FileExists(t, filepath.Join(otherDir, "keep.seg"), "another graph's cache is untouched")
}
