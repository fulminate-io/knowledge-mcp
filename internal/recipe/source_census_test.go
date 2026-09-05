// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// censusFixture loads a view whose two wrong-but-compiling census
// implementations are separable BY THEIR ANSWERS rather than by inspection.
//
// Its edges carry `CONTAINS`, `contains` and `RELATES-TO`: a census that folds
// edge-type case collapses the first two into one member and cannot produce the
// three-member vocabulary. Its metadata keys are stamped by exactly one node
// type each: a per-node-type census loses whichever key its chosen type does not
// stamp, while the graph-wide union keeps both.
func censusFixture(t *testing.T) *sourceView {
	t.Helper()
	f := &fakeGraphCaller{
		nodes: []*knowledgev1.Node{
			{Id: "s1", Type: "section", SymbolName: "Intro", Metadata: map[string]string{"pdf_page": "3"}},
			{Id: "s2", Type: "section", SymbolName: "Body"},
			{Id: "p1", Type: "paragraph", SymbolName: "para", Metadata: map[string]string{"web_url": "https://example/x"}},
			{Id: "p2", Type: "paragraph", SymbolName: "para two"},
		},
		edges: []*knowledgev1.Edge{
			svEdge("s1", "p1", "CONTAINS"),
			svEdge("s1", "p2", "contains"),
			svEdge("s1", "s2", "RELATES-TO"),
		},
	}
	sv, err := loadSourceView(context.Background(), f, kgtypes.GraphWebRaw, "src")
	require.NoError(t, err)
	return sv
}

func TestSourceCensus_ExactCasingAndGraphWideKeys(t *testing.T) {
	sv := censusFixture(t)
	c := sv.census()

	assert.Equal(t, []string{"paragraph", "section"}, c.nodeTypes,
		"every node type the graph carries, sorted")

	// THE CASING ASSERTION. A census that folded edge-type case would report two
	// members here, not three, and would merge two edge families a live web raw
	// graph carries at the same time.
	assert.Equal(t, []string{"CONTAINS", "RELATES-TO", "contains"}, c.edgeTypes,
		"edge types are collected verbatim, so the two casings stay distinct members")

	// THE GRAPH-WIDE ASSERTION. Each key is stamped by exactly one node type, so
	// a per-node-type census returns one of them and this fails.
	assert.Equal(t, []string{"pdf_page", "web_url"}, c.metaKeys,
		"metadata keys are the graph-wide union, not a per-node-type census")

	t.Run("suggest_names_the_casing_sibling", func(t *testing.T) {
		// `relates-to` is absent (the graph carries `RELATES-TO`) and sits 8 edits
		// away, well past the three-edit bound, so ONLY the case-insensitive pass
		// can produce this clause. A helper without that pass returns "".
		got := c.suggest(censusEdgeType, "relates-to")
		assert.Contains(t, got, `"RELATES-TO"`, "the casing sibling is named")
		assert.Contains(t, got, "casing", "and the difference is stated as casing")
		assert.Contains(t, got, "matched exactly", "with the rule that decides membership")
	})

	t.Run("suggest_names_every_casing_sibling", func(t *testing.T) {
		// THE SHAPE THE EXACT-CASING RULE EXISTS FOR. This fixture carries
		// `CONTAINS` and `contains` at once, which is what a web raw graph really
		// looks like: content containment writes one, the github root writes the
		// other, and live recipes traverse each. A suggester that returned on the
		// FIRST match would answer `Contains` with `CONTAINS` alone, purely
		// because the vocabulary sorts uppercase first, and an author who took
		// that and stopped would traverse the wrong family and get wrong rows
		// with no error.
		got := c.suggest(censusEdgeType, "Contains")
		assert.Contains(t, got, `"CONTAINS"`, "the uppercase family is named")
		assert.Contains(t, got, `"contains"`, "and so is the lowercase one")
		assert.Contains(t, got, "more than one casing", "and the clause says the graph is ambiguous here")
		assert.Contains(t, got, "matched exactly", "with the rule that decides membership")
	})

	t.Run("suggest_names_a_near_typo", func(t *testing.T) {
		assert.Equal(t, `did you mean "section"?`, c.suggest(censusNodeType, "sectionn"),
			"an ordinary typo is named by the edit-distance pass")
	})

	t.Run("suggest_invents_nothing", func(t *testing.T) {
		assert.Empty(t, c.suggest(censusMetaKey, "zzqqxxwwvv"),
			"nothing within the bound yields no clause rather than a far one")
	})

	t.Run("suggest_covers_every_vocabulary", func(t *testing.T) {
		// One pass-1 case per kind, so a suggester wired to only one vocabulary is
		// visible rather than hidden behind the edge-type case above.
		assert.Contains(t, c.suggest(censusNodeType, "SECTION"), `"section"`)
		assert.Contains(t, c.suggest(censusMetaKey, "PDF_PAGE"), `"pdf_page"`)
		assert.True(t, strings.HasPrefix(c.suggest(censusNodeType, "SECTION"), "did you mean "),
			"the clause reads as advice, not as a verdict")
	})
}

func TestSourceCensus_ComputedOncePerView(t *testing.T) {
	sv := censusFixture(t)
	require.Equal(t, 0, sv.censusWalks, "the census is not built until it is asked for")

	first := sv.census()
	for range 20 {
		assert.Same(t, first, sv.census(), "every later call returns the memoized census")
	}

	// THE COUNTER, NOT THE POINTER, IS THE MEASUREMENT. Pointer identity is what
	// sync.Once guarantees structurally; the walk count is what distinguishes one
	// walk of the graph from twenty.
	assert.Equal(t, 1, sv.censusWalks, "the graph is walked exactly once per view")
}
