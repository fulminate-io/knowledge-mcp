// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// cache_quarantine_cure_test.go — the quarantine reading RETURNS TO ZERO when the
// rebuild it prescribes lands.
//
// WHY THAT IS THE REQUIREMENT AND NOT A NICETY. The reading is CURRENT
// unreachability: manage(status) renders it beside the sentence "the documents they
// held are unreachable until this graph's segments are rebuilt". A number that could
// never fall would keep asserting that sentence after the rebuild had already made
// those documents searchable, and a loss reading that never clears is noise rather
// than a loss reading. The cure is the landed swap — the publish that replaces the
// whole set — because that is the moment the documents come back.
//
// THE FILE IS STILL KEPT AS EVIDENCE. It moves into a `cured-<stamp>` subdirectory of
// the quarantine directory rather than being deleted, and the re-seed at construction
// reads only the top level, so a cured segment is never counted again.

// TestQuarantineClearsWhenARebuildLands drives the real finalize: a reset rebuild for
// this graph and format, whose layer swap lands, clears that format's withdrawal
// record and leaves the evidence behind.
func TestQuarantineClearsWhenARebuildLands(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "quarantine-cure"

	// A landed layer to quarantine out of.
	stageRebuildRun(t, ctx, mgr, gt, name, vecContentDocs(8))
	res, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	require.True(t, res.Swapped, "the seeding rebuild must land, or there is nothing to withdraw from")

	ids := l2IDsFor(mgr.cacheDir, name, bm25FormatName)
	require.NotEmpty(t, ids, "the seeding rebuild must have written a bm25 layer")

	arm := mgr.bm25ManagerFor(gt, name)
	require.NoError(t, arm.cache.(*diskSegmentCache).Quarantine(ids[0], errors.New("injected: unreadable bytes")))
	require.Equal(t, map[string]int{bm25FormatName: 1}, onlyBM25(mgr.QuarantinedSegmentCounts(gt, name)),
		"precondition: the withdrawal is visible to manage(status)")

	// THE CURE: a reset rebuild whose swap lands for this graph and format.
	stageRebuildRun(t, ctx, mgr, gt, name, vecContentDocs(12))
	res, err = mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	require.True(t, res.Swapped, "the curing rebuild must land: an unlanded swap restores nothing and must clear nothing")

	require.Equal(t, map[string]int{bm25FormatName: 0}, onlyBM25(mgr.QuarantinedSegmentCounts(gt, name)),
		"a landed rebuild makes those documents searchable again, so the loss reading must return to zero")

	// THE EVIDENCE SURVIVES, under a cured- subdirectory rather than in the top level.
	root := graphCacheDirFor(mgr.cacheDir, gt, name, bm25FormatName)
	cured := curedFiles(t, root)
	require.Len(t, cured, 1, "the withdrawn file is kept as evidence under a cured- directory")
	require.Contains(t, cured[0], ids[0])
	require.NoFileExists(t, filepath.Join(root, quarantineDirName, ids[0]+".seg"),
		"and it is no longer in the top level, which is what the re-seed reads")

	// A RESTART STILL READS ZERO: the re-seed skips directories, so a cured file is
	// never counted again.
	require.Zero(t, newDiskSegmentCache(root, 0, adviceRandom).quarantinedCount(),
		"a restart after the cure must not resurrect the count from the evidence")
}

// onlyBM25 projects the per-format reading down to the text arm, so a row about the
// field corpus is not written in terms of whatever the vector arm happens to report.
func onlyBM25(counts map[string]int) map[string]int {
	return map[string]int{bm25FormatName: counts[bm25FormatName]}
}

// curedFiles lists the .seg files preserved under any cured- directory.
func curedFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	matches, err := filepath.Glob(filepath.Join(root, quarantineDirName, "cured-*", "*.seg"))
	require.NoError(t, err)
	for _, m := range matches {
		out = append(out, filepath.Base(m))
	}
	return out
}
