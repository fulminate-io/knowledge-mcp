package pdf

import (
	"errors"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/chunk"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// TestDocumentAdapter_PageRuns_ReturnsRunsAndPageInfo loads the
// onepage fixture and confirms documentAdapter.PageRuns returns
// decoded text runs + populated PageInfo (the inputs Phase B's
// layout.Cluster + classify pass needs).
func TestDocumentAdapter_PageRuns_ReturnsRunsAndPageInfo(t *testing.T) {
	t.Parallel()
	doc, err := OpenFile("testdata/onepage.pdf")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()
	adapter := documentAdapter{d: doc, opts: ChunkOptions{
		LayoutParams:   layout.DefaultLayoutParams,
		ClassifyParams: classify.DefaultClassifyParams,
	}}
	pr, err := adapter.PageRuns(0)
	if err != nil {
		t.Fatalf("PageRuns(0): %v", err)
	}
	if len(pr.Runs) == 0 {
		t.Fatal("len(pr.Runs) = 0 on onepage fixture; expected ≥1 run")
	}
	if pr.PageInfo.MediaBox.X1 <= pr.PageInfo.MediaBox.X0 || pr.PageInfo.MediaBox.Y1 <= pr.PageInfo.MediaBox.Y0 {
		t.Fatalf("PageInfo.MediaBox empty: %+v", pr.PageInfo.MediaBox)
	}
}

// TestDocumentAdapter_HeadersFooters_TranslatesErrNotImplemented
// drives documentAdapter.PageHeadersFooters on the onepage fixture;
// Page.HeadersFooters currently returns ErrNotImplemented (T5
// pending). Adapter must translate that to
// chunk.ErrPageMethodNotImplemented so chunk.Build's errors.Is
// check fires.
func TestDocumentAdapter_HeadersFooters_TranslatesErrNotImplemented(t *testing.T) {
	t.Parallel()
	doc, err := OpenFile("testdata/onepage.pdf")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()
	adapter := documentAdapter{d: doc}
	_, err = adapter.PageHeadersFooters(0)
	if !errors.Is(err, chunk.ErrPageMethodNotImplemented) {
		t.Fatalf("PageHeadersFooters err = %v, want errors.Is(err, chunk.ErrPageMethodNotImplemented)", err)
	}
	// Same for footnotes.
	_, err = adapter.PageFootnotes(0)
	if !errors.Is(err, chunk.ErrPageMethodNotImplemented) {
		t.Fatalf("PageFootnotes err = %v, want errors.Is(err, chunk.ErrPageMethodNotImplemented)", err)
	}
}
