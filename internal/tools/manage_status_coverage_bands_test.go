// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_bands_test.go — the coverage bands that report a state NO
// arm is servicing: [stuck] for a maintained graph whose heal arm has given up, and
// [unmanaged] for a graph outside the working set.
//
// Split from manage_status_coverage_test.go, which sits against the file-length cap,
// along the seam its siblings already use (assembly in _collect_test.go, seams in
// _seam_test.go): these two pin the ARM ORDER and the WIRE SHAPE that the two new
// off-wire inputs introduced.
package tools

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSegCoverageDisposition_StuckAndUnmanagedBands pins the two bands that exist
// because item 4's rule — a band label must not claim an arm that is not servicing
// the row — applies to two states no arm services.
//
// FOUR OF THE FIVE CASES ARE ARM-ORDER CATCHERS, because both new arms sit between
// arms that would otherwise answer first and the collision is by construction:
//
//   - no_segments_wins_over_unmanaged fails if the unmanaged arm is hoisted above the
//     no-segments arm, which would relabel every non-segment graph in the account.
//   - stuck_preempts_self_healing fails if the stuck arm is placed AFTER the ratio
//     arm — a latched graph IS the sub-ratio shape, so the ratio arm would answer
//     first and go on calling it self-healing, which is exactly what item 4 forbids.
//   - unstalled_same_shape_still_self_healing is its pair: identical numbers with no
//     stall stamp must still read self-healing, so the new arm cannot have swallowed
//     the old band wholesale (which would satisfy the case above by itself).
//   - stuck_renders_an_age proves the band reaches the rendered cell with its age,
//     not just the classifier.
func TestSegCoverageDisposition_StuckAndUnmanagedBands(t *testing.T) {
	// A member row deep in the sub-ratio shape: 20 live against 100 embedded is well
	// below CoverageRatioThreshold and well above SegmentCoverageFloor, so absent any
	// new arm it classifies self-healing.
	member := func() CoverageRow {
		return CoverageRow{
			Embedded: 100, SegCovered: 20, LiveResident: 20,
			HasSegments: true, RepairVerified: true, InWorkingSet: true,
		}
	}

	t.Run("unmanaged_for_a_graph_outside_the_working_set", func(t *testing.T) {
		r := member()
		r.InWorkingSet = false
		assert.Equal(t, DispositionUnmanaged, segCoverageDisposition(r),
			"a segment-bearing graph this client does not maintain is unmanaged, not under-covered")
	})

	t.Run("no_segments_wins_over_unmanaged", func(t *testing.T) {
		r := member()
		r.HasSegments = false
		r.InWorkingSet = false
		assert.Equal(t, DispositionNoSegments, segCoverageDisposition(r),
			"a graph with no segment pool keeps the bare dash: there is no coverage to manage")
	})

	t.Run("stuck_preempts_self_healing", func(t *testing.T) {
		r := member()
		r.StalledSinceNanos = time.Now().Add(-2 * time.Hour).UnixNano()
		assert.Equal(t, DispositionStuck, segCoverageDisposition(r),
			"a stalled member reads stuck: no arm is healing it, so self-healing would be a false promise")
	})

	t.Run("unstalled_same_shape_still_self_healing", func(t *testing.T) {
		r := member()
		assert.Zero(t, r.StalledSinceNanos)
		assert.Equal(t, DispositionSelfHealing, segCoverageDisposition(r),
			"the same shape without a stall stamp keeps its old band")
	})

	t.Run("stuck_renders_an_age", func(t *testing.T) {
		r := member()
		r.Total = 500
		r.StalledSinceNanos = time.Now().Add(-134 * time.Minute).UnixNano()
		r.SegDisposition = segCoverageDisposition(r)
		require.Equal(t, DispositionStuck, r.SegDisposition)

		cell := formatCoverageRow(r)
		assert.Contains(t, cell, "[stuck 2h14m",
			"the stuck band renders how long the graph has been stuck, rounded to the minute")

		// KNOWN POSITIVE against a renderer that appends an age to everything: the
		// same row unstalled renders its band bare.
		unstalled := member()
		unstalled.Total = 500
		unstalled.SegDisposition = segCoverageDisposition(unstalled)
		assert.Contains(t, formatCoverageRow(unstalled), "[self-healing]",
			"every other band renders its name alone")
	})
}

// TestCoverageRow_WireShapeStaysTenKeys is the WIRE-SHAPE guard for the two fields
// this row gained for the stuck and unmanaged bands. TestCoverageRowJSONKeysUnchanged
// pins the same ten keys against the row as a whole; this one exists to fail
// specifically when StalledSinceNanos or InWorkingSet loses its json:"-" — so the
// failure names the cause instead of leaving a reader to work out which of eleven
// keys is new.
//
// It asserts SET EQUALITY against a literal ten-element list rather than a count, for
// the reason its sibling states: a count of ten survives dropping one key and adding
// another, which is exactly the shape a careless rename produces.
func TestCoverageRow_WireShapeStaysTenKeys(t *testing.T) {
	row := CoverageRow{
		Graph: "code/knowledge", Total: 10, Summarized: 9, Embedded: 8,
		SegCovered: 7, LiveResident: 6, HasSegments: true,
		SummaryFail: 1, EmbedFail: 2, SegDisposition: DispositionStuck,
		// Every off-wire input populated NON-ZERO: a json tag on any of them would
		// ship an eleventh key to a ten-key consumer, and a zero value could hide
		// behind an omitempty.
		RepairVerified: true, StalledSinceNanos: time.Now().UnixNano(), InWorkingSet: true,
	}

	raw, err := json.Marshal(row)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	got := make([]string, 0, len(decoded))
	for k := range decoded {
		got = append(got, k)
	}
	require.ElementsMatch(t, []string{
		"graph", "total", "summarized", "embedded", "seg_covered",
		"live_resident", "has_segments", "summary_fail", "embed_fail", "seg_disposition",
	}, got, "the ten pinned keys, and no eleventh")
	require.NotContains(t, decoded, "stalled_since_nanos",
		"the stall stamp is json:\"-\" — it feeds the band, it does not ship")
	require.NotContains(t, decoded, "in_working_set",
		"working-set membership is json:\"-\" — it feeds the band, it does not ship")
}
