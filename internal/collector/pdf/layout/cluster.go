// Package layout implements line and block clustering for PDF text runs.
//
// The algorithm uses two complementary techniques:
//
//   - A configurable threshold structure (LineMargin / CharMargin /
//     WordMargin / BoxesFlow / DetectVertical) modeled on
//     pdfminer.six's LAParams (pdfminer/layout.py:78-92, commit
//     a18de2a9c479b4c847538500017b449ddaec177e at
//     github.com/pdfminer/pdfminer.six).
//
//   - Per-page median-based threshold normalization. medianHeight
//     (used in Stage 1 line clustering) and medianGap (used in
//     Stage 2 paragraph break) are computed once per page and passed
//     to the stage helpers, providing per-page adaptivity that
//     handles mixed-font and short-page edge cases better than
//     per-line normalization.
//
// LineMargin × medianHeight controls line-clustering tolerance;
// ParagraphGapRatio × medianGap controls paragraph-break threshold.
// Splitting these (vs conflating them under a single line_margin as
// pdfminer.six does) materially improves accuracy on real-world PDFs.
//
// Default thresholds are empirically tuned for the median-based
// denominator: LineMargin = 0.4, ParagraphGapRatio = 1.6,
// CharMargin = 2.0, WordMargin = 0.1, BoxesFlow = 0.5. pdfminer.six's
// line_margin = 0.5 (intended for a per-line height denominator) is
// intentionally rejected because the denominator basis differs.
package layout

import (
	"fmt"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// PageInfo carries the page-level context the clusterer needs:
// PageIndex (0-indexed), MediaBox (in user-space), and Rotation
// (one of 0, 90, 180, 270). Aliased as pdf.PageInfo at the top
// level for consumer ergonomics.
type PageInfo struct {
	PageIndex int
	MediaBox  Rect
	Rotation  int
}

// Cluster groups runs into blocks using the package's default
// tunables (DefaultLayoutParams). Equivalent to
// ClusterWithParams(runs, page, DefaultLayoutParams).
func Cluster(runs []text.TextRun, page PageInfo) ([]Block, error) {
	return ClusterWithParams(runs, page, DefaultLayoutParams)
}

// ClusterWithParams is the orchestrator: rotation normalize → Y-flip
// → medianHeight (once) → groupRunsToLines → groupLinesToBlocks →
// per-block dehyphenate → un-flip and denormalize the BBOXES ONLY.
// Line.Runs and Block-level runs stay in the internal top-down frame,
// so a run's Y is not comparable with the BBox beside it; see the
// coordinate-frame note on layout.Line.Runs. LinesFromRuns differs
// here deliberately and un-flips its runs too. The algorithm is
// documented in the package godoc above; per-rule citations live in
// lines.go and blocks.go.
func ClusterWithParams(runs []text.TextRun, page PageInfo, lp LayoutParams) ([]Block, error) {
	return clusterAtScale(runs, page, lp, groupRunsToLines)
}

// BlocksFromRuns is the ELEMENT-SCALE peer of ClusterWithParams. It
// runs the identical pipeline and differs in exactly one respect: line
// grouping skips the page-scale few-runs guard, so a residue of one or
// two runs is banded like any other input instead of becoming one
// block per run.
//
// The caller this exists for is the structure-tree reader's hybrid
// merge, which clusters the runs no structure element claimed. On a
// well-tagged page that residue is routinely one or two runs — a
// footer, a folio, a stray label — which is precisely the input the
// page-scale guard mishandles. Measured through HybridFallback before
// this existed: a two-run untagged footer "Chapter 3 | " and "42" on
// one baseline came back as TWO blocks, so two chunk nodes, and the
// chrome-shape detector (which requires a single line in a single
// block) could never fire on the running footers it was written for.
//
// Everything else matches ClusterWithParams, deliberately, including
// leaving Line.Runs in the internal top-down frame: this is a fix to
// the grouping SCALE, not to the coordinate frame, and its output is
// merged with Cluster's own on the same page.
//
// Use ClusterWithParams for a whole page. Its guard is right there: a
// page with fewer than three runs is near-empty, and a median drawn
// from one or two samples is dominated by its own outliers.
func BlocksFromRuns(runs []text.TextRun, page PageInfo, lp LayoutParams) ([]Block, error) {
	return clusterAtScale(runs, page, lp, bandRunsToLines)
}

// clusterAtScale is the shared body. group selects the line-grouping
// entry point, which is the ONLY difference between the page-scale and
// element-scale callers; everything downstream of it is identical, so
// the two cannot drift.
func clusterAtScale(runs []text.TextRun, page PageInfo, lp LayoutParams, group func([]text.TextRun, float64, LayoutParams) []Line) ([]Block, error) {
	if err := validateRotation(page.Rotation); err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}

	// 1. Rotation normalize. Returns input slice when rotation == 0.
	normalized := normalizeForRotation(runs, page.Rotation, page.MediaBox)

	// 2. Y-flip to top-down. The post-rotation MediaBox extents are
	//    the rotated dims; for rotation 0/180 the height is mb.Y1,
	//    for 90/270 the height is mb.X1 (a 90° rotation swaps
	//    width and height of the page extent).
	mbHeight := page.MediaBox.Y1
	if page.Rotation == 90 || page.Rotation == 270 {
		mbHeight = page.MediaBox.X1
	}
	flipped := make([]text.TextRun, len(normalized))
	copy(flipped, normalized)
	for i := range flipped {
		flipped[i].Y = flipY(flipped[i].Y, flipped[i].Height, mbHeight)
	}

	// 3. Compute medianHeight ONCE per page. Falls back to 1.0
	//    when no run has a usable Height (avoids divide-by-zero
	//    in Stage 1's yTolerance = medianHeight × LineMargin).
	medianHeight := medianRunHeight(flipped)
	if medianHeight == 0 {
		medianHeight = 1.0
	}

	// 4. Stage 1 — runs → lines (passes medianHeight). Which grouper
	//    runs is the caller's scale choice: groupRunsToLines applies
	//    the page-scale few-runs guard, bandRunsToLines does not.
	lines := group(flipped, medianHeight, lp)

	// 5. Stage 2 — lines → blocks (computes medianGap inside).
	//    groupLinesToBlocks handles the single-line short-circuit
	//    (Rule 2.0) without computing medianGap.
	blocks := groupLinesToBlocks(lines, page.PageIndex, lp)

	// 7. Per-block dehyphenation. The post-Stage-2 ordering is per
	//    Phase 1 step 2 Rule 3.4 — by running after block grouping,
	//    cross-block dehyphenation is impossible by construction.
	for i := range blocks {
		blocks[i].Lines = dehyphenateLines(blocks[i].Lines)
	}

	// 8. Un-flip Y on every Line/Block BBox + denormalize for
	//    rotation. Consumers expect bboxes in the page's natural
	//    rotated user-space frame.
	for i := range blocks {
		blocks[i].BBox = unflipBBox(blocks[i].BBox, mbHeight)
		blocks[i].BBox = denormalizeBBox(blocks[i].BBox, page.Rotation, page.MediaBox)
		for j := range blocks[i].Lines {
			blocks[i].Lines[j].BBox = unflipBBox(blocks[i].Lines[j].BBox, mbHeight)
			blocks[i].Lines[j].BBox = denormalizeBBox(blocks[i].Lines[j].BBox, page.Rotation, page.MediaBox)
		}
	}
	return blocks, nil
}

