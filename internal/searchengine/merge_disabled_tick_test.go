// SPDX-License-Identifier: Apache-2.0

// merge_disabled_tick_test.go — the MERGER TICK'S WORK WHILE MERGING IS DISABLED
// (the resident-growth work, merger-tick requirement).
//
// Both production engines are constructed with SegmentCountTarget =
// MergeDisabledCountTarget and DeletesPctAllowed = MergeDisabledDeadRatio
// (segmentdist/manager_factory.go). Under those options the count arm of
// pickMergeTargets can never fire — no real set exceeds 1<<30 — and the dead-ratio
// arm is unreachable, because a ratio is dead/total and cannot reach 2.0. So every
// 50 ms tick fell into the entry loop and walked the WHOLE resident set only to
// return nil. An instrumented scratch copy measured 164,512 entry visits and 0
// merges over a two-second idle window at 4,096 resident segments.
//
// THE ASSERTION IS AN EXACT COUNT, NEVER A CLOCK. mergeScanCount() reports the
// entries pickMergeTargets' loop has walked; a wall-clock bound would measure the
// runner rather than the code. The zero carries its known positives in this same
// file, through the same counter, the same field and the same path: a
// default-options engine AT OR BELOW defaultSegmentCountTarget holding live
// entries, and a default-options engine holding an entry over DeletesPctAllowed.
// The first control's SIZE is load-bearing — the count arm returns set.entries
// BEFORE the loop whenever the count exceeds the target, so a default-options
// fixture at the disarmed fixture's segment count would read zero exactly as the
// disarmed arm does and would control nothing.

package searchengine

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// tickIdleWindow spans several mergeTickInterval periods, so a tick that walks the
// set has certainly run by the time the counter is read. It bounds a SLEEP, not an
// assertion: the assertion is the exact visit count.
const tickIdleWindow = 10 * mergeTickInterval

// tickDoc is one single-document batch for the fixtures below.
func tickDoc(prefix string, i int) []Document {
	return []Document{{
		ID:     fmt.Sprintf("%s-%d", prefix, i),
		Fields: map[string]string{FieldContent: "body"},
	}}
}

// disabledTickEngine builds a merge-disabled engine holding one segment per
// document, exactly as both production engines are constructed.
func disabledTickEngine(t *testing.T, segments int) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	e := New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: MergeDisabledCountTarget,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
	})
	t.Cleanup(e.Close)
	for i := range segments {
		require.NoError(t, e.Add(tickDoc("disabled", i)))
	}
	require.Equal(t, segments, e.ResidentSegmentCount(),
		"PRECONDITION: the fixture must hold the segments it is asserting about")
	return e
}

// TestDisabledMergerTickVisitsNoEntries is the tick row.
func TestDisabledMergerTickVisitsNoEntries(t *testing.T) {
	t.Run("a_disarmed_engine_walks_no_entries_on_any_tick", func(t *testing.T) {
		t.Parallel()
		e := disabledTickEngine(t, 64)
		before := e.mergeScanCount()
		time.Sleep(tickIdleWindow)
		require.Equal(t, int64(0), e.mergeScanCount()-before,
			"a merge-disabled engine's 50 ms tick must visit zero resident entries; "+
				"every visit is a liveDocs.DeadCount call that can never select a target")
		require.Equal(t, uint64(0), e.MergeCount(),
			"PRECONDITION: nothing merged, so any visit would have been pure waste")
	})

	t.Run("known_positive_default_options_below_the_count_target_walk_the_entries", func(t *testing.T) {
		t.Parallel()
		// AT OR BELOW defaultSegmentCountTarget deliberately: above it the count arm
		// returns before the loop and this control would read zero for a reason that
		// has nothing to do with the disarm predicate.
		e := New[mockQuery, mockStats](mockFormat{}, Options{MinSegmentDocs: 1})
		t.Cleanup(e.Close)
		for i := range defaultSegmentCountTarget {
			require.NoError(t, e.Add(tickDoc("armed", i)))
		}
		require.Equal(t, defaultSegmentCountTarget, e.ResidentSegmentCount())
		require.Positive(t, e.set.Load().entries[0].meta.DocCount,
			"PRECONDITION: the control's entries carry documents, so the loop has a dead ratio to compute")

		before := e.mergeScanCount()
		time.Sleep(tickIdleWindow)
		require.Positive(t, e.mergeScanCount()-before,
			"an armed engine at or below the count target MUST walk its entries; "+
				"a counter reading zero here is measuring nothing and the zero above would be vacuous")
	})

	t.Run("known_positive_a_dirty_entry_over_the_dead_ratio_is_still_selected", func(t *testing.T) {
		t.Parallel()
		e := New[mockQuery, mockStats](mockFormat{}, Options{MinSegmentDocs: 4})
		t.Cleanup(e.Close)
		docs := make([]Document, 0, 4)
		for i := range 4 {
			docs = append(docs, tickDoc("dirty", i)...)
		}
		require.NoError(t, e.Add(docs))
		require.Equal(t, 1, e.ResidentSegmentCount())
		// Three of four dead is 0.75, over the 0.33 default.
		for i := range 3 {
			e.Delete(fmt.Sprintf("dirty-%d", i))
		}
		require.True(t, e.MergeEligible(),
			"a default-options engine holding an entry over DeletesPctAllowed must select it; the loop is the only arm that can")
		require.Positive(t, e.mergeScanCount(),
			"the dead-ratio arm walks the same loop, through the same counter and the same field")
	})

	t.Run("merge_eligible_still_answers_false_on_a_disarmed_engine", func(t *testing.T) {
		t.Parallel()
		e := disabledTickEngine(t, 8)
		require.False(t, e.MergeEligible(),
			"MergeEligible asks the trigger rather than restating it, so the disarm must reach it through the same predicate")
		require.Equal(t, int64(0), e.mergeScanCount(),
			"and asking the predicate must not itself walk the entries")
	})
}
