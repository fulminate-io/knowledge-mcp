package layout

// coords.go — rotation inverse + Y-flip + bbox + avg-char-width
// helpers for the layout clusterer.
//
// Rotation: the page's /Rotate value (PDF 32000-1:2008 §8.4.5) is a
// CW page-display rotation in {0, 90, 180, 270}. To cluster runs in
// a rotation-invariant frame we apply the INVERSE rotation to each
// run's (X, Y) before clustering and re-apply it to the output BBox
// on emit. The pdfcpu wrapper exposes the raw /Rotate value via
// PageObject.Rotation (collector/pdf/internal/pdfcpu/page.go).
//
// Y-axis convention: PDF user-space is bottom-up (+y up). Clustering
// is more natural top-down (+y down) so the per-page sort by Y
// places the topmost line first. flipY converts a single coordinate;
// callers flip on entry and restore on emit.
//
// Exported surface: NormalizePoint alone. Package structtree needs the
// forward point transform to order a partially-tagged page's blocks in
// the page's reading frame, so that one formula is exported and every
// other declaration here stays package-internal.

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// NormalizePoint maps a single point out of the page's natural,
// unrotated user-space frame and into the rotation-normalized reading
// frame — the frame in which "down the page, then left to right" is
// the order a viewer actually reads.
//
// The PDF /Rotate field specifies CW page-display rotation; content is
// laid out in the *unrotated* frame and the viewer rotates the canvas
// at display time. To work in the visual reading frame we
// counter-rotate (CCW) by the same amount, with mb.X1 and mb.Y1 acting
// as the page-extent pivot:
//
//   - 90° CW page  → counter-rotate by 90° CCW:
//     (x', y') = (y, mb.X1 - x).
//   - 180° page    → (x', y') = (mb.X1 - x, mb.Y1 - y).
//   - 270° CW page → counter-rotate by 270° CCW (≡ 90° CW):
//     (x', y') = (mb.Y1 - y, x).
//
// Rotation 0 is the IDENTITY, so a caller that keys on this function's
// result orders an unrotated page exactly as it did before the call
// existed — byte-identical by construction rather than by measurement.
//
// An unrecognized rotation is the identity too, and that is not a
// silent degrade: every caller reaches this function downstream of
// validateRotation (cluster.go, called first thing in clusterAtScale),
// which rejects any /Rotate outside {0, 90, 180, 270} before geometry
// runs. The default arm is unreachable for a value the collector
// accepted.
//
// A POINT is transformed rather than a rectangle, deliberately. A
// rectangle's extent rotates with it, so its normalized corner
// coordinates shift by the block's ORIGINAL width and no single corner
// is a stable ordering key across all four rotations. A point carries
// no extent to rotate.
//
// Reference: PDF 32000-1:2008 §8.4.5 (Rotate).
func NormalizePoint(x, y float64, rotation int, mb Rect) (float64, float64) {
	switch rotation {
	case 90:
		return y, mb.X1 - x
	case 180:
		return mb.X1 - x, mb.Y1 - y
	case 270:
		return mb.Y1 - y, x
	default:
		return x, y
	}
}

// normalizeForRotation returns a NEW slice (input is not mutated) in
// which every run's (X, Y) anchor and (Width, Height) extents have
// been counter-rotated by the page /Rotate value. For rotation 0
// the input slice is returned as-is (zero-copy).
//
// The anchor transform is NormalizePoint's; this function owns only
// the EXTENT half, which a point transform cannot express: a 90° or
// 270° normalize turns a run's horizontal extent into a vertical one,
// so Width and Height swap, while 180° preserves both. Downstream
// clustering uses the normalized anchors plus these rotated extents
// for line and block grouping.
//
// Reference: PDF 32000-1:2008 §8.4.5 (Rotate). pdfcpu wrapper read
// site: collector/pdf/internal/pdfcpu/page.go.
func normalizeForRotation(runs []text.TextRun, rotation int, mb Rect) []text.TextRun {
	if rotation == 0 || len(runs) == 0 {
		return runs
	}
	out := make([]text.TextRun, len(runs))
	copy(out, runs)
	swapExtent := rotation == 90 || rotation == 270
	for i := range out {
		r := out[i]
		out[i].X, out[i].Y = NormalizePoint(r.X, r.Y, rotation, mb)
		if swapExtent {
			out[i].Width, out[i].Height = r.Height, r.Width
		}
	}
	return out
}

