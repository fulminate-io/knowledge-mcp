package text

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/font"
	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// charflagsRunAdapter wraps a *TextRun so the run satisfies the
// font.Run interface for full-pipeline tests in this file. The merge
// behavior on SetCharFlags mirrors the production adapter at
// collector/pdf/page.go: bitwise-OR onto an existing slice when the
// run already has CharFlags from T2, plain assign otherwise.
type charflagsRunAdapter struct{ r *TextRun }

func (a charflagsRunAdapter) GlyphsCopy() []uint16 { return a.r.Glyphs }
func (a charflagsRunAdapter) FontKeyValue() string { return a.r.FontKey }
func (a charflagsRunAdapter) FontResourcesHint() internalpdf.FormResources {
	return a.r.FontResourcesHint()
}
func (a charflagsRunAdapter) SetText(s string) { a.r.Text = s }
func (a charflagsRunAdapter) SetCharFlags(f []uint8) {
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

// extractAndDecodeFixture loads the named fixture, runs the T2 walker
// + the T3 font.Decode pipeline, and returns the decoded TextRuns.
// Mirrors collector/pdf/font/resolver_test.go's extractAndDecode but
// lives in-package so the assertions can read CharFlags directly.
func extractAndDecodeFixture(t *testing.T, name string) []TextRun {
	t.Helper()
	ctx, err := internalpdf.LoadFile("../testdata/" + name)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", name, err)
	}
	t.Cleanup(func() { _ = ctx.Close() })
	page, err := ctx.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	runs, err := ExtractRuns(page)
	if err != nil {
		t.Fatalf("ExtractRuns(%q): %v", name, err)
	}
	wrapped := make([]font.Run, len(runs))
	for i := range runs {
		wrapped[i] = charflagsRunAdapter{r: &runs[i]}
	}
	if err := font.Decode(wrapped, page); err != nil {
		t.Fatalf("font.Decode(%q): %v", name, err)
	}
	return runs
}

// TestCharFlags_CleanToUnicode_AllZero exercises the rung-1 hit path
// end-to-end: tounicode_clean.pdf has a TrueType subset font with a
// well-formed /ToUnicode CMap, so every glyph resolves cleanly. No
// flag bits should be set on any glyph — the common-path TextRun
// surface stays zeroed (CharFlags is non-nil after T2 populates it,
// but every byte is 0).
func TestCharFlags_CleanToUnicode_AllZero(t *testing.T) {
	t.Parallel()
	runs := extractAndDecodeFixture(t, "tounicode_clean.pdf")
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	r := runs[0]
	if len(r.CharFlags) != len(r.Glyphs) {
		t.Fatalf("CharFlags len: got %d, want %d (parallel to Glyphs)",
			len(r.CharFlags), len(r.Glyphs))
	}
	for i, b := range r.CharFlags {
		if b != 0 {
			t.Errorf("CharFlags[%d] = 0x%02x, want 0 (clean ToUnicode)", i, b)
		}
	}
}

// TestCharFlags_NoEncodingInfo_AllBadMap covers the rung-4 fallback:
// no_encoding_info.pdf has no /ToUnicode and no /Encoding, so every
// glyph hits the U+FFFD path in resolveGlyphsWithFlags. Every glyph
// should carry the CharFlagBadMap bit (and only that bit — there's
// no BMC region in this fixture).
func TestCharFlags_NoEncodingInfo_AllBadMap(t *testing.T) {
	t.Parallel()
	runs := extractAndDecodeFixture(t, "no_encoding_info.pdf")
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	r := runs[0]
	if len(r.CharFlags) != len(r.Glyphs) {
		t.Fatalf("CharFlags len: got %d, want %d (parallel to Glyphs)",
			len(r.CharFlags), len(r.Glyphs))
	}
	if len(r.Glyphs) != 3 {
		t.Fatalf("glyph count: got %d, want 3 (fixture emits 0x41 0x42 0x43)",
			len(r.Glyphs))
	}
	for i, b := range r.CharFlags {
		if b&CharFlagBadMap == 0 {
			t.Errorf("CharFlags[%d] = 0x%02x, want CharFlagBadMap (0x%02x) set",
				i, b, CharFlagBadMap)
		}
		if b&CharFlagMarkedContent != 0 {
			t.Errorf("CharFlags[%d] has CharFlagMarkedContent set unexpectedly (no BMC)", i)
		}
		if b&CharFlagGenerated != 0 {
			t.Errorf("CharFlags[%d] has CharFlagGenerated set unexpectedly (v1 never synthesizes)", i)
		}
	}
}

