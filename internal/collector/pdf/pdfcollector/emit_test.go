// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"strings"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const fixturePath = "/abs/path/Designing-Data.pdf"

// TestEmit_HeadingPlusParagraphs covers the canonical happy path:
// one section heading with two paragraph children. Asserts the
// document → section → paragraph nesting via EdgeContains, the
// bare-name node-type vocabulary, and contiguous position metadata.
func TestEmit_HeadingPlusParagraphs(t *testing.T) {
	t.Parallel()
	chunks := []pdf.Chunk{
		{
			Kind: pdf.BlockHeading, Text: "Reliability", HeadingLevel: 1,
			PageRange: [2]int{0, 0},
			Children: []pdf.Chunk{
				{Kind: pdf.BlockParagraph, Text: "First paragraph body.", PageRange: [2]int{0, 0}},
				{Kind: pdf.BlockParagraph, Text: "Second paragraph body.", PageRange: [2]int{0, 1}},
			},
		},
	}
	nodes, edges := emit(pdf.Metadata{Title: "DDIA"}, fixturePath, chunks)

	if len(nodes) != 4 {
		t.Fatalf("nodes len = %d, want 4 (1 doc + 1 section + 2 paragraph)", len(nodes))
	}
	if nodes[0].Type != "document" || nodes[0].SymbolName != "DDIA" {
		t.Errorf("nodes[0] = {%q,%q}, want {document,DDIA}", nodes[0].Type, nodes[0].SymbolName)
	}
	if nodes[1].Type != "section" || nodes[1].SymbolName != "Reliability" {
		t.Errorf("nodes[1] = {%q,%q}, want {section,Reliability}", nodes[1].Type, nodes[1].SymbolName)
	}
	if nodes[2].Type != "paragraph" || nodes[3].Type != "paragraph" {
		t.Errorf("nodes[2,3] types = (%q,%q), want (paragraph,paragraph)", nodes[2].Type, nodes[3].Type)
	}
	// 3 contains edges: doc→section, section→para, section→para.
	if len(edges) != 3 {
		t.Fatalf("edges len = %d, want 3", len(edges))
	}
	for _, e := range edges {
		if e.Type != kgtypes.EdgeContains {
			t.Errorf("edge type = %q, want EdgeContains", e.Type)
		}
		if e.Method != "pdf-collect" {
			t.Errorf("edge method = %q, want pdf-collect", e.Method)
		}
	}
	// Check parent IDs: edge[0] from doc, edge[1+2] from section.
	if edges[0].FromID != nodes[0].Id || edges[0].ToID != nodes[1].Id {
		t.Errorf("edges[0] = {%s→%s}, want {doc→section}", edges[0].FromID, edges[0].ToID)
	}
	if edges[1].FromID != nodes[1].Id || edges[1].ToID != nodes[2].Id {
		t.Errorf("edges[1] = {%s→%s}, want {section→p0}", edges[1].FromID, edges[1].ToID)
	}
	if edges[2].FromID != nodes[1].Id || edges[2].ToID != nodes[3].Id {
		t.Errorf("edges[2] = {%s→%s}, want {section→p1}", edges[2].FromID, edges[2].ToID)
	}
	// Position metadata on edges should be 0 for first child, 0/1 under section.
	if !strings.Contains(edges[0].Evidence, `"position":"0"`) {
		t.Errorf("edges[0].Evidence = %q, want position 0", edges[0].Evidence)
	}
	if !strings.Contains(edges[2].Evidence, `"position":"1"`) {
		t.Errorf("edges[2].Evidence = %q, want position 1", edges[2].Evidence)
	}
}

// TestEmit_AllChunkKinds asserts every BlockKind round-trips to its
// bare-name node type: heading→section, paragraph→paragraph,
// code→code_block, list_item→list_item, table→table, unknown→block.
func TestEmit_AllChunkKinds(t *testing.T) {
	t.Parallel()
	chunks := []pdf.Chunk{
		{Kind: pdf.BlockHeading, Text: "H", HeadingLevel: 1, PageRange: [2]int{0, 0}},
		{Kind: pdf.BlockParagraph, Text: "P", PageRange: [2]int{0, 0}},
		{Kind: pdf.BlockCode, Text: "func main() {}", PageRange: [2]int{0, 0}},
		{Kind: pdf.BlockListItem, Text: "- item", PageRange: [2]int{0, 0}},
		{Kind: pdf.BlockTable, Text: "row1", PageRange: [2]int{0, 0}},
		{Kind: pdf.BlockUnknown, Text: "stuff", PageRange: [2]int{0, 0}},
	}
	nodes, _ := emit(pdf.Metadata{}, fixturePath, chunks)
	want := []string{"document", "section", "paragraph", "code_block", "list_item", "table", "block"}
	if len(nodes) != len(want) {
		t.Fatalf("nodes len = %d, want %d", len(nodes), len(want))
	}
	for i, w := range want {
		if nodes[i].Type != w {
			t.Errorf("nodes[%d].Type = %q, want %q", i, nodes[i].Type, w)
		}
		// Every emitted node MUST carry source=pdf so recipes stay
		// source-agnostic — locked Q2.
		if nodes[i].Metadata["source"] != "pdf" {
			t.Errorf("nodes[%d].Metadata[source] = %q, want pdf", i, nodes[i].Metadata["source"])
		}
	}
}

