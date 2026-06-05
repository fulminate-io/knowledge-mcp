package layout

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// mkLine builds a Line with the given BBox and runs. Tests pass
// runs sized so avgCharWidth produces sensible values.
func mkLine(x0, y0, x1, y1 float64, runs ...text.TextRun) Line {
	return Line{
		BBox: Rect{X0: x0, Y0: y0, X1: x1, Y1: y1},
		Runs: runs,
	}
}

// runForLine returns a single TextRun whose width and glyph count
// produce avgCharWidth ≈ 6pt (so CharMargin × avg ≈ 12pt). Used
// across blocks_test cases so the X-start tolerance is predictable.
// Width and glyph count are fixed (100pt / 16 glyphs); X is the
// per-call parameter.
func runForLine(x float64) text.TextRun {
	const glyphs = 16
	g := make([]uint16, glyphs)
	for i := range g {
		g[i] = uint16(0x41 + i)
	}
	return text.TextRun{X: x, Width: 100, Glyphs: g, Size: 12}
}

func TestGroupLinesToBlocks_SameXStart_TightSpacing_OneBlock(t *testing.T) {
	t.Parallel()
	// Lines at Y0 = 100, 114, 128 (gap = 14 between centers).
	// medianGap = 14; paragraphGap = 14 × 1.6 = 22.4. All gaps
	// (14) < 22.4 → all 3 lines join one block.
	lines := []Line{
		mkLine(72, 100, 200, 112, runForLine(72)),
		mkLine(72, 114, 200, 126, runForLine(72)),
		mkLine(72, 128, 200, 140, runForLine(72)),
	}
	blocks := groupLinesToBlocks(lines, 0, DefaultLayoutParams)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1; blocks=%+v", len(blocks), blocks)
	}
	if got := len(blocks[0].Lines); got != 3 {
		t.Errorf("len(block0.Lines) = %d, want 3", got)
	}
}

func TestGroupLinesToBlocks_MismatchedXStart_TwoBlocks(t *testing.T) {
	t.Parallel()
	// Lines at X0 = 72, 72, 144. CharMargin × avg ≈ 2.0 × 6.25 = 12.5
	// (avg is per-block; before line 3 joins, block has 2 lines with
	// runs sized at 6pt each). |144 - 72| = 72 >> 12.5 → break.
	lines := []Line{
		mkLine(72, 100, 200, 112, runForLine(72)),
		mkLine(72, 114, 200, 126, runForLine(72)),
		mkLine(144, 128, 240, 140, runForLine(144)),
	}
	blocks := groupLinesToBlocks(lines, 0, DefaultLayoutParams)
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2; blocks=%+v", len(blocks), blocks)
	}
	if got := len(blocks[0].Lines); got != 2 {
		t.Errorf("block0 lines = %d, want 2", got)
	}
	if got := len(blocks[1].Lines); got != 1 {
		t.Errorf("block1 lines = %d, want 1", got)
	}
}

func TestGroupLinesToBlocks_LargeVerticalGap_TwoBlocks(t *testing.T) {
	t.Parallel()
	// Lines at Y centers 100, 114 (gap=14), then 200 (gap=86 from
	// previous). medianGap = median([14, 86]) = (14+86)/2 = 50.
	// paragraphGap = 50 × 1.6 = 80. Gap=86 > 80 → break before
	// line 3. Need 4 lines so the algorithm sees the gap at i=3.
	lines := []Line{
		mkLine(72, 100-6, 200, 100+6, runForLine(72)),
		mkLine(72, 114-6, 200, 114+6, runForLine(72)),
		mkLine(72, 200-6, 200, 200+6, runForLine(72)),
		mkLine(72, 214-6, 200, 214+6, runForLine(72)),
	}
	blocks := groupLinesToBlocks(lines, 0, DefaultLayoutParams)
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2; blocks=%+v", len(blocks), blocks)
	}
}

func TestGroupLinesToBlocks_AllThreeMismatched_ThreeBlocks(t *testing.T) {
	t.Parallel()
	// X-starts 72, 144, 216 — all mismatched within CharMargin × avg.
	// All 3 emit as separate blocks (Rule 2.2 fires twice).
	lines := []Line{
		mkLine(72, 100, 200, 112, runForLine(72)),
		mkLine(144, 114, 280, 126, runForLine(144)),
		mkLine(216, 128, 360, 140, runForLine(216)),
	}
	blocks := groupLinesToBlocks(lines, 0, DefaultLayoutParams)
	if len(blocks) != 3 {
		t.Fatalf("len(blocks) = %d, want 3", len(blocks))
	}
}

