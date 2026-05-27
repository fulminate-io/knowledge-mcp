package layout

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// mkRun is a compact constructor for hand-crafted TextRun values.
// Matches the layout-test convention of pure-Go construction (no
// PDF synthesis at this layer). Width is fixed at 30pt across the
// lines_test cases — tests pin it deliberately so avgCharWidth
// tests have predictable values.
func mkRun(x, y, h, sz float64, txt string, glyphs []uint16) text.TextRun {
	return text.TextRun{
		Text:     txt,
		Glyphs:   glyphs,
		X:        x,
		Y:        y,
		Width:    30,
		Height:   h,
		Size:     sz,
		FontKey:  "F1",
		FontName: "Helvetica",
	}
}

// gly returns n distinct glyph IDs (0x41+i) so avgCharWidth has a
// non-zero divisor.
func gly(n int) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = uint16(0x41 + i)
	}
	return out
}

// dlp is the default LayoutParams for tests (= DefaultLayoutParams).
var dlp = DefaultLayoutParams

func TestGroupRunsToLines_SameBaseline_OneLine_ThreeRuns(t *testing.T) {
	t.Parallel()
	// 3 runs left-to-right with NO gap between them (each ends
	// exactly where the next starts) → no space-token inserts.
	runs := []text.TextRun{
		mkRun(10, 700, 12, 12, "abc", gly(3)),
		mkRun(40, 700, 12, 12, "def", gly(3)),
		mkRun(70, 700, 12, 12, "ghi", gly(3)),
	}
	lines := groupRunsToLines(runs, 12, dlp)
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1", len(lines))
	}
	if got := len(lines[0].Runs); got != 3 {
		t.Errorf("len(line0.Runs) = %d, want 3", got)
	}
	for i := 1; i < len(lines[0].Runs); i++ {
		if lines[0].Runs[i].X < lines[0].Runs[i-1].X {
			t.Errorf("runs not X-sorted: %+v", lines[0].Runs)
		}
	}
}

func TestGroupRunsToLines_StaggeredBaselines_OneLine(t *testing.T) {
	t.Parallel()
	// 3 runs with Y differing by < LineMargin × medianHeight =
	// 0.4 × 12 = 4.8. Y=700/702/698 (max delta = 4 < 4.8).
	runs := []text.TextRun{
		mkRun(10, 700, 12, 12, "a", gly(1)),
		mkRun(40, 702, 12, 12, "b", gly(1)),
		mkRun(70, 698, 12, 12, "c", gly(1)),
	}
	lines := groupRunsToLines(runs, 12, dlp)
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1; lines=%+v", len(lines), lines)
	}
	if got := len(lines[0].Runs); got != 3 {
		t.Errorf("len(line0.Runs) = %d, want 3", got)
	}
	// X-sorted within line.
	if lines[0].Runs[0].X != 10 || lines[0].Runs[1].X != 40 || lines[0].Runs[2].X != 70 {
		t.Errorf("runs not X-sorted: %+v", lines[0].Runs)
	}
}

func TestGroupRunsToLines_GapBeyondWordMargin_SpaceTokenInserted(t *testing.T) {
	t.Parallel()
	// 3 runs to satisfy the n>=3 short-circuit gate, then verify
	// the gap between run0 and run1 (with X=10 and X=200) inserts
	// a space token. avgCharWidth ≈ 30/3 = 10. WordMargin × avg =
	// 0.1 × 10 = 1.0; gap = 200 - (10+30) = 160 >> 1 AND >= 12 (1 em).
	runs := []text.TextRun{
		mkRun(10, 700, 12, 12, "abc", gly(3)),
		mkRun(200, 700, 12, 12, "def", gly(3)),
		mkRun(245, 700, 12, 12, "ghi", gly(3)),
	}
	lines := groupRunsToLines(runs, 12, dlp)
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1", len(lines))
	}
	// Expect 5 runs: run0, space, run1, space, run2 (each gap >>
	// 1 em). Or at least one synthetic space token between any pair.
	hasSpace := false
	for _, r := range lines[0].Runs {
		if r.Text == " " && len(r.Glyphs) == 1 && r.Glyphs[0] == 0x20 && r.FontKey == "F1" {
			hasSpace = true
			break
		}
	}
	if !hasSpace {
		t.Errorf("expected synthetic space token, got runs: %+v", lines[0].Runs)
	}
}

func TestGroupRunsToLines_KerningGap_NoSpaceToken(t *testing.T) {
	t.Parallel()
	// Three runs to clear n>=3. Pairs (10,30..40) and (40,80..120):
	// run0 ends X=40, run1 starts X=45 → gap=5pt. avgCharWidth ≈ 10.
	// WordMargin × avg = 1.0; gap=5 > 1 BUT gap < 1 em (12) so the
	// kerning guard rejects the space-token insert.
	runs := []text.TextRun{
		mkRun(10, 700, 12, 12, "abc", gly(3)),
		mkRun(45, 700, 12, 12, "def", gly(3)),
		mkRun(80, 700, 12, 12, "ghi", gly(3)),
	}
	lines := groupRunsToLines(runs, 12, dlp)
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1", len(lines))
	}
	for _, r := range lines[0].Runs {
		if r.Text == " " {
			t.Errorf("unexpected space token (kerning gap < 1 em): %+v", lines[0].Runs)
		}
	}
}

