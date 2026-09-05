// SPDX-License-Identifier: Apache-2.0

package web

import (
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestCollect_SignalCensus_EveryElementNodeCarriesTagAndDomDepth runs the real
// crawl over testdata/raw_signals.html and censuses EVERY emitted node for the
// two universal raw signals, plus text_length on the prose-bearing kinds.
//
// IT IS A CENSUS, NOT A SPOT CHECK. The node set is enumerated FROM THE RUN
// rather than from a list written here, so an implementation that stamps only
// the record kinds its author remembered fails on the kind it forgot instead of
// passing because this test forgot it too.
//
// FIVE CONTROLS make a passing run mean something:
//
//   - Control 1/2: raw_html is PRESENT, so the exclusion below cannot come to
//     cover an empty set, and it carries NO tag, so the exclusion is a
//     statement about that node rather than a hole in the census.
//   - Control 2b: the fixture produces EXACTLY ONE synthetic depth-0 root
//     section (it opens with prose above the first heading for that purpose),
//     which likewise carries no tag.
//   - Control 3: at least four DISTINCT tag values, rejecting a constant tag.
//   - Control 4: at least three DISTINCT depths, rejecting a constant depth,
//     and the h1-derived section's depth is strictly less than a nested list
//     item's, rejecting a depth that ignores nesting.
//   - Control 5: every text_length is recomputed from the node's OWN Content,
//     an external expectation rather than a read-back of the key under test.
func TestCollect_SignalCensus_EveryElementNodeCarriesTagAndDomDepth(t *testing.T) {
	body, err := os.ReadFile("testdata/raw_signals.html")
	require.NoError(t, err, "the shared raw-signals fixture must be readable")

	_, batch := serveCrawl(t, "raw-signals", string(body))
	nodes := batch.Nodes

	require.GreaterOrEqual(t, len(nodes), 12,
		"the census needs a real node population; %d nodes makes the assertions below near-vacuous", len(nodes))

	// The two kinds with NO SOURCE ELEMENT, identified structurally rather than
	// by trusting the stamp under test: the synthetic per-page retention node,
	// and the walker's depth-0 headingless root section that sinks prose
	// appearing above the first heading.
	isSynthetic := func(nodeType string, md map[string]string) bool {
		return nodeType == "raw_html" ||
			(nodeType == "section" && md["depth"] == "0" && md["heading"] == "")
	}

	rawHTML, rootSections := 0, 0
	tags := map[string]int{}
	depths := map[string]int{}
	missing := 0

	for _, n := range nodes {
		md := n.Metadata
		if isSynthetic(n.Type, md) {
			if n.Type == "raw_html" {
				rawHTML++
			} else {
				rootSections++
			}
			// The exclusion is a STATEMENT about these nodes: they carry no
			// element, so they carry neither signal — not a depth of zero,
			// which would be indistinguishable from a document-root element.
			assert.Empty(t, md["tag"],
				"synthetic node id=%s type=%s must carry no tag, got %q", n.Id, n.Type, md["tag"])
			assert.Empty(t, md["dom_depth"],
				"synthetic node id=%s type=%s must carry no dom_depth, got %q", n.Id, n.Type, md["dom_depth"])
			continue
		}

		if md["tag"] == "" {
			t.Errorf("node id=%s type=%s carries no tag (metadata=%v)", n.Id, n.Type, md)
			missing++
			continue
		}
		if md["dom_depth"] == "" {
			t.Errorf("node id=%s type=%s carries no dom_depth (metadata=%v)", n.Id, n.Type, md)
			missing++
			continue
		}
		tags[md["tag"]]++
		depths[md["dom_depth"]]++
	}

	assert.Equal(t, 0, missing, "%d of %d emitted nodes carry no tag/dom_depth pair", missing, len(nodes))

	// CONTROL 1/2 — the raw_html exclusion covers a node that actually exists.
	assert.Positive(t, rawHTML, "the crawl emitted no raw_html node, so its exclusion covers nothing")
	// CONTROL 2b — and so does the synthetic root-section exclusion. The count
	// is EXACT: the fixture opens with one run of pre-heading prose, so a
	// second such section would mean the walker changed under this test.
	assert.Equal(t, 1, rootSections,
		"the fixture must produce exactly one synthetic depth-0 root section, got %d", rootSections)

	// CONTROL 3 — a constant tag would satisfy the census above.
	assert.GreaterOrEqual(t, len(tags), 4,
		"the census needs several distinct source tags, got %v", tags)
	// CONTROL 4 — as would a constant depth.
	assert.GreaterOrEqual(t, len(depths), 3,
		"the census needs several distinct DOM depths, got %v", depths)

	// CONTROL 4b — and a depth stamped without regard to nesting would satisfy
	// both. The h1's own section sits above the list, whose items are nested
	// one level deeper again.
	sectionDepth := depthOfFirst(t, nodes, func(nodeType string, md map[string]string) bool {
		return nodeType == "section" && md["heading"] == "Raw Signals"
	}, "the h1-derived section")
	itemDepth := depthOfFirst(t, nodes, func(nodeType string, _ map[string]string) bool {
		return nodeType == "list_item"
	}, "a nested list item")
	assert.Less(t, sectionDepth, itemDepth,
		"the h1-derived section (depth %d) must sit strictly above a nested list item (depth %d)",
		sectionDepth, itemDepth)

	// CONTROL 5 — text_length on the prose-bearing kinds, with the expectation
	// RECOMPUTED FROM THE TEXT THE NODE ITSELF EMITTED. Reading the key back
	// against itself would prove nothing; this pins an independent
	// expectation, and in RUNES because that is what the heading heuristic
	// compares.
	//
	// The text is read from Content for a prose node and from Description for
	// a retained navigation strip, because that is where each one puts it —
	// chrome is a signal rather than a chunk, so a links_only run's text is
	// deliberately kept out of Content. Reading only Content here would assert
	// a length of zero against a node that emitted 44 runes.
	prose := 0
	for _, n := range nodes {
		if n.Type != "paragraph" && n.Type != "list_item" {
			continue
		}
		prose++
		text := n.Content
		if n.Metadata["links_only"] == "true" {
			text = n.Description
		}
		got := n.Metadata["text_length"]
		if got == "" {
			t.Errorf("node id=%s type=%s carries no text_length (metadata=%v)", n.Id, n.Type, n.Metadata)
			continue
		}
		assert.Equal(t, strconv.Itoa(len([]rune(text))), got,
			"node id=%s type=%s text_length disagrees with the text it emitted", n.Id, n.Type)
	}
	assert.GreaterOrEqual(t, prose, 4,
		"the fixture must emit several prose-bearing nodes for the text_length leg to mean anything, got %d", prose)
}

// depthOfFirst returns the dom_depth of the first node matching want, failing
// the test when no such node was emitted — an absent subject would otherwise
// make the comparison it feeds vacuous.
func depthOfFirst(t *testing.T, nodes []*knowledgev1.Node, want func(string, map[string]string) bool, label string) int {
	t.Helper()
	for _, n := range nodes {
		if want(n.Type, n.Metadata) {
			d, err := strconv.Atoi(n.Metadata["dom_depth"])
			require.NoError(t, err, "%s carries an unparseable dom_depth %q", label, n.Metadata["dom_depth"])
			return d
		}
	}
	t.Fatalf("the crawl emitted no node matching %s, so the nesting comparison is vacuous", label)
	return 0
}
