package pdfcpu

import "testing"

// Per-fixture field-path tests for the T3 fixtures emitted by
// collector/pdf/testdata/gen.go. Each test asserts the FontSpec the
// fixture was built from round-tripped through pdfcpu's writer +
// reader and our ResolvedFont accessor preserved the distinguishing
// fields. See plan d76acb28 step 3d913d25.

// TestResolvedFont_TouUnicodeClean: tounicode_clean.pdf has a TrueType
// font with a /ToUnicode CMap and no /Encoding override. Asserts
// ToUnicodeBytes is non-empty and Subtype is TrueType.
func TestResolvedFont_ToUnicodeClean(t *testing.T) {
	t.Parallel()
	rf := loadResolvedFont(t, "../../testdata/tounicode_clean.pdf")
	if rf.Subtype != "TrueType" {
		t.Errorf("Subtype: got %q, want TrueType", rf.Subtype)
	}
	if len(rf.ToUnicodeBytes) == 0 {
		t.Errorf("ToUnicodeBytes: got empty, want non-empty CMap stream")
	}
}

// TestResolvedFont_NoToUnicodeWinAnsi: no_tounicode_winansi.pdf has
// Helvetica with /Encoding /WinAnsiEncoding, no /ToUnicode. Asserts
// ToUnicodeBytes is nil and EncodingName == WinAnsiEncoding.
func TestResolvedFont_NoToUnicodeWinAnsi(t *testing.T) {
	t.Parallel()
	rf := loadResolvedFont(t, "../../testdata/no_tounicode_winansi.pdf")
	if rf.BaseFont != "Helvetica" {
		t.Errorf("BaseFont: got %q, want Helvetica", rf.BaseFont)
	}
	if rf.EncodingName != "WinAnsiEncoding" {
		t.Errorf("EncodingName: got %q, want WinAnsiEncoding", rf.EncodingName)
	}
	if rf.ToUnicodeBytes != nil {
		t.Errorf("ToUnicodeBytes: got %d bytes, want nil", len(rf.ToUnicodeBytes))
	}
}

// TestResolvedFont_DifferencesOverride: differences_override.pdf has
// Encoding=WinAnsiEncoding + Differences override at code 0x41.
// Asserts the parsed Differences[0] contains "smileyface".
func TestResolvedFont_DifferencesOverride(t *testing.T) {
	t.Parallel()
	rf := loadResolvedFont(t, "../../testdata/differences_override.pdf")
	if rf.EncodingDictBase != "WinAnsiEncoding" {
		t.Errorf("EncodingDictBase: got %q, want WinAnsiEncoding", rf.EncodingDictBase)
	}
	if len(rf.Differences) != 1 {
		t.Fatalf("Differences: got %d entries, want 1", len(rf.Differences))
	}
	if rf.Differences[0].Code != 0x41 {
		t.Errorf("Differences[0].Code: got %d, want 0x41", rf.Differences[0].Code)
	}
	if len(rf.Differences[0].Names) != 1 || rf.Differences[0].Names[0] != "smileyface" {
		t.Errorf("Differences[0].Names: got %v, want [smileyface]", rf.Differences[0].Names)
	}
}

// TestResolvedFont_NoEncodingInfo: no_encoding_info.pdf has no
// /ToUnicode, no /Encoding, no /Differences. Resolver falls to its
// bottom rung. Asserts everything is empty.
func TestResolvedFont_NoEncodingInfo(t *testing.T) {
	t.Parallel()
	rf := loadResolvedFont(t, "../../testdata/no_encoding_info.pdf")
	if rf.EncodingName != "" {
		t.Errorf("EncodingName: got %q, want empty", rf.EncodingName)
	}
	if rf.EncodingDictBase != "" {
		t.Errorf("EncodingDictBase: got %q, want empty", rf.EncodingDictBase)
	}
	if len(rf.Differences) != 0 {
		t.Errorf("Differences: got %d, want 0", len(rf.Differences))
	}
	if rf.ToUnicodeBytes != nil {
		t.Errorf("ToUnicodeBytes: got %d bytes, want nil", len(rf.ToUnicodeBytes))
	}
}

// TestResolvedFont_Ligatures: ligatures.pdf has Helvetica with a
// ToUnicode mapping CIDs 0x01/0x02 to multi-rune ligature targets.
// Asserts ToUnicodeBytes is non-empty.
func TestResolvedFont_Ligatures(t *testing.T) {
	t.Parallel()
	rf := loadResolvedFont(t, "../../testdata/ligatures.pdf")
	if rf.BaseFont != "Helvetica" {
		t.Errorf("BaseFont: got %q, want Helvetica", rf.BaseFont)
	}
	if len(rf.ToUnicodeBytes) == 0 {
		t.Errorf("ToUnicodeBytes: empty, want non-empty CMap")
	}
}

// TestResolvedFont_CIDFontIdentityH: cidfont_identity_h.pdf has a
// Type 0 font with a CIDFontType2 descendant + /CIDToGIDMap=/Identity.
// Asserts Subtype==Type0, DescendantSubtype==CIDFontType2,
// CIDToGIDIdentity==true, and ToUnicodeBytes is non-empty.
func TestResolvedFont_CIDFontIdentityH(t *testing.T) {
	t.Parallel()
	rf := loadResolvedFont(t, "../../testdata/cidfont_identity_h.pdf")
	if rf.Subtype != "Type0" {
		t.Errorf("Subtype: got %q, want Type0", rf.Subtype)
	}
	if rf.DescendantSubtype != "CIDFontType2" {
		t.Errorf("DescendantSubtype: got %q, want CIDFontType2", rf.DescendantSubtype)
	}
	if !rf.CIDToGIDIdentity {
		t.Errorf("CIDToGIDIdentity: got false, want true")
	}
	if rf.CIDToGIDMap != nil {
		t.Errorf("CIDToGIDMap: got %d entries, want nil (Identity overrides)", len(rf.CIDToGIDMap))
	}
	if len(rf.ToUnicodeBytes) == 0 {
		t.Errorf("ToUnicodeBytes: empty, want non-empty CMap")
	}
}

// loadResolvedFont is a tiny helper: load fixture, resolve F1 on
// page 0, fatal on any error. Every T3 fixture uses "F1" as the
// font resource key, so the key is inlined here.
func loadResolvedFont(t *testing.T, path string) *ResolvedFont {
	t.Helper()
	const key = "F1"
	ctx, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = ctx.Close() })
	page, err := ctx.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	rf, err := page.ResolvedFont(key)
	if err != nil {
		t.Fatalf("ResolvedFont(%q): %v", key, err)
	}
	if rf == nil {
		t.Fatalf("ResolvedFont(%q): nil", key)
	}
	return rf
}
