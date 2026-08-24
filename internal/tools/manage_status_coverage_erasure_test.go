// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// manage_status_coverage_erasure_test.go — the deletion backlog and the two
// consumer ages reach the rendered row.
//
// WHY BOTH HALVES ARE IN ONE CELL: they answer different questions and neither
// substitutes for the other. The server's counters say how much is piling up;
// only this client knows whether its own consumers are still moving — and a
// consumer that stops arriving is exactly what an arrival-driven stall alarm can
// never report.

// erasureRowFor builds a rendered row from the wire counters and the two local
// ages, through the same constructor every other cell uses.
func erasureRowFor(retained int, newestAge, rebuildAge, mergeAge int64) CoverageRow {
	return newCoverageRow("code/repo", &knowledgev1.GraphStats{
		NonProxyNodeCount:     10,
		RetainedErasureCount:  int32(retained),
		NewestErasureAgeNanos: newestAge,
	}, 0, 0, false, false, true, false, 0, rebuildAge, mergeAge)
}

// TestCoverageRow_RendersErasureBacklogAndConsumerAges drives the row.
func TestCoverageRow_RendersErasureBacklogAndConsumerAges(t *testing.T) {
	t.Run("row_renders_retained_count_and_age", func(t *testing.T) {
		row := erasureRowFor(7, int64(3*time.Hour), int64(time.Minute), int64(2*time.Minute))
		require.Equal(t, 7, row.RetainedErasures, "the wire count reaches the row")
		require.Equal(t, int64(3*time.Hour), row.NewestErasureAgeNanos)

		out := formatCoverageRow(row)
		require.Contains(t, out, "7", "the retained count is rendered")
		require.Contains(t, out, "newest 3h0m0s",
			"and the age of the NEWEST erasure beside it — the pair is what says how much is piling up")
	})

	t.Run("row_renders_both_consumer_ages", func(t *testing.T) {
		// BOTH, separately. One consumer advancing while the other has stopped is
		// precisely the state worth seeing, and a single merged age would hide it.
		row := erasureRowFor(3, int64(time.Hour), int64(5*time.Minute), int64(90*time.Minute))
		out := formatCoverageRow(row)
		require.Contains(t, out, "rebuild 5m0s", "the rebuild consumer's age is rendered")
		require.Contains(t, out, "merge 1h30m0s", "and the merge consumer's, separately")
	})

	t.Run("no_position_renders_never", func(t *testing.T) {
		// THE INVERSION GUARD, third appearance in this plan. A consumer with no
		// position has NOT STARTED, which is not the same as being stalled; an age
		// measured from a zero position would be the age of the unix epoch and would
		// say the opposite of the truth.
		row := erasureRowFor(2, int64(time.Hour), 0, int64(4*time.Minute))
		out := formatCoverageRow(row)
		require.Contains(t, out, "rebuild never",
			"a consumer with no recorded position renders 'never', never an age")
		// KNOWN-POSITIVE CONTROL in the same row: the other consumer DOES render an
		// age, so "never" is a decision about this consumer rather than a renderer
		// that cannot produce ages at all.
		require.Contains(t, out, "merge 4m0s",
			"control: the peer with a position still renders its age")
		require.NotContains(t, out, "rebuild 4",
			"and the two are not conflated")
	})

	t.Run("zero_backlog_reads_as_no_backlog", func(t *testing.T) {
		// A blank cell where a zero belongs is how an operator learns to ignore a
		// column, so an empty journal says so in words.
		row := erasureRowFor(0, 0, int64(time.Minute), int64(time.Minute))
		out := formatCoverageRow(row)
		require.Contains(t, out, "none",
			"an empty journal renders as no backlog rather than as a blank or an alarm")
		require.NotContains(t, out, "newest",
			"and carries no age, because with no rows there is no newest stamp to measure from")
	})

	t.Run("unknown_backlog_renders_unknown_not_none", func(t *testing.T) {
		// THE DISTINCTION THE SENTINEL EXISTS FOR. A negative age means the server
		// could not READ the journal — which is not the same as the journal being
		// empty, and rendering both the same way reports certainty nobody has.
		//
		// THE COUNT IS DELIBERATELY NON-ZERO IN THIS FIXTURE. When the age is the
		// unknown signal the count is not meaningful and the client must ignore it,
		// so a renderer that reaches for the count anyway is caught here rather than
		// shipping a backlog figure the server never claimed to have measured.
		unknown := erasureRowFor(5, erasureAgeUnknown, int64(time.Minute), int64(time.Minute))
		empty := erasureRowFor(0, 0, int64(time.Minute), int64(time.Minute))

		unknownCell, emptyCell := erasureBacklogCell(unknown), erasureBacklogCell(empty)
		require.NotEqual(t, emptyCell, unknownCell,
			"unknown and empty must render DIFFERENTLY FROM EACH OTHER — asserting only that each is "+
				"non-empty passes for a renderer that prints the same placeholder for both and collapses "+
				"exactly the distinction the sentinel carries")

		require.Contains(t, formatCoverageRow(unknown), "unknown",
			"an unreadable journal says so in words, and it reaches the rendered row")
		// The negatives are asserted on the CELL rather than the whole row: the row
		// carries other columns whose digits and words would answer for this one.
		require.NotContains(t, unknownCell, "none",
			"and never reads as an empty journal")
		require.NotContains(t, unknownCell, "5",
			"nor renders the count that arrived beside the unknown age — it is not meaningful")

		// KNOWN-POSITIVE CONTROL on the same renderer: a real backlog still renders
		// its count and age, so "unknown" above is a decision about this state rather
		// than a cell that has stopped rendering numbers at all.
		live := formatCoverageRow(erasureRowFor(5, int64(2*time.Hour), int64(time.Minute), int64(time.Minute)))
		require.Contains(t, live, "5", "control: a readable backlog still renders its count")
		require.Contains(t, live, "newest 2h0m0s", "and its age")
	})
}
