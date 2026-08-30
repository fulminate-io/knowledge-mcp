// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// client_segment_balance_exact_test.go pins the EXACT failure subtraction: which
// marked failures leave the owed side, and what happens when the server does not
// say.
//
// THE DEFECT IT GUARDS, RESTATED FROM THE MEASUREMENT RATHER THAN FROM A COMMENT.
// `done` is the graph's unfiltered vector count, so a failure-marked node that
// still HOLDS a vector is inside it. The client's segment-rebuild scan refuses to
// ship such a node — vector possession and an EMPTY embed-failure marker are both
// required (cmd/knowledge-server/internal/store/composite_db_segment_rebuild.go,
// visitLive) — so that same node can never be inside `resident`. It is therefore
// exactly what owed must drop, and the marked failures that hold NO vector were
// never in `done` to begin with, so subtracting them removes something that was
// never there and absorbs a real shortfall of the same size.

// TestBalancedAtQuiescence_SubtractsOnlyFailuresHoldingAVector is the double-count
// case. Every arm below fixes resident and done and varies ONLY which failures hold
// a vector, so the equation is the only thing under test.
func TestBalancedAtQuiescence_SubtractsOnlyFailuresHoldingAVector(t *testing.T) {
	t.Run("marked_failures_that_never_held_a_vector_do_not_reduce_owed", func(t *testing.T) {
		// 10 vectors, 2 marked failures, NEITHER holding a vector, and an index
		// holding 8. The two failures were never counted in done, so the index is
		// genuinely 2 SHORT — and the pre-fix equation (done − failures) reported
		// this exact shape as balanced.
		b := balancedAtQuiescence(8, 10, 2, 0, true)
		require.Equal(t, armDeficit, b.verdict,
			"a marked failure that never held a vector was never inside done, so subtracting "+
				"it removes something that was never there and hides a real 2-document shortfall: %s",
			b.String())
		require.Equal(t, 10, b.owed(),
			"owed must be the full vector count when no marked failure holds one")
	})

	t.Run("marked_failures_still_holding_a_vector_do_reduce_owed", func(t *testing.T) {
		// The SAME operands with both failures still holding their vectors. Those two
		// vectors are inside done and the rebuild scan refuses to ship them, so the
		// index holding 8 is exactly right.
		b := balancedAtQuiescence(8, 10, 2, 2, true)
		require.Equal(t, armBalanced, b.verdict,
			"a failure-marked node holding a vector is counted in done and is excluded from "+
				"every segment, so it is owed to nobody: %s", b.String())
		require.Equal(t, 8, b.owed())
	})

	t.Run("a_partial_subset_subtracts_only_that_subset", func(t *testing.T) {
		// THE DISCRIMINATING ARM. Both all-or-nothing arms above are also satisfied by
		// the inverse equation (done − (failures − holding)) with the operands mirrored,
		// so a middle case is what separates the two readings: with 4 marked failures of
		// which 1 holds a vector, the exact owed is 9 and the inverse reading gives 7.
		b := balancedAtQuiescence(9, 10, 4, 1, true)
		require.Equal(t, 9, b.owed(),
			"owed subtracts the HOLDING subset (1), never the non-holding remainder (3)")
		require.Equal(t, armBalanced, b.verdict, "%s", b.String())

		require.Equal(t, armSurplus, balancedAtQuiescence(9, 10, 4, 3, true).verdict,
			"CONTROL: moving the subset changes the verdict, so the arm above is measuring "+
				"the subset rather than the fixture happening to line up")
	})

	t.Run("an_unmeasured_subset_keeps_the_approximate_equation", func(t *testing.T) {
		// A server that does not supply the subset must NOT be read as supplying zero.
		// Zero would flip this graph to the exact form and change the verdict, which is
		// the silent promotion the presence flag exists to refuse.
		approx := balancedAtQuiescence(8, 10, 2, 0, false)
		require.Equal(t, armBalanced, approx.verdict,
			"the pre-existing approximate equation is retained verbatim when the count is absent")
		require.Equal(t, 8, approx.owed())

		require.Equal(t, armDeficit, balancedAtQuiescence(8, 10, 2, 0, true).verdict,
			"KNOWN POSITIVE on the identical operands: with the count PRESENT at zero the "+
				"verdict changes, so the arm above is gated on presence and not on the value")
	})
}

// TestArmBalanceRender_ExactSubtractionNamesTheSubset pins the DISPLAY half. The
// subtracted number is smaller than the marked-failure count the status table shows
// for the same graph, and an unexplained mismatch between two surfaces reads as a
// bug in one of them.
func TestArmBalanceRender_ExactSubtractionNamesTheSubset(t *testing.T) {
	exact := balancedAtQuiescence(8, 10, 4, 2, true).String()
	require.Contains(t, exact, "owed 8 (done 10 − 2 of 4 marked failures still holding a vector)",
		"the exact form must name BOTH the subtracted subset and the whole marked count")

	// THE APPROXIMATE FORM IS UNCHANGED, which is what makes the assertion above a
	// discrimination rather than a string that renders either way.
	approx := balancedAtQuiescence(960, 1000, 40, 0, false).String()
	require.Contains(t, approx, "owed 960 (done 1000 − 40 marked failures)")
	require.NotContains(t, approx, "still holding a vector",
		"an unmeasured subset must not render as a measured one")

	// A MEASURED ZERO SUBSET still shows the derivation, because owed then equals done
	// while the graph really does carry marked failures — omitting the clause would
	// make the failures invisible on exactly the graph that has them.
	zeroSubset := balancedAtQuiescence(10, 10, 3, 0, true).String()
	require.Contains(t, zeroSubset, "owed 10 (done 10 − 0 of 3 marked failures still holding a vector)")

	// And a graph with NO marked failures grows no clause at all.
	require.NotContains(t, balancedAtQuiescence(10, 10, 0, 0, true).String(), "(done ",
		"at zero marked failures there is no subtraction to show")
}

// TestArmBalanceRender_DuplicationNamesShippedAndLive pins that the duplication
// term reports the two counts it was formed from, in the same grammar
// manage(status) renders for the same pair.
func TestArmBalanceRender_DuplicationNamesShippedAndLive(t *testing.T) {
	dup := balancedAtQuiescence(1000, 1000, 0, 0, true)
	dup.duplicationMeasured = true
	dup.shipped, dup.live = 1012, 1000
	dup.duplication = dup.shipped - dup.live
	require.Contains(t, dup.String(), "12 duplicated resident documents (shipped 1012 · live 1000)",
		"the duplication term must name its operands, not only their difference")

	// KNOWN POSITIVE: a graph with no duplication renders no operands either, so the
	// assertion above is discrimination rather than an always-present string.
	clean := balancedAtQuiescence(1000, 1000, 0, 0, true)
	clean.duplicationMeasured = true
	clean.shipped, clean.live = 1000, 1000
	require.NotContains(t, clean.String(), "shipped ",
		"a graph with zero duplication reports no duplication clause at all")
}