func TestGroupLinesToBlocks_BlockBBoxUnion_CorrectlyEnclosesAllLines(t *testing.T) {
	t.Parallel()
	lines := []Line{
		mkLine(72, 100, 200, 112, runForLine(72)),
		mkLine(72, 114, 250, 126, runForLine(72)), // wider X1
		mkLine(80, 128, 220, 140, runForLine(80)), // X0 shifted slightly
	}
	blocks := groupLinesToBlocks(lines, 0, DefaultLayoutParams)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	got := blocks[0].BBox
	want := Rect{X0: 72, Y0: 100, X1: 250, Y1: 140}
	if got != want {
		t.Errorf("block.BBox = %+v, want %+v", got, want)
	}
}

func TestGroupLinesToBlocks_BlockReadingOrder_TopDownThenLeftRight(t *testing.T) {
	t.Parallel()
	// 2 lines stacked top-down at X=72 + 2 lines stacked top-down at
	// X=300 — visually two columns. T4 does NOT detect columns
	// (T5's job); we just verify the emitted blocks are sorted by
	// Y0 ascending. With this arrangement, all 4 lines are mismatched
	// either by X-start (between columns) or vertical gap from
	// medianGap (large gap between rows). Expect 4 separate blocks
	// in Y-ascending order (then X-ascending tiebreak).
	lines := []Line{
		mkLine(72, 100, 200, 112, runForLine(72)),
		mkLine(300, 100, 400, 112, runForLine(300)),
		mkLine(72, 200, 200, 212, runForLine(72)),
		mkLine(300, 200, 400, 212, runForLine(300)),
	}
	blocks := groupLinesToBlocks(lines, 0, DefaultLayoutParams)
	if len(blocks) < 2 {
		t.Fatalf("expected >= 2 blocks, got %d", len(blocks))
	}
	for i := 1; i < len(blocks); i++ {
		prev, curr := blocks[i-1], blocks[i]
		if curr.BBox.Y0 < prev.BBox.Y0 {
			t.Errorf("block[%d] Y0=%v < prev Y0=%v (not top-down sorted)", i, curr.BBox.Y0, prev.BBox.Y0)
		}
		if curr.BBox.Y0 == prev.BBox.Y0 && curr.BBox.X0 < prev.BBox.X0 {
			t.Errorf("block[%d] tied Y0 but X0=%v < prev X0=%v", i, curr.BBox.X0, prev.BBox.X0)
		}
	}
}

func TestGroupLinesToBlocks_PageIndexPropagated(t *testing.T) {
	t.Parallel()
	lines := []Line{
		mkLine(72, 100, 200, 112, runForLine(72)),
		mkLine(72, 114, 200, 126, runForLine(72)),
	}
	blocks := groupLinesToBlocks(lines, 7, DefaultLayoutParams)
	if len(blocks) == 0 {
		t.Fatalf("no blocks emitted")
	}
	for i, b := range blocks {
		if b.PageIndex != 7 {
			t.Errorf("block[%d].PageIndex = %d, want 7", i, b.PageIndex)
		}
		if b.Kind != BlockUnknown {
			t.Errorf("block[%d].Kind = %q, want BlockUnknown", i, b.Kind)
		}
	}
}

func TestGroupLinesToBlocks_EmptyInput_EmptyOutput(t *testing.T) {
	t.Parallel()
	if got := groupLinesToBlocks(nil, 0, DefaultLayoutParams); got != nil {
		t.Errorf("nil input: got %v, want nil", got)
	}
	if got := groupLinesToBlocks([]Line{}, 0, DefaultLayoutParams); len(got) != 0 {
		t.Errorf("empty input: got %v, want len-0", got)
	}
}

// TestGroupLinesToBlocks_SingleLineShortCircuit covers Rule 2.0 — a
// 1-line input must emit a single Block without computing medianGap.
func TestGroupLinesToBlocks_SingleLineShortCircuit(t *testing.T) {
	t.Parallel()
	lines := []Line{mkLine(72, 100, 200, 112, runForLine(72))}
	blocks := groupLinesToBlocks(lines, 3, DefaultLayoutParams)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1 (single-line short-circuit)", len(blocks))
	}
	if got := len(blocks[0].Lines); got != 1 {
		t.Errorf("block0 lines = %d, want 1", got)
	}
	if blocks[0].PageIndex != 3 {
		t.Errorf("block0.PageIndex = %d, want 3", blocks[0].PageIndex)
	}
}
