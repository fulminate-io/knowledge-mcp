// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// TestClientSegmentCacheDropper_RemovesRealCacheDirs drives the PRODUCTION wiring
// end to end below the MCP boundary: the real segmentCacheDirFor path derivation,
// a real *segmentdist.Manager, the real adapter, and the real accessor nil-guard.
// Every other test in this plan substitutes a fake for one of those, so this is the
// catcher for a seam that compiles and is never actually wired.
func TestClientSegmentCacheDropper_RemovesRealCacheDirs(t *testing.T) {
	root := segmentCacheDirFor(t.TempDir())
	mgr := segmentdist.NewManager(root, 0)
	t.Cleanup(mgr.Close)
	c := &client{segmentMgr: mgr}

	dropper := c.SegmentCacheDropper()
	require.NotNil(t, dropper, "a client holding a segment manager exposes the dropper")

	// Plant per-format artifacts under the REAL derived root, using the same layout
	// the Manager writes: <root>/<format>/<graphType>/<name>.
	plant := func(rel string, size int) string {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, make([]byte, size), 0o600))
		return p
	}
	hnswSeg := plant(filepath.Join("hnsw", "code", "wiredRepo", "a.seg"), 64)
	bm25Seg := plant(filepath.Join("bm25", "code", "wiredRepo", "b.seg"), 32)
	state := plant(filepath.Join("rebuildstate", "code", "wiredRepo", "state.json"), 8)
	survivor := plant(filepath.Join("hnsw", "code", "otherRepo", "c.seg"), 16)

	report, err := dropper.DropGraphCache(kgtypes.GraphCode, "wiredRepo")
	require.NoError(t, err)

	assert.NoFileExists(t, hnswSeg, "the real adapter removed the hnsw artifacts")
	assert.NoFileExists(t, bm25Seg, "the real adapter removed the bm25 artifacts")
	assert.NoFileExists(t, state, "the real adapter removed the rebuild-state record")
	assert.FileExists(t, survivor, "another graph under the same root is untouched")

	// The tools-local report carries the translated totals, not zeroes — a broken
	// adapter that forwarded the call but dropped the report would pass every
	// filesystem assertion above.
	assert.Equal(t, 3, report.Files)
	assert.Equal(t, int64(64+32+8), report.Bytes)
	assert.ElementsMatch(t, []string{"hnsw", "bm25", "rebuildstate"}, report.Formats)
}

// TestClientSegmentCacheDropper_NilWithoutSegmentManager pins the typed-nil catcher:
// the accessor must return a GENUINELY nil interface when the client holds no
// segment manager. Returning a typed-nil adapter would satisfy the handler's
// `!= nil` check and then dereference; require.Nil is what fails on that.
func TestClientSegmentCacheDropper_NilWithoutSegmentManager(t *testing.T) {
	c := &client{}
	require.Nil(t, c.SegmentCacheDropper(), "an unwired client yields a nil SegmentCacheDropper")
}
