// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestSimulate_ExcludingMatchesFold is the PRIMARY lockstep assertion: the
// hand-inlined computePropertiesExcluding copy applies the SAME read-time recency
// weighting as the fold. With a non-existent exclude id (nothing excluded) the two
// must agree field-for-field over a mixed-age, mixed-polarity charge set.
// FAILS-WHEN-ABSENT: dropping the scalar from computePropertiesExcluding makes the
// aged charges diverge from the fold.
func TestSimulate_ExcludingMatchesFold(t *testing.T) {
	now := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	charges := []*knowledgev1.Node{
		recencyCharge("a", "positive", 20, nanosDaysAgo(now, 0)),
		recencyCharge("b", "negative", 10, nanosDaysAgo(now, 180)),
		recencyCharge("c", "positive", 5, nanosDaysAgo(now, 30)),
	}
	fold := computePropertiesFromCharges(charges, now)
	excl := computePropertiesExcluding(charges, "__no_such_id__", now)

	assert.InDelta(t, fold.Valence, excl.Valence, 1e-9, "Valence")
	assert.InDelta(t, fold.Magnitude, excl.Magnitude, 1e-9, "Magnitude")
	assert.InDelta(t, fold.Consistency, excl.Consistency, 1e-9, "Consistency")
	assert.InDelta(t, fold.SelfTrust, excl.SelfTrust, 1e-9, "SelfTrust")
	assert.InDelta(t, fold.PositiveWeight, excl.PositiveWeight, 1e-9, "PositiveWeight")
	assert.InDelta(t, fold.NegativeWeight, excl.NegativeWeight, 1e-9, "NegativeWeight")
	assert.Equal(t, fold.ChargeCount, excl.ChargeCount, "ChargeCount")
}

// TestSimulate_AddChargeAgeZeroFullWeight exercises the REAL SimulateAddCharge
// inline through the influenceCorpus fake. The pre-existing charge carries
// UpdatedAt==0 (neutral scalar 0.5, wall-clock independent) so the result is
// deterministic regardless of SimulateAddCharge's internal time.Now(). The
// hypothetical added charge is age 0 → contributed at FULL weight (no scalar), and
// the inlined SelfTrust matches the fold shape baseSelfTrust + Consistency·log(1+rawCount).
func TestSimulate_AddChargeAgeZeroFullWeight(t *testing.T) {
	c := newInfluenceCorpus()
	c.addThought("T1", "T1", "10")  // one positive charge weight 10
	c.charges["c-T1"].UpdatedAt = 0 // zero timestamp → neutral 0.5 scalar (deterministic)

	const w = 10.0
	res, err := SimulateAddCharge(context.Background(), c, "T1", "positive", w)
	require.NoError(t, err)
	require.Len(t, res.AffectedThoughts, 1)
	before := res.AffectedThoughts[0].Before
	after := res.AffectedThoughts[0].After

	// before: the single weight-10 positive charge decayed by the neutral 0.5 scalar.
	assert.InDelta(t, 0.5*10, before.PositiveWeight, 1e-9, "before reflects the recency-weighted existing charge")
	// The age-0 hypothetical charge is added at FULL weight (no scalar).
	assert.InDelta(t, before.PositiveWeight+w, after.PositiveWeight, 1e-9, "added charge contributes full weight")
	// Inlined SelfTrust matches the fold shape with the RAW count+1.
	wantSelfTrust := baseSelfTrust + after.Consistency*math.Log(1+float64(before.ChargeCount+1))
	assert.InDelta(t, wantSelfTrust, after.SelfTrust, 1e-9, "inline SelfTrust matches the fold shape with raw count")
	assert.Equal(t, before.ChargeCount+1, after.ChargeCount, "ChargeCount increments by 1")
}
