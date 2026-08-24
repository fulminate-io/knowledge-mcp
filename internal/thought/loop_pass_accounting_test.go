// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestPassAccounting_RpcsIssuedIsDerivedFromTheReadInventory pins that the
// rpcs_issued accounting line is DERIVED from the declared read inventory rather
// than restated as a literal.
//
// The line it replaces was a static string that named two reads the collapse had
// already deleted and omitted four that a pass actually issues. Being a literal, it
// could not drift back into truth — the whole point of this gate is that the line and
// the reads now read the same values.
func TestPassAccounting_RpcsIssuedIsDerivedFromTheReadInventory(t *testing.T) {
	// LEG 1: the count is PLAN-MANDATED and locked, not tree-derived. Adding or
	// removing an unconditional edge read without updating the inventory must break
	// this — that failure IS the anti-rot signal, so it is an equality and not a bound.
	require.Len(t, perPassUnconditionalEdgeReads, 4,
		"the unconditional per-pass edge-read inventory holds exactly four entries; "+
			"a read added or removed without updating it must fail here")

	rendered := renderRPCsIssued(perPassUnconditionalEdgeReads)

	// LEG 2: every entry's label appears in the rendered line. Both sides read the
	// same value, so a read whose type set changes cannot leave the line stale.
	for _, r := range perPassUnconditionalEdgeReads {
		assert.Contains(t, rendered, edgeTypesLabel(r.types),
			"the rendered line must carry %q's type label", r.name)
	}

	// LEG 3 — THE DERIVATION LEG, and the reason legs 1 and 2 are not enough. Both are
	// satisfiable by a function that ignores its argument and returns a hardcoded
	// string, which is precisely the defect being fixed. A synthetic inventory whose
	// type set appears nowhere in the real one must drive the output.
	synthetic := []perPassEdgeRead{{
		name:  "synthetic-probe",
		types: []kgtypes.EdgeType{kgtypes.EdgeSupports, kgtypes.EdgeInformedBy},
	}}
	syntheticOut := renderRPCsIssued(synthetic)
	assert.Contains(t, syntheticOut, "synthetic-probe",
		"renderRPCsIssued must render the inventory it is GIVEN")
	assert.Contains(t, syntheticOut, edgeTypesLabel(synthetic[0].types),
		"and that inventory's type label")
	for _, r := range perPassUnconditionalEdgeReads {
		assert.NotContains(t, syntheticOut, r.name,
			"a synthetic inventory must NOT render the real inventory's %q — that would "+
				"mean the renderer ignores its argument", r.name)
	}

	// A nil type set means "every type" at the wire. edgeTypesLabel renders it as the
	// empty string, so the renderer must substitute a readable token rather than emit
	// a bare trailing '='.
	require.Len(t, perPassConditionalEdgeReads, 1,
		"one conditional read is inventoried: leaf-provenance")
	conditional := renderRPCsIssued(perPassConditionalEdgeReads)
	assert.Contains(t, conditional, "<every-type>",
		"a nil type filter renders as <every-type>, never as a bare trailing '='")
	assert.False(t, strings.HasSuffix(conditional, "="),
		"the rendered line must not end in a bare '='")
}

// TestPassAccounting_ConditionalTermsRenderUnderConditionalReads is the
// DISCRIMINATING gate separating the two accounting fields.
//
// It exists because every other criterion on this step passed at a snapshot where
// node-browse and the diffed writeback were appended to rpcs_issued as a hardcoded
// suffix — asserted as UNCONDITIONAL when a warm pass issues NEITHER. The suffix was
// a literal, so no derivation leg could see it, and the field greps matched the
// unchanged prefix. Only an assertion about WHICH FIELD carries them discriminates.
func TestPassAccounting_ConditionalTermsRenderUnderConditionalReads(t *testing.T) {
	require.NotEmpty(t, perPassConditionalTerms,
		"the non-edge conditional terms are inventoried, not inlined into the line")

	unconditional := renderRPCsIssued(perPassUnconditionalEdgeReads)
	conditional := renderRPCsIssued(perPassConditionalEdgeReads) +
		" " + renderConditionalTerms(perPassConditionalTerms)

	for _, term := range perPassConditionalTerms {
		assert.Contains(t, conditional, term,
			"%q is conditional and must render under conditional_reads", term)
		assert.NotContains(t, unconditional, term,
			"%q must NOT appear in rpcs_issued — a warm pass issues neither the node "+
				"browse (the resident snapshot serves it) nor the diffed writeback (it is "+
				"gated on a changed label), so listing them as unconditional is false", term)
	}

	// The retired literal's own words must be gone from BOTH fields, not merely moved.
	for _, retired := range []string{"adjacency-edges", "bulk-kgcontains"} {
		assert.NotContains(t, unconditional, retired)
		assert.NotContains(t, conditional, retired)
	}

	// KNOWN-POSITIVE: the unconditional field is not empty, so the NotContains legs
	// above are not passing merely because there is nothing to look at.
	assert.Contains(t, unconditional, "unified-pivot",
		"the unconditional field still carries the reads that ARE unconditional")
}