// TestEmit_DocumentMetadata covers the document-node metadata fields:
// every Info-dict slot the pdf.Metadata struct exposes lands as a key
// on the root document node (when non-empty), and CreationDate /
// ModDate are RFC3339-formatted UTC.
func TestEmit_DocumentMetadata(t *testing.T) {
	t.Parallel()
	tCreate := time.Date(2017, 3, 18, 12, 0, 0, 0, time.UTC)
	tMod := time.Date(2026, 5, 5, 9, 30, 0, 0, time.UTC)
	meta := pdf.Metadata{
		Title:        "Designing Data-Intensive Applications",
		Author:       "Martin Kleppmann",
		Subject:      "Distributed systems engineering",
		Keywords:     "distributed,scaling,reliability",
		Producer:     "pdfTeX-1.40",
		Creator:      "TeX",
		CreationDate: tCreate,
		ModDate:      tMod,
	}
	nodes, _ := emit(meta, fixturePath, nil)
	if len(nodes) != 1 {
		t.Fatalf("nodes len = %d, want 1 doc-only", len(nodes))
	}
	doc := nodes[0]
	cases := map[string]string{
		"source":        "pdf",
		"path":          fixturePath,
		"title":         meta.Title,
		"author":        meta.Author,
		"subject":       meta.Subject,
		"keywords":      meta.Keywords,
		"producer":      meta.Producer,
		"creator":       meta.Creator,
		"creation_date": "2017-03-18T12:00:00Z",
		"mod_date":      "2026-05-05T09:30:00Z",
	}
	for k, want := range cases {
		if got := doc.Metadata[k]; got != want {
			t.Errorf("doc.Metadata[%q] = %q, want %q", k, got, want)
		}
	}
	// Description should weave the high-signal fields together.
	if !strings.Contains(doc.Description, meta.Title) || !strings.Contains(doc.Description, meta.Author) {
		t.Errorf("doc.Description = %q, want title + author woven in", doc.Description)
	}
}

// TestEmit_StableIDs asserts node IDs are deterministic — re-running
// emit() on the same input produces the same IDs so dedupe + idempotent
// re-collection works.
func TestEmit_StableIDs(t *testing.T) {
	t.Parallel()
	chunks := []pdf.Chunk{
		{Kind: pdf.BlockHeading, Text: "H", HeadingLevel: 1, PageRange: [2]int{0, 0},
			Children: []pdf.Chunk{
				{Kind: pdf.BlockParagraph, Text: "p", PageRange: [2]int{0, 0}},
			}},
	}
	first, _ := emit(pdf.Metadata{Title: "X"}, fixturePath, chunks)
	second, _ := emit(pdf.Metadata{Title: "X"}, fixturePath, chunks)
	if len(first) != len(second) {
		t.Fatalf("len mismatch: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Id != second[i].Id {
			t.Errorf("nodes[%d].Id = %q vs %q (re-run drift)", i, first[i].Id, second[i].Id)
		}
	}
}

// TestEmit_NoChunksDocOnly covers the zero-chunks edge case: emit
// produces a single document node and zero edges. Asserts the
// collector handles empty/early-failure inputs without panicking.
func TestEmit_NoChunksDocOnly(t *testing.T) {
	t.Parallel()
	nodes, edges := emit(pdf.Metadata{Title: "Empty"}, fixturePath, nil)
	if len(nodes) != 1 {
		t.Errorf("nodes len = %d, want 1 (doc-only)", len(nodes))
	}
	if len(edges) != 0 {
		t.Errorf("edges len = %d, want 0", len(edges))
	}
	if nodes[0].Type != "document" {
		t.Errorf("nodes[0].Type = %q, want document", nodes[0].Type)
	}
}

// TestEmit_SourceMetaOnEveryNode pins the source=pdf invariant: every
// emitted node — document, section, paragraph, code_block,
// list_item, table, block — carries Metadata["source"] = "pdf" so
// downstream recipes can filter by source without a per-graph-type
// branch.
func TestEmit_SourceMetaOnEveryNode(t *testing.T) {
	t.Parallel()
	chunks := []pdf.Chunk{
		{Kind: pdf.BlockHeading, Text: "H", HeadingLevel: 1, PageRange: [2]int{0, 0},
			Children: []pdf.Chunk{
				{Kind: pdf.BlockParagraph, Text: "p", PageRange: [2]int{0, 0}},
				{Kind: pdf.BlockCode, Text: "c", PageRange: [2]int{0, 0}},
				{Kind: pdf.BlockListItem, Text: "l", PageRange: [2]int{0, 0}},
				{Kind: pdf.BlockTable, Text: "t", PageRange: [2]int{0, 0}},
				{Kind: pdf.BlockUnknown, Text: "u", PageRange: [2]int{0, 0}},
			}},
	}
	nodes, _ := emit(pdf.Metadata{Title: "X"}, fixturePath, chunks)
	for i, n := range nodes {
		if n.Metadata["source"] != "pdf" {
			t.Errorf("nodes[%d] (Type=%q): Metadata[source] = %q, want pdf", i, n.Type, n.Metadata["source"])
		}
	}
}
