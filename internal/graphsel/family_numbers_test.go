// SPDX-License-Identifier: Apache-2.0

package graphsel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestTypeOfSelector_SurvivingFamilyNumbersHoldAndNineIsUnknown drives
// typeOfSelector BY RAW FAMILY NUMBER, which is what an older peer actually puts
// on the wire.
//
// WHY THE NUMBERS AND NOT THE CONSTANTS. Reading the enum through
// knowledgev1.GraphFamily_GRAPH_FAMILY_CODE and friends would agree with any
// renumbering by construction: renumber the whole enum and a constant-named
// table still passes, because both sides moved together. The wire does not move
// together — a peer built before the change sends the OLD number. Writing the
// numbers out by hand is the only way this test can disagree with the generated
// code, and disagreeing with it is the entire job.
//
// WHY 9 IS THE INTERESTING ONE. Family 9 was GRAPH_FAMILY_TRANSFORMERS until the
// family was removed; the proto now carries `reserved 9` so the number can never
// be handed to a new family. An older peer still addressing transformers puts 9
// on the wire, and this binary must report it UNKNOWN rather than resolve it —
// and specifically must not fall back to the legacy `graph` string, which is the
// silent-wrong-family degrade the enum exists to prevent. The selector below
// carries `graph: "transformers"` precisely so a string fallback would be
// visible: it would return the transformers graph type instead of ok=false.
//
// The ten-number table is an ENUMERATION, so it fails if the population changes
// size in either direction — a family added without a row here, or a family
// removed while its row stays.
func TestTypeOfSelector_SurvivingFamilyNumbersHoldAndNineIsUnknown(t *testing.T) {
	// Every surviving family, spelled as the NUMBER a peer transmits.
	surviving := []struct {
		number int32
		want   kgtypes.GraphType
	}{
		{1, kgtypes.GraphKnowledge},
		{2, kgtypes.GraphCode},
		{3, kgtypes.GraphCloud},
		{4, kgtypes.GraphCICD},
		{5, kgtypes.GraphPractice},
		{6, kgtypes.GraphLogs},
		{7, kgtypes.GraphWebRaw},
		{8, kgtypes.GraphPDFRaw},
		{10, kgtypes.GraphLinkage},
		{11, kgtypes.GraphChecks},
	}
	require.Len(t, surviving, 10, "the surviving family population is ten; a change here is a wire change")

	seen := make(map[kgtypes.GraphType]int32, len(surviving))
	for _, tc := range surviving {
		sel := &knowledgev1.GraphSelector{
			// The legacy string is deliberately WRONG for every row: if any of
			// these resolved through the string instead of the number, it would
			// answer "linkage" rather than the family the number names.
			Graph:  string(kgtypes.GraphLinkage),
			Family: knowledgev1.GraphFamily(tc.number),
		}
		gt, ok := typeOfSelector(sel)
		require.Truef(t, ok, "family number %d must resolve", tc.number)
		assert.Equalf(t, tc.want, gt, "family number %d must resolve to its own graph type", tc.number)

		prev, dup := seen[gt]
		assert.Falsef(t, dup, "graph type %q is claimed by two family numbers, %d and %d", gt, prev, tc.number)
		seen[gt] = tc.number
	}
	assert.Len(t, seen, 10, "ten distinct graph types; two numbers collapsing onto one is a renumbering defect")

	// The reserved number resolves to NOTHING, and does not read the string.
	retired := &knowledgev1.GraphSelector{
		Graph:  "transformers",
		Family: knowledgev1.GraphFamily(9),
	}
	gt, ok := typeOfSelector(retired)
	assert.False(t, ok, "family 9 is reserved and must not resolve")
	assert.Empty(t, gt, "family 9 must not fall back to the legacy graph string")

	// KNOWN-POSITIVE FOR THE STRING PATH. The two assertions above are an
	// absence; without this leg an implementation that never reads the string at
	// all would satisfy them, and "the string fallback is gone entirely" is a
	// different (and wrong) behaviour from "the string fallback is skipped for a
	// set-but-unknown family". UNSPECIFIED means the writer predates the enum, so
	// the SAME string field on the SAME reader must still be honored.
	legacy := &knowledgev1.GraphSelector{
		Graph:  "transformers",
		Family: knowledgev1.GraphFamily_GRAPH_FAMILY_UNSPECIFIED,
	}
	gt, ok = typeOfSelector(legacy)
	require.True(t, ok, "an UNSPECIFIED family still reads the legacy string")
	assert.Equal(t, kgtypes.GraphType("transformers"), gt,
		"the string path is alive: family 9 reporting unknown is a decision about the NUMBER, not a dead code path")
}
