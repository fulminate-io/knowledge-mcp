package chunk

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// TestBuild_ParagraphMode_FlatOutput drives a fakeDoc with 1 page,
// 4 blocks (1 H1 + 3 paragraphs); Build returns 4 Chunks with correct
// Kind values in document order.
func TestBuild_ParagraphMode_FlatOutput(t *testing.T) {
	t.Parallel()
	doc := &fakeDoc{
		pages: [][]layout.Block{
			{
				{Kind: layout.BlockHeading, HeadingLevel: 1, PageIndex: 0,
					Lines: []layout.Line{{Runs: []text.TextRun{txtRun("Heading One")}}}},
				{Kind: layout.BlockParagraph, PageIndex: 0,
					Lines: []layout.Line{{Runs: []text.TextRun{txtRun("first paragraph here")}}}},
				{Kind: layout.BlockParagraph, PageIndex: 0,
					Lines: []layout.Line{{Runs: []text.TextRun{txtRun("second paragraph here")}}}},
				{Kind: layout.BlockParagraph, PageIndex: 0,
					Lines: []layout.Line{{Runs: []text.TextRun{txtRun("third paragraph here")}}}},
			},
		},
	}
	chunks, err := Build(doc, Options{Mode: ModeParagraph})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(chunks) != 4 {
		t.Fatalf("len(chunks) = %d, want 4", len(chunks))
	}
	want := []layout.BlockKind{
		layout.BlockHeading, layout.BlockParagraph, layout.BlockParagraph, layout.BlockParagraph,
	}
	for i, w := range want {
		if chunks[i].Kind != w {
			t.Errorf("chunks[%d].Kind = %s, want %s", i, chunks[i].Kind, w)
		}
	}
}

// TestBuild_SkipHeadersFooters_GracefullyHandlesErrNotImplemented
// drives a fakeDoc whose PageHeadersFooters returns
// ErrPageMethodNotImplemented; Build still emits body chunks without
// erroring.
func TestBuild_SkipHeadersFooters_GracefullyHandlesErrNotImplemented(t *testing.T) {
	t.Parallel()
	doc := &fakeDoc{
		pages: [][]layout.Block{
			{
				{Kind: layout.BlockParagraph, PageIndex: 0,
					Lines: []layout.Line{{Runs: []text.TextRun{txtRun("body chunk after skip")}}}},
			},
		},
		headersErr: map[int]error{0: ErrPageMethodNotImplemented},
	}
	chunks, err := Build(doc, Options{Mode: ModeParagraph, SkipHeadersFooters: true})
	if err != nil {
		t.Fatalf("Build returned error on ErrPageMethodNotImplemented: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
}

// TestBuild_EmptyPagesSkippedSilently runs a 3-page fakeDoc where
// page 1 has 0 blocks; Build returns chunks from pages 0 and 2 only,
// no error.
func TestBuild_EmptyPagesSkippedSilently(t *testing.T) {
	t.Parallel()
	doc := &fakeDoc{
		pages: [][]layout.Block{
			{
				{Kind: layout.BlockParagraph, PageIndex: 0,
					Lines: []layout.Line{{Runs: []text.TextRun{txtRun("page 0 content")}}}},
			},
			{}, // empty page 1
			{
				{Kind: layout.BlockParagraph, PageIndex: 2,
					Lines: []layout.Line{{Runs: []text.TextRun{txtRun("page 2 content")}}}},
			},
		},
	}
	chunks, err := Build(doc, Options{Mode: ModeParagraph})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2 (page 1 empty, skipped)", len(chunks))
	}
	if chunks[0].Text != "page 0 content" || chunks[1].Text != "page 2 content" {
		t.Errorf("texts = %q / %q", chunks[0].Text, chunks[1].Text)
	}
}

// TestBuild_MinChunkCharsDropsShortChunks pins the locked Q4 drop
// semantics: chunks whose Text length is below MinChunkChars are
// dropped entirely. Recursively filters Children.
func TestBuild_MinChunkCharsDropsShortChunks(t *testing.T) {
	t.Parallel()
	doc := &fakeDoc{
		pages: [][]layout.Block{
			{
				{Kind: layout.BlockParagraph, PageIndex: 0,
					Lines: []layout.Line{{Runs: []text.TextRun{txtRun("ok")}}}}, // 2 chars — drop
				{Kind: layout.BlockParagraph, PageIndex: 0,
					Lines: []layout.Line{{Runs: []text.TextRun{txtRun("plenty long enough")}}}}, // keep
				{Kind: layout.BlockParagraph, PageIndex: 0,
					Lines: []layout.Line{{Runs: []text.TextRun{txtRun("hi")}}}}, // 2 chars — drop
			},
		},
	}
	chunks, err := Build(doc, Options{Mode: ModeParagraph, MinChunkChars: 10})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1 (two below threshold dropped)", len(chunks))
	}
	if chunks[0].Text != "plenty long enough" {
		t.Errorf("kept chunk text = %q, want 'plenty long enough'", chunks[0].Text)
	}
}

// TestBuild_SectionModeRoundTripPreservesParagraphContent is the
// round-trip property: section-mode bodies (flattened, skipping
// headings + the synthetic root) match paragraph-mode bodies as a
// multiset. Sanity check that no body content is dropped by the
// section walk.
func TestBuild_SectionModeRoundTripPreservesParagraphContent(t *testing.T) {
	t.Parallel()
	blocks := syntheticHierarchyFixture()
	doc := &fakeDoc{pages: [][]layout.Block{blocks}}

	parOpts := DefaultOptions
	parOpts.Mode = ModeParagraph
	paraChunks, err := Build(doc, parOpts)
	if err != nil {
		t.Fatalf("Build(paragraph): %v", err)
	}

	secOpts := DefaultOptions
	secOpts.Mode = ModeSection
	secChunks, err := Build(doc, secOpts)
	if err != nil {
		t.Fatalf("Build(section): %v", err)
	}

	bodyTextsPara := bodyTextsFlat(paraChunks)
	bodyTextsSec := bodyTextsRecursive(secChunks)
	if !sameTextMultiset(bodyTextsPara, bodyTextsSec) {
		t.Errorf("section-mode round-trip dropped/reordered content:\n  paragraph: %q\n  section:   %q", bodyTextsPara, bodyTextsSec)
	}
}
