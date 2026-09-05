package layout

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// mkSizedRun builds a run at (x, y) with an explicit height, so a
// raised run can be given the smaller box a superscript really has.
func mkSizedRun(x, y, w, h float64, txt string) text.TextRun {
	g := make([]uint16, len(txt))
	for i := range g {
		g[i] = uint16(txt[i])
	}
	return text.TextRun{
		Text: txt, Glyphs: g,
		X: x, Y: y, Width: w, Height: h, Size: h,
		FontKey: "F1", FontName: "Helvetica",
	}
}

// joinLines concatenates every run of every line, in order.
func joinLines(lines []Line) string {
	var sb strings.Builder
	for _, l := range lines {
		for _, r := range l.Runs {
			sb.WriteString(r.Text)
		}
	}
	return sb.String()
}

// TestLinesFromRuns_WithinLineOrderIsByX pins the property a
// hand-rolled "sort by Y, then band" grouper silently loses.
//
// A superscript, footnote marker or ordinal sits a few points ABOVE the
// baseline of the word it annotates. Ordering a line by Y therefore
// puts it FIRST, and the element emits "12text" where the page reads
// "text12". Measured on the grouper this function replaced: the first
// case below emitted "12text more" and the second "ba ".
func TestLinesFromRuns_WithinLineOrderIsByX(t *testing.T) {
	t.Parallel()
	page := PageInfo{MediaBox: letterMB}

	t.Run("raised_marker_stays_after_its_word", func(t *testing.T) {
		t.Parallel()
		lines, err := LinesFromRuns([]text.TextRun{
			mkSizedRun(100, 700, 30, 12, "text"),
			mkSizedRun(140, 704, 10, 7, "12"),
			mkSizedRun(150, 700, 30, 12, " more"),
		}, page, DefaultLayoutParams)
		if err != nil {
			t.Fatalf("LinesFromRuns: %v", err)
		}
		got := joinLines(lines)
		t.Logf("raised marker: lines=%d text=%q", len(lines), got)
		iText, iMark := strings.Index(got, "text"), strings.Index(got, "12")
		if iText < 0 || iMark < 0 {
			t.Fatalf("text %q lost a run", got)
		}
		if iMark < iText {
			t.Errorf("text %q puts the raised marker BEFORE the word it annotates; within-line order is not by X", got)
		}
	})

	t.Run("sub_point_baseline_jitter_stays_by_x", func(t *testing.T) {
		t.Parallel()
		// TWO runs, which is the case that matters. An earlier version
		// of this test used three to get past the page-scale few-runs
		// short-circuit, which routed the test around the guard
		// instead of fixing it and left the two-run element — the
		// common one — unobserved. The guard now lives only on the
		// page-scale entry point, so two runs band here.
		lines, err := LinesFromRuns([]text.TextRun{
			mkSizedRun(100, 700, 12, 12, "a "),
			mkSizedRun(120, 700.3, 12, 12, "b"),
		}, page, DefaultLayoutParams)
		if err != nil {
			t.Fatalf("LinesFromRuns: %v", err)
		}
		got := joinLines(lines)
		t.Logf("baseline jitter: lines=%d text=%q", len(lines), got)
		if len(lines) != 1 {
			t.Errorf("two runs whose baselines differ by a third of a point split into %d lines, want 1", len(lines))
		}
		if got != "a b" {
			t.Errorf("text = %q, want %q", got, "a b")
		}
	})
}

// TestLinesFromRuns_RotationIsNormalized pins that a rotated page is
// read in its own frame rather than in raw coordinates. The grouper
// this replaced did no rotation normalization at all, so a 90-degree
// page came back with its runs reversed.
func TestLinesFromRuns_RotationIsNormalized(t *testing.T) {
	t.Parallel()
	// On a 90-degree page the visual line runs along the user-space Y
	// axis, so two runs at the same X and increasing Y are consecutive
	// words of one line once normalized.
	runs := []text.TextRun{
		mkSizedRun(300, 200, 30, 12, "first"),
		mkSizedRun(300, 240, 30, 12, "second"),
	}
	rotated, err := LinesFromRuns(runs, PageInfo{MediaBox: letterMB, Rotation: 90}, DefaultLayoutParams)
	if err != nil {
		t.Fatalf("LinesFromRuns(rot=90): %v", err)
	}
	got := joinLines(rotated)
	t.Logf("rotation 90: lines=%d text=%q", len(rotated), got)
	if strings.Index(got, "first") > strings.Index(got, "second") {
		t.Errorf("rotation 90 emitted %q, which reverses the two runs; the page frame was not normalized", got)
	}

	// An invalid rotation is refused rather than silently mis-read, and
	// the message names the package rather than a function this caller
	// never called: a reader who reaches LinesFromRuns and is told
	// "layout.Cluster: ..." goes looking in the wrong place.
	_, err = LinesFromRuns(runs, PageInfo{MediaBox: letterMB, Rotation: 45}, DefaultLayoutParams)
	if err == nil {
		t.Fatal("rotation 45 returned no error; an invalid page frame must be refused")
	}
	if msg := err.Error(); !strings.Contains(msg, "invalid PageInfo.Rotation 45") || strings.Contains(msg, "Cluster") {
		t.Errorf("error = %q, want it to name the offending value and NOT a function the caller did not call", msg)
	}
}

