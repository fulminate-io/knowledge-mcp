package pdf_test

// accuracy_selftest_test.go: harness self-tests verifying the corpus
// harness behaves correctly on hand-crafted in-memory goldens. No
// dependency on testdata/corpus/ — uses the existing onepage.pdf
// fixture so the self-tests are deterministic and corpus-free.
//
// Per absorbed reviewer finding T3#4: the originally-planned
// "ReversedOrder" self-test (test 4) is intentionally omitted — the
// 1-chunk onepage.pdf permutation can't reach reversed-tau=-1.0
// because NormalizedKendallTau floors n<2 to 0.0. The reversed-order
// behavior is verified directly in the helper package's
// TestNormalizedKendallTau_FullyReversed (kendall_test.go).

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// onePageOpenChunks opens onepage.pdf and returns the chunk slice.
// Helper to avoid repeating boilerplate across the 3 self-tests.
func onePageOpenChunks(t *testing.T) []pdf.Chunk {
	t.Helper()
	doc, err := pdf.OpenFile(onePageFixture)
	if err != nil {
		t.Fatalf("OpenFile(%s): %v", onePageFixture, err)
	}
	t.Cleanup(func() { _ = doc.Close() })
	chunks, err := doc.Chunks(pdf.ChunkOptions{
		Mode: pdf.ChunkModeParagraph,
		LayoutParams: pdf.LayoutParams{
			LineMargin: 0.4, CharMargin: 2.0, WordMargin: 0.1,
			BoxesFlow: 0.5, ParagraphGapRatio: 1.6,
		},
	})
	if err != nil {
		t.Fatalf("doc.Chunks: %v", err)
	}
	return chunks
}

// TestAccuracySelfTest_PerfectMatch_AllMetricsPass: in-memory golden
// matches actual exactly. Every metric scores at the perfect-match
// extreme.
func TestAccuracySelfTest_PerfectMatch_AllMetricsPass(t *testing.T) {
	t.Parallel()
	actual := onePageOpenChunks(t)
	if len(actual) != 1 {
		t.Fatalf("onepage.pdf yielded %d chunks; expected 1", len(actual))
	}
	c := actual[0]
	golden := []goldenChunk{
		{
			Kind:      string(c.Kind),
			Text:      c.Text,
			PageRange: c.PageRange,
			BBox:      [4]float64{c.BBox.X0, c.BBox.Y0, c.BBox.X1, c.BBox.Y1},
		},
	}
	m := scoreMetrics(actual, golden)
	if m.ChunkCountDelta != 0 {
		t.Errorf("chunkCountDelta: got %v want 0", m.ChunkCountDelta)
	}
	if m.BoundaryIoU != 1.0 {
		t.Errorf("boundaryIoU: got %v want 1.0", m.BoundaryIoU)
	}
	if m.ClassificationAccuracy != 1.0 {
		t.Errorf("classificationAccuracy: got %v want 1.0", m.ClassificationAccuracy)
	}
	if m.ReadingOrderKendallTauDivergence != 0 {
		t.Errorf("readingOrderKendallTauDivergence: got %v want 0", m.ReadingOrderKendallTauDivergence)
	}
	if m.TextSimilarityLevenshtein != 0 {
		t.Errorf("textSimilarityLevenshtein: got %v want 0", m.TextSimilarityLevenshtein)
	}
}

