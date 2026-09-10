// SPDX-License-Identifier: Apache-2.0

// run_recipe_landing_test.go — what a LANDING run changes inside this package,
// which is exactly two things: the run is admitted without extract, and the
// emitted ids hash under the CALLER'S target instead of the extract sentinel.
//
// EVERYTHING ELSE A LANDING DOES IS THE COLLECT LAYER'S. This package still
// writes nothing on either mode, so the assertions below are about the emitted
// set and its ids, never about a graph.

package recipe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunRecipe_LandingHashesIdsUnderTheLandTarget is the id-pinning half of
// requirement 4, and it is the observer for the silent failure mode the whole
// versioning branch rests on.
//
// WHY IT MATTERS RATHER THAN BEING A SPELLING TEST. The collision read looks each
// emitted id up in the combined practice graph to decide base-versus-twin. If the
// ids were still hashed under the extract sentinel, every lookup would miss, the
// twin branch would never fire, and the landing would write a fresh node under an
// id no resident holds — on an add-not-upsert path, which overwrites the row a
// human had edited. Nothing errors and nothing refuses; the run reports success.
// So the id's FIRST COMPONENT is asserted directly, against an externally
// computed expectation rather than against the code's own answer.
func TestRunRecipe_LandingHashesIdsUnderTheLandTarget(t *testing.T) {
	const slug = "hohpe-eip"
	body := `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
}`

	res, err := runLanding(t, twoSectionCaller(), body)
	require.NoError(t, err)
	require.Len(t, res.Nodes, 2, "one node per section row")

	// THE EXTERNAL EXPECTATION: the id a caller would compute for itself, knowing
	// only the target the landing was pointed at.
	wantRouter := StableID("practice/default", slug, "pattern", "Message Router")
	wantChannel := StableID("practice/default", slug, "pattern", "Message Channel")

	got := map[string]bool{}
	for _, n := range res.Nodes {
		got[n.GetId()] = true
	}
	assert.True(t, got[wantRouter], "the emitted id must hash under the LAND target, not the extract sentinel")
	assert.True(t, got[wantChannel], "for every emitted row, not only the first")

	// AND THE NEGATIVE HALF, which is what makes the two assertions above a pin
	// rather than a coincidence: the sentinel ids the SAME body produces under an
	// extract run must be absent from the landing's set.
	sentinel := TargetKey(TargetSpec{GraphType: extractSentinelGraphType, Name: slug})
	assert.False(t, got[StableID(sentinel, slug, "pattern", "Message Router")],
		"no emitted id may still carry the extract sentinel's key: those ids resolve to nothing in the target graph")
}

// TestRunRecipe_LandingStillWritesNothingItself pins the layering: RunRecipe
// composes, the collect layer writes.
//
// A LANDING RUN THAT ISSUED ITS OWN MUTATION would satisfy every id assertion
// above while putting the write behind the refusals the collect layer performs —
// which is the ordering requirement 8 exists to fix. The mutation counter on the
// fake is what sees it.
func TestRunRecipe_LandingStillWritesNothingItself(t *testing.T) {
	caller := twoSectionCaller()
	body := `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
}`

	res, err := runLanding(t, caller, body)
	require.NoError(t, err)

	require.NotEmpty(t, res.Nodes,
		"THE KNOWN-POSITIVE: the run really did emit, so the zero below is a decision rather than a no-op")
	assert.Empty(t, caller.mutations,
		"RunRecipe issues no mutation on a landing run: the collect layer owns the write, after its refusals")
}

// twoSectionCaller serves a two-section web source graph to a RunRecipe call.
func twoSectionCaller() *routingCaller {
	return sourceOnlyCaller(section("s1", "Message Router"), section("s2", "Message Channel"))
}
