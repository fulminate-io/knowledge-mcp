// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBalancedAtQuiescence_ExactBothDirections pins the equation as EXACT in both
// directions with no numeric slack. Every case is written as an INEQUALITY rather
// than as the bare word, because the two readings of "surplus" are opposite
// inequalities and reading one as the other has already inverted a trigger once.
func TestBalancedAtQuiescence_ExactBothDirections(t *testing.T) {
	t.Run("resident_one_below_done_is_deficit", func(t *testing.T) {
		b := balancedAtQuiescence(999, 1000, 0, 0, false)
		require.Equal(t, armDeficit, b.verdict,
			"resident < done by exactly one must report a deficit — a tolerance is the one "+
				"knob that lets a clog sit forever, which is why this has none")
	})

	t.Run("resident_one_above_done_is_surplus", func(t *testing.T) {
		// A pure-predicate construction: with the distinct-and-live resident reader
		// wired, resident > done is expected to be unreachable in a live system. The
		// predicate still has to answer it, and answering it as "balanced" would hide
		// a real disagreement between the two sides.
		b := balancedAtQuiescence(1001, 1000, 0, 0, false)
		require.Equal(t, armSurplus, b.verdict, "resident > done by exactly one")
	})

	t.Run("equality_is_balanced", func(t *testing.T) {
		b := balancedAtQuiescence(1000, 1000, 0, 0, false)
		require.Equal(t, armBalanced, b.verdict)
	})

	t.Run("equality_only_after_subtracting_failures_is_balanced", func(t *testing.T) {
		// THE APPROXIMATE ARM (the still-vectored subset is UNMEASURED here): 1000
		// vectors and 40 marked failures, so every marked failure is subtracted and the
		// index is expected to hold 960. THE FAILURE COUNT IS CARRIED so the shrink
		// stays visible rather than being absorbed into the arithmetic. The EXACT arm,
		// where only the still-vectored subset leaves the owed side, is pinned in
		// client_segment_balance_exact_test.go.
		b := balancedAtQuiescence(960, 1000, 40, 0, false)
		require.Equal(t, armBalanced, b.verdict,
			"the subtraction is on the done side: a marked failure is work the local index "+
				"is no longer expected to hold")
		require.Equal(t, 40, b.failures, "the count must survive into the verdict, not be consumed by it")

		// CONTROL on the same operands: without the subtraction this is a deficit, so
		// the balanced answer above is caused by the failure term and not by the
		// numbers happening to line up.
		require.Equal(t, armDeficit, balancedAtQuiescence(960, 1000, 0, 0, false).verdict,
			"CONTROL: the same operands without the failure term must NOT be balanced")
	})

	t.Run("not_evaluated_is_never_balanced", func(t *testing.T) {
		b := notEvaluated("the resident count could not be read")
		require.Equal(t, armNotEvaluated, b.verdict)
		require.NotEqual(t, armBalanced, b.verdict,
			"a check that could not run must never read as healthy — that collapse is the "+
				"failure this verdict exists to remove")
		require.Contains(t, b.String(), "not evaluated")
		require.Contains(t, b.String(), "could not be read", "a refusal must carry its reason")
	})
}

