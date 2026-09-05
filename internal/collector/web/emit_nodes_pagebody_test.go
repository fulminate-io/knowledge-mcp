// SPDX-License-Identifier: Apache-2.0

package web

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// TestEmit_PageIsItsChunksNotItsBody asserts the user's ruling — "pages are
// made up of chunks never put the whole page as content" — over a real crawl.
//
// IT NAMES BOTH TEXT FIELDS, NOT ONLY Content. The retired flatten wrote
// Description, so an implementation that merely MOVED the body from one field
// to the other would satisfy a Content-only assertion while shipping exactly
// the thing the ruling forbids.
//
// THE REACHABILITY CENSUS IS THE OTHER HALF, and it is what keeps the removal
// from being a data loss: each of seven distinct strings must be reachable on
// EXACTLY ONE node's Content. Zero means retiring the flatten lost content;
// two means a container is duplicating a chunk that already owns the text.
func TestEmit_PageIsItsChunksNotItsBody(t *testing.T) {
	body, err := os.ReadFile("testdata/raw_signals.html")
	require.NoError(t, err, "the shared raw-signals fixture must be readable")

	_, batch := serveCrawl(t, "page-is-its-chunks", string(body))
	nodes := batch.Nodes

	pages := nodesWhere(nodes, func(n *knowledgev1.Node) bool { return n.Type == "page" })
	require.Len(t, pages, 1, "the crawl must emit exactly one page node")
	page := pages[0]

	// THE PAGE LEGS.
	assert.Empty(t, page.Content, "a page node carries no body in Content")
	assert.Empty(t, page.Description, "a page node carries no body in Description either")
	// ...while keeping its identity, so "carries no body" is not satisfied by
	// a page node that carries nothing at all.
	assert.NotEmpty(t, page.SymbolName, "the page must still carry its title")
	assert.NotEmpty(t, page.Metadata["uri"], "the page must still carry its address")

	// THE SHAPE-STAMP LEG. This ticket changes what the collector emits in
	// three phases, and the constant's own doc requires a bump in the same
	// change. It is gated here rather than left to
	// TestEmitFromPage_StampsCollectorSchemaVersion, which compares the stamp
	// to strconv.Itoa(collectorSchemaVersion) and is therefore structurally
	// unable to see the value at all.
	assert.Equal(t, "3", page.Metadata["collector_schema_version"],
		"the web collectorSchemaVersion must be bumped to 3 in this change; a graph carrying 2 claims the "+
			"pre-change shape, in which a page root recorded no source_name and no seed_host")

	// THE REACHABILITY CENSUS — one node's Content each, no more and no less.
	for _, want := range []string{
		"The collector stamps a tag and a DOM depth",     // running prose
		"First list item carries its own text length",    // a list item
		"the code block carries a recognizable sentence", // code-block source
		"Responsibility",      // a table header
		"First data row cell", // a table cell
		"A quoted sentence retained verbatim by the walker",                   // blockquote text
		"Layout cell one wraps a paragraph of prose rather than tabular data", // layout-slot text
	} {
		carriers := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
			return strings.Contains(n.Content, want)
		})
		assert.Len(t, carriers, 1,
			"%q must be reachable on exactly one node's Content, found %d (%s)",
			want, len(carriers), describeNodes(carriers))
	}

	// THE CHROME PAIR. Both halves are required: "not in Content" alone is
	// satisfied by dropping the node, which the retention half of this ticket
	// forbids.
	const navText = "First Navigation Link"
	inContent := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return strings.Contains(n.Content, navText)
	})
	assert.Empty(t, inContent,
		"navigation text must reach zero Content nodes — every content composer reads Content (%s)", describeNodes(inContent))
	inDescription := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return strings.Contains(n.Description, navText)
	})
	assert.Len(t, inDescription, 1,
		"navigation text must be retained on exactly one node's Description")

	// THE LAYOUT-VERSUS-DATA TABLE PAIR. A layout table's cells are walked as
	// their own records, so its node carries the verdict and NO text; a data
	// table IS the chunk and carries its cells.
	layout := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return n.Type == "table" && n.Metadata["table_layout"] == "true"
	})
	require.Len(t, layout, 1, "the fixture must produce exactly one layout table")
	assert.Empty(t, layout[0].Content,
		"a layout table carries no text — its cells are walked into their own records, and writing them here duplicates them")
	assert.NotEmpty(t, layout[0].Metadata["table_row_count"],
		"a layout table still carries its measurements")

	data := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return n.Type == "table" && n.Metadata["table_layout"] == "false"
	})
	require.Len(t, data, 1, "the fixture must produce exactly one data table")
	assert.Contains(t, data[0].Content, "Second data row cell",
		"a data table carries its cells in Content — they are emitted as no other node")
	assert.NotEmpty(t, data[0].Metadata["rows"],
		"a data table keeps its structured rows metadata as well as its text")

	// THE SECTION LEGS. Every HEADED section carries its heading in Content as
	// well as SymbolName, because with the page body retired the heading text
	// exists on no other node.
	headed, rootSections := 0, 0
	for _, n := range nodes {
		if n.Type != "section" {
			continue
		}
		// The synthetic depth-0 root section is excluded BY NAME and COUNTED:
		// it was opened by no heading, so requiring Content on it would red
		// against correct work on any page whose prose opens above the first
		// heading.
		if n.Metadata["depth"] == "0" && n.Metadata["heading"] == "" {
			rootSections++
			continue
		}
		headed++
		assert.Equal(t, n.Metadata["heading"], n.Content,
			"section %q must carry its heading in Content", n.SymbolName)
		assert.Equal(t, n.SymbolName, n.Content,
			"section %q must carry the same text in Content and SymbolName", n.SymbolName)
	}
	assert.Equal(t, 1, rootSections,
		"the fixture opens with pre-heading prose, so exactly one synthetic root section must exist for the exclusion to be live")
	assert.GreaterOrEqual(t, headed, 2, "the census needs several headed sections, got %d", headed)
}

