// SPDX-License-Identifier: Apache-2.0

package graphsel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestFamilyOf_CoversEveryEnumValue drives from the ENUM'S OWN DESCRIPTOR, not
// from a hand-written list of families.
//
// THAT IS THE WHOLE POINT OF THE ENUM. The family vocabulary used to live in a
// comment on a string field, and the comment rotted — it listed eight families
// and omitted three. A test checking a hand-written list against another
// hand-written list would have rotted the same way, because a family absent from
// both is absent from the comparison. Walking the generated descriptor means a
// family added to the proto and not mapped here turns this red on the next run.
func TestFamilyOf_CoversEveryEnumValue(t *testing.T) {
	values := knowledgev1.GraphFamily(0).Descriptor().Values()
	require.Greater(t, values.Len(), 1,
		"the descriptor exposed no family beyond UNSPECIFIED, so this test measured nothing")

	covered := 0
	for i := range values.Len() {
		v := values.Get(i)
		f := knowledgev1.GraphFamily(v.Number())
		if f == knowledgev1.GraphFamily_GRAPH_FAMILY_UNSPECIFIED {
			continue
		}
		gt, ok := typeOfFamily[f]
		assert.True(t, ok, "%s is declared on the wire but maps to no client graph type", v.Name())
		if !ok {
			continue
		}
		assert.Equal(t, f, FamilyOf(gt),
			"%s must round-trip through the client's graph type", v.Name())
		covered++
	}
	assert.Equal(t, values.Len()-1, covered,
		"every declared family except UNSPECIFIED must map to a client graph type")
}

// TestTypeOfSelector_PrefersFamilyAndRefusesUnknown pins the three reads a
// selector can produce, including the one that must NOT fall back.
func TestTypeOfSelector_PrefersFamilyAndRefusesUnknown(t *testing.T) {
	// The enum wins when set, even against a disagreeing string. A peer that
	// sets both is authoritative in its typed field.
	gt, ok := typeOfSelector(&knowledgev1.GraphSelector{
		Graph:  "practice",
		Family: knowledgev1.GraphFamily_GRAPH_FAMILY_CHECKS,
	})
	require.True(t, ok)
	assert.Equal(t, kgtypes.GraphChecks, gt, "a set family must win over the legacy string")

	// UNSPECIFIED means the writer predates the enum — read its string.
	gt, ok = typeOfSelector(&knowledgev1.GraphSelector{Graph: "practice"})
	require.True(t, ok)
	assert.Equal(t, kgtypes.GraphPractice, gt, "an unset family must read the legacy string")

	// SET BUT UNKNOWN must refuse rather than read the string. This is the
	// assertion that separates a version-skew read from an error-masking one: a
	// reader meeting a family it does not know must not quietly address whatever
	// the string happened to say.
	_, ok = typeOfSelector(&knowledgev1.GraphSelector{
		Graph:  "practice",
		Family: knowledgev1.GraphFamily(9999),
	})
	assert.False(t, ok, "an unrecognized family must refuse, not fall back to the string")
}

// TestGraphSelectorFor_PopulatesBothFields pins the transition invariant: a
// writer sends the typed family AND the legacy string, because client and server
// skew in practice and neither field alone reaches every peer.
func TestGraphSelectorFor_PopulatesBothFields(t *testing.T) {
	for _, gt := range []kgtypes.GraphType{
		kgtypes.GraphCode, kgtypes.GraphChecks, kgtypes.GraphPractice, kgtypes.GraphKnowledge,
	} {
		sel := GraphSelectorFor(gt, "x", false)
		assert.Equal(t, string(gt), sel.GetGraph(), "%s must still send the legacy string", gt)
		assert.NotEqual(t, knowledgev1.GraphFamily_GRAPH_FAMILY_UNSPECIFIED, sel.GetFamily(),
			"%s must also send the typed family", gt)
	}
}