// denormalizeBBox is the inverse of normalizeForRotation applied to
// a rectangle (Block.BBox / Line.BBox). The clusterer builds bboxes
// in the rotation-normalized frame; consumers expect bboxes in the
// page's natural rotated frame. denormalizeBBox restores them.
//
// The inverse transforms are derived from the anchor formulas in
// normalizeForRotation by solving for the original (x, y):
//
//   - 90° inverse:  (x, y) = (mb.X1 - y', x')
//   - 180° inverse: (x, y) = (mb.X1 - x', mb.Y1 - y')
//   - 270° inverse: (x, y) = (y', mb.Y1 - x')
//
// Because these are point transforms they map opposite corners of
// a rectangle to opposite corners; we transform (X0, Y0) and
// (X1, Y1) and re-canonicalize the rect so X0 ≤ X1, Y0 ≤ Y1.
func denormalizeBBox(b Rect, rotation int, mb Rect) Rect {
	if rotation == 0 {
		return b
	}
	mbW := mb.X1
	mbH := mb.Y1
	var p0x, p0y, p1x, p1y float64
	switch rotation {
	case 90:
		p0x = mbW - b.Y0
		p0y = b.X0
		p1x = mbW - b.Y1
		p1y = b.X1
	case 180:
		p0x = mbW - b.X0
		p0y = mbH - b.Y0
		p1x = mbW - b.X1
		p1y = mbH - b.Y1
	case 270:
		p0x = b.Y0
		p0y = mbH - b.X0
		p1x = b.Y1
		p1y = mbH - b.X1
	default:
		return b
	}
	// Re-canonicalize so X0 ≤ X1 and Y0 ≤ Y1.
	x0, x1 := p0x, p1x
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	y0, y1 := p0y, p1y
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	return Rect{X0: x0, Y0: y0, X1: x1, Y1: y1}
}

// flipY converts a single bottom-up Y coordinate to top-down within
// a page of height mbHeight. PDF user-space is +y up; for clustering
// we want +y down so a normal y-ascending sort places the topmost
// line first.
//
// Inverse: flipY(flipY(y, h, H), h, H) == y.
func flipY(y, h, mbHeight float64) float64 {
	return mbHeight - y - h
}

// bboxUnion returns the smallest Rect enclosing both inputs. Used
// when extending a Line.BBox or Block.BBox during clustering.
func bboxUnion(a, b Rect) Rect {
	out := a
	if b.X0 < out.X0 {
		out.X0 = b.X0
	}
	if b.Y0 < out.Y0 {
		out.Y0 = b.Y0
	}
	if b.X1 > out.X1 {
		out.X1 = b.X1
	}
	if b.Y1 > out.Y1 {
		out.Y1 = b.Y1
	}
	return out
}

// avgCharWidth returns the mean run.Width / glyph-count over runs
// with non-zero Glyphs. Used by Stage-1 (space-token insertion) and
// Stage-2 (X-start-similarity check) as the per-line normalizer for
// X-axis thresholds (CharMargin × avg_char_width,
// WordMargin × avg_char_width).
//
// Returns 0 when no runs have decoded glyphs — caller treats as
// "no gap test possible" and falls back to skipping the test.
func avgCharWidth(runs []text.TextRun) float64 {
	var sum float64
	var count int
	for _, r := range runs {
		if len(r.Glyphs) == 0 {
			continue
		}
		sum += r.Width / float64(len(r.Glyphs))
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