// TestCharFlags_BMCRegion_MarkedContentSetInside synthesizes a content
// stream with an explicit BDC..EMC region. Two Tj operators emit two
// runs: the first inside the BDC region (every glyph carries
// CharFlagMarkedContent), the second outside (every glyph zero). This
// exercises T2's appendRun wiring against the marked-content stack
// without involving the T3 decoder.
func TestCharFlags_BMCRegion_MarkedContentSetInside(t *testing.T) {
	t.Parallel()
	body := []byte(`/Span /P << /MCID 5 >> BDC BT /F1 12 Tf 0 0 Td (tagged) Tj ET EMC BT /F1 12 Tf 0 0 Td (untagged) Tj ET`)
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	// Run 0: inside BDC — every glyph carries CharFlagMarkedContent.
	if len(runs[0].CharFlags) != len(runs[0].Glyphs) {
		t.Fatalf("run 0 CharFlags len: got %d, want %d",
			len(runs[0].CharFlags), len(runs[0].Glyphs))
	}
	for i, b := range runs[0].CharFlags {
		if b&CharFlagMarkedContent == 0 {
			t.Errorf("run 0 (tagged) CharFlags[%d] = 0x%02x, want CharFlagMarkedContent set",
				i, b)
		}
	}
	// Run 1: outside the EMC — every glyph zero.
	if len(runs[1].CharFlags) != len(runs[1].Glyphs) {
		t.Fatalf("run 1 CharFlags len: got %d, want %d",
			len(runs[1].CharFlags), len(runs[1].Glyphs))
	}
	for i, b := range runs[1].CharFlags {
		if b != 0 {
			t.Errorf("run 1 (untagged) CharFlags[%d] = 0x%02x, want 0", i, b)
		}
	}
}

// TestCharFlags_Mixed_BMCAndBadMap exercises the merge path: a run
// emitted inside a BDC region (T2 sets CharFlagMarkedContent) AND
// resolved against a font with no /ToUnicode and no /Encoding (T3
// sets CharFlagBadMap on every glyph). Both bits must end up set on
// every glyph — the production adapter ORs T3's flag slice onto the
// T2-populated CharFlags rather than overwriting.
func TestCharFlags_Mixed_BMCAndBadMap(t *testing.T) {
	t.Parallel()
	// Load the no-encoding-info fixture purely for its page handle —
	// we need a real page so font.Decode can resolve /F1. The fixture
	// page's /F1 is the no-encoding-info TrueType font.
	ctx, err := internalpdf.LoadFile("../testdata/no_encoding_info.pdf")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	t.Cleanup(func() { _ = ctx.Close() })
	page, err := ctx.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	// Construct a TextRun the way T2 would have inside a BDC region:
	// pre-populate CharFlags with CharFlagMarkedContent set on every
	// glyph. The 3 glyph codes match no_encoding_info.pdf's body
	// (0x41 0x42 0x43) so font.Decode hits the rung-4 fallback for
	// every one.
	run := TextRun{
		Glyphs:    []uint16{0x41, 0x42, 0x43},
		FontKey:   "F1",
		CharFlags: []uint8{CharFlagMarkedContent, CharFlagMarkedContent, CharFlagMarkedContent},
	}
	wrapped := []font.Run{charflagsRunAdapter{r: &run}}
	if err := font.Decode(wrapped, page); err != nil {
		t.Fatalf("font.Decode: %v", err)
	}
	if len(run.CharFlags) != 3 {
		t.Fatalf("CharFlags len after Decode: got %d, want 3", len(run.CharFlags))
	}
	for i, b := range run.CharFlags {
		if b&CharFlagMarkedContent == 0 {
			t.Errorf("mixed[%d] = 0x%02x: CharFlagMarkedContent dropped (T3 should OR, not overwrite)",
				i, b)
		}
		if b&CharFlagBadMap == 0 {
			t.Errorf("mixed[%d] = 0x%02x: CharFlagBadMap not set (T3 rung-4 must mark)",
				i, b)
		}
	}
}

// TestCharFlags_GeneratedAlwaysZero locks in the v1 reservation: no
// production T2 emit path sets CharFlagGenerated. The bit is reserved
// for a future layout-pass synthesis stage; v1 emits only glyphs the
// content stream literally contains. Exercise a synthesized stream
// with a wider word-margin gap (ABC then a translation, then DEF) and
// assert no glyph in either run carries the Generated bit.
func TestCharFlags_GeneratedAlwaysZero(t *testing.T) {
	t.Parallel()
	body := []byte("BT /F1 12 Tf 0 0 Td (ABC) Tj 100 0 Td (DEF) Tj ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	for ri, r := range runs {
		for i, b := range r.CharFlags {
			if b&CharFlagGenerated != 0 {
				t.Errorf("run %d CharFlags[%d] = 0x%02x: CharFlagGenerated set, but v1 does not synthesize",
					ri, i, b)
			}
		}
	}
}
