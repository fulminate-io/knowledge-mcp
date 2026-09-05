package pdf_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
)

const onePageFixture = "testdata/onepage.pdf"

// TestOpen_GarbageInput_TypedErrorNoPanic verifies the typed-error-no-
// panic contract on Open with garbage input. The error must NOT be
// ErrEncrypted (that branch is for password-protected PDFs only) and
// NOT ErrNotImplemented (that branch is for stubbed methods).
func TestOpen_GarbageInput_TypedErrorNoPanic(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Open panicked on garbage input: %v", r)
		}
	}()

	doc, err := pdf.Open(strings.NewReader("not a pdf"))
	if err == nil {
		t.Fatal("expected non-nil error from Open(garbage), got nil")
	}
	if doc != nil {
		t.Errorf("expected nil document on error, got %v", doc)
	}
	if errors.Is(err, pdf.ErrEncrypted) {
		t.Errorf("expected non-encrypted parse error, got ErrEncrypted: %v", err)
	}
	if errors.Is(err, pdf.ErrNotImplemented) {
		t.Errorf("expected parse error, got ErrNotImplemented: %v", err)
	}
}

// TestOpenFile_OnePagePDF_FullSurface walks the working surface (open
// → page count → page metadata → IsTagged → Metadata) on the synthetic
// 1-page fixture. None of these methods are stubbed; T1 ships them.
func TestOpenFile_OnePagePDF_FullSurface(t *testing.T) {
	t.Parallel()

	doc, err := pdf.OpenFile(onePageFixture)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", onePageFixture, err)
	}
	defer doc.Close()

	if got := doc.PageCount(); got != 1 {
		t.Fatalf("PageCount = %d, want 1", got)
	}
	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	if page.Number() != 1 {
		t.Errorf("Number = %d, want 1 (1-indexed)", page.Number())
	}
	mb := page.MediaBox()
	width := mb.X1 - mb.X0
	height := mb.Y1 - mb.Y0
	if width < 100 || height < 100 {
		t.Errorf("MediaBox is degenerate or too small: %+v (w=%v, h=%v)", mb, width, height)
	}
	if page.Rotation() != 0 {
		t.Errorf("Rotation = %d, want 0", page.Rotation())
	}
	if doc.IsTagged() {
		t.Error("IsTagged = true on synthetic untagged fixture, want false")
	}

	// Metadata() must return without panic on a fixture whose Info
	// dict may be empty (the synthetic generator emits no Info entries).
	// Empty fields are acceptable; we just need a stable shape.
	_ = doc.Metadata()
}

// TestOpenFile_StubMethods_ReturnErrNotImplemented confirms each of the
// 5 stubbed methods returns ErrNotImplemented. Originally 8 at T1; T2
// wired Page.TextRuns; T4 wired Page.Blocks + Page.BlocksWithParams.
// Tripwire that breaks loudly if a future ticket lands real behavior
// without removing the stub branch.
func TestOpenFile_StubMethods_ReturnErrNotImplemented(t *testing.T) {
	t.Parallel()

	doc, err := pdf.OpenFile(onePageFixture)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", onePageFixture, err)
	}
	defer doc.Close()

	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}

	// Each of the 8 stubs is exercised. The error must satisfy
	// errors.Is(err, pdf.ErrNotImplemented) so consumers can branch
	// off a single sentinel rather than per-method matching.
	cases := []struct {
		name string
		fn   func() error
	}{
		// Document.Chunks is no longer a stub — T8 wired it to
		// chunk.Build via documentAdapter. Positive coverage lives in
		// collector/pdf/chunks_integration_test.go +
		// collector/pdf/chunks_adapter_test.go.
		// Document.StructTree is no longer a stub — T6 wired it to
		// structtree.Tree. Untagged documents surface
		// structtree.ErrNotTagged, which is its own sentinel; positive
		// coverage lives in collector/pdf/structtree_integration_test.go.
		// Page.TextRuns is no longer a stub — T2 wired it to
		// text.ExtractRuns. Positive coverage lives in
		// TestPage_TextRuns_OnePagePDF below.
		// Page.Blocks + Page.BlocksWithParams are no longer stubs —
		// T4 wired them to layout.Cluster / ClusterWithParams.
		// Positive coverage lives in TestPage_Blocks_* below.
		// Page.HeadersFooters and Page.Footnotes are no longer stubs
		// here because they no longer exist: the skip leg they fed
		// never worked, and running page chrome is now identified by
		// cross-page text repetition and stamped as a signal instead.
		{"Page.ReadingOrder", func() error {
			_, err := page.ReadingOrder()
			return err
		}},
	}
	for _, c := range cases {
		gotErr := c.fn()
		if !errors.Is(gotErr, pdf.ErrNotImplemented) {
			t.Errorf("%s: err = %v, want ErrNotImplemented", c.name, gotErr)
		}
	}
}

