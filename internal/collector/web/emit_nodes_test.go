// SPDX-License-Identifier: Apache-2.0

package web

import (
	"encoding/base64"
	"strconv"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// fixtureBody is the retained response body buildFixture claims to have
// captured. ContentHash and RawHTMLBase64 below are both derived from it, so
// the fixture cannot assert a round-trip its own two fields contradict.
const fixtureBody = `<html><body><h1>Intro</h1><p>first para</p></body></html>`

// buildFixture assembles a pageRecord with one section, two paragraphs,
// one code_block, one list with 2 items, one internal link record, one
// external cite, and the retained raw body. The fixture is the canonical
// input used by the table of assertions below.
func buildFixture() *pageRecord {
	sec := &sectionRecord{
		Heading: "Intro",
		Depth:   1,
		Anchor:  "intro",
		Children: []contentRecord{
			paragraphRecord{Text: "first para"},
			paragraphRecord{Text: "second para"},
			codeBlockRecord{Language: "go", Source: "package x"},
			listRecord{Ordered: false, Kind: "ul", Items: []listItemRecord{
				{Text: "item-a", Position: 0},
				{Text: "item-b", Position: 1},
			}},
			linkRecord{URL: "https://example.com/other", Rel: "internal", Text: "other"},
		},
	}
	return &pageRecord{
		URL:           "https://example.com/page",
		FinalURL:      "https://example.com/page",
		Title:         "Intro",
		HTTPStatus:    200,
		FetchedAt:     time.Unix(0, 0).UTC(),
		ContentHash:   hashBody([]byte(fixtureBody)),
		RawHTMLBase64: base64.StdEncoding.EncodeToString([]byte(fixtureBody)),
		TopSections:   []*sectionRecord{sec},
		InternalLinks: []string{"https://example.com/other"},
		ExternalCites: []*linkRecord{{URL: "https://ext.example.org/x", Rel: "external"}},
	}
}

// mustEmitFromPage runs the emitter and FAILS THE TEST on the loud-error
// return, rather than discarding it into a blank identifier at twenty call
// sites.
//
// emitFromPage's error reports a condition no correct build reaches — the four
// marshal branches that used to drop a metadata key or an edge's evidence in
// silence. Discarding it here would put the tests in exactly the posture the
// ticket removed from the production code: a failure that happened, and
// nothing that says so.
func mustEmitFromPage(t *testing.T, p *pageRecord, collectedAt time.Time) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	t.Helper()
	// A ZERO graphSource: these tests assert the page shape, not the crawl's
	// provenance, and the zero value writes no source keys.
	nodes, edges, err := emitFromPage(p, collectedAt, graphSource{})
	if err != nil {
		t.Fatalf("emitFromPage: %v", err)
	}
	return nodes, edges
}

func TestEmitFromPage_NodeCountAndTypes(t *testing.T) {
	p := buildFixture()
	nodes, _ := mustEmitFromPage(t, p, time.Time{})

	// Expected: 1 page + 1 section + 2 paragraphs + 1 code_block + 1 list
	// + 2 list_items + 1 link + 1 raw_html = 10 nodes.
	want := 10
	if len(nodes) != want {
		types := make([]string, len(nodes))
		for i, n := range nodes {
			types[i] = n.Type
		}
		t.Fatalf("want %d nodes, got %d: %v", want, len(nodes), types)
	}

	wantTypes := map[string]int{
		"page":       1,
		"section":    1,
		"paragraph":  2,
		"code_block": 1,
		"list":       1,
		"list_item":  2,
		"link":       1,
		"raw_html":   1,
	}
	counts := map[string]int{}
	for _, n := range nodes {
		counts[n.Type]++
	}
	for typ, n := range wantTypes {
		if counts[typ] != n {
			t.Errorf("type %s: want %d got %d (all: %v)", typ, n, counts[typ], counts)
		}
	}
}

// THE TWO PAGE-BODY TESTS THAT STOOD HERE ARE RETIRED, and this note is the
// record of where they went rather than a silence a later reader has to
// reconstruct.
//
// TestEmitFromPage_PageDescriptionFlattens asserted that the page node carried
// a flattened body of its sections' heading, paragraph and list-item text.
// That flatten no longer happens: under the ruling "pages are made up of
// chunks never put the whole page as content" the page-level flatten is
// retired rather than relocated, so the test's subject does not exist. Its
// substance is not lost — the same words are asserted reachable, on the chunk
// nodes that own them, by the reachability census in
// TestEmit_PageIsItsChunksNotItsBody.
//
// TestEmitFromPage_PageDescriptionIsUntruncated was the untruncated guard: it
// proved a page whose prose ran past the removed 8000-character ceiling
// reached the graph whole. THE CONCERN IT GUARDED IS NOW SATISFIED BY
// CONSTRUCTION, which is why the guard is removed rather than rewritten: a
// page body cannot be silently truncated when no page body is composed at
// all, and every chunk node carries its own untruncated text with no ceiling
// anywhere on that path.

