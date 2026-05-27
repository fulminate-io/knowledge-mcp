package fixturelib

import "strings"

// FontSpec is the rich font-dict description fixturelib emits into a
// page's /Resources/Font subdict. It supersedes the old
// `map[string]string` shape used by T2 fixtures so T3 fixtures can
// exercise /ToUnicode, /Encoding /Differences, /CIDToGIDMap, /FirstChar,
// /Widths, and FontDescriptor /MissingWidth.
//
// Field semantics (PDF 32000-1:2008 references):
//   - §9.6.2  /Type, /Subtype, /BaseFont, /Encoding (Name)
//   - §9.6.6  /Encoding (dict with /BaseEncoding + /Differences)
//   - §9.10.2 /ToUnicode stream
//   - §9.7.3  /DescendantFonts (Type 0 only)
//   - §9.7.4  /CIDToGIDMap (Type 2 CIDFonts)
//   - §9.6.2.1 /FirstChar + /LastChar + /Widths
//   - §9.8.2  FontDescriptor /MissingWidth
type FontSpec struct {
	Key      string // resource key, e.g. "F1"
	BaseFont string // PostScript name, e.g. "Helvetica"
	Subtype  string // "Type1" / "TrueType" / "Type0" / "Type3" / "MMType1"

	// T3 deep fields. Empty values omit the corresponding entry from
	// the emitted font dict.
	Encoding          string         // predefined encoding name OR "" (when Differences-based)
	Differences       []FontSpecDiff // per-code overrides
	ToUnicodeBytes    []byte         // raw CMap source for the /ToUnicode stream
	DescendantSubtype string         // for Type0: "CIDFontType0" or "CIDFontType2"
	CIDToGIDIdentity  bool           // for Type0
	CIDToGIDMap       []uint16       // for Type0 with stream CIDToGIDMap

	// Width plumbing (per T2-3):
	FirstChar    int   // index of Widths[0] in the byte-code space
	Widths       []int // per-glyph advance widths in 1/1000 em
	MissingWidth int   // FontDescriptor /MissingWidth (per T2-2); 0 when absent
}

// FontSpecDiff is one (starting code, names...) run inside an
// /Encoding /Differences array. Same shape as
// internal/pdfcpu.DifferenceEntry, kept independent here so fixturelib
// stays import-free of the wrapper package.
type FontSpecDiff struct {
	Code  int
	Names []string
}

// SimpleFontSpec returns a FontSpec for a Standard 14 font with
// /FirstChar + /Widths populated from the single-source
// standard14_widths.dat (per T3-2). Non-Standard-14 fonts get a
// FontSpec with empty Widths; the resolver falls back via the
// 4-rung width-resolution ladder.
//
// Use SimpleFontSpec from gen.go fixture builders; the conversion
// from old-style `map[string]string` to `[]FontSpec` is one liner via
// SimpleFontSpecMap.
func SimpleFontSpec(key, baseFont string) FontSpec {
	spec := FontSpec{Key: key, BaseFont: baseFont, Subtype: inferSubtype(baseFont)}
	// Match pdfcpu's CoreFontDict default: non-Symbol Standard 14
	// fonts emit /Encoding /WinAnsiEncoding so the runtime resolver
	// can map content-stream byte codes to glyph names without a
	// /Differences override.
	if spec.Subtype == "Type1" && baseFont != "Symbol" && baseFont != "ZapfDingbats" {
		spec.Encoding = "WinAnsiEncoding"
	}
	if firstChar, widths, ok := standard14WidthsFor(baseFont); ok {
		spec.FirstChar = firstChar
		spec.Widths = widths
	}
	return spec
}

// SimpleFontSpecMap converts the legacy `map[string]string`
// (key→BaseFont) shape used by T1/T2 fixtures into a `[]FontSpec`
// slice. Each FontSpec auto-populates /FirstChar + /Widths via
// SimpleFontSpec.
//
// Map iteration order in Go is randomized; we sort keys so the
// generated PDF byte sequence is deterministic across regen runs.
// Tests don't rely on byte equality, but a deterministic PDF makes
// `git diff` of regenerated fixtures readable.
func SimpleFontSpecMap(m map[string]string) []FontSpec {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// stable sort: simple selection so we don't pull in sort package
	// for one helper. fixturelib stays small.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	out := make([]FontSpec, 0, len(keys))
	for _, k := range keys {
		out = append(out, SimpleFontSpec(k, m[k]))
	}
	return out
}

// inferSubtype maps a BaseFont name to the most likely PDF /Subtype.
// Used by SimpleFontSpec when the caller doesn't supply one.
//
// Standard 14 fonts are Type1; everything else defaults to TrueType
// (the most common subset for embedded fonts). Type0 / CIDFontType*
// require explicit Subtype on the FontSpec.
func inferSubtype(baseFont string) string {
	switch baseFont {
	case "Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique",
		"Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic",
		"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique",
		"Symbol", "ZapfDingbats":
		return "Type1"
	}
	if strings.Contains(baseFont, "+") {
		return "TrueType"
	}
	return "TrueType"
}
