// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// client_segment_balance_duplication_test.go pins the verdict's duplication operand
// against THE SAME two counts manage(status) renders as "shipped N · live M".
//
// TWO SURFACES REPORTING ONE POOL MUST NOT DISAGREE. If the verdict derived its
// duplication from readers the status table does not use, an operator comparing the
// two would be reconciling two measurements of the same thing — and would have no way
// to tell a real divergence from two spellings of the same number.

// TestBalanceVerdict_DuplicationOperandsMatchTheStatusTable ties the verdict's
// carried operands to the status column's own readers on one fixture.
func TestBalanceVerdict_DuplicationOperandsMatchTheStatusTable(t *testing.T) {
	gt, name := kgtypes.GraphKnowledge, propagationGraphName
	c, _ := balanceFixtureWithEngine(t, balanceFixtureResidentDocs)

	b := c.evaluateArmBalance(balanceCtx(), gt, name)
	require.NotEqual(t, armNotEvaluated, b.verdict,
		"FIXTURE CONTROL: the verdict must form, or the operands below are zero values: %s",
		b.String())
	require.True(t, b.duplicationMeasured,
		"a verdict formed from a successful pair read reports its duplication as MEASURED")

	// The status column's own readers, called exactly as segCoveredFor calls them.
	shipped, err := c.SegmentCoverage().ShippedSegmentDocCount(balanceCtx(), gt, name)
	require.NoError(t, err)
	live := c.SegmentCoverage().LiveResidentDocCount(gt, name)

	// THE KNOWN POSITIVE, first: both readers must report a real corpus. Two zeros
	// are equal to each other and would satisfy every assertion below while proving
	// that neither side measured anything.
	require.Equal(t, balanceFixtureResidentDocs, live,
		"CONTROL: the status reader must see the seeded corpus, or the equalities below "+
			"are between two zeros")
	require.Positive(t, shipped, "CONTROL: and so must the shipped reader")

	require.Equal(t, shipped, b.shipped,
		"the verdict's shipped operand IS the status table's shipped count")
	require.Equal(t, live, b.live,
		"the verdict's live operand IS the status table's live count")
	require.Equal(t, shipped-live, b.duplication,
		"and the duplication it reports is exactly their difference — measured from one "+
			"snapshot of one engine, not derived across two reads")
}
