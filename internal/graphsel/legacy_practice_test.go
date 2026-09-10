// SPDX-License-Identifier: Apache-2.0

package graphsel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestRefuseNonCanonicalPracticeLanguage_RefusesWithoutRewriting pins the fence's
// two properties: it admits a name that is already its own slug, and it REFUSES
// one that is not, naming both spellings. A version that returned the slug — or
// that admitted the display spelling — would be the silent coercion this rule
// exists to prevent, and neither shape is distinguishable from the other by a
// caller that only checks for a nil error.
func TestRefuseNonCanonicalPracticeLanguage_RefusesWithoutRewriting(t *testing.T) {
	for _, canonical := range []string{"go", "design-patterns", "javascript-typescript", "cplusplus", "go-cox-buday-v4"} {
		require.NoError(t, RefuseNonCanonicalPracticeLanguage("sync push", canonical),
			"%q is its own slug and names a graph the read path opens", canonical)
	}

	for _, tc := range []struct{ in, want string }{
		{"Design Patterns", "design-patterns"},
		{"JavaScript/TypeScript", "javascript-typescript"},
		{"C++", "cplusplus"},
		{"Go", "go"},
	} {
		err := RefuseNonCanonicalPracticeLanguage("sync pull", tc.in)
		require.Error(t, err, "%q is not canonical", tc.in)
		assert.Contains(t, err.Error(), tc.in, "the refusal names the offending spelling")
		assert.Contains(t, err.Error(), tc.want, "and the spelling that would have worked")
		assert.Contains(t, err.Error(), "sync pull", "and the operation that refused")
	}
}

// TestRefuseNonCanonicalPracticeLanguage_EmptyIsTheCombinedGraph pins the arm
// every non-practice sync and every combined-graph sync takes. An empty name is
// not a bad name: it is the absence of a legacy selector, and refusing it would
// fail every knowledge, code and combined-practice sync on this path.
func TestRefuseNonCanonicalPracticeLanguage_EmptyIsTheCombinedGraph(t *testing.T) {
	require.NoError(t, RefuseNonCanonicalPracticeLanguage("sync push", ""))
}

// TestLegacyPracticeSelector_CarriesTheNameOnLanguage pins the field the legacy
// name rides. The server's practice selector policy consumes `language` and
// carries no instance field, so a set `name` is refused before routing while a
// set `language` reaches the resolver and opens the graph it names. The family
// enum rides along like every other selector this package builds.
func TestLegacyPracticeSelector_CarriesTheNameOnLanguage(t *testing.T) {
	sel := LegacyPracticeSelector("go")
	assert.Equal(t, "practice", sel.GetGraph())
	assert.Equal(t, "go", sel.GetLanguage())
	assert.Empty(t, sel.GetName(), "a set name on a practice selector is refused server-side")
	assert.Empty(t, sel.GetRepo())
	assert.Empty(t, sel.GetAccount())
	assert.Equal(t, FamilyOf(kgtypes.GraphPractice), sel.GetFamily())

	// The contrast that makes the divergence legible: the general builder cannot
	// express this, because practice is in its FieldNone arm.
	general := GraphSelectorFor(kgtypes.GraphPractice, "go", false)
	assert.Empty(t, general.GetLanguage(),
		"GraphSelectorFor drops a practice name — correct for the combined graph, wrong for a legacy one")
	assert.Empty(t, general.GetName())
}
