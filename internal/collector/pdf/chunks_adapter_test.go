package pdf

import (
	"testing"

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
