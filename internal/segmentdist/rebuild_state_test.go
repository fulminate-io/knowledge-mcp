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
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

		watermark, tombstoned, err := mgr.LoadRebuildState(kgtypes.GraphCode, "neverRebuilt")
		require.NoError(t, err, "a graph that has never been rebuilt is the ordinary case, not a failure")
		require.Zero(t, watermark, "no record means scan the whole corpus")
		require.Empty(t, tombstoned)
	})

	t.Run("saved values survive a reload", func(t *testing.T) {
		dir := t.TempDir()
		mgr := closeOnCleanup(t, NewManager(dir, 0))
		ids := []searchengine.ExternalID{"gone-1", "gone-2"}

		require.NoError(t, mgr.SaveRebuildState(kgtypes.GraphCode, "repo", horizon, ids))

		// A SECOND Manager over the same directory is the daemon restart: the record
		// exists to spare it a full re-emit, so it must read what the first wrote.
		restarted := closeOnCleanup(t, NewManager(dir, 0))
		watermark, tombstoned, err := restarted.LoadRebuildState(kgtypes.GraphCode, "repo")
		require.NoError(t, err)
		require.Equal(t, horizon, watermark)
		require.Equal(t, ids, tombstoned)
	})

	t.Run("records are per graph", func(t *testing.T) {
		dir := t.TempDir()
		mgr := closeOnCleanup(t, NewManager(dir, 0))

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
		mgr := closeOnCleanup(t, NewManager(dir, 0))
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
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()

	// A full reset rebuild: one complete partition carrying both formats, which is what
	// the driver produces from a full-corpus scan.
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	require.NoError(t, mgr.StageRebuildPartition(ctx, kgtypes.GraphCode, "swapRepo",
		hnswVecDocs(searchCorpusN), bm25FieldDocs(searchCorpusN)))

	res, err := mgr.FinalizeRebuild(ctx, kgtypes.GraphCode, "swapRepo")
	require.NoError(t, err)
	swapped := res.Swapped
	require.True(t, swapped, "a full rebuild publishes both formats — the manifest swap landed")
	require.NotEmpty(t, l2HNSWIDs(mgr.cacheDir, "swapRepo"),
		"the rebuild must leave its corpus on disk")

	// THE REFUSAL LEG, re-pointed onto the guard that survives. It used to stage a
	// SLIVER of the corpus — four documents against a shipped record of a thousand —
	// and rely on the publish coverage gate to refuse it. That gate read a shipped
	// manifest as its denominator and is deleted with the rail; the surviving guard,
	// prospectiveLayerOK, refuses exactly one condition: a prospective layer with an
	// EMPTY live set, which is the wipe it exists to stop.
	//
	// A finalize with nothing staged is that condition, and it is reached the same way
	// a real one is — through FinalizeRebuild, returning a NIL ERROR — which is the
	// point of the assertion: a skipped swap is a reported state, never a failure, so a
	// caller reading the error learns nothing.
	//
	// THE THIN-BUT-NON-EMPTY CASE IS NO LONGER REFUSED, and that is recorded rather
	// than asserted here: a four-document layer replacing a thousand-document one now
	// swaps. See the finding filed against this changeset — this test does not pretend
	// the narrowed guard is the old one.
	empty := closeOnCleanup(t, NewManager(mgr.cacheDir, 0))
	emptyRes, err := empty.FinalizeRebuild(ctx, kgtypes.GraphCode, "swapRepo")
	require.NoError(t, err, "a skipped swap is not an error — that is precisely why the error is not the signal")
	swapped = emptyRes.Swapped
	require.False(t, swapped, "no layer swap landed, so a caller must not treat this as a finalize")
	require.NotEmpty(t, l2HNSWIDs(mgr.cacheDir, "swapRepo"),
		"and the prior corpus is STILL ON DISK — the refusal is what keeps it there")
}