// TestAccuracySelfTest_OneChunkSwapped_BoundaryAndOrderDegrade: golden
// adds a fake heading chunk before the real paragraph, with bbox far
// from the paragraph. Asserts the documented divergence shape.
func TestAccuracySelfTest_OneChunkSwapped_BoundaryAndOrderDegrade(t *testing.T) {
	t.Parallel()
	actual := onePageOpenChunks(t)
	if len(actual) != 1 {
		t.Fatalf("onepage.pdf yielded %d chunks; expected 1", len(actual))
	}
	c := actual[0]
	golden := []goldenChunk{
		{
			// Fake heading far from any actual chunk.
			Kind:         string(layout.BlockHeading),
			Text:         "Fake Heading",
			HeadingLevel: 1,
			PageRange:    [2]int{0, 0},
			BBox:         [4]float64{0, 0, 50, 20},
		},
		{
			Kind:      string(c.Kind),
			Text:      c.Text,
			PageRange: c.PageRange,
			BBox:      [4]float64{c.BBox.X0, c.BBox.Y0, c.BBox.X1, c.BBox.Y1},
		},
	}
	m := scoreMetrics(actual, golden)
	// 1 actual vs 2 golden → |1-2|/max(1,2) = 0.5.
	if m.ChunkCountDelta != 0.5 {
		t.Errorf("chunkCountDelta: got %v want 0.5", m.ChunkCountDelta)
	}
	// Asymmetric mean coverage averages over goldens; the fake heading
	// is fully unmatched (cov=0), the real paragraph fully matched
	// (cov=1.0). Mean = 0.5 < 0.85 threshold.
	if m.BoundaryIoU >= 0.85 {
		t.Errorf("boundaryIoU: got %v want < 0.85", m.BoundaryIoU)
	}
	// Classification accuracy: the fake heading's match is the same
	// actual paragraph (low coverage) — kind paragraph != heading.
	// Real paragraph matches the same paragraph (kind matches). 1/2.
	if m.ClassificationAccuracy >= 0.90 {
		t.Errorf("classificationAccuracy: got %v want < 0.90", m.ClassificationAccuracy)
	}
}

// TestAccuracySelfTest_PerfectMatch_KendallTauReturnsOne: actual has
// 3 chunks (synthetic — built directly), golden mirrors them in
// identity order. Kendall-tau = 1.0 → divergence = 0.
//
// This test does NOT need a real PDF; scoreMetrics is pure on the
// chunk + golden slice inputs, so we construct in-memory pdf.Chunks.
func TestAccuracySelfTest_PerfectMatch_KendallTauReturnsOne(t *testing.T) {
	t.Parallel()
	actual := []pdf.Chunk{
		{Kind: layout.BlockParagraph, Text: "first", PageRange: [2]int{0, 0}},
		{Kind: layout.BlockParagraph, Text: "second", PageRange: [2]int{0, 0}},
		{Kind: layout.BlockParagraph, Text: "third", PageRange: [2]int{0, 0}},
	}
	// Set bboxes so coverage matches positionally.
	actual[0].BBox.X0, actual[0].BBox.Y0, actual[0].BBox.X1, actual[0].BBox.Y1 = 0, 0, 100, 20
	actual[1].BBox.X0, actual[1].BBox.Y0, actual[1].BBox.X1, actual[1].BBox.Y1 = 0, 30, 100, 50
	actual[2].BBox.X0, actual[2].BBox.Y0, actual[2].BBox.X1, actual[2].BBox.Y1 = 0, 60, 100, 80
	golden := []goldenChunk{
		{Kind: string(layout.BlockParagraph), Text: "first", PageRange: [2]int{0, 0}, BBox: [4]float64{0, 0, 100, 20}},
		{Kind: string(layout.BlockParagraph), Text: "second", PageRange: [2]int{0, 0}, BBox: [4]float64{0, 30, 100, 50}},
		{Kind: string(layout.BlockParagraph), Text: "third", PageRange: [2]int{0, 0}, BBox: [4]float64{0, 60, 100, 80}},
	}
	m := scoreMetrics(actual, golden)
	if m.ReadingOrderKendallTauDivergence != 0 {
		t.Errorf("readingOrderKendallTauDivergence: got %v want 0", m.ReadingOrderKendallTauDivergence)
	}
	if m.BoundaryIoU != 1.0 {
		t.Errorf("boundaryIoU: got %v want 1.0", m.BoundaryIoU)
	}
}
