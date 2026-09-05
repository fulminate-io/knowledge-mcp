// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

const fixturePath = "/abs/path/Designing-Data.pdf"

// mustEmit runs the emitter and FAILS THE TEST on its loud-error return,
// rather than discarding it into a blank identifier at sixteen call sites.
//
// emit's error reports a condition no correct build reaches — the marshal
// branch that used to drop an edge's evidence in silence. Discarding it here
// would put these tests in exactly the posture the fix removed from the
// production code: a failure that happened, and nothing that says so. Its
// counterpart in the web collector is mustEmitFromPage, and this helper exists
// for the same reason.
//
// The pdf path is not a parameter: every caller in this package emits against
// the one fixturePath constant, and a parameter only ever handed one value is a
// generality nothing exercises.
func mustEmit(t *testing.T, meta pdf.Metadata, chunks []pdf.Chunk, collectedAt time.Time) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	t.Helper()
	nodes, edges, err := emit(meta, fixturePath, chunks, collectedAt)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	return nodes, edges
}

// edgeMeta decodes an EdgeContains Evidence blob into its string map.
func edgeMeta(t *testing.T, raw string) map[string]string {
	t.Helper()
	m := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return m
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("edge evidence not JSON: %q (%v)", raw, err)
	}
	return m
}

// indexNodes keys emitted nodes by ID for edge-target lookups.
func indexNodes(nodes []*knowledgev1.Node) map[string]*knowledgev1.Node {
	m := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		m[n.Id] = n
	}
	return m
}

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
	nodes, edges := mustEmit(t, pdf.Metadata{Title: "DDIA"}, chunks, time.Time{})

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

