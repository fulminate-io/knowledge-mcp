// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// TestCorpusCacheRecord_SurvivesSegmentGraphDrop is the BEHAVIORAL half of the
// record's placement decision. The thought package's own path test asserts the
// record is not under the segment cache root as a string prefix; this asserts the
// consequence, which is the thing that would actually destroy a user's warm start.
//
// DropGraphCache does one ReadDir of the segment cache root and treats EVERY
// directory it finds there as a storage FORMAT — deliberately, so a format added
// later is swept without editing that file — then removes
// <cacheRoot>/<format>/<graphType>/<name>. A record parked at
// <root>/segments/thought/knowledge/default/corpus.bin would therefore be read as a
// format directory named "thought" and deleted by any graph drop on knowledge/default.
//
// It lives in bootstrap because the thought package must not import segmentdist;
// bootstrap is the composition root and already imports both.
//
// The nil first argument to NewManager is the established source-less-fixture
// precedent and is safe here: DropGraphCache never reaches a source — it touches the
// filesystem under the cache dir and nothing else.
func TestCorpusCacheRecord_SurvivesSegmentGraphDrop(t *testing.T) {
	root := t.TempDir()
	segRoot := segmentCacheDirFor(root)
	mgr := segmentdist.NewManager(segRoot, 0)
	t.Cleanup(mgr.Close)
	c := &client{segmentMgr: mgr}

	// A real segment artifact for the SAME graph the record describes, in the layout
	// the Manager writes.
	seg := filepath.Join(segRoot, "hnsw", string(kgtypes.GraphKnowledge), "default", "a.seg")
	require.NoError(t, os.MkdirAll(filepath.Dir(seg), 0o750))
	require.NoError(t, os.WriteFile(seg, []byte("segment"), 0o600))

	// The record at the PRODUCTION path. Planting at a recomputed literal instead
	// would keep passing if the layout ever moved — the fixture would plant where
	// nothing lives and then prove nothing deleted it.
	recPath := clientthought.CorpusCachePathFor(root)
	require.NoError(t, os.MkdirAll(filepath.Dir(recPath), 0o750))
	require.NoError(t, os.WriteFile(recPath, []byte("record"), 0o600))

	dropper := c.SegmentCacheDropper()
	require.NotNil(t, dropper)
	report, err := dropper.DropGraphCache(kgtypes.GraphKnowledge, "default")
	require.NoError(t, err)

	// KNOWN-POSITIVE CONTROL, FIRST. Without it the survival assertion below proves
	// nothing: a dropper that no-oped entirely — wrong graph name, unwired adapter,
	// empty cache dir — would leave the record alone for the wrong reason and the
	// test would pass while asserting nothing.
	require.Equal(t, 1, report.Files, "the dropper acted: it removed the planted segment artifact")
	assert.NoFileExists(t, seg, "the segment artifact for this graph is gone")

	assert.FileExists(t, recPath,
		"the corpus record survives a whole-graph teardown of the very graph it describes")

	// STRUCTURAL LEG FOR THE PRUNER, stated as such. PruneCache is not run here: it
	// needs a complete live set and a list cross-check, so a faithful behavioral run
	// would need a segment source this fixture has no reason to build. What the
	// pruner's REACH is, is plain — orphan removal is a direct remove of
	// <dir>/<id>.seg where dir is a per-format L2 cache root under the manager's
	// cache dir — so a path outside that root, without a .seg extension, is
	// unreachable by it on two independent counts.
	require.False(t, strings.HasPrefix(recPath, segRoot+string(os.PathSeparator)),
		"the record is outside the segment cache root, so the orphan pruner cannot reach it either")
}
