package layout

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// mkRunAt builds a TextRun at (x, y) with the given width and text,
// pinned to 12pt (Height + Size both 12). All cluster_test cases
// run at 12pt body text; if a future test needs a different size,
// promote sz back to a parameter.
func mkRunAt(x, y, w float64, txt string) text.TextRun {
	g := make([]uint16, len(txt))
	for i := range g {
		g[i] = uint16(txt[i])
	}
	return text.TextRun{
		Text:     txt,
		Glyphs:   g,
		X:        x,
		Y:        y,
		Width:    w,
		Height:   12,
		Size:     12,
		FontKey:  "F1",
		FontName: "Helvetica",
	}
}

var letterMB = Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}

func TestCluster_NilRuns_EmptyResult(t *testing.T) {
	t.Parallel()
	out, err := Cluster(nil, PageInfo{MediaBox: letterMB})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(out) != 0 {
		t.Errorf("blocks = %v, want empty", out)
	}
}

func TestCluster_EmptyRuns_EmptyResult(t *testing.T) {
	t.Parallel()
	out, err := Cluster([]text.TextRun{}, PageInfo{MediaBox: letterMB})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(out) != 0 {
		t.Errorf("blocks = %v, want empty", out)
	}
}

func TestCluster_FewRunsShortCircuit(t *testing.T) {
	t.Parallel()
	// 2 runs at different X-starts AND different Y-baselines. Stage 1
	// short-circuits (Rule 1.5: n<3) → 2 distinct Lines. Stage 2 then
	// applies its rules: gap = 20pt, medianGap = 20, paragraphGap =
	// 32; X-start mismatch (72 vs 200, |Δ|=128 > CharMargin × avg ≈
	// 12.5) drives a block split. Expect 2 blocks.
	runs := []text.TextRun{
		mkRunAt(72, 700, 30, "abc"),
		mkRunAt(200, 720, 30, "def"),
	}
	out, err := Cluster(runs, PageInfo{MediaBox: letterMB})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("blocks = %d, want 2 (n<3 → 2 lines → X-start mismatch → 2 blocks)", len(out))
	}
}

func TestCluster_TypicalParagraph_ThreeLinesOneBlock(t *testing.T) {
	t.Parallel()
	// 9 runs across 3 baselines (Y=700, 686, 672 in PDF bottom-up).
	// All same X0=72, line height 12 → tight spacing. After flipY
	// the lines are top-down at flipped-Y centers ~80, 94, 108.
	runs := []text.TextRun{}
	for _, y := range []float64{700, 686, 672} {
		for _, x := range []float64{72, 90, 108} {
			runs = append(runs, mkRunAt(x, y, 18, "abc"))
		}
	}
	out, err := Cluster(runs, PageInfo{MediaBox: letterMB})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("blocks = %d, want 1; out=%+v", len(out), out)
	}
	if got := len(out[0].Lines); got != 3 {
		t.Errorf("len(block0.Lines) = %d, want 3", got)
	}
}

func TestClusterWithParams_TightThresholds_BreaksIntoBlocks(t *testing.T) {
	t.Parallel()
	// 9 runs across 3 Y-baselines (in PDF bottom-up coords, smaller
	// Y means lower on the page); using Y=700/680/660 gives 20pt
	// vertical separation. Default-cluster groups all 3 lines into
	// one block. Tightening BOTH LineMargin and ParagraphGapRatio
	// makes Stage 2's paragraphGap smaller than the per-pair gap,
	// driving multiple blocks.
	runs := []text.TextRun{}
	for _, y := range []float64{700, 680, 660} {
		for _, x := range []float64{72, 90, 108} {
			runs = append(runs, mkRunAt(x, y, 18, "abc"))
		}
	}
	pi := PageInfo{MediaBox: letterMB}
	defaultOut, _ := Cluster(runs, pi)
	tight := DefaultLayoutParams
	tight.LineMargin = 0.05
	tight.ParagraphGapRatio = 0.5
	tightOut, err := ClusterWithParams(runs, pi, tight)
	if err != nil {
		t.Fatalf("tight cluster err: %v", err)
	}
	if len(tightOut) <= len(defaultOut) {
		t.Errorf("tight thresholds should produce more blocks; tight=%d default=%d",
			len(tightOut), len(defaultOut))
	}
}

func TestCluster_AllRotations(t *testing.T) {
	t.Parallel()
	runs := []text.TextRun{
		mkRunAt(72, 700, 30, "abc"),
		mkRunAt(72, 686, 30, "def"),
		mkRunAt(72, 672, 30, "ghi"),
	}
	for _, rot := range []int{0, 90, 180, 270} {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			out, err := Cluster(runs, PageInfo{MediaBox: letterMB, Rotation: rot})
			if err != nil {
				t.Fatalf("rot=%d: err = %v", rot, err)
			}
			if len(out) == 0 {
				t.Errorf("rot=%d: no blocks emitted", rot)
			}
			if rot == 0 && len(out) > 0 && out[0].BBox.X0 != 72 {
				t.Errorf("rot=0: block.BBox.X0 = %v, want 72", out[0].BBox.X0)
			}
		})
	}
}

