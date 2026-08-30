// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// client_segment_band_flip_test.go pins what the HNSW arm asserts AWAY FROM QUIESCENCE
// once the ratio band is retired.

// TestHNSWArm_RetiredBandSizes_NoLongerDriveARebuild covers all three sizes the retired
// band branched on, IN ONE TEST.
//
// THEY ARE TOGETHER DELIBERATELY. Two of the three assert a NON-event — the arm no
// longer firing — and a file containing only those would pass identically if the whole
// predicate were deleted and replaced with `return false`. The third case is the
// known-positive that makes the other two mean something: it drives the same predicate
// to TRUE in the same run, so "does not fire" is discrimination rather than silence.
func TestHNSWArm_RetiredBandSizes_NoLongerDriveARebuild(t *testing.T) {
	// The literals are named rather than inlined so a change to either constant moves
	// this test's inputs with it rather than leaving it asserting against a threshold
	// that no longer exists.
	aboveFloor := tools.SegmentCoverageFloor * 10 // 640: comfortably above the retired floor
	subFloor := tools.SegmentCoverageFloor - 1    // 63: the retired floor's other side

	t.Run("a partially-covered graph no longer drives a rebuild", func(t *testing.T) {
		// 40% COVERAGE, chosen because it is on the FIRING side of the retired 50%
		// threshold. A 60% graph would be a weaker case: the band did not fire there
		// either, so asserting the new predicate is silent on it would demonstrate no
		// change at all. This shape is one the band DID call degenerate, and the exact
		// verdict now decides it at the quiescence edge instead.
		resident := aboveFloor * 40 / 100
		assert.True(t, degenerateAgainstEmbedded(resident, aboveFloor),
			"CONTROL: the retired band DID fire on this shape — without this the next "+
				"assertion would not be a change in behaviour")
		assert.False(t, hnswPoolLost(resident, aboveFloor),
			"a partially-covered graph must no longer drive a rebuild through the HNSW arm "+
				"away from quiescence; the exact verdict decides it at the quiescence edge")
	})

	t.Run("a sub-floor graph holding some of its corpus likewise does not", func(t *testing.T) {
		assert.False(t, hnswPoolLost(1, subFloor),
			"a small graph holding part of its corpus must not drive a rebuild")
		assert.False(t, hnswPoolLost(subFloor, subFloor),
			"nor must a small graph holding all of it")
	})

	t.Run("a graph holding zero with vectors present still does", func(t *testing.T) {
		// THE KNOWN-POSITIVE. A lost pool is exact under any denominator, which is why
		// this branch survived the band's retirement — and why deleting the whole
		// predicate cannot green this file.
		assert.True(t, hnswPoolLost(0, aboveFloor),
			"an empty pool against a non-empty embedded corpus is a LOST CACHE and must "+
				"still drive a rebuild, at any magnitude")
		assert.True(t, hnswPoolLost(0, subFloor),
			"including below the retired floor — the floor existed only to guard the "+
				"ratio, so it must not suppress the empty-pool trigger")
	})

	t.Run("an empty graph is not a lost pool", func(t *testing.T) {
		assert.False(t, hnswPoolLost(0, 0),
			"zero resident against zero vectors is a graph with nothing to hold, not a "+
				"lost cache — rebuilding it would churn every empty graph on every tick")
	})
}
