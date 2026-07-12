// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// recencyCharge builds a charge node carrying the given polarity + weight (as the
// string metadata the fold parses) and the given UpdatedAt (unix-nanos). Shared by
// the recency fold/lockstep/matrix tests in this package.
func recencyCharge(id, polarity string, weight float64, updatedAtNanos int64) *knowledgev1.Node {
	ch := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeCharge), UpdatedAt: updatedAtNanos}
	kgtypes.SetValue(ch, "polarity", polarity)
	kgtypes.SetValue(ch, "weight", fmt.Sprintf("%g", weight))
	return ch
}

// nanosDaysAgo returns the unix-nanos timestamp `days` days before now. A negative
// days yields a FUTURE timestamp (used for the clock-skew guard case).
func nanosDaysAgo(now time.Time, days float64) int64 {
	return now.Add(-time.Duration(days * 24 * float64(time.Hour))).UnixNano()
}

// TestRecencyScalar pins the logarithmic curve recencyScalar = 1/(1+log2(1+ageDays/τ))
// at the verified control points, plus the zero-timestamp (neutral 0.5) and
// future-timestamp (clamp to 1.0) guards. FAILS-WHEN-ABSENT: a no-decay scalar
// (always 1.0) fails every aged point.
func TestRecencyScalar(t *testing.T) {
	now := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		updatedAt int64
		want      float64
	}{
		{"age0d", nanosDaysAgo(now, 0), 1.0},
		{"age30d", nanosDaysAgo(now, 30), 0.5},
		{"age90d", nanosDaysAgo(now, 90), 1.0 / 3.0},
		{"age210d", nanosDaysAgo(now, 210), 0.25},
		{"age450d", nanosDaysAgo(now, 450), 0.20},
		{"zeroTimestamp", 0, 0.5},
		{"future10d", nanosDaysAgo(now, -10), 1.0},
	}
	for _, tc := range cases {
		got := recencyScalar(now, tc.updatedAt)
		assert.InDelta(t, tc.want, got, 1e-9, tc.name)
	}
}

// TestFold_ValenceCancellationEqualAge proves the recency scalar CANCELS in the
// valence ratio when all charges share an age: equal-age +20/-10 yields the
// no-decay baseline valence 1/3, and two equal-age positives yield +1.0,
// regardless of how old the charges are.
func TestFold_ValenceCancellationEqualAge(t *testing.T) {
	now := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	age := nanosDaysAgo(now, 120)

	mixed := []*knowledgev1.Node{
		recencyCharge("p", "positive", 20, age),
		recencyCharge("n", "negative", 10, age),
	}
	props := computePropertiesFromCharges(mixed, now)
	// No-decay baseline valence = (20-10)/(20+10) = 1/3; the shared scalar cancels.
	assert.InDelta(t, 1.0/3.0, props.Valence, 1e-9, "equal-age scalar cancels in the valence ratio")

	positives := []*knowledgev1.Node{
		recencyCharge("p1", "positive", 5, nanosDaysAgo(now, 300)),
		recencyCharge("p2", "positive", 7, nanosDaysAgo(now, 300)),
	}
	pp := computePropertiesFromCharges(positives, now)
	assert.InDelta(t, 1.0, pp.Valence, 1e-9, "two same-age positives → Valence +1.0 regardless of age")
}

// TestFold_MixedAgeValenceSkew proves valence skews toward the more RECENT side
// when equal-weight opposing charges have different ages.
func TestFold_MixedAgeValenceSkew(t *testing.T) {
	now := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)

	recentPos := []*knowledgev1.Node{
		recencyCharge("p", "positive", 10, nanosDaysAgo(now, 0)),
		recencyCharge("n", "negative", 10, nanosDaysAgo(now, 90)),
	}
	assert.Positive(t, computePropertiesFromCharges(recentPos, now).Valence,
		"recent positive outweighs old negative → Valence > 0")

	recentNeg := []*knowledgev1.Node{
		recencyCharge("p", "positive", 10, nanosDaysAgo(now, 90)),
		recencyCharge("n", "negative", 10, nanosDaysAgo(now, 0)),
	}
	assert.Negative(t, computePropertiesFromCharges(recentNeg, now).Valence,
		"recent negative outweighs old positive → Valence < 0")
}

// TestFold_MagnitudeRecencyOrdering proves an all-OLD charge set yields strictly
// lower Magnitude than an all-RECENT set of identical stored weights/polarities.
func TestFold_MagnitudeRecencyOrdering(t *testing.T) {
	now := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	old := []*knowledgev1.Node{
		recencyCharge("a", "positive", 10, nanosDaysAgo(now, 90)),
		recencyCharge("b", "positive", 10, nanosDaysAgo(now, 90)),
	}
	recent := []*knowledgev1.Node{
		recencyCharge("a", "positive", 10, nanosDaysAgo(now, 0)),
		recencyCharge("b", "positive", 10, nanosDaysAgo(now, 0)),
	}
	mOld := computePropertiesFromCharges(old, now).Magnitude
	mRecent := computePropertiesFromCharges(recent, now).Magnitude
	assert.Less(t, mOld, mRecent, "older charges yield smaller Magnitude than recent ones")
}

// TestFold_ChargeCountRaw proves ChargeCount stays the RAW integer count
// (len(charges)) regardless of the charges' ages (the locked decision).
func TestFold_ChargeCountRaw(t *testing.T) {
	now := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	charges := []*knowledgev1.Node{
		recencyCharge("a", "positive", 10, nanosDaysAgo(now, 0)),
		recencyCharge("b", "negative", 10, nanosDaysAgo(now, 400)),
		recencyCharge("c", "positive", 10, 0), // zero timestamp
	}
	assert.Equal(t, 3, computePropertiesFromCharges(charges, now).ChargeCount,
		"ChargeCount is the raw integer count regardless of ages")
}

// TestFold_ZeroUpdatedAtHalfWeight proves a charge with no timestamp (UpdatedAt==0)
// contributes exactly 0.5×weight (the neutral scalar).
func TestFold_ZeroUpdatedAtHalfWeight(t *testing.T) {
	now := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	const w = 10.0
	props := computePropertiesFromCharges([]*knowledgev1.Node{
		recencyCharge("a", "positive", w, 0),
	}, now)
	assert.InDelta(t, 0.5*w, props.PositiveWeight, 1e-9, "zero UpdatedAt → 0.5×weight contribution")
	assert.InDelta(t, math.Log(1+0.5*w), props.Magnitude, 1e-9, "Magnitude = log(1 + 0.5×weight)")
}
