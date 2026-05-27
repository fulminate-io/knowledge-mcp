package text

import internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"

// Rect is the text-package's local copy of a PDF rectangle. Every
// sub-package that needs an axis-aligned user-space rect carries its
// own struct (collector/pdf/types.go, layout/types.go, chunk/types.go,
// internal/pdfcpu/page.go, structtree/types.go) — text→layout would be
// a back-edge (layout already imports text), so the established pattern
// is a local definition. Field shape is identical across all five
// duplicates; conversion at package boundaries is field-for-field.
type Rect struct {
	X0, Y0, X1, Y1 float64
}

// Per-glyph flag bits carried in TextRun.CharFlags. Each constant is a
// single-bit mask; combine via bitwise OR. Zero means "no flags set",
// the common clean-mapping case.
const (
	// CharFlagGenerated marks a glyph that was synthesized by the
	// extractor rather than emitted by the content stream — e.g. a
	// space inserted by a layout pass between two glyph-runs separated
	// by a wider-than-glyph horizontal gap. Reserved for layout-pass
	// synthesized spaces; v1 does not synthesize at extraction time, so
	// production code paths in this package never set this bit. Kept on
	// the type for downstream consumers and tests that synthesize
	// glyphs manually.
	CharFlagGenerated uint8 = 1 << 0

	// CharFlagBadMap marks a glyph whose Unicode mapping is low-trust:
	// the resolver fell back past the /ToUnicode CMap, /Encoding table,
	// and Adobe Glyph List rungs, and emitted U+FFFD (or a fingerprint
	// guess in later phases). Downstream consumers can use the flag to
	// route the run through OCR or to surface a confidence score.
	CharFlagBadMap uint8 = 1 << 1

	// CharFlagMarkedContent marks a glyph emitted while the marked-
	// content stack was non-empty — i.e. inside a BMC/BDC ... EMC
	// region of the content stream. T2 sets the bit uniformly across
	// every glyph in a run when the run is appended inside a marked
	// region; mid-run BMC/BDC variation (rare) is out of v1 scope.
	CharFlagMarkedContent uint8 = 1 << 2
)

// TextRun is a single positioned glyph-run extracted from a PDF page.
// T1 only declares the type; T2 fills it during content-stream walking
// and T3 fills the decoded UTF-8 string. The shape is the ticket-pinned
// 13-field surface that the rest of the pipeline (layout, classify,
// chunk, structtree) reads from.
type TextRun struct {
	// Text is the decoded UTF-8 string for this run. Populated by T3
	// after CMap / Encoding / glyph→Unicode mapping has resolved each
	// raw glyph id. Empty until then.
	Text string

	// Glyphs is the raw glyph-id sequence emitted by the content stream
	// (one uint16 per glyph). Populated by T2's content-stream walker.
	Glyphs []uint16

	// X is the lower-left x coordinate of the run's bounding box in
	// user-space (PDF points, +y up).
	X float64

	// Y is the lower-left y coordinate of the run's bounding box in
	// user-space (PDF points, +y up).
	Y float64

	// Width is the run's advance width in user-space points.
	Width float64

	// Height is the run's font-derived height (typically the font size,
	// possibly adjusted by ascender / descender) in user-space points.
	Height float64

	// FontName is the human-readable PostScript name from the font
	// dict's /BaseFont entry (e.g. "Helvetica", "TimesNewRoman-Bold",
	// "AAAAAA+RobotoMono"). This is the document-stable identity
	// downstream consumers use to dedupe and classify fonts; two
	// TextRuns sharing the same FontName use the same font even if
	// they were selected via different page-resource keys.
	FontName string

	// FontKey is the page-resource name from the content-stream Tf
	// operand (e.g. "F1", "TT2"). It identifies which page-resource
	// /Font subdict entry the run was rendered under. FontKey is
	// PAGE-LOCAL: the same key on different pages may resolve to
	// different fonts. For document-stable identity, use FontName.
	FontKey string

	// Size is the font size selected by the Tf operator, in points.
	Size float64

	// Mono is true if the font is monospaced (every glyph has the same
	// advance). Populated by T3 from the font descriptor.
	Mono bool

	// Bold is true if the font is bold per its descriptor flags.
	Bold bool

	// Italic is true if the font is italic per its descriptor flags.
	Italic bool

	// MCID is the marked-content ID associated with the run, or 0 when
	// the run is outside any marked-content sequence. T2 populates;
	// T6 (structtree) reads when matching content to structure
	// elements.
	MCID int

	// CharBounds carries one axis-aligned user-space rectangle per
	// emitted glyph, parallel to Glyphs (len(CharBounds) == len(Glyphs)
	// when populated). Coordinates are PDF points with +y up. The slice
	// is empty / nil when the emitter chose not to populate per-glyph
	// bounds (e.g. legacy callers, runs constructed by tests). For glyph
	// i, left/bottom = (X+cumulativeAdvance, Y) and right/top =
	// (X+cumulativeAdvance+glyphAdvance, Y+fontSize), where
	// cumulativeAdvance is the sum of prior glyph advances (including
	// char-spacing and word-spacing) and glyphAdvance is the
	// width-resolved advance for this glyph at the current font size.
	// Rotated text passes through the same combined Tm × CTM transform as
	// the run-level (X,Y), so CharBounds remains axis-aligned in
	// user-space via per-corner min/max.
	CharBounds []Rect

	// CharFlags carries one bitmask byte per emitted glyph in Glyphs
	// (len(CharFlags) == len(Glyphs) when populated). Each byte is a
	// bitwise-OR of CharFlag* constants — see CharFlagGenerated /
	// CharFlagBadMap / CharFlagMarkedContent for bit definitions. Zero
	// means "no flags set", the common clean-mapping case. The slice is
	// empty / nil when no emitter has populated it (legacy callers,
	// runs constructed by tests).
	CharFlags []uint8

	// formResources carries the active Form XObject /Resources dict
	// when this run was emitted inside a Form XObject recursion (T4.5);
	// nil for page-level runs. Read by font.Decode through the exported
	// FontResourcesHint accessor: when non-nil, the resolver looks up
	// fontKey via page.ResolvedFontInResources(key, formResources)
	// rather than the page-level page.ResolvedFont(key) — Form
	// XObjects with their own /Resources dict can shadow the page-level
	// font mapping (e.g. a Form's T1_0 referring to a font absent from
	// the page's /Resources/Font subdict). Field is unexported so it
	// does NOT widen the ticket-pinned 13-field public TextRun surface;
	// the type alias for FormResources lives behind the internalpdf
	// package boundary so text/ avoids a direct dependency on the
	// upstream pdfcpu library (confinement rule).
	formResources internalpdf.FormResources
}

// FontResourcesHint returns the active Form XObject /Resources dict
// for the run, or nil when the run was emitted at page level. The
// font.Decode resolver consults this hint before falling back to the
// page-level font lookup; see collector/pdf/font/resolver.go's
// lookupDecoder for the mirror of walker.resolveFont's no-cascade
// semantics. The returned value is the same map the walker holds —
// callers must NOT mutate it.
func (r *TextRun) FontResourcesHint() internalpdf.FormResources {
	if r == nil {
		return nil
	}
	return r.formResources
}