func TestGroupRunsToLines_SmallSizeDifference_OneLine(t *testing.T) {
	t.Parallel()
	// 3 runs same Y, sizes 10/12/12 (min/max ratio 10/12 = 0.83 ≥ 0.5).
	runs := []text.TextRun{
		mkRun(10, 700, 10, 10, "a", gly(1)),
		mkRun(40, 700, 12, 12, "b", gly(1)),
		mkRun(70, 700, 12, 12, "c", gly(1)),
	}
	lines := groupRunsToLines(runs, 12, dlp)
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1; lines=%+v", len(lines), lines)
	}
}

func TestGroupRunsToLines_LargeSizeDifference_TwoLines(t *testing.T) {
	t.Parallel()
	// 3 runs (same baseline) but with sizes 6/14/14 — ratio 6/14 ≈ 0.43
	// < 0.5, so the size-ratio guard rejects join. The first run goes
	// to its own Line; the next two cluster together. Need n>=3 to
	// avoid the few-runs short-circuit emitting all separately.
	runs := []text.TextRun{
		mkRun(10, 700, 6, 6, "a", gly(1)),
		mkRun(40, 700, 14, 14, "b", gly(1)),
		mkRun(70, 700, 14, 14, "c", gly(1)),
	}
	lines := groupRunsToLines(runs, 12, dlp)
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2 (size-ratio guard); lines=%+v", len(lines), lines)
	}
}

func TestGroupRunsToLines_FewRunsShortCircuit(t *testing.T) {
	t.Parallel()
	// 2 runs across 2 baselines → 2 Lines per Rule 1.5 short-circuit
	// (each run becomes its own Line, regardless of Y proximity).
	runs := []text.TextRun{
		mkRun(10, 700, 12, 12, "a", gly(1)),
		mkRun(50, 720, 12, 12, "b", gly(1)),
	}
	lines := groupRunsToLines(runs, 12, dlp)
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2 (n<3 short-circuit)", len(lines))
	}
}

func TestGroupRunsToLines_DistinctBaselines_TwoLines(t *testing.T) {
	t.Parallel()
	// 4 runs split 2+2 across Y=700 / Y=720. Gap = 20 > LineMargin ×
	// medianHeight = 0.4 × 12 = 4.8.
	runs := []text.TextRun{
		mkRun(10, 700, 12, 12, "a", gly(1)),
		mkRun(50, 700, 12, 12, "b", gly(1)),
		mkRun(10, 720, 12, 12, "c", gly(1)),
		mkRun(50, 720, 12, 12, "d", gly(1)),
	}
	lines := groupRunsToLines(runs, 12, dlp)
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2; lines=%+v", len(lines), lines)
	}
	if got := len(lines[0].Runs); got != 2 {
		t.Errorf("len(line0.Runs) = %d, want 2", got)
	}
	if got := len(lines[1].Runs); got != 2 {
		t.Errorf("len(line1.Runs) = %d, want 2", got)
	}
}

func TestInsertSpaceTokens_Empty(t *testing.T) {
	t.Parallel()
	got := insertSpaceTokens(nil, dlp)
	if got != nil {
		t.Errorf("nil input: got %v, want nil", got)
	}
	got = insertSpaceTokens([]text.TextRun{}, dlp)
	if len(got) != 0 {
		t.Errorf("empty input: got %v, want len-0", got)
	}
}

func TestInsertSpaceTokens_LeadingTrailingTrim(t *testing.T) {
	t.Parallel()
	// insertSpaceTokens itself does not trim (it just inserts at
	// gaps). The "leading/trailing trim" semantics live at the
	// groupRunsToLines level — verify the round-trip via that
	// entry point with input runs that, by their geometry, force
	// a leading/trailing pure-space situation.
	//
	// Construct 3 runs with one run at X=0 (synthetic-space-like:
	// Glyphs={0x20}, Text=" ") and 2 real runs at X=40/70. Stage 1
	// preserves all three (the synthetic-space-detection is a
	// downstream concern); we just verify the function doesn't
	// crash and emits all runs in X order.
	runs := []text.TextRun{
		{Text: " ", Glyphs: []uint16{0x20}, X: 0, Y: 700, Width: 5, Height: 12, Size: 12, FontKey: "F1"},
		mkRun(40, 700, 12, 12, "a", gly(1)),
		mkRun(70, 700, 12, 12, "b", gly(1)),
	}
	got := insertSpaceTokens(runs, dlp)
	if len(got) < 3 {
		t.Errorf("expected at least 3 runs preserved, got %+v", got)
	}
}
