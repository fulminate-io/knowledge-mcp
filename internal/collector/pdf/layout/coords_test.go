package layout

import (
	"math"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// mbStandard is the "Letter portrait" page extent used by most
// rotation tests: 612 × 792 points (mb.X0 = mb.Y0 = 0).
var mbStandard = Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}

const eps = 1e-9

func approxEq(a, b float64) bool { return math.Abs(a-b) < eps }

func TestNormalizeForRotation_Zero(t *testing.T) {
	t.Parallel()
	runs := []text.TextRun{{X: 10, Y: 20, Width: 5, Height: 12}}
	out := normalizeForRotation(runs, 0, mbStandard)
	if len(out) != 1 || out[0].X != runs[0].X || out[0].Y != runs[0].Y ||
		out[0].Width != runs[0].Width || out[0].Height != runs[0].Height {
		t.Fatalf("rotation=0: expected pass-through, got %+v", out)
	}
}

func TestNormalizeForRotation_NinetyDegrees(t *testing.T) {
	t.Parallel()
	in := []text.TextRun{{X: 10, Y: 20, Width: 5, Height: 12}}
	got := normalizeForRotation(in, 90, mbStandard)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	r := got[0]
	// (x', y') = (y, mb.X1 - x); width<->height.
	if !approxEq(r.X, 20) || !approxEq(r.Y, 612-10) || !approxEq(r.Width, 12) || !approxEq(r.Height, 5) {
		t.Errorf("90: got (X=%v Y=%v W=%v H=%v), want (20 602 12 5)", r.X, r.Y, r.Width, r.Height)
	}
	// Input must not be mutated.
	if in[0].X != 10 || in[0].Y != 20 {
		t.Errorf("input mutated: %+v", in[0])
	}
}

func TestNormalizeForRotation_OneEighty(t *testing.T) {
	t.Parallel()
	in := []text.TextRun{{X: 10, Y: 20, Width: 5, Height: 12}}
	got := normalizeForRotation(in, 180, mbStandard)
	r := got[0]
	// (x', y') = (mb.X1 - x, mb.Y1 - y); preserve W/H.
	if !approxEq(r.X, 612-10) || !approxEq(r.Y, 792-20) || !approxEq(r.Width, 5) || !approxEq(r.Height, 12) {
		t.Errorf("180: got (X=%v Y=%v W=%v H=%v), want (602 772 5 12)", r.X, r.Y, r.Width, r.Height)
	}
}

func TestNormalizeForRotation_TwoSeventy(t *testing.T) {
	t.Parallel()
	in := []text.TextRun{{X: 10, Y: 20, Width: 5, Height: 12}}
	got := normalizeForRotation(in, 270, mbStandard)
	r := got[0]
	// (x', y') = (mb.Y1 - y, x); width<->height.
	if !approxEq(r.X, 792-20) || !approxEq(r.Y, 10) || !approxEq(r.Width, 12) || !approxEq(r.Height, 5) {
		t.Errorf("270: got (X=%v Y=%v W=%v H=%v), want (772 10 12 5)", r.X, r.Y, r.Width, r.Height)
	}
}

// normalizeBBoxForRoundTrip applies the forward point transform —
// NormalizePoint, the same one normalizeForRotation applies to a run's
// anchor — to a Rect's two opposite corners. It exists only in test
// code so we can verify denormalizeBBox is the literal inverse:
// denormalizeBBox(normalizeBBoxForRoundTrip(r)) == r.
//
// It delegates rather than carrying its own copy of the switch, so
// this round-trip proof is a proof about the production formula and
// cannot drift away from it.
func normalizeBBoxForRoundTrip(b Rect, rotation int, mb Rect) Rect {
	if rotation == 0 {
		return b
	}
	p0x, p0y := NormalizePoint(b.X0, b.Y0, rotation, mb)
	p1x, p1y := NormalizePoint(b.X1, b.Y1, rotation, mb)
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

func TestDenormalizeBBox_RoundTrip(t *testing.T) {
	t.Parallel()
	in := Rect{X0: 50, Y0: 100, X1: 200, Y1: 150}
	for _, rot := range []int{0, 90, 180, 270} {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			normalized := normalizeBBoxForRoundTrip(in, rot, mbStandard)
			restored := denormalizeBBox(normalized, rot, mbStandard)
			if !approxEq(restored.X0, in.X0) || !approxEq(restored.Y0, in.Y0) ||
				!approxEq(restored.X1, in.X1) || !approxEq(restored.Y1, in.Y1) {
				t.Errorf("round-trip(rot=%d): in=%+v restored=%+v", rot, in, restored)
			}
		})
	}
}

func TestFlipY(t *testing.T) {
	t.Parallel()
	got := flipY(100, 12, 792)
	if !approxEq(got, 680) {
		t.Errorf("flipY(100, 12, 792) = %v, want 680", got)
	}
	// Symmetry: flipY(flipY(y, h, H), h, H) == y for any y/h/H.
	if !approxEq(flipY(flipY(123, 5, 600), 5, 600), 123) {
		t.Errorf("flipY symmetry broken")
	}
}

func TestBBoxUnion(t *testing.T) {
	t.Parallel()
	a := Rect{X0: 0, Y0: 0, X1: 10, Y1: 10}
	// Identical inputs.
	if got := bboxUnion(a, a); got != a {
		t.Errorf("union(a,a) = %+v, want %+v", got, a)
	}
	// Disjoint inputs return enclosing rect.
	b := Rect{X0: 20, Y0: 30, X1: 40, Y1: 50}
	want := Rect{X0: 0, Y0: 0, X1: 40, Y1: 50}
	if got := bboxUnion(a, b); got != want {
		t.Errorf("union disjoint = %+v, want %+v", got, want)
	}
	// Overlap.
	c := Rect{X0: 5, Y0: -5, X1: 15, Y1: 7}
	want2 := Rect{X0: 0, Y0: -5, X1: 15, Y1: 10}
	if got := bboxUnion(a, c); got != want2 {
		t.Errorf("union overlap = %+v, want %+v", got, want2)
	}
}

func TestAvgCharWidth(t *testing.T) {
	t.Parallel()
	// Empty slice returns 0.
	if got := avgCharWidth(nil); !approxEq(got, 0) {
		t.Errorf("empty = %v, want 0", got)
	}
	// All-zero-glyphs returns 0.
	zeroGlyphs := []text.TextRun{{Width: 100, Glyphs: nil}, {Width: 50, Glyphs: nil}}
	if got := avgCharWidth(zeroGlyphs); !approxEq(got, 0) {
		t.Errorf("zero-glyphs = %v, want 0", got)
	}
	// Mixed: one run with 5 glyphs (Width=50 → per-glyph 10);
	// one run with 0 glyphs (skipped); one run with 2 glyphs
	// (Width=12 → per-glyph 6). Mean over the 2 non-zero runs = 8.
	mixed := []text.TextRun{
		{Width: 50, Glyphs: []uint16{1, 2, 3, 4, 5}},
		{Width: 99, Glyphs: nil},
		{Width: 12, Glyphs: []uint16{1, 2}},
	}
	if got := avgCharWidth(mixed); !approxEq(got, 8) {
		t.Errorf("mixed = %v, want 8", got)
	}
}
