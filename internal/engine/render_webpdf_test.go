// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_webpdf.go had no sibling test: RenderRawGraphResults, writeRawGraphHit
// and rawGraphPageSpan were named in no test file in the repo. The text half was
// reached transitively by the route test in package tools, but that test feeds a
// WEB-shaped corpus to both its web and pdf arms, so the pdf-only code — the
// page-span rendering — executed nowhere.

// rawHit builds a hit around a node, at a fixed score so the rendered header is
// predictable.
func rawHit(n *knowledgev1.Node, heading string) RawGraphHit {
	return RawGraphHit{
		Result:        SearchResult{Node: n, Score: 0.5, Graph: "web", GraphInstance: "doc"},
		ParentHeading: heading,
	}
}

// TestRawGraphPageSpan_CoversEveryBranch drives all four branches plus the web
// case. The keys are metadata the PDF emitter writes and the web emitter never
// does, which is why the empty case is the one every web node takes.
//
// The empty case is the known-positive control's mirror image: without the three
// non-empty cases beside it, a rawGraphPageSpan that returned "" unconditionally
// would satisfy it.
func TestRawGraphPageSpan_CoversEveryBranch(t *testing.T) {
	cases := []struct {
		name        string
		first, last string
		want        string
	}{
		{name: "neither_key_is_a_web_node", first: "", last: "", want: ""},
		{name: "last_absent_is_a_single_page", first: "4", last: "", want: "p. 4"},
		{name: "equal_bounds_is_a_single_page", first: "4", last: "4", want: "p. 4"},
		{name: "first_absent_is_a_single_page", first: "", last: "5", want: "p. 5"},
		{name: "distinct_bounds_is_a_span", first: "4", last: "5", want: "pp. 4-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md := map[string]string{}
			if tc.first != "" {
				md["page_first"] = tc.first
			}
			if tc.last != "" {
				md["page_last"] = tc.last
			}
			got := rawGraphPageSpan(&knowledgev1.Node{Id: "n", Type: "chunk", Metadata: md})
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestRenderRawGraphResults_PDFHitCarriesItsPageLocality is the pdf half the
// route test never reaches. A pdf chunk's only locality is its page span, so a
// renderer that dropped the span would leave the hit locatable by nothing.
func TestRenderRawGraphResults_PDFHitCarriesItsPageLocality(t *testing.T) {
	chunk := &knowledgev1.Node{
		Id: "c1", Type: "chunk", Source: "pdf-collect",
		Description: "this chunk discusses connection pooling at length",
		Metadata:    map[string]string{"page_first": "4", "page_last": "5"},
	}
	out := RenderRawGraphResults("pdf", "paper", "pooling", []RawGraphHit{rawHit(chunk, "")}, "BM25-only")
	require.NotEmpty(t, out.Content)
	body := out.Content[0].Text

	assert.Contains(t, body, "pp. 4-5", "a pdf chunk's page span must render as locality context")
	assert.Contains(t, body, "pdf/paper", "the context line must name the source graph and instance")
	assert.Contains(t, body, "this chunk discusses connection pooling",
		"this hit exercises the Description fallback rung and must render")
	assert.Contains(t, body, "ID: c1")

	// KNOWN NEGATIVE for the span assertion: the same renderer over a node
	// carrying no page keys must NOT invent one, so "pp." above is the metadata
	// speaking rather than the renderer always printing a span.
	web := &knowledgev1.Node{Id: "p1", Type: "paragraph", Content: "a web paragraph body"}
	webBody := RenderRawGraphResults("web", "doc", "pooling", []RawGraphHit{rawHit(web, "")}, "BM25-only").Content[0].Text
	assert.NotContains(t, webBody, "pp. ", "a web node carries no page keys and must render no span")
	assert.NotContains(t, webBody, "p. ", "a web node must render no single-page locality either")
}

// TestRenderRawGraphResults_LabelFallsBackThroughEveryRung pins the three-rung
// ladder in writeRawGraphHit. The rungs are ordered, so each case must SUPPRESS
// the rungs above it — a case that left SymbolName set could not tell rung two
// from rung one.
func TestRenderRawGraphResults_LabelFallsBackThroughEveryRung(t *testing.T) {
	cases := []struct {
		name    string
		node    *knowledgev1.Node
		heading string
		wantIn  string
		wantOut string
	}{
		{
			name:    "symbol_name_wins",
			node:    &knowledgev1.Node{Id: "s1", Type: "section", SymbolName: "Connection Pooling"},
			heading: "An Outer Heading",
			wantIn:  "Connection Pooling",
			// The heading still renders on the context line, so the check that
			// SymbolName won is that the HEADER line carries it.
			wantOut: "[section] An Outer Heading —",
		},
		{
			name:    "parent_heading_is_rung_two",
			node:    &knowledgev1.Node{Id: "p1", Type: "paragraph", Content: "body text"},
			heading: "Containing Section",
			wantIn:  "[paragraph] Containing Section —",
		},
		{
			// Rung three. A paragraph with neither a name nor a resolved parent
			// must still be labeled by something that is not a bare hex id.
			name:   "node_type_is_the_floor",
			node:   &knowledgev1.Node{Id: "deadbeef", Type: "paragraph", Content: "body text"},
			wantIn: "[paragraph] paragraph —",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := RenderRawGraphResults("web", "doc", "q",
				[]RawGraphHit{rawHit(tc.node, tc.heading)}, "BM25-only").Content[0].Text
			assert.Contains(t, body, tc.wantIn)
			if tc.wantOut != "" {
				assert.NotContains(t, body, tc.wantOut,
					"a lower rung won over a rung that should have taken precedence")
			}
		})
	}
}

// TestRenderRawGraphResults_ContextAndBodyComeFromTheNode covers the optional
// context segments and the Content-then-Description body ladder in one render,
// because they are assembled by one function over one node.
func TestRenderRawGraphResults_ContextAndBodyComeFromTheNode(t *testing.T) {
	t.Run("every_optional_segment_renders_when_present", func(t *testing.T) {
		n := &knowledgev1.Node{
			Id: "p1", Type: "paragraph", Content: "a bounded set of live connections",
			Metadata: map[string]string{
				"url":        "https://example.com/doc",
				"anchor":     "pooling",
				"page_first": "7",
			},
		}
		body := RenderRawGraphResults("web", "doc", "q",
			[]RawGraphHit{rawHit(n, "Connection Pooling")}, "BM25-only").Content[0].Text

		for _, want := range []string{
			"web/doc", "under: Connection Pooling",
			"https://example.com/doc", "#pooling", "p. 7",
		} {
			assert.Contains(t, body, want)
		}
	})

	// KNOWN NEGATIVE: the same node stripped of every optional key must render
	// the graph/name segment ALONE. Without this, the assertions above would be
	// satisfied by a renderer that emitted every label unconditionally.
	t.Run("absent_segments_are_absent_not_empty", func(t *testing.T) {
		n := &knowledgev1.Node{Id: "p1", Type: "paragraph", Content: "body"}
		body := RenderRawGraphResults("web", "doc", "q",
			[]RawGraphHit{rawHit(n, "")}, "BM25-only").Content[0].Text

		assert.NotContains(t, body, "under: ")
		// The anchor is checked as its RENDERED segment rather than as a bare
		// "#": the per-hit header is a markdown "### " heading, so a bare "#"
		// matches every render and would assert nothing.
		assert.NotContains(t, body, " | #", "an absent anchor must contribute no context segment")
		assert.NotContains(t, body, " | http", "an absent url must contribute no context segment")
		// The context line is exactly the graph/name segment with no separators.
		require.Contains(t, body, "\nweb/doc\n",
			"a node with no optional context must render the bare graph/name line")
	})

	// Description is the second rung and only reachable with Content empty,
	// which is exactly how the pdf emitter builds a chunk.
	t.Run("description_is_the_body_fallback", func(t *testing.T) {
		n := &knowledgev1.Node{Id: "c1", Type: "chunk", Description: "described body only"}
		body := RenderRawGraphResults("pdf", "paper", "q",
			[]RawGraphHit{rawHit(n, "")}, "BM25-only").Content[0].Text
		assert.Contains(t, body, "described body only")
	})

	// A node with neither must render no body line at all rather than a blank
	// one — the tell would be two consecutive newlines before the ID line.
	t.Run("no_body_renders_no_body_line", func(t *testing.T) {
		n := &knowledgev1.Node{Id: "s1", Type: "section", SymbolName: "Heading Only"}
		body := RenderRawGraphResults("web", "doc", "q",
			[]RawGraphHit{rawHit(n, "")}, "BM25-only").Content[0].Text
		assert.Contains(t, body, "web/doc\nID: s1")
	})
}

// TestRenderRawGraphResults_HeaderCountsAndFooterDiscloses covers the two things
// the function writes outside the per-hit loop. The footer discloses THE ARMS
// THAT ACTUALLY RAN, computed by the caller and passed in — it used to be a fixed
// "BM25-only" literal spelled in the renderer, which was true only while raw
// graphs were never embedded and became a falsehood the moment they were enrolled
// embed-only. So the footer legs below pin three separable things: that the
// passed label reaches the footer VERBATIM rather than a literal the renderer
// chose, that the disclosure survives a zero-result render, and that an empty
// label writes no footer at all rather than an empty one.
func TestRenderRawGraphResults_HeaderCountsAndFooterDiscloses(t *testing.T) {
	hits := []RawGraphHit{
		rawHit(&knowledgev1.Node{Id: "a", Type: "paragraph", Content: "one"}, ""),
		rawHit(&knowledgev1.Node{Id: "b", Type: "paragraph", Content: "two"}, ""),
	}
	// The label is deliberately NOT the old hardcoded literal. A renderer that
	// still spelled "BM25-only" itself would fail here, which is what makes this
	// leg evidence that the caller's label is what reaches the reader.
	body := RenderRawGraphResults("web", "doc", "connection pooling", hits, "vector+text").Content[0].Text

	assert.Contains(t, body, `## web/doc — 2 results for "connection pooling"`)
	assert.Contains(t, body, "_search mode: vector+text_")
	assert.NotContains(t, body, "BM25-only",
		"the retired hardcoded literal must not survive anywhere in the render")
	// Ranks are 1-based and in order.
	assert.Less(t, strings.Index(body, "### 1."), strings.Index(body, "### 2."))

	empty := RenderRawGraphResults("pdf", "paper", "nothing", nil, "BM25-only").Content[0].Text
	assert.Contains(t, empty, `## pdf/paper — 0 results for "nothing"`)
	assert.Contains(t, empty, "_search mode: BM25-only_",
		"the arm disclosure must render even when nothing matched")

	// KNOWN NEGATIVE for both legs above: the same renders with NO label must
	// carry no footer line at all. Without this, a renderer that appended
	// "_search mode: _" unconditionally would satisfy every Contains above.
	unlabeled := RenderRawGraphResults("web", "doc", "connection pooling", hits, "").Content[0].Text
	assert.Contains(t, unlabeled, `## web/doc — 2 results for "connection pooling"`,
		"an absent label must suppress only the footer, not the render")
	assert.NotContains(t, unlabeled, "_search mode:",
		"a caller with nothing to disclose must write no footer rather than an empty one")
}
