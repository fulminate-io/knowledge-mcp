// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// client_segment_balance_wire_test.go drives the failure-holding-vector count
// END TO END: it is served on a real Stats response over a real Connect handler,
// decoded by the real coverage reader, and consumed by the real verdict.
//
// TWO TESTS FLANKING THE SEAM WOULD PROVE NEITHER SIDE HANDS OVER. The whole point
// of the optional field is that ABSENCE survives serialization, and a unit test
// setting the struct field by hand never exercises the wire form, where a sent zero
// and an omitted field are one byte apart.

// balanceFixtureWithEngine is balanceFixture with the fake engine returned, so an
// arm can script the Stats response the verdict will read.
func balanceFixtureWithEngine(t *testing.T, embedded int32) (*client, *reconcileEngine) {
	t.Helper()
	c, eng, dir := buildReconcileClientWithDir(t, embedded)
	seedL2Corpus(t, dir, kgtypes.GraphKnowledge, propagationGraphName, balanceFixtureResidentDocs)
	return c, eng
}

// TestBalanceVerdict_FailureHoldingCountCrossesTheWire pins that a PRESENT subset
// retires the approximation caveat and drives the exact equation, and that an ABSENT
// one keeps both.
func TestBalanceVerdict_FailureHoldingCountCrossesTheWire(t *testing.T) {
	const (
		// The server reports 6 vectors against the fixture's 4 resident documents,
		// with 2 marked failures. Whether those two hold vectors is the ONLY thing
		// the arms below vary, so the operand is the only thing under test.
		embedded int32 = 6
		marked   int32 = 2
	)
	gt, name := kgtypes.GraphKnowledge, propagationGraphName

	arm := func(t *testing.T, holding *int32) armBalance {
		t.Helper()
		c, eng := balanceFixtureWithEngine(t, embedded)
		eng.setEmbedFailures(marked, holding)
		b := c.evaluateArmBalance(balanceCtx(), gt, name)
		require.NotEqual(t, armNotEvaluated, b.verdict,
			"FIXTURE CONTROL: the verdict must actually form, or every assertion below "+
				"passes on a refusal: %s", b.String())
		require.Equal(t, balanceFixtureResidentDocs, b.resident,
			"FIXTURE CONTROL: the seeded corpus must be resident")
		require.Equal(t, int(marked), b.failures,
			"FIXTURE CONTROL: the marked-failure count must cross the wire too, or the "+
				"subset has nothing to be a subset of")
		return b
	}

	t.Run("absent_subset_keeps_the_caveat_and_the_approximate_equation", func(t *testing.T) {
		b := arm(t, nil)
		require.False(t, b.failuresHoldingVectorMeasured,
			"an omitted wire field must decode as UNMEASURED, never as a measured zero")
		require.Contains(t, b.reason, "operands approximate",
			"the caveat is retained against a server that does not report the subset")
		require.Equal(t, int(embedded)-int(marked), b.owed(),
			"and the approximate equation is retained verbatim: done − every marked failure")
		require.Equal(t, armBalanced, b.verdict,
			"4 resident against owed 4 — the MASKING shape, reported with its caveat: %s", b.String())
	})

	t.Run("present_subset_at_zero", func(t *testing.T) {
		// THE DISCRIMINATING CASE for presence-versus-value. The number is the same
		// zero the absent arm's decoded struct holds; only its PRESENCE differs.
		b := arm(t, proto.Int32(0))
		require.True(t, b.failuresHoldingVectorMeasured,
			"a field sent as 0 must decode as MEASURED — presence is read off the pointer, "+
				"never off the value")
		require.Empty(t, b.reason,
			"the caveat retires on presence: this server DID report the subset")
		require.Equal(t, int(embedded), b.owed(),
			"neither marked failure holds a vector, so neither was ever inside done and "+
				"neither is subtracted")
		require.Equal(t, armDeficit, b.verdict,
			"the same operands the absent arm called BALANCED are a real 2-document "+
				"shortfall once the subset is known: %s", b.String())
	})

	t.Run("present_subset_covering_every_marked_failure", func(t *testing.T) {
		b := arm(t, proto.Int32(marked))
		require.True(t, b.failuresHoldingVectorMeasured)
		require.Equal(t, int(embedded)-int(marked), b.owed(),
			"both marked failures hold vectors that are counted in done and that no segment "+
				"will ever ship, so both leave the owed side")
		require.Equal(t, armBalanced, b.verdict, "%s", b.String())
		require.Contains(t, b.String(), "2 of 2 marked failures still holding a vector")
	})
}
