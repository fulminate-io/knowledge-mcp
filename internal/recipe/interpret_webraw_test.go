// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// interpret_webraw_test.go holds the consumer-side gates for the web
// collector's true-raw shape. They live in this package rather than in the
// collector because the interpreter's source view is package-private, and
// because the property under test is what a CONSUMER actually gets — which no
// assertion inside the collector can observe.

// webRawEdge builds a CONTAINS edge the way the web collector stamps one:
// UPPERCASE type, position on Evidence.
//
// THE CASE IS LOAD-BEARING. childEdgesOrdered compares the edge type by
// equality, so a fixture spelling it "contains" would compose an empty body
// and this file's assertions would pass or fail for a reason that has nothing
// to do with the collector.
func webRawEdge(from, to, pos string) *knowledgev1.Edge {
	return &knowledgev1.Edge{
		FromId: from, ToId: to, Type: "CONTAINS",
		Evidence: `{"position":"` + pos + `"}`,
	}
}

// TestInterpret_MigratedWebRecipe_ComposesChunksAndExcludesChrome runs the
// MIGRATED body form through the real Parse and Interpret over a node set
// shaped exactly like the collector's output.
//
// THIS IS THE GATE ON THE RETIREMENT'S CONSUMER SIDE. With the page-level
// flatten gone, subtree_concat over CONTAINS/content IS the page-body path, so
// what a recipe composes is the only remaining answer to "what does a consumer
// get for this page". It must gather the chunks — and it must NOT gather the
// navigation strip, whose text is deliberately kept out of Content for exactly
// this reason.
func TestInterpret_MigratedWebRecipe_ComposesChunksAndExcludesChrome(t *testing.T) {
	const (
		prose   = "A message router consumes a message and republishes it to a channel."
		code    = "router.route(msg)"
		heading = "Problem"
		nav     = "Home Docs Patterns Index"
	)

	page := &knowledgev1.Node{Id: "p1", Type: "page", SymbolName: "Message Router"}
	sec := &knowledgev1.Node{Id: "s1", Type: "section", SymbolName: heading, Content: heading}
	para := &knowledgev1.Node{Id: "c1", Type: "paragraph", Content: prose}
	block := &knowledgev1.Node{Id: "c2", Type: "code_block", Content: code}
	// The retained navigation strip: its text is on Description, and Content is
	// empty. That is the whole mechanism this test exists to confirm.
	strip := &knowledgev1.Node{
		Id: "c3", Type: "paragraph", Description: nav,
		Metadata: map[string]string{"links_only": "true"},
	}

	sv := renderView(
		[]*knowledgev1.Node{page, sec, para, block, strip},
		[]*knowledgev1.Edge{
			webRawEdge("p1", "s1", "0"),
			webRawEdge("s1", "c1", "0"),
			webRawEdge("s1", "c2", "1"),
			webRawEdge("s1", "c3", "2"),
		},
	)

	body := `select page
emit pattern {
    type := "pattern"
    name := page.symbol_name
    description := subtree_concat("CONTAINS", "content", "\n\n", "6")
}`
	result, err := Interpret(context.Background(), parseOrFatal(t, body), sv, recipeTargetSpec(), "web-raw", Options{})
	require.NoError(t, err)
	require.Len(t, result.Nodes, 1, "one pattern per page row")

	composed := result.Nodes[0].Description
	assert.Contains(t, composed, prose, "the composed body must carry the page's prose chunk")
	assert.Contains(t, composed, code, "the composed body must carry the page's code chunk")
	assert.Contains(t, composed, heading, "the composed body must carry the section heading, which exists on no other node")
	// THE EXCLUSION IS THE POINT. A navigation strip left in Content would
	// contaminate every composed body, one package away from any collector
	// test that could see it.
	assert.NotContains(t, composed, nav,
		"the composed body must not carry navigation text — chrome is a signal, not a chunk")
}

