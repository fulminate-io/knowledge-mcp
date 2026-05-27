package font_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/font"
	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// fixturePath helper — fixtures live under collector/pdf/testdata,
// resolver_test.go runs from collector/pdf/font/.
func fixturePath(name string) string { return "../testdata/" + name }

// loadPage loads a fixture and returns Page(0). Fatals on any error.
func loadPage(t *testing.T, name string) *internalpdf.PageObject {
	t.Helper()
	ctx, err := internalpdf.LoadFile(fixturePath(name))
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", name, err)
	}
	t.Cleanup(func() { _ = ctx.Close() })
	page, err := ctx.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	return page
}

// runAdapter is a tiny shim wrapping text.TextRun so it satisfies the
// font.Run interface (set on the value pointer to mutate Text).
type runAdapter struct {
	r *text.TextRun
}

func (a runAdapter) GlyphsCopy() []uint16 { return a.r.Glyphs }
func (a runAdapter) FontKeyValue() string { return a.r.FontKey }
func (a runAdapter) FontResourcesHint() internalpdf.FormResources {
	return a.r.FontResourcesHint()
}
func (a runAdapter) SetText(s string) { a.r.Text = s }
func (a runAdapter) SetCharFlags(f []uint8) {
	if len(f) == 0 {
		return
	}
	if len(a.r.CharFlags) == len(f) {
		for i, b := range f {
			a.r.CharFlags[i] |= b
		}
		return
	}
	a.r.CharFlags = f
}

// extractAndDecode runs the full pipeline on page 0 of name and
// returns the decoded TextRuns.
func extractAndDecode(t *testing.T, name string) []text.TextRun {
	t.Helper()
	page := loadPage(t, name)
	runs, err := text.ExtractRuns(page)
	if err != nil {
		t.Fatalf("ExtractRuns(%q): %v", name, err)
	}
	wrapped := make([]font.Run, len(runs))
	for i := range runs {
		wrapped[i] = runAdapter{r: &runs[i]}
	}
	if err := font.Decode(wrapped, page); err != nil {
		t.Fatalf("Decode(%q): %v", name, err)
	}
	return runs
}

// TestDecode_ToUnicodeClean: tounicode_clean.pdf emits hex <4142> via a
// TrueType subset font with a clean ToUnicode CMap. Expected text "AB".
func TestDecode_ToUnicodeClean(t *testing.T) {
	t.Parallel()
	runs := extractAndDecode(t, "tounicode_clean.pdf")
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Text != "AB" {
		t.Errorf("Text: got %q, want \"AB\"", runs[0].Text)
	}
}

// TestDecode_NoToUnicode_WinAnsi: Helvetica with /Encoding /WinAnsiEncoding
// and "Hello" literal. Encoding-table → AGL path produces "Hello".
func TestDecode_NoToUnicode_WinAnsi(t *testing.T) {
	t.Parallel()
	runs := extractAndDecode(t, "no_tounicode_winansi.pdf")
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Text != "Hello" {
		t.Errorf("Text: got %q, want \"Hello\"", runs[0].Text)
	}
}

// TestDecode_DifferencesOverride: byte 0x41 mapped to "smileyface" (a
// glyph name NOT in AGL). The resolver falls through to the
// replacement char per rung 4.
func TestDecode_DifferencesOverride(t *testing.T) {
	t.Parallel()
	runs := extractAndDecode(t, "differences_override.pdf")
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Text != string(rune(0xFFFD)) {
		t.Errorf("Text: got %q (%U), want U+FFFD", runs[0].Text, []rune(runs[0].Text))
	}
}

// TestDecode_NoEncodingInfo: TrueType font with no /ToUnicode and no
// /Encoding. Resolver falls through to replacement char for every
// glyph.
func TestDecode_NoEncodingInfo(t *testing.T) {
	t.Parallel()
	runs := extractAndDecode(t, "no_encoding_info.pdf")
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	// 3 bytes (0x41 0x42 0x43) → 3 replacement chars.
	want := strings.Repeat(string(rune(0xFFFD)), 3)
	if runs[0].Text != want {
		t.Errorf("Text: got %q, want 3 U+FFFD", runs[0].Text)
	}
}

// TestDecode_Ligatures: ligatures.pdf maps CID 0x01 → "fi" (UTF-16BE
// 0066 0069) and 0x02 → "fl". 2 glyphs decode to 4 codepoints.
func TestDecode_Ligatures(t *testing.T) {
	t.Parallel()
	runs := extractAndDecode(t, "ligatures.pdf")
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	got := runs[0].Text
	if got != "fifl" {
		t.Errorf("Text: got %q, want \"fifl\"", got)
	}
	if utf8.RuneCountInString(got) != 4 {
		t.Errorf("rune count: got %d, want 4 (decomposed ligatures)", utf8.RuneCountInString(got))
	}
}

// TestDecode_CIDFontIdentityH: Type 0 font with Identity-H encoding,
// CIDFontType2 descendant, /CIDToGIDMap=/Identity, ToUnicode mapping
// CIDs 0x0001 → 'A', 0x0002 → 'B'. Content emits 2 CIDs → text "AB".
func TestDecode_CIDFontIdentityH(t *testing.T) {
	t.Parallel()
	runs := extractAndDecode(t, "cidfont_identity_h.pdf")
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Text != "AB" {
		t.Errorf("Text: got %q, want \"AB\"", runs[0].Text)
	}
}

// TestFontResolver_DocScopeCaching covers criterion 07da97a0 (T3-8 +
// T3-1): iterate pages[i%3] for i=0..99 against multipage_one_font.pdf;
// the document-scoped resolver should call parseCMap exactly ONCE
// because all 3 pages share Helvetica F1 by content.
func TestFontResolver_DocScopeCaching(t *testing.T) {
	// Not t.Parallel() — ParseCMapCalls() reads global counter.
	font.ResetParseCMapCalls()

	ctx, err := internalpdf.LoadFile(fixturePath("multipage_one_font.pdf"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	defer ctx.Close()
	if pc := ctx.PageCount(); pc != 3 {
		t.Fatalf("page count: got %d, want 3", pc)
	}
	pages := make([]*internalpdf.PageObject, 3)
	for i := range 3 {
		p, err := ctx.Page(i)
		if err != nil {
			t.Fatalf("Page(%d): %v", i, err)
		}
		pages[i] = p
	}
	resolver := font.NewDocResolver(ctx)
	for i := range 100 {
		page := pages[i%3]
		runs, err := text.ExtractRuns(page)
		if err != nil {
			t.Fatalf("ExtractRuns iter=%d: %v", i, err)
		}
		wrapped := make([]font.Run, len(runs))
		for j := range runs {
			wrapped[j] = runAdapter{r: &runs[j]}
		}
		if err := resolver.DecodePage(wrapped, page); err != nil {
			t.Fatalf("DecodePage iter=%d: %v", i, err)
		}
	}
	calls := font.ParseCMapCalls()
	if calls != 1 {
		t.Errorf("ParseCMapCalls: got %d, want 1 (cache must dedupe by content)", calls)
	}
}

// TestDecode_NilSlice covers the empty-input shortcut in Decode.
func TestDecode_NilSlice(t *testing.T) {
	t.Parallel()
	if err := font.Decode(nil, nil); err != nil {
		t.Fatalf("Decode(nil, nil): %v", err)
	}
}
