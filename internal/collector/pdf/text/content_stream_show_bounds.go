package text

// glyphExtent records the user-space cumulative-advance bracket of one
// emitted glyph. left is the cumulative advance at the glyph's left
// edge (sum of all prior glyph advances + their Tc/Tw spacings, with
// horizScale folded in); right is left + this glyph's pure
// glyphAdvance (no Tc/Tw — those create a gap between right and the
// next glyph's left). appendRun maps each (left, right) pair through
// the combined Tm × CTM transform (Trm per PDF 32000-1:2008 §9.4.4) to produce the user-space CharBounds
// rect for the glyph.
type glyphExtent struct {
	left, right float64
}

// computeCharBounds produces one axis-aligned user-space Rect per
// glyph, using the same combined Tm × CTM transform (Trm per PDF 32000-1:2008 §9.4.4) applied to the
// run-level origin. For glyph i the text-space corners are
// (e.left, rise) and (e.right, rise+fontSize) — the four corners
// transform through `combined` and per-axis min/max delivers an
// axis-aligned rect even under non-axis-aligned (rotated/skewed) text.
// extents is the per-glyph (left, right) extent slice from
// advanceForString, parallel to the glyphs slice. Tc and Tw show up as
// a gap between successive glyphs' bounds rather than a widened glyph
// extent.
func computeCharBounds(combined matrix, rise, fontSize float64, extents []glyphExtent) []Rect {
	bounds := make([]Rect, 0, len(extents))
	appendCharBounds(&bounds, combined, rise, fontSize, extents)
	return bounds
}

// appendCharBounds is computeCharBounds's arena-aware twin: appends
// the per-glyph rects to *dst (so the destination's underlying array
// grows in place). Used by walker.appendRun when an arena is set —
// the per-page arena slab carries the rects so each run's CharBounds
// is a 3-arg-capped sub-slice of arena.bounds rather than its own
// allocation.
func appendCharBounds(dst *[]Rect, combined matrix, rise, fontSize float64, extents []glyphExtent) {
	top := rise + fontSize
	for _, e := range extents {
		x0, y0 := combined.transformPoint(e.left, rise)
		x1, y1 := combined.transformPoint(e.right, rise)
		x2, y2 := combined.transformPoint(e.left, top)
		x3, y3 := combined.transformPoint(e.right, top)
		*dst = append(*dst, Rect{
			X0: min4(x0, x1, x2, x3),
			Y0: min4(y0, y1, y2, y3),
			X1: max4(x0, x1, x2, x3),
			Y1: max4(y0, y1, y2, y3),
		})
	}
}

// min4 returns the smallest of four float64 values. Used by
// computeCharBounds to pick the axis-aligned bounding-box minimum
// after transforming all four corners of a glyph's text-space rect.
func min4(a, b, c, d float64) float64 {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	if d < m {
		m = d
	}
	return m
}

// max4 returns the largest of four float64 values. Used by
// computeCharBounds to pick the axis-aligned bounding-box maximum
// after transforming all four corners of a glyph's text-space rect.
func max4(a, b, c, d float64) float64 {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	if d > m {
		m = d
	}
	return m
}
