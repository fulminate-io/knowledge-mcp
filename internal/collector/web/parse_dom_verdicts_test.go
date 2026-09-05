// SPDX-License-Identifier: Apache-2.0

package web

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestCollect_ClassifierVerdictsCarryTheirInputs runs ONE crawl of the shared
// fixture and asserts that each of the four judgements emits the inputs that
// produced it rather than a bare verdict.
//
// EVERY LEG CARRIES ITS OWN PROPERTY PAIR, so a stub that always answers one
// way fails: an implementation that stamps every section as heuristic, or
// every table as layout, or every run as links_only, satisfies the positive
// half of its leg and reds on the negative half.
func TestCollect_ClassifierVerdictsCarryTheirInputs(t *testing.T) {
	body, err := os.ReadFile("testdata/raw_signals.html")
	require.NoError(t, err, "the shared raw-signals fixture must be readable")

	_, batch := serveCrawl(t, "classifier-verdicts", string(body))
	nodes := batch.Nodes

	// --- LEG 1: the presentation-heading heuristic --------------------------
	//
	// The promoted markers must say WHICH arm promoted them and carry the four
	// measurements the pre-pass computed and previously discarded.
	promoted := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return n.Type == "section" && n.Metadata["heading_source"] == "heuristic"
	})
	require.Len(t, promoted, 2,
		"the fixture's two-member marker series must be promoted by the heuristic arm")

	for _, n := range promoted {
		md := n.Metadata
		assert.Equal(t, "marker", md["heuristic_class_group"],
			"section %q must name the normalized class group that grouped it", n.SymbolName)
		assert.Equal(t, "2", md["heuristic_group_size"],
			"section %q must report the size the repetition gate actually saw", n.SymbolName)

		textLen, err := strconv.Atoi(md["heuristic_text_length"])
		require.NoError(t, err, "section %q carries an unparseable heuristic_text_length %q", n.SymbolName, md["heuristic_text_length"])
		assert.Equal(t, len([]rune(n.SymbolName)), textLen,
			"section %q reports a text length that is not its own heading's rune count", n.SymbolName)

		// The baseline is checked as a RELATION, not against a literal: it is
		// the median that ADMITTED this marker, so it must clear the
		// calibration ratio by the margin the fixture was built to give.
		median, err := strconv.ParseFloat(md["heuristic_sibling_median"], 64)
		require.NoError(t, err, "section %q carries an unparseable heuristic_sibling_median %q", n.SymbolName, md["heuristic_sibling_median"])
		assert.NotContains(t, md["heuristic_sibling_median"], "e",
			"the sibling median must be a plain decimal number, got %q", md["heuristic_sibling_median"])
		assert.Greater(t, median, float64(textLen)*2,
			"section %q reports a sibling median that would not have admitted it", n.SymbolName)
	}

	// PAIR — the authoritative arms name themselves and carry NO calibration,
	// because they were never calibrated against anything.
	native := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return n.Type == "section" && (n.SymbolName == "Raw Signals" || n.SymbolName == "Tables")
	})
	require.Len(t, native, 2, "the fixture's native h1 and h2 must both open sections")
	for _, n := range native {
		assert.Equal(t, "native", n.Metadata["heading_source"],
			"section %q came from a native heading and must say so", n.SymbolName)
		for _, key := range []string{
			"heuristic_class_group", "heuristic_text_length",
			"heuristic_sibling_median", "heuristic_group_size",
		} {
			assert.NotContains(t, n.Metadata, key,
				"native section %q carries %s — it describes a calibration that never happened", n.SymbolName, key)
		}
	}

	// --- LEG 2: the layout-table verdict ------------------------------------
	//
	// A verdict that DELETED the node, and one that kept the node but stopped
	// walking its subtree, both fail here.
	layout := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return n.Type == "table" && n.Metadata["table_role"] == "presentation"
	})
	require.Len(t, layout, 1,
		"the role=presentation table must still reach the graph as a table node")
	assert.Equal(t, "true", layout[0].Metadata["table_layout"],
		"the role=presentation table must carry the layout verdict")
	for _, key := range []string{
		"table_header_signal", "table_row_count", "table_uniform", "table_cell_has_block",
	} {
		assert.Contains(t, layout[0].Metadata, key,
			"the layout verdict must carry %s — the measurement is the contract", key)
	}
	// ...and its wrapped content is still walked into its own records.
	wrapped := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return n.Type == "paragraph" && strings.Contains(n.Content, "Layout cell one wraps a paragraph")
	})
	require.Len(t, wrapped, 1,
		"the layout table's wrapped paragraph must still be walked — the verdict is a signal, not a stop")

	// PAIR — the header-bearing table is DATA, and says which measurement made
	// it so.
	data := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return n.Type == "table" && n.Metadata["table_role"] == ""
	})
	require.Len(t, data, 1, "the fixture's thead-bearing data table must emit exactly one table node")
	assert.Equal(t, "false", data[0].Metadata["table_layout"],
		"the thead-bearing table must carry the data verdict")
	assert.Equal(t, "true", data[0].Metadata["table_header_signal"],
		"the thead-bearing table must report the header signal that decided it")

	// --- LEG 3: link-only run retention -------------------------------------
	linksOnly := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return n.Metadata["links_only"] == "true"
	})
	require.Len(t, linksOnly, 1,
		"the navigation link strip must be retained as a node carrying links_only")
	strip := linksOnly[0]
	assert.Equal(t, "paragraph", strip.Type,
		"the retained strip keeps the paragraph node type — the graph keeps its node vocabulary")
	// THE FIELD IS THE LOCKED HALF OF THE RULE: chrome is a signal, not a
	// chunk, so the text lands on Description and stays OUT of Content, which
	// is what every content composer reads.
	assert.Contains(t, strip.Description, "First Navigation Link")
	assert.Contains(t, strip.Description, "Second Navigation Link")
	assert.Empty(t, strip.Content,
		"the navigation strip's text must not reach Content, which every content composer reads")

	// ...and the anchors still emit exactly their own link nodes, neither
	// dropped nor duplicated by the retention.
	for _, href := range []string{"/home", "/docs"} {
		matched := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
			return n.Type == "link" && strings.HasSuffix(n.Metadata["url"], href)
		})
		assert.Len(t, matched, 1, "the anchor %s must emit exactly one link node", href)
	}

	// PAIR — ordinary prose carries no links_only key at all.
	plainProse := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		if n.Type != "paragraph" {
			return false
		}
		_, marked := n.Metadata["links_only"]
		return !marked
	})
	assert.GreaterOrEqual(t, len(plainProse), 4,
		"ordinary prose paragraphs must carry no links_only key, got %d unmarked", len(plainProse))

	// --- LEG 4: attribute provenance ----------------------------------------
	climbed := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return n.Metadata["attr_source"] == "ancestor"
	})
	require.Len(t, climbed, 1,
		"the run under an unclassed div inside a classed box must record the climb")
	run := climbed[0]
	assert.NotEmpty(t, run.Metadata["attr_source_tag"],
		"a climbed record must name the element its attributes came from")
	assert.Equal(t, "outerbox", run.Metadata["class"],
		"the climbed record must still inherit the classed ancestor's class")
	assert.NotEqual(t, run.Metadata["dom_depth"], run.Metadata["attr_source_depth"],
		"the climb must be visible as a distance: dom_depth %q equals attr_source_depth %q",
		run.Metadata["dom_depth"], run.Metadata["attr_source_depth"])

	// PAIR — element-derived records report their own element and name no
	// source, so "ancestor" stays a statement rather than a default.
	own := nodesWhere(nodes, func(n *knowledgev1.Node) bool {
		return n.Metadata["attr_source"] == "own"
	})
	require.NotEmpty(t, own, "no element-derived record reported attr_source=own")
	for _, n := range own {
		assert.NotContains(t, n.Metadata, "attr_source_tag",
			"node id=%s type=%s reports its own element yet names a source it climbed to", n.Id, n.Type)
	}
}

// nodesWhere returns every emitted node satisfying want, in emission order.
func nodesWhere(nodes []*knowledgev1.Node, want func(*knowledgev1.Node) bool) []*knowledgev1.Node {
	var out []*knowledgev1.Node
	for _, n := range nodes {
		if want(n) {
			out = append(out, n)
		}
	}
	return out
}
