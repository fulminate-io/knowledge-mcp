package text

import (
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// fontsF1 is the canonical {F1: Helvetica/Type1} table used by every
// per-operator test that needs an applyTf hit.
func fontsF1() map[string]*internalpdf.ResolvedFont {
	return map[string]*internalpdf.ResolvedFont{"F1": helveticaF1()}
}

func TestExtract_BT_ET_Tf_Tj(t *testing.T) {
	t.Parallel()
	body := []byte("BT /F1 12 Tf 100 700 Td (Hello) Tj ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1: %+v", len(runs), runs)
	}
	r := runs[0]
	if r.FontKey != "F1" || r.FontName != "Helvetica" || r.Size != 12 {
		t.Errorf("font fields: %+v", r)
	}
	if r.X != 100 || r.Y != 700 {
		t.Errorf("origin: got (%v, %v), want (100, 700)", r.X, r.Y)
	}
	if len(r.Glyphs) != 5 {
		t.Errorf("glyphs len: %d, want 5", len(r.Glyphs))
	}
	if r.Width <= 0 {
		t.Errorf("Width should be positive: %v", r.Width)
	}
}

func TestExtract_Tc_AddsCharSpacing(t *testing.T) {
	t.Parallel()
	bodyNoTc := []byte("BT /F1 12 Tf 0 0 Td (Hello) Tj ET")
	bodyWithTc := []byte("BT /F1 12 Tf 2 Tc 0 0 Td (Hello) Tj ET")
	a := extractFromBytes(t, bodyNoTc, fontsF1(), ExtractOptions{})
	b := extractFromBytes(t, bodyWithTc, fontsF1(), ExtractOptions{})
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("expected one run each: a=%d b=%d", len(a), len(b))
	}
	// Tc=2 over 5 glyphs adds 10 user units to the width.
	if got := b[0].Width - a[0].Width; got < 9.5 || got > 10.5 {
		t.Errorf("Tc width delta: got %v, want ~10", got)
	}
}

func TestExtract_Tw_AddsWordSpacingOnSpaces(t *testing.T) {
	t.Parallel()
	bodyNoTw := []byte("BT /F1 12 Tf 0 0 Td (a b c) Tj ET")
	bodyWithTw := []byte("BT /F1 12 Tf 5 Tw 0 0 Td (a b c) Tj ET")
	a := extractFromBytes(t, bodyNoTw, fontsF1(), ExtractOptions{})
	b := extractFromBytes(t, bodyWithTw, fontsF1(), ExtractOptions{})
	// Tw=5 adds 5 per literal space; "a b c" has 2 spaces -> +10.
	if got := b[0].Width - a[0].Width; got < 9.5 || got > 10.5 {
		t.Errorf("Tw width delta: got %v, want ~10", got)
	}
}

func TestExtract_Tz_ScalesWidth(t *testing.T) {
	t.Parallel()
	bodyNo := []byte("BT /F1 12 Tf 0 0 Td (Hello) Tj ET")
	bodyDouble := []byte("BT /F1 12 Tf 200 Tz 0 0 Td (Hello) Tj ET")
	a := extractFromBytes(t, bodyNo, fontsF1(), ExtractOptions{})
	b := extractFromBytes(t, bodyDouble, fontsF1(), ExtractOptions{})
	// Tz=200 -> horizScale = 2.0; width should approximately double.
	ratio := b[0].Width / a[0].Width
	if ratio < 1.9 || ratio > 2.1 {
		t.Errorf("Tz width ratio: got %v, want ~2.0", ratio)
	}
}