func TestCluster_InvalidRotation_Error(t *testing.T) {
	t.Parallel()
	_, err := Cluster([]text.TextRun{mkRunAt(72, 700, 30, "abc")}, PageInfo{MediaBox: letterMB, Rotation: 45})
	if err == nil {
		t.Fatalf("expected error for invalid rotation, got nil")
	}
	if !strings.Contains(err.Error(), "Rotation") && !strings.Contains(err.Error(), "rotation") {
		t.Errorf("error message = %q, want substring 'rotation'", err.Error())
	}
}

func TestCluster_DehyphenationAcrossLinesInBlock(t *testing.T) {
	t.Parallel()
	// 6 runs (n>=3) on 2 baselines with matching X0 — one block,
	// two lines. Line 0's last (X-sorted) run text ends "inter-";
	// line 1's first run text starts "national".
	runs := []text.TextRun{
		mkRunAt(72, 700, 30, "in"),
		mkRunAt(102, 700, 30, "ter-"),
		mkRunAt(132, 700, 30, "x"),
		mkRunAt(72, 686, 30, "nat"),
		mkRunAt(102, 686, 30, "ion"),
		mkRunAt(132, 686, 30, "al"),
	}
	// Patch the last run of the upper Y to end with "-" so it's the
	// trailing run after X-sort. We use the helper: replace mkRunAt
	// for index 2 with text "ter-" at X=132.
	runs[2] = mkRunAt(132, 700, 30, "ter-")
	out, err := Cluster(runs, PageInfo{MediaBox: letterMB})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var found bool
	for _, b := range out {
		if len(b.Lines) < 2 {
			continue
		}
		if b.Lines[0].WasDehyphenated {
			found = true
			lastRun := b.Lines[0].Runs[len(b.Lines[0].Runs)-1]
			if strings.HasSuffix(lastRun.Text, "-") {
				t.Errorf("dehyphenated line still ends with '-': %q", lastRun.Text)
			}
			if !strings.HasPrefix(b.Lines[1].Runs[0].Text, "nat") {
				t.Errorf("line 1 first run = %q, want prefix 'nat'", b.Lines[1].Runs[0].Text)
			}
		}
	}
	if !found {
		t.Errorf("no block with WasDehyphenated=true; out=%+v", out)
	}
}

func TestCluster_DehyphenationDoesNotCrossBlockBoundary(t *testing.T) {
	t.Parallel()
	// Two distinct X-start groups: column A at X=72 ends with "pre-";
	// column B at X=300 starts with "fix". Different X-start means
	// the lines land in different blocks, so dehyphenation MUST NOT
	// fire across the boundary.
	runs := []text.TextRun{
		mkRunAt(72, 700, 30, "abc"),
		mkRunAt(72, 686, 30, "pre-"),
		mkRunAt(72, 672, 30, "abc"),
		mkRunAt(300, 700, 30, "fix"),
		mkRunAt(300, 686, 30, "abc"),
		mkRunAt(300, 672, 30, "abc"),
	}
	out, err := Cluster(runs, PageInfo{MediaBox: letterMB})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// No block should have a "pre-"-ending line marked WasDehyphenated.
	for _, b := range out {
		for _, l := range b.Lines {
			if l.WasDehyphenated {
				t.Errorf("unexpected cross-block dehyphenation: block %+v line %+v", b.BBox, l)
			}
		}
	}
}

func TestCluster_PageIndexPropagated(t *testing.T) {
	t.Parallel()
	runs := []text.TextRun{
		mkRunAt(72, 700, 30, "abc"),
		mkRunAt(72, 686, 30, "def"),
		mkRunAt(72, 672, 30, "ghi"),
	}
	out, err := Cluster(runs, PageInfo{MediaBox: letterMB, PageIndex: 42})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for i, b := range out {
		if b.PageIndex != 42 {
			t.Errorf("block[%d].PageIndex = %d, want 42", i, b.PageIndex)
		}
	}
}

func TestCluster_BlockKindDefaultsToUnknown(t *testing.T) {
	t.Parallel()
	runs := []text.TextRun{
		mkRunAt(72, 700, 30, "abc"),
		mkRunAt(72, 686, 30, "def"),
		mkRunAt(72, 672, 30, "ghi"),
	}
	out, _ := Cluster(runs, PageInfo{MediaBox: letterMB})
	for i, b := range out {
		if b.Kind != BlockUnknown {
			t.Errorf("block[%d].Kind = %q, want BlockUnknown", i, b.Kind)
		}
	}
}
