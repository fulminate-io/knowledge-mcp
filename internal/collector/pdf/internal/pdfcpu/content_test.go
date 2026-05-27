package pdfcpu

import (
	"testing"
)

// TestContentStream_OnePagePDF_ReturnsNonEmpty exercises the T2 wrapper
// extension: ExtractPageContent on the synthetic fixture should return
// non-empty bytes (the writer emits a stroke-grid content stream of
// roughly 100-300 bytes per page after Flate decode).
func TestContentStream_OnePagePDF_ReturnsNonEmpty(t *testing.T) {
	t.Parallel()
	ctx, err := LoadFile(onePageFixture)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", onePageFixture, err)
	}
	defer ctx.Close()

	page, err := ctx.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	bb, err := page.ContentStream()
	if err != nil {
		t.Fatalf("ContentStream: %v", err)
	}
	if len(bb) == 0 {
		t.Fatalf("ContentStream returned zero bytes for synthesized fixture")
	}
	// Soft sanity: the synthesized fixture's content stream should
	// contain at least one operator. We don't pin a specific operator
	// because pdfcpu's emitted form may evolve; just confirm there's a
	// recognizable PDF operator letter in the byte run.
	hasOp := false
	for _, b := range bb {
		if b == 'l' || b == 'm' || b == 's' || b == 'q' || b == 'Q' {
			hasOp = true
			break
		}
	}
	if !hasOp {
		t.Errorf("ContentStream bytes contain no recognizable operator: %q", bb)
	}
}

// TestContentStream_NilReceiver_ReturnsError covers the defensive nil
// branches that page.go relies on to avoid panics from corrupt graph
// state.
func TestContentStream_NilReceiver_ReturnsError(t *testing.T) {
	t.Parallel()
	var p *PageObject
	if _, err := p.ContentStream(); err == nil {
		t.Errorf("ContentStream on nil PageObject: want error, got nil")
	}
}