// TestCollect_NavStripOnlyHarvest_StillReportsChromeOnly drives the
// chrome-only invariant THROUGH A REAL CRAWL of a page whose only content is a
// strip of bare anchors in a div.
//
// IT MUST BE A REAL CRAWL. A hand-built composition cannot see the walker
// change that turns chrome into a paragraph node, and that change is exactly
// what would silently disarm the guard: the retained strip carries the
// `paragraph` type, so without the non-substantive subtraction this harvest
// counts one paragraph and reports plain success.
func TestCollect_NavStripOnlyHarvest_StillReportsChromeOnly(t *testing.T) {
	const navStripOnlyHTML = `<!doctype html>
<html>
<head><title>Nav Strip Only</title></head>
<body>
<div class="linkstrip"><a href="/one">First Navigation Link</a> <a href="/two">Second Navigation Link</a></div>
</body>
</html>
`
	comp := serveComposition(t, "nav-strip-only", navStripOnlyHTML)

	assert.Positive(t, comp.NodesByType["page"], "the crawl must emit a page node — this is what arms the page gate")
	// THE PREMISE, stated so the assertion below cannot pass for the wrong
	// reason: the strip IS retained, and it IS typed paragraph.
	assert.Equal(t, 1, comp.NodesByType["paragraph"],
		"the nav strip must be retained as a paragraph-typed node — that retention is what the subtraction exists to correct for")
	assert.Equal(t, 1, comp.NonSubstantiveNodes,
		"the retained strip must be counted as non-substantive by the collector that knows what it is")

	err := collector.CheckComposition("web", comp)
	require.Error(t, err, "a harvest whose only paragraph is a retained navigation strip must not report plain success")
	assert.Contains(t, err.Error(), "harvest captured nothing usable")
}

// describeNodes renders a compact id/type list for a census failure message,
// so a regression names the nodes that carried the text rather than only the
// count that was wrong.
func describeNodes(nodes []*knowledgev1.Node) string {
	if len(nodes) == 0 {
		return "no nodes"
	}
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, n.Id+"/"+n.Type)
	}
	return strings.Join(parts, ", ")
}
