package layout

import "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"

// LinesFromRuns groups a set of runs into reading-order Lines, using
// the SAME machinery Cluster uses — rotation normalization, the Y-flip
// into top-down space, median-height line banding, the per-line X sort,
// space-token insertion, end-of-line dehyphenation, and the un-flip
// back into the page's natural user-space frame.
//
// It exists for callers that already know which runs belong together
// and need only the line structure, not the block grouping. The
// structure-tree reader is the case: a tagged element declares its own
// extent, so Stage 2's geometric block grouping would be re-deciding
// something the document already stated, but the element's runs still
// have to be split into the lines they were rendered on before
// anything downstream can work across a line boundary.
//
// WHY THIS IS AN EXPORT RATHER THAN A SECOND GROUPER. A hand-rolled
// "sort by Y then band" is not a simplification of this function, it is
// a defective copy of it: it drops the per-line X sort, so a
// superscript or footnote marker sitting a few points above its
// baseline sorts AHEAD of the word it annotates and the element emits
// "12text" where the page reads "text12". It drops the Y-CENTER
// banding and the size-ratio gate that put a raised run on the right
// line in the first place, the space-token insertion that separates
// runs with a wide horizontal gap, and rotation normalization, so a
// rotated page comes back reversed. Every one of those was shipped and
// then found by review.
//
// lp must be fully populated; pass DefaultLayoutParams. The median
// height that scales the line-banding tolerance is computed over the
// runs SUPPLIED, not over the whole page, so a caller handing in one
// element's runs gets a tolerance scaled to that element.
//
// NO FEW-RUNS GUARD. This calls bandRunsToLines rather than
// groupRunsToLines, so a two-run input is banded like any other. The
// page-scale entry point refuses to band fewer than three runs because
// a two-sample median is untrustworthy for a whole page; at element
// scale two runs on one baseline is the ordinary case and splitting
// them fabricates a line break the document does not contain.
//
// THE RUN UN-FLIP IS ON THIS PATH ONLY. Cluster un-flips its line and
// block boxes but leaves its runs in the internal top-down frame, and
// that is unchanged — existing Cluster consumers see byte-identical
// output. Only callers of this function get runs back in the frame
// they supplied. The asymmetry is deliberate: exporting coordinates in
// an internal frame is a trap, and it broke the structure-tree
// reader's synthesized-run geometry contract when this function was
// first written without the un-flip.
//
// A ZERO MediaBox IS TOLERATED rather than rejected. It yields
// mbHeight 0, so flipY becomes the reflection y -> -y - h, which is
// order-reversing exactly like the real flip and is undone exactly by
// the un-flip at the end. Line banding and the X sort are unaffected,
// so a caller that cannot supply a media box still gets correct line
// structure and correct reading order; only the absolute box
// coordinates are meaningless, and they were meaningless in the input
// too.
//
// Returns nil for an empty run set, and an error only for an invalid
// page rotation.
func LinesFromRuns(runs []text.TextRun, page PageInfo, lp LayoutParams) ([]Line, error) {
	if err := validateRotation(page.Rotation); err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}

	normalized := normalizeForRotation(runs, page.Rotation, page.MediaBox)

	mbHeight := page.MediaBox.Y1
	if page.Rotation == 90 || page.Rotation == 270 {
		mbHeight = page.MediaBox.X1
	}
	flipped := make([]text.TextRun, len(normalized))
	copy(flipped, normalized)
	for i := range flipped {
		flipped[i].Y = flipY(flipped[i].Y, flipped[i].Height, mbHeight)
	}

	medianHeight := medianRunHeight(flipped)
	if medianHeight == 0 {
		medianHeight = 1.0
	}

	lines := dehyphenateLines(bandRunsToLines(flipped, medianHeight, lp))

	// Undo the Y-flip on the LINE BBOXES and on the RUNS themselves,
	// so a caller gets its coordinates back in the frame it supplied
	// rather than in the grouper's internal top-down one. Cluster
	// un-flips only the boxes, because its own consumers read boxes;
	// leaving run coordinates flipped in a public API is a trap, and
	// it broke the structure-tree reader's synthesized-run contract
	// the first time this function was written without it. flipY is
	// its own inverse for a fixed height.
	//
	// On a ROTATED page the runs stay in the rotation-normalized
	// frame, which is exactly what Cluster leaves its own runs in, so
	// the two paths agree; only the boxes are denormalized.
	for i := range lines {
		lines[i].BBox = unflipBBox(lines[i].BBox, mbHeight)
		lines[i].BBox = denormalizeBBox(lines[i].BBox, page.Rotation, page.MediaBox)
		for j := range lines[i].Runs {
			lines[i].Runs[j].Y = flipY(lines[i].Runs[j].Y, lines[i].Runs[j].Height, mbHeight)
		}
	}
	return lines, nil
}
