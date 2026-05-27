package pdfcpu

import (
	"testing"
)

// TestFontResource_OnePageFixture_F1Helvetica covers the happy path:
// the synthesized fixture registers Helvetica as F1, the wrapper
// resolves it, and the FontResource fields match the standard-14
// Helvetica defaults (no FontDescriptor present; Mono/Bold/Italic
// fall through name-pattern fallbacks all returning false).
func TestFontResource_OnePageFixture_F1Helvetica(t *testing.T) {
	t.Parallel()
	ctx, err := LoadFile(onePageFixture)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", onePageFixture, err)
	}
	defer ctx.Close()

	page, err := ctx.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}

	res, err := page.FontResource("F1")
	if err != nil {
		t.Fatalf("FontResource(F1): %v", err)
	}
	if res == nil {
		t.Fatal("FontResource(F1): got nil, want a resolved resource")
	}
	if res.Key != "F1" {
		t.Errorf("Key: got %q, want %q", res.Key, "F1")
	}
	if res.BaseFont != "Helvetica" {
		t.Errorf("BaseFont: got %q, want %q", res.BaseFont, "Helvetica")
	}
	if res.Subtype != "Type1" {
		t.Errorf("Subtype: got %q, want %q", res.Subtype, "Type1")
	}
	if res.Mono {
		t.Errorf("Mono: got true, want false (Helvetica is proportional)")
	}
	if res.Bold {
		t.Errorf("Bold: got true, want false (BaseFont is plain Helvetica)")
	}
	if res.Italic {
		t.Errorf("Italic: got true, want false (BaseFont is plain Helvetica)")
	}
}

// TestFontResource_MissingKey_ReturnsNilNil covers criterion
// 1060337969739f2649fb0ac399db3d8d: an absent resource name returns
// (nil, nil), not an error. The walker uses this to skip
// content-stream Tf references to undeclared fonts.
func TestFontResource_MissingKey_ReturnsNilNil(t *testing.T) {
	t.Parallel()
	ctx, err := LoadFile(onePageFixture)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", onePageFixture, err)
	}
	defer ctx.Close()

	page, err := ctx.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}

	res, err := page.FontResource("F999-not-real")
	if err != nil {
		t.Errorf("FontResource(missing): err = %v, want nil", err)
	}
	if res != nil {
		t.Errorf("FontResource(missing): got %+v, want nil", res)
	}
}

// TestFontResource_NilReceiver_ReturnsError covers the defensive
// branch for nil PageObjects (panic-avoidance).
func TestFontResource_NilReceiver_ReturnsError(t *testing.T) {
	t.Parallel()
	var p *PageObject
	if _, err := p.FontResource("F1"); err == nil {
		t.Errorf("FontResource on nil PageObject: want error, got nil")
	}
}

// TestBaseFontStyleHeuristics covers the name-pattern fallbacks used
// when /FontDescriptor is absent (Standard 14 fonts), per criterion
// e89a48539b19ee42e291de92555798a2.
func TestBaseFontStyleHeuristics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		baseFont              string
		mono, bold, italic    bool
		stripExpectedBaseFont string
	}{
		{baseFont: "Helvetica"},
		{baseFont: "Helvetica-Bold", bold: true, stripExpectedBaseFont: "Helvetica-Bold"},
		{baseFont: "Helvetica-Oblique", italic: true},
		{baseFont: "Helvetica-BoldOblique", bold: true, italic: true},
		{baseFont: "Times-Italic", italic: true},
		{baseFont: "Courier", mono: true},
		{baseFont: "Courier-BoldOblique", mono: true, bold: true, italic: true},
		{baseFont: "AAAAAA+TimesNewRoman-Bold", bold: true},
		{baseFont: "BCDEFG+RobotoMono-Italic", mono: true, italic: true},
		{baseFont: "Helvetica-Heavy", bold: true},
		{baseFont: "Helvetica-Black", bold: true},
	}
	for _, c := range cases {
		t.Run(c.baseFont, func(t *testing.T) {
			t.Parallel()
			if got := baseFontIsMono(c.baseFont); got != c.mono {
				t.Errorf("baseFontIsMono(%q): got %v, want %v", c.baseFont, got, c.mono)
			}
			if got := baseFontIsBold(c.baseFont); got != c.bold {
				t.Errorf("baseFontIsBold(%q): got %v, want %v", c.baseFont, got, c.bold)
			}
			if got := baseFontIsItalic(c.baseFont); got != c.italic {
				t.Errorf("baseFontIsItalic(%q): got %v, want %v", c.baseFont, got, c.italic)
			}
		})
	}
}