func TestEmitFromPage_StableIDs(t *testing.T) {
	p := buildFixture()
	n1, _ := mustEmitFromPage(t, p, time.Time{})
	n2, _ := mustEmitFromPage(t, p, time.Time{})
	if len(n1) != len(n2) {
		t.Fatalf("emit not deterministic: %d vs %d", len(n1), len(n2))
	}
	for i := range n1 {
		if n1[i].Id != n2[i].Id {
			t.Fatalf("node %d ID drift: %q vs %q", i, n1[i].Id, n2[i].Id)
		}
		if n1[i].Id == "" {
			t.Fatalf("node %d has empty ID: %+v", i, n1[i])
		}
		if len(n1[i].Id) != 16 {
			t.Fatalf("node %d ID not 16-hex: %q", i, n1[i].Id)
		}
	}
}

func TestEmitFromPage_ContainsEdgeTree(t *testing.T) {
	p := buildFixture()
	nodes, edges := mustEmitFromPage(t, p, time.Time{})
	byID := indexNodes(nodes)

	pageID := nodes[0].Id
	if nodes[0].Type != "page" {
		t.Fatalf("first node not page: %s", nodes[0].Type)
	}

	// page → section edge present.
	sectionID := findOneChild(t, edges, pageID, "section", byID)
	if sectionID == "" {
		t.Fatalf("no page→section EdgeContains edge")
	}

	// section → paragraph, code_block, list, link each present.
	children := childrenOf(edges, sectionID, byID)
	wantChildTypes := []string{"paragraph", "paragraph", "code_block", "list", "link"}
	if !equalStrings(children, wantChildTypes) {
		t.Fatalf("section child types (ordered): want %v got %v", wantChildTypes, children)
	}

	// list → 2 list_items.
	listID := findFirstChildID(edges, sectionID, "list", byID)
	items := childrenOf(edges, listID, byID)
	if !equalStrings(items, []string{"list_item", "list_item"}) {
		t.Fatalf("list children: want [list_item list_item], got %v", items)
	}
}

func TestEmitFromPage_PositionMetadataMonotonic(t *testing.T) {
	p := buildFixture()
	nodes, edges := mustEmitFromPage(t, p, time.Time{})
	byID := indexNodes(nodes)

	pageID := nodes[0].Id
	sectionID := findOneChild(t, edges, pageID, "section", byID)

	positions := sectionChildPositions(edges, sectionID, byID)
	// Expected: 0,1,2,3,4 across the 5 children in document order.
	want := []int{0, 1, 2, 3, 4}
	if !equalInts(positions, want) {
		t.Fatalf("section child positions: want %v got %v", want, positions)
	}
}

// TestEmitFromPage_PositionMetadataOnEveryContainedNode asserts the
// node-level half of document order: EVERY node that is the target of a
// CONTAINS edge carries a `position` metadata key equal to that edge's
// Evidence position, and the parentless page root carries none.
//
// Complementary to TestEmitFromPage_PositionMetadataMonotonic above, which
// reads EDGE positions only and never touches node metadata — do not fold
// the two together.
func TestEmitFromPage_PositionMetadataOnEveryContainedNode(t *testing.T) {
	p := buildFixture()
	nodes, edges := mustEmitFromPage(t, p, time.Time{})
	byID := indexNodes(nodes)

	walked := 0
	for _, e := range edges {
		if e.Type != kgtypes.EdgeContains {
			continue
		}
		walked++
		md := parseMeta(t, e.Evidence)
		child := byID[e.ToID]
		if child == nil {
			t.Fatalf("contains edge %s→%s: no such node", e.FromID, e.ToID)
		}
		got, ok := child.Metadata["position"]
		if !ok {
			t.Errorf("node %s (type %s) has NO position metadata; edge position=%q",
				child.Id, child.Type, md["position"])
			continue
		}
		if got != md["position"] {
			t.Errorf("node %s (type %s): position=%q, want %q (its CONTAINS edge position)",
				child.Id, child.Type, got, md["position"])
		}
	}
	// Guard: a fixture or edge-type mistake must not let this pass vacuously.
	if walked == 0 {
		t.Fatal("walked 0 CONTAINS edges — the assertion never ran")
	}

	if _, ok := nodes[0].Metadata["position"]; ok {
		t.Errorf("page root %s carries a position key %q; a parentless root has no edge position to mirror",
			nodes[0].Id, nodes[0].Metadata["position"])
	}
}

