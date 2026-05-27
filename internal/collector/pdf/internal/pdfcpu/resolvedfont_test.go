package pdfcpu

import (
	"testing"
)

// TestResolvedFont_OnePageFixture_F1Helvetica is the baseline smoke test:
// onepage.pdf's F1 → Helvetica is a Standard 14 font with no ToUnicode,
// no /Encoding override, no /Widths, no FontDescriptor. ResolvedFont
// should return the embedded FontResource fully populated and every new
// field at its zero value.
func TestResolvedFont_OnePageFixture_F1Helvetica(t *testing.T) {
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

	rf, err := page.ResolvedFont("F1")
	if err != nil {
		t.Fatalf("ResolvedFont(F1): %v", err)
	}
	if rf == nil {
		t.Fatal("ResolvedFont(F1): got nil, want a resolved font")
	}
	if rf.FontResource == nil {
		t.Fatal("ResolvedFont(F1).FontResource: got nil, want embedded base")
	}
	if rf.BaseFont != "Helvetica" {
		t.Errorf("BaseFont: got %q, want Helvetica", rf.BaseFont)
	}
	if rf.Subtype != "Type1" {
		t.Errorf("Subtype: got %q, want Type1", rf.Subtype)
	}
	if rf.ToUnicodeBytes != nil {
		t.Errorf("ToUnicodeBytes: got %d bytes, want nil", len(rf.ToUnicodeBytes))
	}
	// pdfcpu's writer auto-emits /Encoding /WinAnsiEncoding for core
	// fonts other than Symbol/ZapfDingbats (pkg/pdfcpu/font/fontDict.go).
	// onepage.pdf is Helvetica, so we get the Name encoding rather than
	// an /Encoding dict — assert the predefined-name path.
	if rf.EncodingName != "WinAnsiEncoding" {
		t.Errorf("EncodingName: got %q, want WinAnsiEncoding (pdfcpu auto-emits)", rf.EncodingName)
	}
	if rf.EncodingDictBase != "" {
		t.Errorf("EncodingDictBase: got %q, want empty", rf.EncodingDictBase)
	}
	if len(rf.Differences) != 0 {
		t.Errorf("Differences: got %d entries, want 0", len(rf.Differences))
	}
	if rf.CIDToGIDIdentity {
		t.Errorf("CIDToGIDIdentity: got true, want false (Helvetica is not Type0)")
	}
	if rf.CIDToGIDMap != nil {
		t.Errorf("CIDToGIDMap: got %d entries, want nil", len(rf.CIDToGIDMap))
	}
	if rf.DescendantFontDict != nil {
		t.Errorf("DescendantFontDict: got non-nil, want nil")
	}
	if rf.DescendantSubtype != "" {
		t.Errorf("DescendantSubtype: got %q, want empty", rf.DescendantSubtype)
	}
	// Phase 5 fixturelib auto-populates /FirstChar + /Widths from
	// standard14_widths.dat for Standard 14 fonts (per T2-3). Codes
	// 0x20..0xFF (224 entries) are populated.
	if rf.FirstChar != 0x20 {
		t.Errorf("FirstChar: got %d, want 0x20 (WinAnsi printable range start)", rf.FirstChar)
	}
	if len(rf.Widths) != 0xFF-0x20+1 {
		t.Errorf("Widths: got %d entries, want %d (codes 0x20..0xFF)", len(rf.Widths), 0xFF-0x20+1)
	}
	// Spot-check: code 0x20 (space) → Helvetica space width = 278.
	if len(rf.Widths) > 0 && rf.Widths[0] != 278 {
		t.Errorf("Widths[0] (space): got %d, want 278", rf.Widths[0])
	}
	// Spot-check: code 0x54 (T) → 0x54-0x20 = 0x34 → 611.
	if len(rf.Widths) > 0x34 && rf.Widths[0x34] != 611 {
		t.Errorf("Widths[T@0x54]: got %d, want 611", rf.Widths[0x34])
	}
	if rf.MissingWidth != 0 {
		t.Errorf("MissingWidth: got %d, want 0 (no FontDescriptor)", rf.MissingWidth)
	}
}

// TestResolvedFont_MissingKey_ReturnsNilNil mirrors the equivalent
// FontResource test — an absent resource name returns (nil, nil) so the
// resolver can decide policy (skip the run, fall back, etc.) rather than
// surfacing a misleading error.
func TestResolvedFont_MissingKey_ReturnsNilNil(t *testing.T) {
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

	rf, err := page.ResolvedFont("F999-not-real")
	if err != nil {
		t.Errorf("ResolvedFont(missing): err = %v, want nil", err)
	}
	if rf != nil {
		t.Errorf("ResolvedFont(missing): got %+v, want nil", rf)
	}
}

// TestResolvedFont_NilReceiver_ReturnsError mirrors the FontResource
// defensive branch.
func TestResolvedFont_NilReceiver_ReturnsError(t *testing.T) {
	t.Parallel()
	var p *PageObject
	if _, err := p.ResolvedFont("F1"); err == nil {
		t.Errorf("ResolvedFont on nil PageObject: want error, got nil")
	}
}

// TestPageObject_Context covers the new accessor exposing the Context
// pointer for the document-scoped font resolver.
func TestPageObject_Context(t *testing.T) {
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
	got := page.Context()
	if got != ctx {
		t.Errorf("Context(): got %p, want %p (the owning ctx)", got, ctx)
	}
}