// TestLinesFromRuns_ElementScaleDoesNotSplitTwoRuns pins the boundary
// the page-scale few-runs guard gets wrong for a structure element.
//
// groupRunsToLines refuses to band fewer than three runs, because a
// median drawn from one or two samples is dominated by its own
// outliers — sound at PAGE scale, where fewer than three runs means a
// near-empty page. At ELEMENT scale two runs is ordinary: a heading
// with one bold word, a table cell, a running header, a mid-word font
// switch. Splitting them fabricates a line break the document does not
// contain, and the joiner downstream then renders it as a space or a
// newline.
//
// Measured before the split, through this same entry point: "Frame" and
// "work" on one baseline came back as two lines and joined as "Frame
// work"; two runs handed in bottom-first came back bottom-first.
func TestLinesFromRuns_ElementScaleDoesNotSplitTwoRuns(t *testing.T) {
	t.Parallel()
	page := PageInfo{MediaBox: letterMB}

	t.Run("two_runs_one_baseline_are_one_line", func(t *testing.T) {
		t.Parallel()
		lines, err := LinesFromRuns([]text.TextRun{
			mkSizedRun(100, 700, 30, 12, "Frame"),
			mkSizedRun(140, 700, 24, 12, "work"),
		}, page, DefaultLayoutParams)
		if err != nil {
			t.Fatalf("LinesFromRuns: %v", err)
		}
		t.Logf("two runs on one baseline: lines=%d text=%q", len(lines), joinLines(lines))
		if len(lines) != 1 {
			t.Errorf("two runs on ONE baseline produced %d lines, want 1 - a line break was fabricated inside a word", len(lines))
		}
		if got := joinLines(lines); got != "Framework" {
			t.Errorf("text = %q, want %q", got, "Framework")
		}
	})

	t.Run("two_runs_bottom_first_come_back_top_first", func(t *testing.T) {
		t.Parallel()
		// Reading order is a property of geometry, not of the order the
		// content stream happened to emit.
		lines, err := LinesFromRuns([]text.TextRun{
			mkSizedRun(100, 686, 40, 12, "second"),
			mkSizedRun(100, 700, 30, 12, "first"),
		}, page, DefaultLayoutParams)
		if err != nil {
			t.Fatalf("LinesFromRuns: %v", err)
		}
		got := joinLines(lines)
		t.Logf("bottom-first input: lines=%d text=%q", len(lines), got)
		if len(lines) != 2 {
			t.Fatalf("two runs on distinct baselines produced %d lines, want 2", len(lines))
		}
		if strings.Index(got, "first") > strings.Index(got, "second") {
			t.Errorf("text = %q, want the top line first - bottom-first input came back bottom-first", got)
		}
	})
}

// TestBlocksFromRuns_ElementScaleAgainstPageScale pins the one
// difference between the two block-producing entry points, inside the
// package that owns both.
//
// Until this existed the guard was defended only from structtree:
// deleting it left every layout test green and only a downstream
// package went red, which is the wrong place to learn that a
// layout-package invariant moved.
//
// The subject is two runs sharing ONE baseline, because that is the
// input the two scales disagree about. At page scale the few-runs
// short-circuit gives each run its own line, which is a deliberate
// refusal to trust a two-sample median for a whole page. At element
// scale they band, because two runs on one baseline inside a single
// structure element is one rendered line, not two.
func TestBlocksFromRuns_ElementScaleAgainstPageScale(t *testing.T) {
	t.Parallel()
	page := PageInfo{MediaBox: letterMB}
	runs := []text.TextRun{
		mkSizedRun(100, 60, 70, 12, "Chapter 3 | "),
		mkSizedRun(172, 60, 12, 12, "42"),
	}

	countLines := func(bs []Block) int {
		n := 0
		for _, b := range bs {
			n += len(b.Lines)
		}
		return n
	}

	element, err := BlocksFromRuns(runs, page, DefaultLayoutParams)
	if err != nil {
		t.Fatalf("BlocksFromRuns: %v", err)
	}
	if len(element) != 1 || countLines(element) != 1 {
		t.Errorf("element scale gave %d blocks / %d lines, want 1 / 1 - two runs on one baseline are one rendered line",
			len(element), countLines(element))
	}

	pageScale, err := ClusterWithParams(runs, page, DefaultLayoutParams)
	if err != nil {
		t.Fatalf("ClusterWithParams: %v", err)
	}
	if countLines(pageScale) != 2 {
		t.Errorf("page scale gave %d lines, want 2 - the few-runs guard is the page entry point's contract",
			countLines(pageScale))
	}

	t.Logf("element scale: %d blocks / %d lines; page scale: %d blocks / %d lines",
		len(element), countLines(element), len(pageScale), countLines(pageScale))
}
