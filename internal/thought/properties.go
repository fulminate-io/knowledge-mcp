// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"fmt"
	"math"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// ThoughtProperties holds computed properties derived from a thought's
// charge topology. None of these values are stored — they are always
// recomputed from charges. Mirrors the type that originally lived in
// domains/thought/thought.go; client-side reflective code computes them
// from prefetched charge slices (chargeMapForThoughts) instead of
// per-thought RPCs.
type ThoughtProperties struct {
	Valence        float64 // -1.0 to +1.0: balance of positive/negative charges
	Magnitude      float64 // 0 to ~10: logarithmic significance from total charge weight
	SelfTrust      float64 // how resistant this thought is to propagation influence
	Consistency    float64 // 0 to 1: how uniform the charge polarity is
	ChargeCount    int     // total number of charges
	PositiveWeight float64
	NegativeWeight float64
}

// baseSelfTrust is the minimum self-trust for any thought (even with no
// charges). Client-side only — the server binary declares no counterpart.
const baseSelfTrust = 0.1

// recencyTauDays is the time constant (in days) of the logarithmic read-time
// recency scalar: a charge exactly recencyTauDays old contributes at half
// weight (see recencyScalar). This is the first decay-tuning knob; making it a
// per-graph config surface is a later ticket — for now it is a named const.
const recencyTauDays = 30.0

// recencyScalar returns a read-time fade factor in (0, 1] for a charge's
// UpdatedAt (unix-nanos), so recent reasoning dominates and stale charges
// recede WITHOUT mutating the stored weight. Logarithmic decay:
//
//	scalar = 1 / (1 + log2(1 + ageDays/recencyTauDays))
//
// Curve (τ=30d): 0d→1.0, 30d→0.5, 90d→0.333, 210d→0.25, 450d→0.20. Mirrors
// the zero-timestamp→0.5 convention of computeTemporalScore
// (intercept_search_knowledge.go) but is logarithmic (not exponential half-life)
// and takes an explicit now for testability. A zero timestamp (IsZero) is
// neutral at 0.5; a future timestamp (clock skew → age<0) clamps to 1.0.
func recencyScalar(now time.Time, updatedAtNanos int64) float64 {
	if updatedAtNanos == 0 {
		return 0.5 // neutral for charges with no timestamp (IsZero).
	}
	ageDays := now.Sub(nanosToTime(updatedAtNanos)).Hours() / 24.0
	if ageDays < 0 {
		ageDays = 0 // future/clock-skew guard: scalar never exceeds 1.0.
	}
	return 1.0 / (1.0 + math.Log2(1.0+ageDays/recencyTauDays))
}

// computePropertiesFromCharges is the pure folder over charge nodes.
// Accepts a pre-fetched slice and derives the full ThoughtProperties.
// Mirrors the server-side helper of the same name; kept in sync as a
// pure local computation. The now param drives the read-time recency
// scalar applied to each charge's weight (see recencyScalar); charges are
// never mutated.
func computePropertiesFromCharges(charges []*knowledgev1.Node, now time.Time) ThoughtProperties {
	var props ThoughtProperties
	props.ChargeCount = len(charges)

	for _, c := range charges {
		w := parseFloat(kgtypes.Value(c, "weight")) * recencyScalar(now, c.GetUpdatedAt())
		switch kgtypes.Value(c, "polarity") {
		case "positive":
			props.PositiveWeight += w
		case "negative":
			props.NegativeWeight += w
		}
	}

	total := props.PositiveWeight + props.NegativeWeight
	if total > 0 {
		props.Valence = (props.PositiveWeight - props.NegativeWeight) / total
		props.Magnitude = math.Log(1 + total)
		maxSide := math.Max(props.PositiveWeight, props.NegativeWeight)
		minSide := math.Min(props.PositiveWeight, props.NegativeWeight)
		props.Consistency = 1 - (minSide / maxSide)
	}

	// ChargeCount stays the RAW integer count (len(charges)); recency never
	// touches it. Recency reaches SelfTrust ONLY through the recency-weighted
	// Consistency term above — never through the count.
	props.SelfTrust = baseSelfTrust + props.Consistency*math.Log(1+float64(props.ChargeCount))
	return props
}

// ComputePropertiesFromCharges is the exported alias for callers
// outside the package (e.g. the moved personality_test.go was the only
// user; kept exported per Phase 3 step body for symmetry). now drives the
// read-time recency scalar (see computePropertiesFromCharges).
func ComputePropertiesFromCharges(charges []*knowledgev1.Node, now time.Time) ThoughtProperties {
	return computePropertiesFromCharges(charges, now)
}

// parseFloat parses a string to float64, returning 0 on error. Mirrors
// the server-side helper.
func parseFloat(s string) float64 {
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0
	}
	return f
}
