// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestDrainDerivesCountFromTrueCorpus closes the count-source trap: the drain must
// derive its partition count from the TRUE corpus, not from a resident reading
// that cross-segment duplication inflates.
//
// THE FIXTURE IS ONE WHERE THE TWO READINGS DISAGREE, which is the whole point.
// Two layers over the same 8192 ids report a SUMMED resident of 16384 while the
// true distinct corpus is 8192, and the two derive different partition counts —
// BucketCountFor(16434) is 32, BucketCountFor(8192) is 8. A drain reading the
// summed number manufactures a crossing the corpus never made, and that crossing
// is what puts segments spanning several partitions in front of the swap.
//
// WHY THE PER-SEGMENT DocCount FIX DOES NOT COVER THIS. That change (owned by the
// swap step) makes each segment count its own members distinctly. The duplication
// here is ACROSS segments, so summing per-segment counts still double-counts every
// id, and it correctly reports 16384 before the drain. Cross-segment duplicates
// are removed by the REPARTITION, never by the counter — a gate demanding
// otherwise would push ResidentDocCount toward an O(corpus) walk on the search
// path.
//
// EXPECTATIONS ARE COMPUTED FROM BucketCountFor over fixture CONSTANTS, never
// hard-coded and never read back from the run being judged.
func TestDrainDerivesCountFromTrueCorpus(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "repro"
	mgr, dm, base := twoLayerFixture(t)

	// The two candidate sources, and the proof they genuinely disagree — without
	// this the test could pass on a fixture where the trap cannot appear.
	summed := dm.engine.ResidentDocCount()
	distinct := dm.engine.DistinctResidentDocCount()
	require.Equal(t, 2*dupLayerCorpus, summed, "the layered fixture must report a SUMMED resident of twice the corpus")
	require.Equal(t, dupLayerCorpus, distinct, "the route map indexes each id once, so the distinct reading is the true corpus")

	inflatedCount := searchengine.BucketCountFor(summed + dupLayerWindow)
	trueCount := searchengine.BucketCountFor(dupLayerCorpus)
	require.NotEqual(t, trueCount, inflatedCount,
		"the fixture must be one where the two sources DERIVE DIFFERENT COUNTS, or this gate proves nothing")

	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, base[:dupLayerWindow]))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	segments := len(dm.engine.Export())
	t.Logf("summed=%d distinct=%d inflatedCount=%d trueCount=%d segmentsAfterDrain=%d",
		summed, distinct, inflatedCount, trueCount, segments)

	// The drain repartitions into one segment per partition, so the resulting layout
	// reports which count it derived.
	require.LessOrEqual(t, segments, trueCount,
		"the drain must partition by the TRUE corpus; a layout wider than that means it derived from the inflated resident reading")
	require.NotEqual(t, inflatedCount, segments,
		"the layout matches the count derived from the inflated reading — the trap is still open")

	// And the repartition is still lossless, so this cannot be satisfied by simply
	// emitting fewer partitions than the corpus needs.
	require.Len(t, presentMemberIDs(dm, base), dupLayerCorpus,
		"deriving a smaller count must not drop members")
}
