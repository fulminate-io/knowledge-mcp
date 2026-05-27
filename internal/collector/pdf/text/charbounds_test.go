package text

import (
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// TestCharBounds_HelloMonospaced extracts a 5-glyph "Hello" run via
// Helvetica F1 (12pt, identity TM). It asserts the run carries one
// CharBounds entry per emitted glyph, that bounds advance strictly
// left-to-right (X0 monotone increasing), and that the first glyph's
// left edge sits at the run origin while the last glyph's right edge
// matches the run width — i.e. CharBounds tiles the run width without
// overlap or gap (the Standard 14 width table handles the per-glyph
// advances).
func TestCharBounds_HelloMonospaced(t *testing.T) {
	t.Parallel()
	body := []byte("BT /F1 12 Tf 100 700 Td (Hello) Tj ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	r := runs[0]
	if len(r.CharBounds) != 5 {
		t.Fatalf("CharBounds len: got %d, want 5", len(r.CharBounds))
	}
	if len(r.Glyphs) != len(r.CharBounds) {
		t.Fatalf("CharBounds parallel to Glyphs: got %d bounds for %d glyphs",
			len(r.CharBounds), len(r.Glyphs))
	}
	// Strictly monotone X0; each glyph's right edge meets the next
	// glyph's left edge (no Tc, identity TM, single Tj).
	for i, b := range r.CharBounds {
		if b.X1 <= b.X0 {
			t.Errorf("bounds[%d]: X1<=X0 (%v<=%v)", i, b.X1, b.X0)
		}
		if b.Y1 <= b.Y0 {
			t.Errorf("bounds[%d]: Y1<=Y0 (%v<=%v)", i, b.Y1, b.Y0)
		}
		if i == 0 {
			continue
		}
		prev := r.CharBounds[i-1]
		if b.X0 <= prev.X0 {
			t.Errorf("bounds[%d].X0=%v not > prev.X0=%v", i, b.X0, prev.X0)
		}
		if delta := b.X0 - prev.X1; delta < -1e-6 || delta > 1e-6 {
			t.Errorf("bounds[%d].X0 - prev.X1 = %v, want ~0 (no Tc)", i, delta)
		}
	}
	// First glyph's left edge sits at the run origin; last glyph's
	// right edge sits at run origin + run width.
	if dx := r.CharBounds[0].X0 - r.X; dx < -1e-6 || dx > 1e-6 {
		t.Errorf("first bounds.X0 - run.X = %v, want ~0", dx)
	}
	if dx := r.CharBounds[len(r.CharBounds)-1].X1 - (r.X + r.Width); dx < -1e-6 || dx > 1e-6 {
		t.Errorf("last bounds.X1 - (run.X+run.Width) = %v, want ~0", dx)
	}
	// Y bounds: bottom = run.Y, top = run.Y + Size (fontSize), since
	// rise=0 and identity TM.
	if dy := r.CharBounds[0].Y0 - r.Y; dy < -1e-6 || dy > 1e-6 {
		t.Errorf("bounds[0].Y0 - run.Y = %v, want ~0", dy)
	}
	if dy := r.CharBounds[0].Y1 - (r.Y + r.Size); dy < -1e-6 || dy > 1e-6 {
		t.Errorf("bounds[0].Y1 - (run.Y+Size) = %v, want ~0", dy)
	}
}

// TestCharBounds_EmptyRun confirms an empty Tj operand emits no run
// (per the Phase 8 contract on emitTj). The test guards the negative:
// when no run is appended, no CharBounds slice can leak through. This
// is the empty-run boundary condition for the per-glyph bounds.
func TestCharBounds_EmptyRun(t *testing.T) {
	t.Parallel()
	body := []byte("BT /F1 12 Tf 100 700 Td () Tj ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 0 {
		t.Fatalf("empty Tj: got %d runs, want 0 (CharBounds=%v)",
			len(runs), runs)
	}
}

// TestCharBounds_Type0CIDFont exercises a Type0 / multi-byte CID font.
// The walker decodes 2 bytes per glyph; the width table maps the
// combined CID directly. Assert the CharBounds slice has one entry
// per emitted CID and the bounds advance left-to-right per the
// per-CID widths.
func TestCharBounds_Type0CIDFont(t *testing.T) {
	t.Parallel()
	cidFont := &internalpdf.ResolvedFont{
		FontResource: &internalpdf.FontResource{
			Key:      "F2",
			BaseFont: "MyCIDFont",
			Subtype:  "Type0",
		},
		FirstChar: 0,
		// Three CIDs at 500/750/1000 em — distinct widths so the test
		// catches any "all-glyphs-same-width" bug.
		Widths: []int{500, 750, 1000},
	}
	fonts := map[string]*internalpdf.ResolvedFont{"F2": cidFont}
	// 6 bytes = 3 CIDs (2 bytes each: 0x0000, 0x0001, 0x0002).
	body := []byte("BT /F2 10 Tf 0 0 Td <000000010002> Tj ET")
	runs := extractFromBytes(t, body, fonts, ExtractOptions{})
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	r := runs[0]
	if len(r.Glyphs) != 3 {
		t.Fatalf("Glyphs len: got %d, want 3", len(r.Glyphs))
	}
	if len(r.CharBounds) != 3 {
		t.Fatalf("CharBounds len: got %d, want 3", len(r.CharBounds))
	}
	// Per-glyph widths in user space at fontSize=10: 5, 7.5, 10.
	wants := []float64{5.0, 7.5, 10.0}
	for i, want := range wants {
		got := r.CharBounds[i].X1 - r.CharBounds[i].X0
		if got < want-1e-6 || got > want+1e-6 {
			t.Errorf("bounds[%d] width: got %v, want %v", i, got, want)
		}
	}
	// Cumulative advances tile contiguously (no Tc).
	for i := 1; i < len(r.CharBounds); i++ {
		if delta := r.CharBounds[i].X0 - r.CharBounds[i-1].X1; delta < -1e-6 || delta > 1e-6 {
			t.Errorf("bounds[%d].X0 - prev.X1 = %v, want 0", i, delta)
		}
	}
}

// TestCharBounds_VariableWidth uses a font with a non-uniform /Widths
// array to confirm sequential per-glyph bounds reflect the width
// variation rather than an averaged or uniform advance. The test
// supplies an explicit /Widths so the width-resolution ladder hits
// rung 1 (the Widths array) and bypasses the Standard 14 fallback.
func TestCharBounds_VariableWidth(t *testing.T) {
	t.Parallel()
	font := &internalpdf.ResolvedFont{
		FontResource: &internalpdf.FontResource{
			Key:      "F3",
			BaseFont: "VariableFont",
			Subtype:  "Type1",
		},
		FirstChar: 'A',
		// 'A' = 600, 'B' = 300, 'C' = 900 (1/1000 em).
		Widths: []int{600, 300, 900},
	}
	fonts := map[string]*internalpdf.ResolvedFont{"F3": font}
	body := []byte("BT /F3 10 Tf 0 0 Td (ABC) Tj ET")
	runs := extractFromBytes(t, body, fonts, ExtractOptions{})
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	r := runs[0]
	if len(r.CharBounds) != 3 {
		t.Fatalf("CharBounds len: got %d, want 3", len(r.CharBounds))
	}
	// Widths at fontSize=10: 6, 3, 9.
	want := []float64{6.0, 3.0, 9.0}
	for i, w := range want {
		got := r.CharBounds[i].X1 - r.CharBounds[i].X0
		if got < w-1e-6 || got > w+1e-6 {
			t.Errorf("bounds[%d] width: got %v, want %v", i, got, w)
		}
	}
	// Bounds must visibly differ; a uniform-advance bug would yield
	// equal widths here.
	w0 := r.CharBounds[0].X1 - r.CharBounds[0].X0
	w1 := r.CharBounds[1].X1 - r.CharBounds[1].X0
	w2 := r.CharBounds[2].X1 - r.CharBounds[2].X0
	if w0 == w1 || w1 == w2 {
		t.Errorf("expected variable widths, got %v/%v/%v", w0, w1, w2)
	}
}

// TestCharBounds_CharSpacingTc asserts Tc (character-spacing) is
// folded into per-glyph bounds: each glyph's right edge equals
// previous-right + glyphAdvance + Tc. The test compares two runs of
// the same source string with and without Tc and checks that the gap
// between successive glyph bounds matches the configured Tc value.
func TestCharBounds_CharSpacingTc(t *testing.T) {
	t.Parallel()
	font := &internalpdf.ResolvedFont{
		FontResource: &internalpdf.FontResource{
			Key:      "F4",
			BaseFont: "TcFont",
			Subtype:  "Type1",
		},
		FirstChar: 'A',
		Widths:    []int{500, 500, 500}, // uniform 0.5em widths
	}
	fonts := map[string]*internalpdf.ResolvedFont{"F4": font}
	noTc := extractFromBytes(t,
		[]byte("BT /F4 10 Tf 0 0 Td (ABC) Tj ET"), fonts, ExtractOptions{})
	withTc := extractFromBytes(t,
		[]byte("BT /F4 10 Tf 2 Tc 0 0 Td (ABC) Tj ET"), fonts, ExtractOptions{})
	if len(noTc) != 1 || len(withTc) != 1 {
		t.Fatalf("expected 1 run each: noTc=%d withTc=%d", len(noTc), len(withTc))
	}
	rn := noTc[0]
	rt := withTc[0]
	if len(rn.CharBounds) != 3 || len(rt.CharBounds) != 3 {
		t.Fatalf("CharBounds len: noTc=%d withTc=%d, want 3 each",
			len(rn.CharBounds), len(rt.CharBounds))
	}
	// Without Tc, glyphs tile contiguously (gap == 0). With Tc=2, the
	// gap between successive right-edges is the Tc value (horizScale=1).
	for i := 1; i < 3; i++ {
		gapNo := rn.CharBounds[i].X0 - rn.CharBounds[i-1].X1
		if gapNo < -1e-6 || gapNo > 1e-6 {
			t.Errorf("noTc bounds[%d] gap: got %v, want 0", i, gapNo)
		}
		gapTc := rt.CharBounds[i].X0 - rt.CharBounds[i-1].X1
		if gapTc < 2-1e-6 || gapTc > 2+1e-6 {
			t.Errorf("withTc bounds[%d] gap: got %v, want 2", i, gapTc)
		}
	}
	// Glyph widths themselves remain 5 user-units (the Tc adds AFTER
	// the advance, not multiplied into the glyph width).
	for i := range 3 {
		gw := rt.CharBounds[i].X1 - rt.CharBounds[i].X0
		if gw < 5-1e-6 || gw > 5+1e-6 {
			t.Errorf("withTc bounds[%d] width: got %v, want 5", i, gw)
		}
	}
}
