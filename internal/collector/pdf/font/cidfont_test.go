package font

import (
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// TestCIDFontDecoder_IdentityH_ToUnicode loads the cidfont_identity_h
// fixture and asserts decodeCID(1) → 'A', decodeCID(2) → 'B' via the
// embedded /ToUnicode CMap.
func TestCIDFontDecoder_IdentityH_ToUnicode(t *testing.T) {
	t.Parallel()
	rf := loadResolvedFontFromFixture(t, "../testdata/cidfont_identity_h.pdf", "F1")
	if rf.Subtype != "Type0" {
		t.Fatalf("Subtype: got %q, want Type0", rf.Subtype)
	}
	if !rf.CIDToGIDIdentity {
		t.Errorf("CIDToGIDIdentity: got false, want true")
	}
	d, err := newCIDFontDecoder(rf)
	if err != nil {
		t.Fatalf("newCIDFontDecoder: %v", err)
	}
	if !d.hasToUnicode() {
		t.Fatalf("hasToUnicode: false, want true (fixture embeds CMap)")
	}
	rs, ok := d.decodeCID(0x0001)
	if !ok || len(rs) != 1 || rs[0] != 'A' {
		t.Errorf("decodeCID(0x0001): got %v ok=%v, want ['A']", rs, ok)
	}
	rs, ok = d.decodeCID(0x0002)
	if !ok || len(rs) != 1 || rs[0] != 'B' {
		t.Errorf("decodeCID(0x0002): got %v ok=%v, want ['B']", rs, ok)
	}
}

// TestCIDFontDecoder_UnmappedCID covers the (nil, false) miss path
// for CIDs outside the fixture's bfchar pairs.
func TestCIDFontDecoder_UnmappedCID(t *testing.T) {
	t.Parallel()
	rf := loadResolvedFontFromFixture(t, "../testdata/cidfont_identity_h.pdf", "F1")
	d, err := newCIDFontDecoder(rf)
	if err != nil {
		t.Fatalf("newCIDFontDecoder: %v", err)
	}
	if rs, ok := d.decodeCID(0x0099); ok {
		t.Errorf("decodeCID(0x0099): ok=true rs=%v, want false", rs)
	}
}

// TestCIDFontDecoder_NoToUnicode covers the nil-cmap path: a Type0
// font without /ToUnicode produces a decoder whose hasToUnicode() is
// false and whose decodeCID always returns (nil, false).
func TestCIDFontDecoder_NoToUnicode(t *testing.T) {
	t.Parallel()
	rf := &internalpdf.ResolvedFont{
		FontResource:     &internalpdf.FontResource{BaseFont: "AAAAAA+TestNoToUnicode", Subtype: "Type0"},
		CIDToGIDIdentity: true,
	}
	d, err := newCIDFontDecoder(rf)
	if err != nil {
		t.Fatalf("newCIDFontDecoder: %v", err)
	}
	if d.hasToUnicode() {
		t.Errorf("hasToUnicode: true, want false")
	}
	if rs, ok := d.decodeCID(0x0001); ok {
		t.Errorf("decodeCID(0x0001): ok=true rs=%v, want false", rs)
	}
}

// loadResolvedFontFromFixture is a shared test helper: load the
// fixture, return ResolvedFont for `key` on page 0. Fatals on any
// error. Mirrors text/golden_test.go's loadFixture pattern.
func loadResolvedFontFromFixture(t *testing.T, path, key string) *internalpdf.ResolvedFont {
	t.Helper()
	ctx, err := internalpdf.LoadFile(path)
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