// TestWhereTree_DescendantLeafExpressesHasAChunkChild probes the replacement
// for the retired duplicate-title filter.
//
// The retired form asked whether the page had a body of its own. With no page
// body there is nothing to ask that of, so the question becomes structural:
// does this page HAVE A CONTENT-BEARING CHUNK CHILD?
//
// THREE PAGES, AND THE THIRD IS WHAT MAKES THE LEAF'S STRICTNESS LOAD-BEARING.
// A childless redirect stub is rejected by almost any descendant leaf, so a
// fixture of two pages passes even with the guard weakened to
// {"exists": {"of": "node.type"}} — every child has a type. The third page has
// exactly one child and it is a RETAINED NAVIGATION STRIP: a links_only node
// whose text lives on Description, so its Content is empty. Only the strict
// node.content form excludes it, which is the property the guard has to have —
// a page whose sole child is chrome has no body to compose and must not
// overwrite a real article by StableID.
func TestWhereTree_DescendantLeafExpressesHasAChunkChild(t *testing.T) {
	const title = "Scatter-Gather"

	real := &knowledgev1.Node{Id: "p1", Type: "page", SymbolName: title}
	chunk := &knowledgev1.Node{Id: "c1", Type: "paragraph", Content: "The scatter-gather broadcasts a request to multiple recipients."}
	stub := &knowledgev1.Node{Id: "p2", Type: "page", SymbolName: title}
	chromeOnly := &knowledgev1.Node{Id: "p3", Type: "page", SymbolName: "Splitter"}
	strip := &knowledgev1.Node{
		Id: "c3", Type: "paragraph", Description: "Home Docs Patterns Index",
		Metadata: map[string]string{"links_only": "true"},
	}

	sv := renderView(
		[]*knowledgev1.Node{real, chunk, stub, chromeOnly, strip},
		[]*knowledgev1.Edge{
			webRawEdge("p1", "c1", "0"),
			webRawEdge("p3", "c3", "0"),
		},
	)

	body := `select page where {"descendant": {"edge": "CONTAINS", "where": {"exists": {"of": "node.content"}}}}
emit pattern {
    type := "pattern"
    name := page.symbol_name
}`
	result, err := Interpret(context.Background(), parseOrFatal(t, body), sv, recipeTargetSpec(), "web-raw", Options{})
	require.NoError(t, err)

	names := make([]string, 0, len(result.Nodes))
	for _, n := range result.Nodes {
		names = append(names, n.SymbolName)
	}
	assert.Equal(t, []string{title}, names,
		"only the page with a CONTENT-BEARING chunk child may survive: the childless stub and the chrome-only page both have no body to compose")
}

// TestWhereTree_NegatedMatchIsNotTheLiteralTranslation is the leg that catches
// a SILENT INVERSION rather than a refusal.
//
// Two of the recipes' predicate sites are NEGATED matches — exclusions. A
// literal translation into {"matches": ...} PARSES CLEANLY and selects exactly
// the pages the exclusion exists to drop, so nothing fails and the recipe
// quietly emits the front matter instead of the patterns.
//
// IT ASSERTS ON THE ROW NAMES, NOT A COUNT. Both forms emit exactly one row
// here; only the names tell an inverted predicate from a faithful one, which
// is why a count-based check would have been blind to it.
func TestWhereTree_NegatedMatchIsNotTheLiteralTranslation(t *testing.T) {
	frontMatter := &knowledgev1.Node{Id: "p1", Type: "page", SymbolName: "Table of Contents"}
	pattern := &knowledgev1.Node{Id: "p2", Type: "page", SymbolName: "Scatter-Gather"}
	sv := renderView([]*knowledgev1.Node{frontMatter, pattern}, nil)

	emitted := func(t *testing.T, where string) []string {
		t.Helper()
		body := "select page where " + where + `
emit pattern {
    type := "pattern"
    name := page.symbol_name
}`
		result, err := Interpret(context.Background(), parseOrFatal(t, body), sv, recipeTargetSpec(), "web-raw", Options{})
		require.NoError(t, err)
		names := make([]string, 0, len(result.Nodes))
		for _, n := range result.Nodes {
			names = append(names, n.SymbolName)
		}
		return names
	}

	const frontMatterRegex = `{"matches": {"of": "page.symbol_name", "regex": "^Table of Contents$"}}`

	// THE LITERAL TRANSLATION selects the page the exclusion drops.
	assert.Equal(t, []string{"Table of Contents"}, emitted(t, frontMatterRegex),
		"the literal matches form selects the front-matter page — this is the inversion, reproduced")

	// THE FAITHFUL TRANSLATION negates it and selects the pattern page.
	assert.Equal(t, []string{"Scatter-Gather"}, emitted(t, `{"not": `+frontMatterRegex+`}`),
		"the negated form is the faithful translation of an exclusion predicate")
}
