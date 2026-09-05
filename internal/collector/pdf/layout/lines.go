// lines.go: Stage 1 of the T4 layout clusterer — group decoded
// TextRuns into Lines.
//
// Uses pdfminer.six's LAParams threshold surface (layout.py:78-92,
// commit a18de2a9c479b4c847538500017b449ddaec177e at
// github.com/pdfminer/pdfminer.six) with median-based Y-tolerance
// normalization. The default LineMargin = 0.4 is empirically tuned
// against the median-height denominator; pdfminer.six's
// line_margin=0.5 (tuned for a per-line height denominator) is
// rejected because the denominator basis differs.
//
// medianHeight is computed ONCE per page in cluster.go (the Phase 5
// orchestrator) and passed in here; lines.go does NOT recompute. The
// rules implemented below correspond to the 18-decision-point
// enumeration in the Phase 1 step 2 think note
// `t4-algorithm-decision-points` (Rules 1.0-1.5).
//
// Performance shape: serial per-page. O(n*L) where n=runs and L=
// active lines on the page; L is bounded by the visual line count
// (rarely > 100). Page-level parallelism is provided by the
// indexer fanning out pages, not by us.

package layout

import (
	"slices"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// groupRunsToLines is the Stage-1 entry point for a whole PAGE. runs
// MUST already be rotation-normalized + Y-flipped to top-down by the
// caller (cluster.go). medianHeight is the per-page median(run.Height
// for runs with Height > 0); cluster.go computes it once and passes
// in. lp supplies the threshold surface; only LineMargin and
// WordMargin are read here (CharMargin is consumed by Stage 2).
func groupRunsToLines(runs []text.TextRun, medianHeight float64, lp LayoutParams) []Line {
	if len(runs) == 0 {
		return nil
	}
	// Few-runs short-circuit (Rule 1.5), and it is scoped to the PAGE
	// scale on purpose. A page carrying fewer than three runs is a
	// near-empty page, and a median computed from one or two runs is
	// dominated by its own outliers, so banding them is less
	// trustworthy than leaving each alone.
	//
	// That reasoning does NOT transfer to a smaller unit. Two runs are
	// unremarkable inside one structure element — a heading with one
	// bold word, a table cell, a running header, a mid-word font
	// switch — and there the guard is actively wrong: it splits one
	// rendered line into two, which the joiner downstream then
	// separates with a space or a newline. Callers working at element
	// scale call bandRunsToLines directly and skip this guard.
	if len(runs) < 3 {
		out := make([]Line, 0, len(runs))
		for i := range runs {
			out = append(out, newLine(runs[i]))
		}
		return out
	}
	return bandRunsToLines(runs, medianHeight, lp)
}

// bandRunsToLines groups runs into lines by Y-center proximity with no
// minimum-input guard: it always bands, always sorts each line's runs
// by X ascending, and always returns the lines top-to-bottom.
//
// This is the body of the grouper, split out so a caller that knows
// its runs belong to ONE ELEMENT rather than one page can reach it
// without the page-scale few-runs short-circuit above. At page scale
// that guard is a reasonable refusal to trust a two-sample median; at
// element scale it is a defect, because two runs on one baseline is
// the common case and splitting them fabricates a line break that the
// document does not contain.
//
// Inputs must already be rotation-normalized and Y-flipped, exactly as
// for groupRunsToLines. Returns nil for an empty run set.
func bandRunsToLines(runs []text.TextRun, medianHeight float64, lp LayoutParams) []Line {
	if len(runs) == 0 {
		return nil
	}

	yTolerance := medianHeight * lp.LineMargin

	// Sort runs by Y ascending (top-down after flipY) for the
	// streaming greedy assignment below. Stable sort so identical-Y
	// runs preserve their content-stream order.
	sorted := make([]text.TextRun, len(runs))
	copy(sorted, runs)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Y < sorted[j].Y })

	lines := make([]Line, 0, 16)
	for _, r := range sorted {
		idx := findJoinableLine(lines, r, yTolerance)
		if idx < 0 {
			lines = append(lines, newLine(r))
			continue
		}
		extendLine(&lines[idx], r)
	}

	// Within each line, sort runs by X ascending (Rule 1.4).
	for i := range lines {
		runs := lines[i].Runs
		sort.SliceStable(runs, func(a, b int) bool { return runs[a].X < runs[b].X })
		// Rule 1.5: insert space tokens between runs whose
		// horizontal gap exceeds WordMargin × avgCharWidth AND >= 1 em.
		lines[i].Runs = insertSpaceTokens(runs, lp)
		// Re-compute Line.BBox after space-token insertion (synthetic
		// space tokens may extend the X span; Y/Width on the original
		// runs are unchanged so this is a defensive recompute).
		lines[i].BBox = bboxOfRuns(lines[i].Runs)
	}

	// Sort the lines themselves by Y ascending so consumers see
	// reading-order top-to-bottom (Rule 1.4 also).
	sort.SliceStable(lines, func(i, j int) bool {
		return lineCenterY(lines[i]) < lineCenterY(lines[j])
	})
	return lines
}

