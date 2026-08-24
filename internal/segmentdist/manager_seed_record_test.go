// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// manager_seed_record_test.go — the seed carries base's rebuild record, and
// captures it BEFORE the partitions so the branch's watermark can never outrun
// the blobs it holds.

// TestSeedBranchBucket_CopiesRebuildRecord covers both fields and the ordering.
func TestSeedBranchBucket_CopiesRebuildRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	format := hnsw.New().Name()
	const repo = "record-repo"
	const branch = "record-repo@feature"

	t.Run("watermark_and_tombstones_copied", func(t *testing.T) {
		cacheDir := t.TempDir()
		plantBlob(t, cacheDir, repo, format, "seg-a", []byte("alpha"))
		published := &publishedOnlySource{metas: []searchengine.SegmentMeta{{ID: "seg-a", Format: format}}}
		mgr := closeOnCleanup(t, NewManager(loginStateStub{}, cacheDir, 0, withSegmentSource(published)))

		// Base has a real record: a non-zero watermark AND tombstoned ids.
		const baseWatermark = int64(1_700_000_000_000_000_000)
		baseTombs := []searchengine.ExternalID{"gone-one", "gone-two"}
		require.NoError(t, mgr.SaveRebuildState(kgtypes.GraphCode, repo, baseWatermark, baseTombs))

		_, err := mgr.SeedBranchBucketFromBase(ctx, kgtypes.GraphCode, repo, branch, format,
			branchEngineCache(cacheDir, branch, format, 0))
		require.NoError(t, err)

		gotWatermark, gotTombs, err := mgr.LoadRebuildState(kgtypes.GraphCode, branch)
		require.NoError(t, err)
		require.Equal(t, baseWatermark, gotWatermark,
			"the branch inherits base's watermark, or its first rebuild is a full-corpus scan and the seed buys nothing")
		// THE TOMBSTONE HALF IS NOT DECORATION: the branch's seeded partitions ARE
		// base's partitions and carry the same stale ids, so a copy that took only
		// the watermark leaves the branch serving deleted ids with nothing
		// scheduled to remove them.
		require.ElementsMatch(t, baseTombs, gotTombs,
			"the branch inherits base's tombstoned ids, which its seeded partitions still carry")
	})

	t.Run("record_captured_before_partitions", func(t *testing.T) {
		cacheDir := t.TempDir()
		plantBlob(t, cacheDir, repo, format, "seg-a", []byte("alpha"))

		const captured = int64(1_700_000_000_000_000_000)
		const laterPublish = int64(1_900_000_000_000_000_000)

		// The source's List runs DURING the seed, between the record capture and the
		// partition copy — so advancing base's record from inside it reproduces
		// exactly the window the capture-first ordering exists to close.
		var mgr *Manager
		published := &publishedOnlySource{metas: []searchengine.SegmentMeta{{ID: "seg-a", Format: format}}}
		published.onList = func() {
			require.NoError(t, mgr.SaveRebuildState(kgtypes.GraphCode, repo, laterPublish, nil))
		}
		mgr = closeOnCleanup(t, NewManager(loginStateStub{}, cacheDir, 0, withSegmentSource(published)))
		require.NoError(t, mgr.SaveRebuildState(kgtypes.GraphCode, repo, captured, nil))

		_, err := mgr.SeedBranchBucketFromBase(ctx, kgtypes.GraphCode, repo, branch, format,
			branchEngineCache(cacheDir, branch, format, 0))
		require.NoError(t, err)

		// FIXTURE CONTROL: base really did advance mid-seed, so the assertion below
		// discriminates between two different values rather than comparing a number
		// to itself.
		baseNow, _, err := mgr.LoadRebuildState(kgtypes.GraphCode, repo)
		require.NoError(t, err)
		require.Equal(t, laterPublish, baseNow, "fixture control: base advanced during the seed")

		gotWatermark, _, err := mgr.LoadRebuildState(kgtypes.GraphCode, branch)
		require.NoError(t, err)
		require.Equal(t, captured, gotWatermark,
			"the branch takes the CAPTURED watermark, not the later one — a watermark past a document the "+
				"branch never received means that document is never scanned again and is permanently missing")
	})
}
