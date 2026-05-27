// SPDX-License-Identifier: Apache-2.0

package web

import (
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// buildFixture assembles a pageRecord with one section, two paragraphs,
// one code_block, one list with 2 items, one internal link record, and
// one external cite. The fixture is the canonical input used by the
// table of assertions below.
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
		ContentHash:   "deadbeef",
		TopSections:   []*sectionRecord{sec},
		InternalLinks: []string{"https://example.com/other"},
		ExternalCites: []*linkRecord{{URL: "https://ext.example.org/x", Rel: "external"}},
	}
}

func TestEmitFromPage_NodeCountAndTypes(t *testing.T) {
	p := buildFixture()
	nodes, _ := emitFromPage(p)

	// Expected: 1 page + 1 section + 2 paragraphs + 1 code_block + 1 list
	// + 2 list_items + 1 link = 9 nodes.
	want := 9
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

// TestEmitFromPage_PageDescriptionFlattens confirms the page node carries
// the flattened body — heading + paragraph + list-item text — under
// Node.Description. Without this, recipes translating page → pattern
// (azure-patterns, hohpe-eip, ...) emit pattern nodes with no body
// content, and BM25 / HNSW indexes silently drop them from search.
func TestEmitFromPage_PageDescriptionFlattens(t *testing.T) {
	p := buildFixture()
	nodes, _ := emitFromPage(p)

	var pageNode *knowledgev1.Node
	for i := range nodes {
		if nodes[i].Type == "page" {
			pageNode = nodes[i]
			break
		}
	}
	if pageNode == nil {
		t.Fatal("page node missing")
	}
	desc := pageNode.Description
	if desc == "" {
		t.Fatal("page Description is empty — flattenPageBody did not populate")
	}
	wantSubstrings := []string{
		"# Intro",     // heading w/ depth 1
		"first para",  // paragraph 1
		"second para", // paragraph 2
		"- item-a",    // list item
		"- item-b",    // list item
	}
	for _, want := range wantSubstrings {
		if !contains(desc, want) {
			t.Errorf("page.Description missing %q\n--- got ---\n%s", want, desc)
		}
	}
	// Code blocks, links, and external cites must NOT appear in the
	// flattened body — those are shape nodes, not searchable prose.
	for _, unwant := range []string{"package x", "https://example.com/other"} {
		if contains(desc, unwant) {
			t.Errorf("page.Description should not contain %q (shape node leaked into body)\n--- got ---\n%s", unwant, desc)
		}
	}
}

// TestEmitFromPage_PageDescriptionRespectsCap confirms the body is hard-
// capped at pageDescriptionCap. A long article must not blow the rerank
// doc-text or BM25 field budgets when downstream recipes copy the
// description into a pattern node.
func TestEmitFromPage_PageDescriptionRespectsCap(t *testing.T) {
	const filler = "lorem ipsum dolor sit amet "
	repeats := (pageDescriptionCap / len(filler)) + 100
	bigPara := paragraphRecord{Text: repeatString(filler, repeats)}
	sec := &sectionRecord{
		Heading:  "Big",
		Depth:    1,
		Children: []contentRecord{bigPara},
	}
	p := &pageRecord{
		URL:         "https://example.com/big",
		FinalURL:    "https://example.com/big",
		Title:       "Big",
		HTTPStatus:  200,
		ContentHash: "abc",
		TopSections: []*sectionRecord{sec},
	}
	nodes, _ := emitFromPage(p)
	var pageNode *knowledgev1.Node
	for i := range nodes {
		if nodes[i].Type == "page" {
			pageNode = nodes[i]
			break
		}
	}
	if pageNode == nil {
		t.Fatal("page node missing")
	}
	if got := len(pageNode.Description); got > pageDescriptionCap {
		t.Errorf("page.Description over cap: got %d bytes, cap=%d", got, pageDescriptionCap)
	}
}

// repeatString concatenates s n times. strings.Repeat would do, but the
// test file already imports nothing from strings — keep imports minimal.
func repeatString(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

// contains is a tiny helper kept here to avoid a strings import.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestEmitFromPage_StableIDs(t *testing.T) {
	p := buildFixture()
	n1, _ := emitFromPage(p)
	n2, _ := emitFromPage(p)
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
	nodes, edges := emitFromPage(p)
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
	nodes, edges := emitFromPage(p)
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

func TestEmitFromPage_InternalExternalEdgesDistinguishable(t *testing.T) {
	p := buildFixture()
	nodes, edges := emitFromPage(p)
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
	nodes, edges := emitFromPage(nil)
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
	nodes, _ := emitFromPage(p)
	assertInlineEmphasis(t, nodes, "paragraph", "a bold c it d", emphsPara)
	assertInlineEmphasis(t, nodes, "paragraph", "plain line", nil)
	assertInlineEmphasis(t, nodes, "list_item", "set `FOO` now", emphsItem)
	assertInlineEmphasis(t, nodes, "list_item", "plain item", nil)
}

// Shared assertion helpers live in emit_nodes_helpers_test.go to
// keep this file under the 300 LOC recommended cap.