// TestEmitFromPage_StampsCollectorSchemaVersion asserts the version stamp lands
// on the page ROOT and on no child node. The negative half is what rejects an
// implementation that stamps every node.
//
// Asserted against the package constant rather than a hardcoded "1" so a future
// bump does not false-red the test, plus a separate assertion that the constant
// is greater than zero so its value cannot be read as unstamped.
func TestEmitFromPage_StampsCollectorSchemaVersion(t *testing.T) {
	if collectorSchemaVersion <= 0 {
		t.Fatalf("collectorSchemaVersion = %d, want > 0 — zero cannot be told from unstamped", collectorSchemaVersion)
	}
	p := buildFixture()
	nodes, edges := mustEmitFromPage(t, p, time.Time{})
	byID := indexNodes(nodes)

	want := strconv.Itoa(collectorSchemaVersion)
	if got := nodes[0].Metadata["collector_schema_version"]; got != want {
		t.Errorf("page root collector_schema_version = %q, want %q", got, want)
	}

	pageID := nodes[0].Id
	sectionID := findOneChild(t, edges, pageID, "section", byID)
	paragraphID := findFirstChildID(edges, sectionID, "paragraph", byID)
	if sectionID == "" || paragraphID == "" {
		t.Fatal("fixture did not yield a section and a paragraph — the negative half never ran")
	}
	for _, id := range []string{sectionID, paragraphID} {
		if got, ok := byID[id].Metadata["collector_schema_version"]; ok {
			t.Errorf("%s node %s carries collector_schema_version=%q; the stamp belongs on the root only",
				byID[id].Type, id, got)
		}
	}
}

func TestEmitFromPage_InternalExternalEdgesDistinguishable(t *testing.T) {
	p := buildFixture()
	nodes, edges := mustEmitFromPage(t, p, time.Time{})
	pageID := nodes[0].Id

	var internal, external int
	for _, e := range edges {
		if e.Type != kgtypes.EdgeReferences || e.FromID != pageID {
			continue
		}
		md := parseMeta(t, e.Evidence)
		switch md["rel"] {
		case "internal":
			internal++
			if md["url"] != "https://example.com/other" {
				t.Errorf("internal url mismatch: %q", md["url"])
			}
		case "external":
			external++
			if md["url"] != "https://ext.example.org/x" {
				t.Errorf("external url mismatch: %q", md["url"])
			}
		default:
			t.Errorf("EdgeReferences without rel metadata: %+v", md)
		}
	}
	if internal != 1 {
		t.Errorf("want 1 internal references edge, got %d", internal)
	}
	if external != 1 {
		t.Errorf("want 1 external references edge, got %d", external)
	}
}

func TestEmitFromPage_NilReturnsEmpty(t *testing.T) {
	nodes, edges := mustEmitFromPage(t, nil, time.Time{})
	if len(nodes) != 0 || len(edges) != 0 {
		t.Fatalf("nil page: want empty, got %d nodes, %d edges", len(nodes), len(edges))
	}
}

// TestEmit_InlineEmphasis covers paragraph + list_item + blockquote
// emission: each record's InlineEmphasis slice is JSON-encoded into the
// metadata under the stable "inline_emphasis" key, and parses back to
// the same list. Records with zero emphasis entries omit the key.
func TestEmit_InlineEmphasis(t *testing.T) {
	emphsPara := []inlineEmphasis{
		{Tag: "strong", Text: "bold", Position: 2},
		{Tag: "em", Text: "it", Position: 9},
	}
	emphsItem := []inlineEmphasis{
		{Tag: "code", Text: "FOO", Position: 4},
	}
	emphsQuote := []inlineEmphasis{
		{Tag: "strong", Text: "done", Position: 7},
	}
	p := &pageRecord{
		URL:        "https://example.com/emph",
		FinalURL:   "https://example.com/emph",
		Title:      "Emph",
		HTTPStatus: 200,
		FetchedAt:  time.Unix(0, 0).UTC(),
		TopSections: []*sectionRecord{{
			Heading: "Intro",
			Depth:   1,
			Children: []contentRecord{
				paragraphRecord{Text: "a bold c it d", InlineEmphasis: emphsPara},
				// Paragraph with no emphasis — key must be absent.
				paragraphRecord{Text: "plain line"},
				listRecord{Kind: "ul", Items: []listItemRecord{
					{Text: "set `FOO` now", Position: 0, InlineEmphasis: emphsItem},
					{Text: "plain item", Position: 1},
				}},
				quoteRecord{Text: "always done well", InlineEmphasis: emphsQuote},
			},
		}},
	}
	nodes, _ := mustEmitFromPage(t, p, time.Time{})
	assertInlineEmphasis(t, nodes, "paragraph", "a bold c it d", emphsPara)
	assertInlineEmphasis(t, nodes, "paragraph", "plain line", nil)
	assertInlineEmphasis(t, nodes, "list_item", "set `FOO` now", emphsItem)
	assertInlineEmphasis(t, nodes, "list_item", "plain item", nil)
}

// Shared assertion helpers live in emit_nodes_helpers_test.go to
// keep this file under the 300 LOC recommended cap.