// TestArmBalanceRender_ShowsFailuresOnlyWhenNonZero pins the DISPLAY half of the
// failures rule. Carrying the count in the struct and never printing it satisfies
// "failures stay visible" only on paper; printing it on every healthy graph trains
// a reader to skip the line, which costs the same visibility by another route.
func TestArmBalanceRender_ShowsFailuresOnlyWhenNonZero(t *testing.T) {
	withFailures := balancedAtQuiescence(960, 1000, 40, 0, false).String()
	require.Contains(t, withFailures, "40 marked failures",
		"a non-zero failure count must appear in the RENDERED verdict")
	// THE SUBTRACTION IS SHOWN, and the compared quantity is named "owed". The
	// previous form rendered `done 960` beside a separate `with 40 marked failures`,
	// which reads as though the two ADD to 1000 — the arithmetic runs the other way,
	// and a reader reconstructing it wrongly concludes the operands disagree.
	require.Contains(t, withFailures, "owed 960 (done 1000 \u2212 40 marked failures)",
		"the render must name the compared quantity and show its derivation")
	require.NotContains(t, withFailures, "done 960",
		"960 is the OWED count, not the done count; labeling it `done` is the ambiguity")

	zeroFailures := balancedAtQuiescence(1000, 1000, 0, 0, false).String()
	require.NotContains(t, zeroFailures, "marked failures",
		"the clause must be omitted entirely at zero, not rendered as 'with 0'")
	require.Contains(t, zeroFailures, "balanced")

	// THE RENDER NAMES THE INEQUALITY, never the bare word alone.
	require.Contains(t, balancedAtQuiescence(999, 1000, 0, 0, false).String(), "resident 999 < owed 1000")
	require.Contains(t, balancedAtQuiescence(1001, 1000, 0, 0, false).String(), "resident 1001 > owed 1000")
	require.Contains(t, zeroFailures, "resident 1000 == owed 1000")
	require.NotContains(t, zeroFailures, "(done ",
		"at zero failures there is no subtraction to show, so the derivation clause is omitted")

	// DUPLICATION FOLLOWS THE SAME RULE and is reported beside the verdict.
	dup := balancedAtQuiescence(1000, 1000, 0, 0, false)
	dup.duplication = 12
	require.Contains(t, dup.String(), "12 duplicated resident documents")
	require.NotContains(t, zeroFailures, "duplicated resident documents",
		"omitted entirely at zero")
	require.True(t, strings.HasPrefix(dup.String(), "balanced"),
		"duplication is reported BESIDE the verdict, never folded into it — a duplicated "+
			"corpus is still balanced")
}

// TestArmBalanceRender_UnmeasuredDuplicationIsSaidOutLoud pins that a FAILED duplication
// read is distinguishable from a clean graph.
//
// THE TWO ARE OTHERWISE IDENTICAL: both leave duplication at zero, and the renderer omits
// a zero clause — so without this the loss would be silent at exactly the surface built
// to make things visible.
func TestArmBalanceRender_UnmeasuredDuplicationIsSaidOutLoud(t *testing.T) {
	lost := balancedAtQuiescence(1000, 1000, 0, 0, false)
	lost.duplicationReason = "the summing resident count could not be read"
	require.False(t, lost.duplicationMeasured, "the zero value is UNMEASURED, deliberately")
	require.Contains(t, lost.String(), "duplication unmeasured",
		"a duplication the reader could not produce must be reported as unmeasured")
	require.Contains(t, lost.String(), "the summing resident count could not be read",
		"and it must carry the reason, so an operator can tell a fault from a cold engine")

	// THE KNOWN-POSITIVE: a MEASURED zero renders no such clause, which is what makes the
	// assertion above discrimination rather than a string that is always present.
	clean := balancedAtQuiescence(1000, 1000, 0, 0, false)
	clean.duplicationMeasured = true
	require.NotContains(t, clean.String(), "duplication unmeasured",
		"a graph whose duplication was measured at zero must NOT be reported as unmeasured")

	// The verdict itself stands in both cases — duplication never enters it.
	require.Contains(t, lost.String(), "balanced")
	require.Contains(t, clean.String(), "balanced")
}

// TestArmBalanceRender_OperandCaveatIsRendered pins the INTERIM disclosure that the
// failure subtraction is an approximation.
//
// WHY IT IS RENDERED RATHER THAN MERELY KNOWN: the failure count subtracted from `done`
// includes nodes that never held a vector and so were never counted in `done` at all, so
// a genuine shortfall of up to that size reads BALANCED. The subset count that makes the
// equation exact now exists, but a server that does not REPORT it leaves the verdict on
// this approximate form — and printing the numbers without the qualification is how an
// approximation gets read as an exact result. Its retirement on presence is pinned in
// client_segment_balance_exact_test.go and client_segment_balance_wire_test.go.
func TestArmBalanceRender_OperandCaveatIsRendered(t *testing.T) {
	qualified := balancedAtQuiescence(8, 10, 2, 0, false)
	require.Equal(t, armBalanced, qualified.verdict,
		"fixture: 8 resident against owed 8 balances — this is the MASKING shape, where a "+
			"real 2-document shortfall is absorbed by the subtraction")
	qualified.reason = "operands approximate: 2 marked failures were subtracted"
	require.Contains(t, qualified.String(), "[operands approximate:",
		"an evaluated verdict carrying a caveat must RENDER it beside the numbers")

	// KNOWN-POSITIVE: an unqualified verdict renders no bracket at all.
	require.NotContains(t, balancedAtQuiescence(1000, 1000, 0, 0, false).String(), "[",
		"a verdict with no caveat must not grow a decorative one")
}
