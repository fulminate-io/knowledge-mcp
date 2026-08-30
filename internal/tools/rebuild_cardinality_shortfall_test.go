// SPDX-License-Identifier: Apache-2.0

package tools

// rebuild_cardinality_shortfall_test.go proves the rebuild cardinality gate CAN STILL
// GO RED after the cloud segment rail's deletion re-pointed its denominator.
//
// WHY A DEDICATED FILE. The gate used to read a published manifest back from the
// server and compare it against the build count — two numbers from two places. The
// manifest is gone, and the read is now the local engine's resident segment count, so
// the standing risk is that the comparison quietly became an identity: a number
// checked against a restatement of itself passes forever and reports nothing. Every
// subtest here therefore drives a DISAGREEING pair first and an agreeing pair second.
// A gate satisfied only by the agreeing arm is exactly the failure this file exists
// to make impossible.
//
// BOTH GATES ARE COVERED because the two rebuild arms compare different things. The
// corpus-complete arm compares the DERIVATION (how many partitions the scanned corpus
// should occupy) against what the engine holds; the delta arm compares the engine's
// own BEFORE and AFTER across an operation that must not move the count. They fail
// differently and neither one's coverage implies the other's.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestRebuildCardinalityShortfallIsReported drives both cardinality gates in both
// directions.
//
// THE DENOMINATORS ARE NOT SELF-REPORTS. On the corpus-complete arm `built` is
// BucketCountFor over the scanned corpus and the engine reports what it holds; on the
// delta arm the two operands are the same engine read twice, before and after. In
// neither case is a number being compared against something the same call derived
// from it, which is what makes a RED reachable at all.
func TestRebuildCardinalityShortfallIsReported(t *testing.T) {
	ctx := context.Background()

	t.Run("full_rebuild_SHORT_of_its_derivation_is_reported", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		// The corpus derives 2 partitions; the engine ends up holding 1. Content that
		// was built is not in the live set — the reading the gate exists for.
		shipper := &fakeRebuildShipper{manifestConfigured: true, manifestCount: 1}

		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "short-repo", true)
		require.NoError(t, err,
			"the corpus is already published by then — a verification read must never fail a landed rebuild")
		require.True(t, out.Published, "fixture: the swap must land or the read-back never runs")
		require.Equal(t, 2, out.Built, "fixture: the scanned corpus must derive two partitions")

		require.Equal(t, 1, out.ResidentSegmentCount,
			"the outcome must carry what the ENGINE actually holds, not what the run hoped for")
		require.Less(t, out.ResidentSegmentCount, out.Built,
			"THE RED: the gate's own operands must disagree here, or nothing below is measuring a shortfall")

		warns := logs.linesContaining(slog.LevelWarn, "FEWER sealed segments")
		require.NotEmpty(t, warns,
			"a resident set shorter than the derivation must be LOUD — nothing else on this path can check it")
		require.Contains(t, warns[0], "derived=2")
		require.Contains(t, warns[0], "resident_segments=1")

		require.Positive(t, shipper.manifestReads.Load(),
			"the cardinality was never READ BACK from the engine — a local-vs-local comparison "+
				"cannot fail for the reason this gate exists")
		require.Equal(t, []string{hnsw.New().Name()}, shipper.manifestFormats,
			"the read must target the HNSW arm: the deterministic rebuild publishes exactly the "+
				"buckets it built there, while the shared BM25 engine legitimately carries extra sealed tails")
	})

	t.Run("full_rebuild_MATCHING_its_derivation_is_silent", func(t *testing.T) {
		// THE OTHER DIRECTION. Without it, a gate hard-wired to warn on every run
		// would pass the subtest above and report a defect on correct work forever.
		logs := captureRebuildLogs(t)
		shipper := &fakeRebuildShipper{manifestConfigured: true, manifestCount: 2}

		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "exact-repo", true)
		require.NoError(t, err)
		require.Equal(t, 2, out.Built)
		require.Equal(t, out.Built, out.ResidentSegmentCount,
			"the engine holds exactly what the corpus derives")
		require.Empty(t, logs.linesContaining(slog.LevelWarn, "FEWER sealed segments"),
			"an agreeing cardinality must NOT warn")
	})

	t.Run("full_rebuild_HOLDING_MORE_is_not_a_fault", func(t *testing.T) {
		// A ship landing between the swap and the read-back legitimately grows the
		// resident set. Only SHORT means built content is unreferenced, so a gate that
		// flagged this would be reporting correct work as a defect.
		logs := captureRebuildLogs(t)
		shipper := &fakeRebuildShipper{manifestConfigured: true, manifestCount: 5}

		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "long-repo", true)
		require.NoError(t, err)
		require.Equal(t, 5, out.ResidentSegmentCount, "the reading is reported so a caller can compare it")
		require.Empty(t, logs.linesContaining(slog.LevelWarn, "FEWER sealed segments"),
			"a resident set LARGER than the derivation is not the shortfall this gate names")
	})

	t.Run("delta_that_CHANGED_the_count_is_reported", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		// before=4, after=2, at a partition count that did not move: a delta re-emits
		// partitions in place, so a drop means content left the live set.
		shipper := &fakeRebuildShipper{manifestSeq: []int{4, 2}, deltaBucketCount: 4}
		shipper.watermark = deltaPriorWatermark

		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "delta-short", false)
		require.NoError(t, err)
		require.Positive(t, shipper.deltaCalls.Load(), "fixture: this must actually take the DELTA path")
		require.Equal(t, 2, out.ResidentSegmentCount, "the outcome carries the post-swap reading")
		require.NotEmpty(t, logs.linesContaining(slog.LevelWarn, "CHANGED the resident segment cardinality"),
			"a delta that dropped a partition at a stable count must be loud")
	})

	t.Run("delta_that_HELD_the_count_is_silent", func(t *testing.T) {
		// The agreeing arm of the delta gate, for the same reason the full path has one.
		logs := captureRebuildLogs(t)
		shipper := &fakeRebuildShipper{manifestSeq: []int{4, 4}, deltaBucketCount: 4}
		shipper.watermark = deltaPriorWatermark

		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "delta-stable", false)
		require.NoError(t, err)
		require.Equal(t, 4, out.ResidentSegmentCount)
		require.Empty(t, logs.linesContaining(slog.LevelWarn, "CHANGED the resident segment cardinality"),
			"an unchanged cardinality at a stable partition count is the clean reading")
	})

	t.Run("delta_across_a_REALIGNMENT_is_unmeasured_not_flagged", func(t *testing.T) {
		// The gate is conditioned on a stable partition count, and that condition is
		// part of the contract rather than a loophole: a segment aligned to the old
		// count spans two partitions of the new one, so correct work legitimately moves
		// the cardinality and there is no honest equality left to assert.
		logs := captureRebuildLogs(t)
		shipper := &fakeRebuildShipper{manifestSeq: []int{2, 3}, deltaBucketCount: 4}
		shipper.watermark = deltaPriorWatermark

		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "delta-realign", false)
		require.NoError(t, err)
		require.Equal(t, derivedBucketCardinalityUnmeasured, out.ResidentSegmentCount,
			"no honest equality holds across a realignment, so the run reports UNMEASURED")
		require.Equal(t, int64(1), shipper.manifestReads.Load(),
			"and it must not even pay the second read once the count is known to have moved")
		require.Empty(t, logs.linesContaining(slog.LevelWarn, "CHANGED the resident segment cardinality"),
			"a legitimate realignment must NEVER be reported as a defect")
	})
}
