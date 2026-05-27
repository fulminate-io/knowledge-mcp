package text

import (
	"math"
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// dodTolerance is the ticket-DoD tolerance per ticket
// 6ad90fa5d46f3b5f7e2a48ca2a9838b8: width assertions must hold within
// fontSize × 5e-4 = 0.5 per-mille. Computed for the 12pt fixtures used
// throughout golden_test.go. Real /Widths fire via the Phase 10
// 4-rung ladder (rung 1 for Standard 14 fonts that emit /Widths via
// fixturelib per T2-3, OR rung 3 for fonts without /Widths via the
// single-source standard14_widths.dat per T3-2).
const dodTolerance = 12 * 5e-4

// loadFixture is a small helper: opens the synthesized PDF at
// testdata/<name>, returns the Page(0) handle. Fatals on any error.
// All golden tests use this — no extras needed at this scope.
func loadFixture(t *testing.T, name string) *internalpdf.PageObject {
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
	return page
}

// TestGolden_OnePage_HelloT1 pins the baseline fixture's run shape:
// one TextRun, FontKey=F1, FontName=Helvetica, X=100, Y=700, 9 glyphs
// for "Hello, T1".
func TestGolden_OnePage_HelloT1(t *testing.T) {
	t.Parallel()
	page := loadFixture(t, "onepage.pdf")

	runs, err := ExtractRuns(page)
	if err != nil {
		t.Fatalf("ExtractRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1: %+v", len(runs), runs)
	}
	r := runs[0]
	if r.FontKey != "F1" {
		t.Errorf("FontKey: got %q, want F1", r.FontKey)
	}
	if r.FontName != "Helvetica" {
		t.Errorf("FontName: got %q, want Helvetica", r.FontName)
	}
	if r.Size != 12 {
		t.Errorf("Size: got %v, want 12", r.Size)
	}
	if r.X != 100 || r.Y != 700 {
		t.Errorf("origin: got (%v, %v), want (100, 700)", r.X, r.Y)
	}
	if len(r.Glyphs) != 9 {
		t.Errorf("glyphs len: got %d, want 9 (Hello, T1)", len(r.Glyphs))
	}
	// Tolerance: fontSize * 5e-4 = 0.006pt at 12pt — ticket-DoD
	// ±0.5 per-mille per ticket 6ad90fa5d46f3b5f7e2a48ca2a9838b8.
	// Real /Widths fire via the Phase 10 ladder (rung 1 for Standard
	// 14 with /Widths emitted by fixturelib per T2-3).
	//
	// Expected width = sum of Helvetica AFM widths × 12pt / 1000:
	//   H(722) + e(556) + l(222) + l(222) + o(556) + ,(278) +
	//   space(278) + T(611) + one(556) = 4001 / 1000 × 12 = 48.012pt.
	const wantWidth = 48.012
	if math.Abs(r.Width-wantWidth) > dodTolerance {
		t.Errorf("Width: got %v, want %v ± %v (ticket-DoD)", r.Width, wantWidth, dodTolerance)
	}
}

// TestGolden_Paragraph pins the 3-line paragraph: 3 TextRuns, each
// 12pt Helvetica, decreasing Y by 14pt (TL = 14).
func TestGolden_Paragraph(t *testing.T) {
	t.Parallel()
	page := loadFixture(t, "paragraph.pdf")

	runs, err := ExtractRuns(page)
	if err != nil {
		t.Fatalf("ExtractRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("got %d runs, want 3", len(runs))
	}
	for i, r := range runs {
		if r.FontKey != "F1" || r.Size != 12 {
			t.Errorf("run %d font: %q/%v", i, r.FontKey, r.Size)
		}
	}
	// First-line origin: 100 700 Td; subsequent T* drops by leading=14.
	wantY := []float64{700, 686, 672}
	for i, w := range wantY {
		if runs[i].Y != w {
			t.Errorf("run %d Y: got %v, want %v", i, runs[i].Y, w)
		}
		if runs[i].X != 100 {
			t.Errorf("run %d X: got %v, want 100", i, runs[i].X)
		}
	}
}

// TestGolden_TJKerning pins the kerning fixture: the TJ array
// "[(Hel) -100 (lo) -50 (world)]" produces 1 TextRun whose Width
// reflects the per-mille kerning subtractions (negative numbers in
// PDF TJ are converted to positive width additions in our walker).
func TestGolden_TJKerning(t *testing.T) {
	t.Parallel()
	page := loadFixture(t, "tj_kerning.pdf")

	runs, err := ExtractRuns(page)
	if err != nil {
		t.Fatalf("ExtractRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1: %+v", len(runs), runs)
	}
	r := runs[0]
	// Glyphs: "Hel" (3) + "lo" (2) + "world" (5) = 10
	if len(r.Glyphs) != 10 {
		t.Errorf("glyphs: got %d, want 10", len(r.Glyphs))
	}
	// Tolerance: ticket-DoD ±0.5 per-mille per ticket
	// 6ad90fa5d46f3b5f7e2a48ca2a9838b8. Real Helvetica AFM widths
	// land via the Phase 10 ladder (rung 1 — /Widths emitted by
	// fixturelib per T2-3).
	//
	// Expected width = sum of Helvetica AFM widths × 12pt / 1000
	// minus TJ kerning subtractions:
	//   "Hel" = H(722) + e(556) + l(222) = 1500
	//   "lo"  = l(222) + o(556) = 778
	//   "world" = w(722) + o(556) + r(333) + l(222) + d(556) = 2389
	//   Sum = 4667; ×12/1000 = 56.004pt.
	//   TJ subtraction: -100 → width -= -1.2 → +1.2pt;
	//                  -50  → width -= -0.6 → +0.6pt.
	//   Total = 56.004 + 1.2 + 0.6 = 57.804pt.
	const wantWidth = 57.804
	if math.Abs(r.Width-wantWidth) > dodTolerance {
		t.Errorf("Width: got %v, want %v ± %v (ticket-DoD)", r.Width, wantWidth, dodTolerance)
	}
}

// TestGolden_TfChanges pins the mid-stream font-switch fixture: 2
// TextRuns, the first under F1 (Helvetica), the second under F2
// (Helvetica-Bold).
func TestGolden_TfChanges(t *testing.T) {
	t.Parallel()
	page := loadFixture(t, "tf_changes.pdf")

	runs, err := ExtractRuns(page)
	if err != nil {
		t.Fatalf("ExtractRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2: %+v", len(runs), runs)
	}
	if runs[0].FontKey != "F1" || runs[0].FontName != "Helvetica" {
		t.Errorf("run 0 font: %q/%q", runs[0].FontKey, runs[0].FontName)
	}
	if runs[1].FontKey != "F2" || runs[1].FontName != "Helvetica-Bold" {
		t.Errorf("run 1 font: %q/%q", runs[1].FontKey, runs[1].FontName)
	}
	// Bold heuristic: BaseFont contains "Bold".
	if !runs[1].Bold {
		t.Errorf("run 1 Bold flag: got false, want true (Helvetica-Bold)")
	}
}

// TestGolden_MarkedContent pins the BDC/EMC fixture: 2 TextRuns, the
// first carrying MCID=5 (inside the BDC region), the second carrying
// MCID=0 (untagged). Verifies criterion a568144b6f86d16cf611e3adfafeb21d.
func TestGolden_MarkedContent(t *testing.T) {
	t.Parallel()
	page := loadFixture(t, "marked_content.pdf")

	runs, err := ExtractRuns(page)
	if err != nil {
		t.Fatalf("ExtractRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2: %+v", len(runs), runs)
	}
	if runs[0].MCID != 5 {
		t.Errorf("tagged MCID: got %d, want 5", runs[0].MCID)
	}
	if runs[1].MCID != 0 {
		t.Errorf("untagged MCID: got %d, want 0", runs[1].MCID)
	}
}

// TestGolden_FormXObject pins the Form XObject recursion path: the
// page references /Fm1 via a Do operator; the Form's content stream
// has BT/Tj/ET inside ("xobj-text"), and T2 walks into the Form,
// emitting that text as a regular TextRun. Supersedes the prior
// log-and-skip scope decision (the OSS-publishing rule + acceptance
// criterion of parsing real publisher PDFs require recursion: tech-
// book code blocks, figure captions, and cover pages all live inside
// Form XObjects).
//
// T4.6 extends this test with the no-Form-Resources inheritance
// regression: when the Form has NO own /Resources dict, its run's
// FontResourcesHint is nil, so font.Decode falls back to the
// page-level resolver path — the existing T3 behavior. The added
// font.Decode + clean-CharFlags assertions cover that fall-through.
func TestGolden_FormXObject(t *testing.T) {
	t.Parallel()
	runs := extractAndDecodeFixture(t, "form_xobject.pdf")
	if len(runs) != 1 {
		t.Fatalf("got %d TextRuns, want 1 (recursed Form text 'xobj-text'): %+v", len(runs), runs)
	}
	r := runs[0]
	if r.FontKey != "F1" {
		t.Errorf("FontKey: got %q, want F1", r.FontKey)
	}
	if r.Size != 12 {
		t.Errorf("Size: got %v, want 12", r.Size)
	}
	if len(r.Glyphs) != len("xobj-text") {
		t.Errorf("glyph count: got %d, want %d (xobj-text)", len(r.Glyphs), len("xobj-text"))
	}
	// Inheritance regression: Form has no /Resources, so the run's
	// FontResourcesHint is nil and font.Decode resolves T1_0... no,
	// resolves /F1 against the page-level Resources. The Form's text
	// renders via Helvetica (no /ToUnicode in this fixture), so no
	// BadMap bits should be set — Standard-14 implicit StandardEncoding
	// covers ASCII. CharFlags must be zero for every glyph.
	if r.FontResourcesHint() != nil {
		t.Errorf("FontResourcesHint: got non-nil, want nil (Form has no /Resources)")
	}
	for i, b := range r.CharFlags {
		if b&CharFlagBadMap != 0 {
			t.Errorf("CharFlags[%d] = %#x, BadMap bit set on inherited-F1 path", i, b)
		}
	}
}

// TestGolden_FormXObjectOwnFonts pins the T4.6 acceptance shape: the
// page emits one run via page-level F1 (Helvetica) decoding to
// "page-text", and recurses into a Form XObject whose own
// /Resources/Font defines T1_0 (Helvetica-Bold) with its own
// /ToUnicode CMap decoding to "xobj-text". The page-level Resources
// does NOT define T1_0, so without the FontResourcesHint plumbing the
// resolver would fall back to U+FFFD and set CharFlagBadMap on every
// xobj glyph. With the threading in place, both runs decode cleanly.
func TestGolden_FormXObjectOwnFonts(t *testing.T) {
	t.Parallel()
	runs := extractAndDecodeFixture(t, "form_xobject_own_fonts.pdf")
	if len(runs) != 2 {
		t.Fatalf("got %d TextRuns, want 2 (page F1 + Form T1_0): %+v", len(runs), runs)
	}

	page := runs[0]
	if page.FontKey != "F1" {
		t.Errorf("page run FontKey: got %q, want F1", page.FontKey)
	}
	if page.Text != "page-text" {
		t.Errorf("page run Text: got %q, want %q", page.Text, "page-text")
	}
	if page.FontResourcesHint() != nil {
		t.Errorf("page run FontResourcesHint: got non-nil, want nil (page-level run)")
	}
	for i, b := range page.CharFlags {
		if b&CharFlagBadMap != 0 {
			t.Errorf("page run CharFlags[%d] = %#x, BadMap bit set", i, b)
		}
	}

	form := runs[1]
	if form.FontKey != "T1_0" {
		t.Errorf("form run FontKey: got %q, want T1_0", form.FontKey)
	}
	if form.Text != "xobj-text" {
		t.Errorf("form run Text: got %q, want %q", form.Text, "xobj-text")
	}
	// Load-bearing: the Form-context run must carry the Form's
	// /Resources hint so the resolver consults it instead of the
	// page-level Resources (which lacks T1_0).
	if form.FontResourcesHint() == nil {
		t.Errorf("form run FontResourcesHint: got nil, want Form's /Resources dict")
	}
	for i, b := range form.CharFlags {
		if b&CharFlagBadMap != 0 {
			t.Errorf("form run CharFlags[%d] = %#x, BadMap bit set (FontResourcesHint plumbing failed)", i, b)
		}
	}
}