// TestPage_TextRuns_OnePagePDF is the T2 positive smoke: TextRuns
// against the synthesized 1-page fixture returns at least one run
// with the expected font key. Detailed assertions live in
// collector/pdf/text/golden_test.go; this test covers the public
// pdf.Page.TextRuns wire-through.
func TestPage_TextRuns_OnePagePDF(t *testing.T) {
	t.Parallel()

	doc, err := pdf.OpenFile(onePageFixture)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", onePageFixture, err)
	}
	defer doc.Close()

	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}

	runs, err := page.TextRuns()
	if err != nil {
		t.Fatalf("TextRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatalf("TextRuns returned no runs for a text-bearing fixture")
	}
	if runs[0].FontKey != "F1" {
		t.Errorf("first run FontKey: got %q, want %q", runs[0].FontKey, "F1")
	}
	if runs[0].Size != 12 {
		t.Errorf("first run Size: got %v, want 12", runs[0].Size)
	}
	// Per T3 ticket DoD: Page.TextRuns chains font.Decode so the
	// Text field is populated with UTF-8 extracted from the page's
	// font dicts. The fixture's body is "Hello, T1".
	if runs[0].Text != "Hello, T1" {
		t.Errorf("first run Text: got %q, want %q", runs[0].Text, "Hello, T1")
	}
}

// TestPage_Blocks_OnePagePDF_PositiveSmoke is the T4 positive smoke:
// Blocks against the synthesized 1-page fixture returns at least one
// Block. Detailed assertions live in collector/pdf/layout/*_test.go;
// this test covers the public pdf.Page.Blocks wire-through.
func TestPage_Blocks_OnePagePDF_PositiveSmoke(t *testing.T) {
	t.Parallel()

	doc, err := pdf.OpenFile(onePageFixture)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", onePageFixture, err)
	}
	defer doc.Close()

	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}

	blocks, err := page.Blocks()
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatalf("Blocks returned no blocks for a text-bearing fixture")
	}
	// onepage.pdf has 1 TextRun ('Hello, T1') — len(runs)<3
	// short-circuit emits 1 Block per run, so we expect exactly 1 Block.
	if len(blocks) != 1 {
		t.Errorf("len(blocks): got %d, want 1 (few-runs short-circuit)", len(blocks))
	}
	if blocks[0].PageIndex != 0 {
		t.Errorf("PageIndex: got %d, want 0", blocks[0].PageIndex)
	}
	if blocks[0].Kind != pdf.BlockUnknown {
		t.Errorf("Kind: got %q, want BlockUnknown (T7 classifies)", blocks[0].Kind)
	}
	if len(blocks[0].Lines) != 1 {
		t.Errorf("len(Lines): got %d, want 1", len(blocks[0].Lines))
	}
	if len(blocks[0].Lines[0].Runs) != 1 {
		t.Errorf("len(Lines[0].Runs): got %d, want 1", len(blocks[0].Lines[0].Runs))
	}
	if blocks[0].Lines[0].Runs[0].Text != "Hello, T1" {
		t.Errorf("Lines[0].Runs[0].Text: got %q, want %q", blocks[0].Lines[0].Runs[0].Text, "Hello, T1")
	}
}

// TestDocument_Classify_DelegatesToPackage is the T7 wire-through
// smoke: Document.Classify takes a slice with a paragraph-shaped block
// and the package classifier annotates Kind. The stub previously
// returned blocks unchanged; T7's wire-through must mutate Kind off
// BlockUnknown for at least one classifiable input.
func TestDocument_Classify_DelegatesToPackage(t *testing.T) {
	t.Parallel()

	doc, err := pdf.OpenFile(onePageFixture)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()
	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	blocks, err := page.Blocks()
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatalf("no blocks for fixture")
	}
	for _, b := range blocks {
		if b.Kind != pdf.BlockUnknown {
			t.Fatalf("pre-classify Kind = %q, want BlockUnknown", b.Kind)
		}
	}
	out := doc.Classify(blocks)
	if len(out) != len(blocks) {
		t.Fatalf("Classify length mismatch: %d vs %d", len(out), len(blocks))
	}
	// At least one block must end with a non-Unknown Kind. The
	// fixture's single 'Hello, T1' block becomes a BlockParagraph by
	// the orchestrator's default branch.
	any := false
	for _, b := range out {
		if b.Kind != pdf.BlockUnknown {
			any = true
		}
	}
	if !any {
		t.Errorf("Classify did not annotate any block; got %+v", out)
	}
}

// TestPage_BlocksWithParams_OnePagePDF_PositiveSmoke is the param
// variant of the smoke test. Confirms the public surface accepts a
// custom LayoutParams and round-trips it to layout.ClusterWithParams.
func TestPage_BlocksWithParams_OnePagePDF_PositiveSmoke(t *testing.T) {
	t.Parallel()

	doc, err := pdf.OpenFile(onePageFixture)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()
	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}

	blocks, err := page.BlocksWithParams(pdf.LayoutParams{LineMargin: 0.4, CharMargin: 2.0, WordMargin: 0.1, BoxesFlow: 0.5, ParagraphGapRatio: 1.6})
	if err != nil {
		t.Fatalf("BlocksWithParams: %v", err)
	}
	if len(blocks) != 1 {
		t.Errorf("len(blocks): got %d, want 1", len(blocks))
	}
}
