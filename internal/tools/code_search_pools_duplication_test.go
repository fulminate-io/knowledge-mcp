// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestShippedCompleteForUnifiedSearch_DuplicationCannotFakeCompleteness is the
// regression gate on a false "complete" the summing resident count used to admit.
//
// THE RULE, NOT THE REPRODUCTION: the two operands must count each node ONCE, or
// the comparison means nothing. The bar counts nodes carrying a vector, once each.
// The summing resident count counts an id resident in TWO segments TWICE — the
// ordinary state after two rebuilds land without the first being retired — so a
// bucket holding Rd distinct documents against a bar of N passed the gate whenever
// Rd + duplication >= N. A genuinely short bucket cleared it on the strength of its
// own duplication, and the caller then served a PARTIAL corpus under a healthy
// banner, which is exactly the outcome this gate exists to refuse.
//
// THE FIXTURE IS THE ARITHMETIC. bar = 10, distinct = 8, summing = 12 (four ids
// resident twice). Under the old operand 12 >= 10 read COMPLETE while only 8 of the
// 10 documents were reachable; under the distinct operand 8 >= 10 is false and the
// two-pool union stays.
//
// THE BOUNDARY IS CLOSED ON BOTH SIDES below, because the fix is a change of
// operand and not of comparison — an off-by-one introduced while swapping the
// reader would otherwise pass the duplication case and fail nothing.
func TestShippedCompleteForUnifiedSearch_DuplicationCannotFakeCompleteness(t *testing.T) {
	ctx := context.Background()

	t.Run("duplication_does_not_make_a_short_bucket_complete", func(t *testing.T) {
		f := newCollapseFixture("dup-repo", 12, 10)
		// Four ids resident in two segments each: the summing reader says 12, the
		// distinct reader says 8. Only 8 real documents can ever be returned.
		f.cov.liveCovered = 8

		require.False(t, shippedCompleteForUnifiedSearch(ctx, f.cdeps, "dup-repo", collapseFixtureBranch),
			"a bucket holding 8 distinct documents against a bar of 10 is SHORT — it must not "+
				"read as complete because cross-segment duplication pushes its summing count to "+
				"12. Collapsing onto that bucket drops 2 documents from every branch search")
	})

	t.Run("control_the_same_bucket_without_duplication_is_complete", func(t *testing.T) {
		// THE KNOWN POSITIVE, and it is what stops the assertion above from passing
		// against a gate that has simply stopped collapsing. Same bar, same summing
		// count — the ONLY difference is that the documents are distinct.
		f := newCollapseFixture("dup-repo-control", 12, 10)
		f.cov.liveCovered = 12

		require.True(t, shippedCompleteForUnifiedSearch(ctx, f.cdeps, "dup-repo-control", collapseFixtureBranch),
			"CONTROL: 12 distinct documents against a bar of 10 is genuinely complete. If this "+
				"fails, the gate collapses nothing and the assertion above proves nothing about "+
				"duplication")
	})

	t.Run("exactly_meeting_the_bar_on_distinct_documents_is_complete", func(t *testing.T) {
		// The boundary, closed on the passing side: distinct == bar, with a large
		// summing count that must not influence the answer either way.
		f := newCollapseFixture("dup-repo-exact", 40, 10)
		f.cov.liveCovered = 10

		require.True(t, shippedCompleteForUnifiedSearch(ctx, f.cdeps, "dup-repo-exact", collapseFixtureBranch),
			"distinct == bar is non-shortfall and must collapse, however large the summing count is")
	})

	t.Run("one_distinct_document_short_is_not_complete", func(t *testing.T) {
		// The boundary, closed on the failing side.
		f := newCollapseFixture("dup-repo-short", 40, 10)
		f.cov.liveCovered = 9

		require.False(t, shippedCompleteForUnifiedSearch(ctx, f.cdeps, "dup-repo-short", collapseFixtureBranch),
			"one distinct document short is a real gap and the union must stay — the gate has no "+
				"tolerance, and a summing count of 40 must not buy that document back")
	})
}