// validateRotation returns nil for the 4 legal /Rotate values
// (0, 90, 180, 270) and an error otherwise.
func validateRotation(rotation int) error {
	switch rotation {
	case 0, 90, 180, 270:
		return nil
	default:
		return fmt.Errorf("layout: invalid PageInfo.Rotation %d (must be one of 0, 90, 180, 270)", rotation)
	}
}

// medianRunHeight returns the median of run.Height over runs with
// Height > 0. Returns 0 when no run qualifies; callers fall back to
// 1.0 to avoid divide-by-zero in the yTolerance expression. Sort +
// middle-index; no in-tree analog under collector/pdf/* — small and
// file-local.
func medianRunHeight(runs []text.TextRun) float64 {
	heights := make([]float64, 0, len(runs))
	for _, r := range runs {
		if r.Height > 0 {
			heights = append(heights, r.Height)
		}
	}
	if len(heights) == 0 {
		return 0
	}
	sort.Float64s(heights)
	n := len(heights)
	if n%2 == 1 {
		return heights[n/2]
	}
	return (heights[n/2-1] + heights[n/2]) / 2.0
}

// unflipBBox inverts the top-down Y flip applied during clustering,
// returning the rectangle into PDF bottom-up user-space. The flip
// is symmetric in (Y0, Y1) — both endpoints get flipped, then
// re-canonicalized so Y0 ≤ Y1.
func unflipBBox(b Rect, mbHeight float64) Rect {
	y0 := flipY(b.Y0, 0, mbHeight) // single-coord flip; h=0 since b is a rect.
	y1 := flipY(b.Y1, 0, mbHeight)
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	return Rect{X0: b.X0, Y0: y0, X1: b.X1, Y1: y1}
}
