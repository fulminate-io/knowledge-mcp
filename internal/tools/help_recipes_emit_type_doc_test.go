// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// help_recipes_emit_type_doc_test.go pins the two refusal classes the emit-type
// rule adds to help("recipes"), and pins each to the BEHAVIOR it claims rather
// than to its own words.
//
// WHY A SUBSTRING CENSUS ALONE WOULD BE WORTH LITTLE. The help text is what a
// caller reads instead of the source, so the failure that matters is a
// documented refusal that no longer happens — and a test that only asserted the
// sentence is present would stay green through exactly that. Each row below
// therefore asserts the line AND drives the refusal it describes, in the same
// subtest: the doc and the behavior go red together or not at all.
//
// THE TWO LISTS ARE DIFFERENT PLACES ON PURPOSE. A computed emit type is refused
// at PARSE time, from the recipe text alone; an unenrolled emit type is refused
// BEFORE THE WALK, and needs the whole rule list. A line filed under the wrong
// heading tells a reader to look for the wrong kind of message.
func TestHelpRecipes_EmitTypeRefusalsAreDocumentedAndReal(t *testing.T) {
	parseTimeSection, beforeWalkSection := helpRecipesRefusalSections(t)

	t.Run("a non-literal emit type is documented under parse time, and is refused there", func(t *testing.T) {
		assert.Contains(t, parseTimeSection, "emit whose node type is not a bare identifier",
			"the parse-time list names the class")
		_, err := recipe.Parse([]byte("select section\nemit $t {\n    name := node.symbol_name\n}"))
		require.Error(t, err, "and the class is real: a computed emit type does not parse")
		assert.Contains(t, err.Error(), "expected target node type after 'emit'")
	})

	t.Run("an unenrolled emit type is documented before the walk, and is refused there", func(t *testing.T) {
		assert.Contains(t, beforeWalkSection, "emit node type the combined practice graph does not enroll",
			"the before-the-walk list names the class")
		assert.Contains(t, beforeWalkSection, "extract included",
			"and says the extract mode is refused too, which is the half a reader would otherwise assume the other way")

		c := newLandingCaller()
		deps := &recipeDeps{sink: &recipeCaptureSink{}, gc: c}
		handled, res := InterceptCollect(opCtx(), deps,
			collectParamsFor(t, unenrolledEmitBody, map[string]any{"extract": true}))
		require.True(t, handled)
		require.True(t, res.IsError, "and the class is real, in the mode the line calls out")
	})
}

// helpRecipesRefusalSections splits the help's refusal chapter into its two
// lists, so a row cannot pass by finding its line under the wrong heading.
//
// It FAILS rather than returning empty strings when a heading is missing: two
// empty sections would make every Contains assertion above fail for a reason
// that has nothing to do with the class under test, and a reader chasing that
// would look in the wrong place.
func helpRecipesRefusalSections(t *testing.T) (parseTime, beforeWalk string) {
	t.Helper()
	const (
		parseHeading = "Refused at parse time"
		walkHeading  = "Refused before the walk"
		closing      = "THESE ARE REPORTED TOGETHER"
	)
	parseAt := strings.Index(helpRecipes, parseHeading)
	walkAt := strings.Index(helpRecipes, walkHeading)
	closeAt := strings.Index(helpRecipes, closing)
	require.Positive(t, parseAt, "the help lost its %q heading", parseHeading)
	require.Greater(t, walkAt, parseAt, "the help lost its %q heading, or the two lists swapped order", walkHeading)
	require.Greater(t, closeAt, walkAt, "the help lost the closing paragraph that bounds the second list")
	return helpRecipes[parseAt:walkAt], helpRecipes[walkAt:closeAt]
}