func TestExtract_TL_TStar_DropsByLeading(t *testing.T) {
	t.Parallel()
	body := []byte("BT /F1 12 Tf 14 TL 100 700 Td (line1) Tj T* (line2) Tj ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	// Line2 must drop by 14 user-units below line1.
	if dy := runs[0].Y - runs[1].Y; dy < 13.5 || dy > 14.5 {
		t.Errorf("line drop: got %v, want ~14", dy)
	}
	// X should stay at the line-start (since TStar resets line origin).
	if runs[0].X != runs[1].X {
		t.Errorf("line2 X: got %v vs line1 %v", runs[1].X, runs[0].X)
	}
}

func TestExtract_Td_TD(t *testing.T) {
	t.Parallel()
	// TD sets leading to -ty AND moves cursor; verify subsequent
	// T* lands at -2*leading-from-origin.
	body := []byte("BT /F1 12 Tf 100 700 Td 5 -10 TD (a) Tj T* (b) Tj ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	// First run after TD: tx-cursor moves to 105, ty-cursor moves to 690.
	if runs[0].X != 105 || runs[0].Y != 690 {
		t.Errorf("after TD: got (%v, %v), want (105, 690)", runs[0].X, runs[0].Y)
	}
	// T* with leading=10 drops Y by another 10 to 680.
	if runs[1].Y != 680 {
		t.Errorf("after TD-then-TStar: got Y=%v, want 680", runs[1].Y)
	}
}

func TestExtract_Tm_AbsoluteSet(t *testing.T) {
	t.Parallel()
	body := []byte("BT /F1 12 Tf 1 0 0 1 50 60 Tm (X) Tj ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].X != 50 || runs[0].Y != 60 {
		t.Errorf("Tm origin: got (%v, %v), want (50, 60)", runs[0].X, runs[0].Y)
	}
}

func TestExtract_Tr_InvisibleSuppressed_ByDefault(t *testing.T) {
	t.Parallel()
	body := []byte("BT /F1 12 Tf 3 Tr 0 0 Td (hidden) Tj ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 0 {
		t.Errorf("Tr 3 default: got %d runs, want 0", len(runs))
	}
}

func TestExtract_Tr_InvisibleEmittedWith_IncludeInvisible(t *testing.T) {
	t.Parallel()
	body := []byte("BT /F1 12 Tf 3 Tr 0 0 Td (hidden) Tj ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{IncludeInvisible: true})
	if len(runs) != 1 {
		t.Errorf("IncludeInvisible: got %d runs, want 1", len(runs))
	}
}

func TestExtract_Ts_RiseAffectsBaselineY(t *testing.T) {
	t.Parallel()
	// Two runs: first plain, second with rise=5. Y should differ by 5.
	body := []byte("BT /F1 12 Tf 0 100 Td (a) Tj 5 Ts (b) Tj ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 2 {
		t.Fatalf("got %d, want 2", len(runs))
	}
	if dy := runs[1].Y - runs[0].Y; dy < 4.5 || dy > 5.5 {
		t.Errorf("Ts rise dy: got %v, want ~5", dy)
	}
}

func TestExtract_TJ_ArrayKerning(t *testing.T) {
	t.Parallel()
	// Negative kerning = positive width subtraction (PDF convention).
	// Plain "Hello" vs "[(Hel) -100 (lo)]" should differ by ~1.2 units
	// (100/1000 * 12 = 1.2).
	plain := []byte("BT /F1 12 Tf 0 0 Td (Hello) Tj ET")
	kerned := []byte("BT /F1 12 Tf 0 0 Td [(Hel) -100 (lo)] TJ ET")
	a := extractFromBytes(t, plain, fontsF1(), ExtractOptions{})
	b := extractFromBytes(t, kerned, fontsF1(), ExtractOptions{})
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("a=%d b=%d", len(a), len(b))
	}
	// kerned width should be GREATER than plain (negative number adds
	// width in our convention). Approximately 1.2 units more.
	if got := b[0].Width - a[0].Width; got < 1.0 || got > 1.4 {
		t.Errorf("kerning width delta: got %v, want ~1.2", got)
	}
}

func TestExtract_QuoteAposOperator_DropsLineThenShows(t *testing.T) {
	t.Parallel()
	body := []byte("BT /F1 12 Tf 14 TL 100 700 Td (first) Tj (second) ' ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 2 {
		t.Fatalf("got %d, want 2", len(runs))
	}
	if dy := runs[0].Y - runs[1].Y; dy < 13.5 || dy > 14.5 {
		t.Errorf("apos drop: got %v, want ~14", dy)
	}
}

func TestExtract_QuoteQuoteOperator_SetsTcTwAndShows(t *testing.T) {
	t.Parallel()
	body := []byte(`BT /F1 12 Tf 14 TL 100 700 Td 5 1 (foo) " ET`)
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 1 {
		t.Fatalf("got %d, want 1", len(runs))
	}
	// Width should reflect Tc=1 added per glyph (3 glyphs * 1 = +3)
	// plus Tw=5 on no spaces (no effect since "foo" has none).
	if runs[0].Width <= 0 {
		t.Errorf("Width should be positive: %v", runs[0].Width)
	}
}

func TestExtract_Q_CmRoundtrip(t *testing.T) {
	t.Parallel()
	// q + cm pushes/translates; Q restores. Two emits at same logical
	// position but Q-bracketed transforms should put run2 back at run1.
	body := []byte(`BT /F1 12 Tf 100 700 Td (a) Tj ET q 1 0 0 1 200 0 cm BT /F1 12 Tf 100 700 Td (b) Tj ET Q`)
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 2 {
		t.Fatalf("got %d, want 2", len(runs))
	}
	// Run 'a' at (100,700); run 'b' at (300,700) due to cm tx=200.
	if runs[0].X != 100 || runs[1].X != 300 {
		t.Errorf("q/cm origins: a=(%v,%v) b=(%v,%v)", runs[0].X, runs[0].Y, runs[1].X, runs[1].Y)
	}
}

// Marked-content + suppression + malformed-operator + depth-cap tests
// live in content_stream_marked_test.go.