// TestEmit_PositionMetadataOnEveryContainedNode asserts the node-level half
// of document order: EVERY node that is the target of a CONTAINS edge carries
// a `position` metadata key equal to that edge's Evidence position, and the
// parentless document root carries none.
//
// The fixture nests a section whose children sit at differing indices AND
// places a second chunk at top-level index 1, so a constant-zero or
// top-level-only implementation is rejected.
func TestEmit_PositionMetadataOnEveryContainedNode(t *testing.T) {
	t.Parallel()
	chunks := []pdf.Chunk{
		{
			Kind: pdf.BlockHeading, Text: "Reliability", HeadingLevel: 1,
			PageRange: [2]int{0, 0},
			Children: []pdf.Chunk{
				{Kind: pdf.BlockParagraph, Text: "p0", PageRange: [2]int{0, 0}},
				{
					Kind: pdf.BlockHeading, Text: "Nested", HeadingLevel: 2,
					PageRange: [2]int{1, 1},
					Children: []pdf.Chunk{
						{Kind: pdf.BlockParagraph, Text: "n0", PageRange: [2]int{1, 1}},
						{Kind: pdf.BlockCode, Text: "n1", PageRange: [2]int{1, 1}},
					},
				},
			},
		},
		{Kind: pdf.BlockParagraph, Text: "top-level at index 1", PageRange: [2]int{2, 2}},
	}
	nodes, edges := mustEmit(t, pdf.Metadata{Title: "DDIA"}, chunks, time.Time{})
	byID := indexNodes(nodes)

	walked := 0
	for _, e := range edges {
		if e.Type != kgtypes.EdgeContains {
			continue
		}
		walked++
		md := edgeMeta(t, e.Evidence)
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
		t.Errorf("document root %s carries a position key %q; a parentless root has no edge position to mirror",
			nodes[0].Id, nodes[0].Metadata["position"])
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
	nodes, _ := mustEmit(t, pdf.Metadata{}, chunks, time.Time{})
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

// TestEmit_PageNumbersAreOneIndexed asserts the emitted page keys carry the
// PRINTED page number, not the zero-indexed internal page index. Two distinct
// ranges are asserted so an implementation that converts only one key, or
// hardcodes "1", is rejected.
func TestEmit_PageNumbersAreOneIndexed(t *testing.T) {
	t.Parallel()
	chunks := []pdf.Chunk{
		{Kind: pdf.BlockParagraph, Text: "first page body", PageRange: [2]int{0, 0}},
		{Kind: pdf.BlockParagraph, Text: "spans pages", PageRange: [2]int{4, 6}},
	}
	nodes, _ := mustEmit(t, pdf.Metadata{Title: "X"}, chunks, time.Time{})
	if len(nodes) != 3 {
		t.Fatalf("nodes len = %d, want 3 (1 doc + 2 paragraphs)", len(nodes))
	}
	cases := []struct {
		node                *knowledgev1.Node
		wantFirst, wantLast string
	}{
		{nodes[1], "1", "1"},
		{nodes[2], "5", "7"},
	}
	for i, c := range cases {
		if got := c.node.Metadata["page_first"]; got != c.wantFirst {
			t.Errorf("chunk %d page_first = %q, want %q (one-indexed)", i, got, c.wantFirst)
		}
		if got := c.node.Metadata["page_last"]; got != c.wantLast {
			t.Errorf("chunk %d page_last = %q, want %q (one-indexed)", i, got, c.wantLast)
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
	nodes, _ := mustEmit(t, meta, nil, time.Time{})
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
	// The blurb weaves the high-signal fields together, and it lives in
	// Content: every raw node's searchable text does, the root included.
	if !strings.Contains(doc.Content, meta.Title) || !strings.Contains(doc.Content, meta.Author) {
		t.Errorf("doc.Content = %q, want title + author woven in", doc.Content)
	}
	if doc.Description != "" {
		t.Errorf("doc.Description = %q, want empty — the blurb moved to Content", doc.Description)
	}
}

// TestEmit_ChunkBodyLandsInContent pins the field rule: EVERY raw node's
// searchable text lands in Content. A leaf's Content is its body, a
// section's Content is its heading, and the document root's Content is
// the Info-dict blurb. A section ALSO keeps its heading in SymbolName,
// because that is the label the read surface renders a hit with, and no
// node uses Description at all. The Description assertions are what
// reject a partial move that leaves one kind's text behind.
//
// The two language subtests are a SCOPE FENCE, not a gate on new behaviour:
// no pdf pipeline stage sets a language, so the absent case is what goes red
// if a future author invents a guessed default or an "unknown" placeholder.
func TestEmit_ChunkBodyLandsInContent(t *testing.T) {
	t.Parallel()
	chunks := []pdf.Chunk{
		{
			Kind: pdf.BlockHeading, Text: "Reliability", HeadingLevel: 1,
			PageRange: [2]int{0, 0},
			Children: []pdf.Chunk{
				{Kind: pdf.BlockParagraph, Text: "First paragraph body.", PageRange: [2]int{0, 0}},
				{Kind: pdf.BlockCode, Text: "SELECT 1;", PageRange: [2]int{0, 0}},
			},
		},
	}
	nodes, _ := mustEmit(t, pdf.Metadata{Title: "DDIA", Author: "MK"}, chunks, time.Time{})
	if len(nodes) != 4 {
		t.Fatalf("nodes len = %d, want 4 (doc + section + paragraph + code_block)", len(nodes))
	}
	doc, section, para, code := nodes[0], nodes[1], nodes[2], nodes[3]

	for _, leaf := range []*knowledgev1.Node{para, code} {
		if leaf.Content == "" {
			t.Errorf("%s: Content is empty, want the body text", leaf.Type)
		}
		if leaf.Description != "" {
			t.Errorf("%s: Description = %q, want empty (body text lives in Content)", leaf.Type, leaf.Description)
		}
		if leaf.SymbolName != "" {
			t.Errorf("%s: SymbolName = %q, want empty on a leaf", leaf.Type, leaf.SymbolName)
		}
	}
	if section.SymbolName != "Reliability" {
		t.Errorf("section SymbolName = %q, want the heading", section.SymbolName)
	}
	if section.Content != "Reliability" {
		t.Errorf("section Content = %q, want the heading — a section's own text IS its heading", section.Content)
	}
	if section.Description != "" {
		t.Errorf("section Description = %q, want empty", section.Description)
	}
	if !strings.Contains(doc.Content, "DDIA") {
		t.Errorf("document root Content = %q, want the metadata blurb", doc.Content)
	}
	if doc.Description != "" {
		t.Errorf("document root Description = %q, want empty — the blurb lives in Content", doc.Description)
	}

	t.Run("code_block_language_passes_through", func(t *testing.T) {
		n, _ := mustEmit(t, pdf.Metadata{}, []pdf.Chunk{
			{Kind: pdf.BlockCode, Text: "SELECT 1;", PageRange: [2]int{0, 0},
				Metadata: map[string]string{"language": "sql"}},
		}, time.Time{})
		if got := n[1].Metadata["language"]; got != "sql" {
			t.Errorf("metadata[language] = %q, want sql (generic per-chunk copy)", got)
		}
	})

	t.Run("code_block_language_absent_when_unset", func(t *testing.T) {
		n, _ := mustEmit(t, pdf.Metadata{}, []pdf.Chunk{
			{Kind: pdf.BlockCode, Text: "SELECT 1;", PageRange: [2]int{0, 0}},
		}, time.Time{})
		if got, ok := n[1].Metadata["language"]; ok {
			t.Errorf("metadata[language] = %q, want ABSENT — no stage produces one and no default is invented", got)
		}
		if n[1].Language != "" {
			t.Errorf("Node.Language = %q, want empty", n[1].Language)
		}
	})
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
	first, _ := mustEmit(t, pdf.Metadata{Title: "X"}, chunks, time.Time{})
	second, _ := mustEmit(t, pdf.Metadata{Title: "X"}, chunks, time.Time{})
	if len(first) != len(second) {
		t.Fatalf("len mismatch: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Id != second[i].Id {
			t.Errorf("nodes[%d].Id = %q vs %q (re-run drift)", i, first[i].Id, second[i].Id)
		}
	}
}

// TestEmit_StampsCollectorSchemaVersion asserts the version stamp lands on the
// document ROOT and on no child node. The negative half is what rejects an
// implementation that stamps every node.
//
// THE EXPECTED VALUE IS A LITERAL, deliberately. Comparing the emitted stamp
// against the package constant makes the test value-BLIND: it passes for any
// value the constant happens to hold, so reverting the constant leaves this and
// every other package green while collected graphs go out labeled with a shape
// they are not. The constant documents itself as bumped in the same change as
// any alteration to what this collector emits; a literal here is what makes that
// obligatory, because a shape change then has to move the constant AND this
// number together. The separate `<= 0` assertion stays: it rejects a zero, which
// cannot be told from unstamped, independently of what the literal says.
func TestEmit_StampsCollectorSchemaVersion(t *testing.T) {
	t.Parallel()
	if collectorSchemaVersion <= 0 {
		t.Fatalf("collectorSchemaVersion = %d, want > 0 — zero cannot be told from unstamped", collectorSchemaVersion)
	}
	chunks := []pdf.Chunk{
		{Kind: pdf.BlockHeading, Text: "H", HeadingLevel: 1, PageRange: [2]int{0, 0},
			Children: []pdf.Chunk{
				{Kind: pdf.BlockParagraph, Text: "p", PageRange: [2]int{0, 0}},
			}},
	}
	nodes, _ := mustEmit(t, pdf.Metadata{Title: "X"}, chunks, time.Time{})
	if len(nodes) != 3 {
		t.Fatalf("nodes len = %d, want 3 (doc + section + paragraph)", len(nodes))
	}
	const want = "3"
	if got := nodes[0].Metadata["collector_schema_version"]; got != want {
		t.Errorf("document root collector_schema_version = %q, want %q - the emitted shape and the stamp have diverged", got, want)
	}
	for _, n := range nodes[1:] {
		if got, ok := n.Metadata["collector_schema_version"]; ok {
			t.Errorf("%s node %s carries collector_schema_version=%q; the stamp belongs on the root only",
				n.Type, n.Id, got)
		}
	}
}

// TestEmit_NoChunksDocOnly covers the zero-chunks edge case: emit
// produces a single document node and zero edges. Asserts the
// collector handles empty/early-failure inputs without panicking.
func TestEmit_NoChunksDocOnly(t *testing.T) {
	t.Parallel()
	nodes, edges := mustEmit(t, pdf.Metadata{Title: "Empty"}, nil, time.Time{})
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
	nodes, _ := mustEmit(t, pdf.Metadata{Title: "X"}, chunks, time.Time{})
	for i, n := range nodes {
		if n.Metadata["source"] != "pdf" {
			t.Errorf("nodes[%d] (Type=%q): Metadata[source] = %q, want pdf", i, n.Type, n.Metadata["source"])
		}
	}
}