// newLine constructs a single-run Line with BBox derived from the
// run's anchor + extents. Used by groupRunsToLines for both new
// lines and the Rule 1.5 short-circuit.
func newLine(r text.TextRun) Line {
	return Line{
		Runs: []text.TextRun{r},
		BBox: Rect{X0: r.X, Y0: r.Y, X1: r.X + r.Width, Y1: r.Y + r.Height},
	}
}

// findJoinableLine returns the index of the most-recent active line
// whose Y-center is within yTolerance of r's Y-center, or -1 when
// no active line matches. Walks back-to-front because the most
// recently-added line is the most likely match for a streaming
// Y-sorted input.
func findJoinableLine(lines []Line, r text.TextRun, yTolerance float64) int {
	rCenterY := r.Y + r.Height/2.0
	for i, v := range slices.Backward(lines) {
		if !sizeRatioOK(v, r) {
			continue
		}
		dy := rCenterY - lineCenterY(v)
		if dy < 0 {
			dy = -dy
		}
		if dy <= yTolerance {
			return i
		}
	}
	return -1
}

// sizeRatioOK guards against tiny subscript / superscript runs
// joining a body line. The check is `min(line.size, run.size) /
// max(...) >= 0.5` — preserved from pdfminer.six's line_overlap
// docstring (layout.py:51-53). Falls open when either size is zero
// (treat as unknown size, allow join).
func sizeRatioOK(line Line, r text.TextRun) bool {
	if len(line.Runs) == 0 {
		return true
	}
	lineSize := line.Runs[0].Size
	runSize := r.Size
	if lineSize == 0 || runSize == 0 {
		return true
	}
	mn, mx := lineSize, runSize
	if mn > mx {
		mn, mx = mx, mn
	}
	return mn/mx >= 0.5
}

// extendLine appends r to the line and grows the line's BBox to
// enclose it.
func extendLine(l *Line, r text.TextRun) {
	l.Runs = append(l.Runs, r)
	l.BBox = bboxUnion(l.BBox, Rect{X0: r.X, Y0: r.Y, X1: r.X + r.Width, Y1: r.Y + r.Height})
}

// lineCenterY is the Y-center of l.BBox; used for the join test
// and for sorting lines top-to-bottom.
func lineCenterY(l Line) float64 {
	return (l.BBox.Y0 + l.BBox.Y1) / 2.0
}

// bboxOfRuns returns the smallest Rect enclosing all runs.
// Returns the zero Rect for empty input.
func bboxOfRuns(runs []text.TextRun) Rect {
	if len(runs) == 0 {
		return Rect{}
	}
	out := Rect{X0: runs[0].X, Y0: runs[0].Y, X1: runs[0].X + runs[0].Width, Y1: runs[0].Y + runs[0].Height}
	for _, r := range runs[1:] {
		out = bboxUnion(out, Rect{X0: r.X, Y0: r.Y, X1: r.X + r.Width, Y1: r.Y + r.Height})
	}
	return out
}

// insertSpaceTokens walks adjacent runs (already X-sorted) and
// inserts a synthetic single-space TextRun where the gap exceeds
// BOTH lp.WordMargin × avgCharWidth(runs) AND the preceding run's
// font size (1 em). Negative or near-zero gaps (kerning overlap)
// produce no insert. Returns a NEW slice; input is not mutated.
//
// The synthetic token carries the preceding run's FontKey, FontName,
// and Size so downstream consumers see a coherent run sequence.
// Glyphs is set to a single 0x20 (ASCII space) so avgCharWidth
// continues to count the synthetic token correctly.
func insertSpaceTokens(runs []text.TextRun, lp LayoutParams) []text.TextRun {
	if len(runs) < 2 {
		return runs
	}
	avg := avgCharWidth(runs)
	if avg <= 0 {
		// Without an avg we cannot apply the WordMargin × avg gate.
		// Skip space-token insertion rather than guess.
		return runs
	}
	wordGap := lp.WordMargin * avg
	out := make([]text.TextRun, 0, len(runs))
	for i := range runs {
		r := runs[i]
		if i > 0 {
			// G602 false positive: i > 0 guards the i-1 access.
			prev := runs[i-1] //nolint:gosec

			gap := r.X - (prev.X + prev.Width)
			emGate := prev.Size
			if emGate <= 0 {
				emGate = avg
			}
			if gap > wordGap && gap >= emGate {
				out = append(out, text.TextRun{
					Text:     " ",
					Glyphs:   []uint16{0x20},
					X:        prev.X + prev.Width,
					Y:        prev.Y,
					Width:    gap,
					Height:   prev.Height,
					FontName: prev.FontName,
					FontKey:  prev.FontKey,
					Size:     prev.Size,
					MCID:     prev.MCID,
				})
			}
		}
		out = append(out, r)
	}
	return out
}
