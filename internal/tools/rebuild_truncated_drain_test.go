// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestTruncatedDrainNeverReachesTheFinalize is the CORPUS-REPLACEMENT wipe signature,
// pinned on the evidence that actually prevents it.
//
// THE WIPE SHAPE. A rebuild lays out a whole new layer and the swap retires the old
// one entire. A run that saw only a SLIVER of the corpus and was allowed to finalize
// therefore replaces a complete corpus with a fragment — the four-documents-replacing-
// a-thousand shape — and it does so reporting a nil error, because a swap that lands
// IS a success. Nothing downstream can tell that layer from a legitimately small one.
//
// WHY THE GUARD IS NOT A RATIO, and this is the part worth stating in code rather
// than in a review thread. "Far fewer documents than last time" has legitimate
// instances — a mass deletion produces exactly that — so a numeric band cannot
// separate the wipe from the correct rebuild, and a band tuned to catch the wipe
// blocks the deletion. The discriminator is not SIZE, it is whether the run has
// EVIDENCE THAT ITS DRAIN COMPLETED.
//
// THE EVIDENCE EXISTS ALREADY, and this test pins it rather than adding a second
// mechanism beside it. scanRebuildSegmentsAs terminates on exactly one non-error
// condition — an EMPTY page, which is exhaustion — and returns an error on any page
// failure. RebuildSegments returns immediately on that error, BEFORE
// StageRebuildPartition and BEFORE FinalizeRebuild. So an incomplete drain is not
// refused at the swap; it never reaches the swap. Both counters below are asserted
// because they are two different claims: nothing was staged (so no sliver is left
// behind for a LATER finalize to take — takeRebuildWork drains whatever is staged)
// and nothing was finalized (so no layer was replaced).
//
// THE CONTROL IS THE OTHER HALF. A guard that refused every rebuild would satisfy the
// first arm perfectly, so the same fixture with the failure removed must still stage
// and finalize. Without it this test is satisfied by a driver that does nothing.
//
// THE RESIDUAL, stated rather than implied: this is evidence that the drain did not
// ERROR, not proof that the server served every row it holds. A backend that returned
// a short page and then an empty one is indistinguishable from exhaustion at the
// client, and no client-side check can close that — it would need a server-supplied
// count to compare against. The horizon the scan echoes is a watermark, not a
// cardinality, and is not that evidence.
func TestTruncatedDrainNeverReachesTheFinalize(t *testing.T) {
	const corpus = 64

	// One full page of real items, then whatever the arm scripts next.
	firstPage := makeScanPage("trunc-", 0, corpus)

	t.Run("a drain that fails mid-scan stages nothing and finalizes nothing", func(t *testing.T) {
		scanner := &fakeWatermarkScanner{
			pages:         [][]*knowledgev1.PipelineScanItem{firstPage},
			failAfterPage: 1, // page 1 lands; page 2 fails — the truncated drain
		}
		shipper := &fakeRebuildShipper{}

		out, err := RebuildSegments(
			context.Background(), scanner, shipper, kgtypes.GraphCode, "truncated-repo", true)
		require.Error(t, err, "a drain that could not finish must FAIL the rebuild, not publish what it got")
		require.ErrorIs(t, err, errScanTruncated, "and the failure must be the scan's, not a substituted one")
		require.False(t, out.Ran, "a rebuild that never reached the build cannot report that it ran")

		require.Zero(t, shipper.stageCalls.Load(),
			"NOTHING MAY BE STAGED: staged partitions survive the failed run and the NEXT finalize takes "+
				"them, so a sliver staged here becomes a layer later with nothing to attribute it to")
		require.Zero(t, shipper.finalizeCalls.Load(),
			"NOTHING MAY BE FINALIZED: the finalize retires the whole prior layer, so reaching it on a "+
				"sliver of the corpus is the wipe itself")
	})

	t.Run("CONTROL: the same drain, completed, does stage and finalize", func(t *testing.T) {
		scanner := &fakeWatermarkScanner{
			pages: [][]*knowledgev1.PipelineScanItem{firstPage}, // then an empty page = exhausted
		}
		shipper := &fakeRebuildShipper{}

		out, err := RebuildSegments(
			context.Background(), scanner, shipper, kgtypes.GraphCode, "complete-repo", true)
		require.NoError(t, err)
		require.True(t, out.Ran)
		require.Positive(t, shipper.stageCalls.Load(),
			"CONTROL: a completed drain stages its partitions — without this the arm above is satisfied "+
				"by a driver that stages nothing under any input")
		require.EqualValues(t, 1, shipper.finalizeCalls.Load(),
			"CONTROL: and finalizes exactly once")
	})
}
