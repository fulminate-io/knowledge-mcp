// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestRebuildStateRoundTrip pins the durable per-graph record: what is saved comes
// back, an absent record reads as the full-rebuild default rather than an error,
// and two graphs never read each other's.
func TestRebuildStateRoundTrip(t *testing.T) {
	t.Parallel()

	const horizon = int64(1_700_000_000_123_456_789)

	t.Run("absent record is a zero watermark, not an error", func(t *testing.T) {
		mgr := closeOnCleanup(t, NewManager(loginStateStub{}, t.TempDir(), 0))

		watermark, tombstoned, err := mgr.LoadRebuildState(kgtypes.GraphCode, "neverRebuilt")
		require.NoError(t, err, "a graph that has never been rebuilt is the ordinary case, not a failure")
		require.Zero(t, watermark, "no record means scan the whole corpus")
		require.Empty(t, tombstoned)
	})

	t.Run("saved values survive a reload", func(t *testing.T) {
		dir := t.TempDir()
		mgr := closeOnCleanup(t, NewManager(loginStateStub{}, dir, 0))
		ids := []searchengine.ExternalID{"gone-1", "gone-2"}

		require.NoError(t, mgr.SaveRebuildState(kgtypes.GraphCode, "repo", horizon, ids))

		// A SECOND Manager over the same directory is the daemon restart: the record
		// exists to spare it a full re-emit, so it must read what the first wrote.
		restarted := closeOnCleanup(t, NewManager(loginStateStub{}, dir, 0))
		watermark, tombstoned, err := restarted.LoadRebuildState(kgtypes.GraphCode, "repo")
		require.NoError(t, err)
		require.Equal(t, horizon, watermark)
		require.Equal(t, ids, tombstoned)
	})

	t.Run("records are per graph", func(t *testing.T) {
		dir := t.TempDir()
		mgr := closeOnCleanup(t, NewManager(loginStateStub{}, dir, 0))

		require.NoError(t, mgr.SaveRebuildState(kgtypes.GraphCode, "repoA", horizon, nil))

		watermark, _, err := mgr.LoadRebuildState(kgtypes.GraphCode, "repoB")
		require.NoError(t, err)
		require.Zero(t, watermark, "one graph's watermark must never scope another graph's scan")

		watermark, _, err = mgr.LoadRebuildState(kgtypes.GraphKnowledge, "repoA")
		require.NoError(t, err)
		require.Zero(t, watermark, "the same name under a different graph type is a different corpus")
	})

	t.Run("wiping the cache root resets to a full rebuild", func(t *testing.T) {
		dir := t.TempDir()
		mgr := closeOnCleanup(t, NewManager(loginStateStub{}, dir, 0))
		require.NoError(t, mgr.SaveRebuildState(kgtypes.GraphCode, "repo", horizon, nil))

		// The record lives under the L2 cache root deliberately: it describes those
		// blobs, so losing them must lose it too.
		require.NoError(t, os.RemoveAll(dir))

		watermark, _, err := mgr.LoadRebuildState(kgtypes.GraphCode, "repo")
		require.NoError(t, err)
		require.Zero(t, watermark, "a wiped cache has no blobs left to describe — the next rebuild must be full")
	})
}

// TestFinalizeRebuildReportsCompletedSwap is the proof that the finalize signal
// is the manifest SWAP and not the error.
//
// The skipped case is the one that matters. A publish the coverage gate refuses
// returns a NIL ERROR, so a caller reading the error alone cannot tell it apart
// from a landed publish — and the rebuild driver advances a durable watermark on
// that answer. Here the second process holds only a sliver of the shipped corpus,
// which is exactly the degenerate live set the gate exists to refuse.
func TestFinalizeRebuildReportsCompletedSwap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, gc := newSegmentHarness(t)

	// A full reset rebuild: one complete partition carrying both formats, which is what
	// the driver produces from a full-corpus scan.
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	require.NoError(t, mgr.StageRebuildPartition(ctx, kgtypes.GraphCode, "swapRepo",
		hnswVecDocs(searchCorpusN), bm25FieldDocs(searchCorpusN)))

	res, err := mgr.FinalizeRebuild(ctx, kgtypes.GraphCode, "swapRepo")
	require.NoError(t, err)
	swapped := res.Swapped
	require.True(t, swapped, "a full rebuild publishes both formats — the manifest swap landed")
	require.NotEmpty(t, shippedHNSWIDs(svc))

	// A fresh process holding only a handful of documents against that shipped
	// corpus: the coverage gate refuses the publish and reports it with a nil error.
	// BOTH formats are staged, thinly. Swapped is the AND of the two legs, so staging
	// only the vector share would leave the field leg reporting not-swapped for its own
	// reasons and the assertion below would hold without the coverage gate ever firing.
	partial := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	require.NoError(t, partial.StageRebuildPartition(ctx, kgtypes.GraphCode, "swapRepo",
		hnswVecDocs(4), bm25FieldDocs(4)))

	partialRes, err := partial.FinalizeRebuild(ctx, kgtypes.GraphCode, "swapRepo")
	require.NoError(t, err, "a skipped publish is not an error — that is precisely why the error is not the signal")
	swapped = partialRes.Swapped
	require.False(t, swapped, "no manifest swap landed, so a caller must not treat this as a finalize")
}
