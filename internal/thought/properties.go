// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"fmt"
	"math"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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
// charges). Mirrors the server-side constant in domains/thought.
const baseSelfTrust = 0.1

// computePropertiesFromCharges is the pure folder over charge nodes.
// Accepts a pre-fetched slice and derives the full ThoughtProperties.
// Mirrors the server-side helper of the same name; kept in sync as a
// pure local computation.
func computePropertiesFromCharges(charges []*knowledgev1.Node) ThoughtProperties {
	var props ThoughtProperties
	props.ChargeCount = len(charges)

	for _, c := range charges {
		w := parseFloat(kgtypes.Value(c, "weight"))
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

	props.SelfTrust = baseSelfTrust + props.Consistency*math.Log(1+float64(props.ChargeCount))
	return props
}

// ComputePropertiesFromCharges is the exported alias for callers
// outside the package (e.g. the moved personality_test.go was the only
// user; kept exported per Phase 3 step body for symmetry).
func ComputePropertiesFromCharges(charges []*knowledgev1.Node) ThoughtProperties {
	return computePropertiesFromCharges(charges)
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
