package layout_test

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
)

// TestSensitivity_LineMargin_MonotonicNonIncreasing sweeps the
// LineMargin parameter from 0.3 to 1.0 in 0.1 steps and asserts the
// resulting block-count sequence is monotonically non-increasing
// (counts[i] <= counts[i-1] for all i ≥ 1). Per Q4 lock 2026-05-04
// the criterion is non-increase with ties — strict-decrease would
// be fragile on a 3-line fixture.
//
// In the median-based hybrid, LineMargin scales the per-page
// medianHeight; tightening LineMargin can split lines that would
// otherwise cluster, generally driving more (or equal) blocks.
// Loosening LineMargin can only merge more aggressively, never
// produce more blocks at the same input.
func TestSensitivity_LineMargin_MonotonicNonIncreasing(t *testing.T) {
	t.Parallel()

	doc, err := pdf.OpenFile("../testdata/t4_paragraph_simple.pdf")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()
	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}

	margins := []float64{0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0}
	counts := make([]int, len(margins))
	for i, lm := range margins {
		params := pdf.LayoutParams{
			LineMargin:        lm,
			CharMargin:        2.0,
			WordMargin:        0.1,
			BoxesFlow:         0.5,
			ParagraphGapRatio: 1.6,
		}
		blocks, err := page.BlocksWithParams(params)
		if err != nil {
			t.Fatalf("BlocksWithParams(LineMargin=%v): %v", lm, err)
		}
		counts[i] = len(blocks)
	}

	t.Logf("LineMargin sweep counts: margins=%v counts=%v", margins, counts)
	for i := 1; i < len(counts); i++ {
		if counts[i] > counts[i-1] {
			t.Errorf("non-increase violated at i=%d: counts[%d]=%d > counts[%d]=%d (margins=%v counts=%v)",
				i, i, counts[i], i-1, counts[i-1], margins, counts)
		}
	}
}
