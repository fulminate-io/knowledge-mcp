package font

import (
	"bufio"
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// standard14_widths.dat is the GENERATED single-source TSV of (font, glyph,
// width) rows produced by collector/pdf/font/testdata/regen_widths.go. The
// regenerator iterates pdfcpu's `font.CharWidth` public API for every
// Standard 14 font × every AGL glyph; output is pinned to pdfcpu@v0.12.0.
// See the .dat header for the regen procedure. Re-run when pdfcpu's
// vendored AFM tables change.
//
//go:embed standard14_widths.dat
var standard14WidthsDat string

var (
	standard14Widths     map[string]map[string]int // font BaseFont → glyph name → width (1/1000 em)
	standard14WidthsErr  error
	standard14WidthsOnce sync.Once
)

// loadStandard14Widths parses the embedded .dat lazily. Returns the cached
// map and any first-encountered parse error. Parse errors are a build-time
// bug (the .dat ships malformed); callers should panic rather than silently
// degrade — see the loud-fail rationale documented in T3-1 and the runtime
// helper `standard14Width` below.
func loadStandard14Widths() (map[string]map[string]int, error) {
	standard14WidthsOnce.Do(func() {
		m := make(map[string]map[string]int, 14)
		sc := bufio.NewScanner(strings.NewReader(standard14WidthsDat))
		// AGL × 14 fonts produces ~60k rows; longer than the default
		// 64KB buffer per line is irrelevant (rows are ~25 bytes), but
		// raise the cap just in case future regens emit comments.
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) != 3 {
				standard14WidthsErr = fmt.Errorf("standard14_widths.dat:%d: expected 3 tab-separated fields, got %d (line: %q)", lineNo, len(parts), line)
				return
			}
			width, err := strconv.Atoi(parts[2])
			if err != nil {
				standard14WidthsErr = fmt.Errorf("standard14_widths.dat:%d: parse width %q: %w", lineNo, parts[2], err)
				return
			}
			if m[parts[0]] == nil {
				m[parts[0]] = make(map[string]int, 4400)
			}
			m[parts[0]][parts[1]] = width
		}
		if err := sc.Err(); err != nil {
			standard14WidthsErr = fmt.Errorf("standard14_widths.dat: scan error: %w", err)
			return
		}
		standard14Widths = m
	})
	return standard14Widths, standard14WidthsErr
}

// standard14Width returns the AFM advance width (units = 1/1000 em) for the
// given Standard 14 BaseFont and glyph name. ok=false when the font isn't
// Standard 14 OR the glyph name isn't in the AFM tables for that font.
//
// Panics when the embedded .dat is malformed. That is a build-time bug
// (the wrong .dat shipped in the binary) and silent degradation would mask
// real transcription failures — see the T3-1 loud-fail rationale.
func standard14Width(baseFont, glyphName string) (int, bool) {
	widths, err := loadStandard14Widths()
	if err != nil {
		panic(fmt.Sprintf("font: standard14_widths.dat is malformed (build-time bug): %v", err))
	}
	fontMap, ok := widths[baseFont]
	if !ok {
		return 0, false
	}
	w, ok := fontMap[glyphName]
	return w, ok
}

// Standard14WidthForCode is the public access path the text package
// uses to plumb /Widths-fallback widths into its content-stream
// walker. Resolves the (baseFont, code) → glyph name → AFM width
// chain in one shot. Returns ok=false when:
//   - baseFont isn't a Standard 14 font name, OR
//   - code is out of range [0, 255], OR
//   - the WinAnsi mapping doesn't have a glyph for code, OR
//   - the AFM doesn't have a width for that glyph.
//
// Width units are 1/1000 em. The text package's glyphAdvance helper
// converts to user-space points via `width / 1000.0 * fontSize`.
//
// IMPORTANT: this lookup uses WinAnsiEncoding to map code → glyph
// name, which matches the Standard 14 default that pdfcpu's writer
// emits and most real-corpus PDFs rely on. Symbol/ZapfDingbats use
// their own encodings and aren't covered here — those fonts ship with
// /Widths in their dicts (rung 1 of the ladder), so the fallback
// rung 3 doesn't fire for them.
func Standard14WidthForCode(baseFont string, code uint32) (int, bool) {
	if !isStandard14(baseFont) || code > 0xFF {
		return 0, false
	}
	if baseFont == "Symbol" || baseFont == "ZapfDingbats" {
		// Same caveat as fixturelib: these fonts use Symbol-specific
		// encodings rather than WinAnsi. Return miss so the text
		// package's ladder falls through to rung 4 (half-em) when
		// /Widths is also absent — extremely rare in practice.
		return 0, false
	}
	name := winAnsiNameForCode(int(code))
	if name == "" {
		return 0, false
	}
	return standard14Width(baseFont, name)
}

// winAnsiNameForCode is the same code→glyph-name table fixturelib's
// widths.go uses internally — duplicated here to avoid creating an
// import edge from font → testdata/fixturelib (which would violate
// the layering audit). The two tables agree by construction (same
// PDF Annex D source); the spot-check tests in encoding_test.go
// catch any drift.
func winAnsiNameForCode(code int) string {
	if code < 0 || code > 255 {
		return ""
	}
	g := WinAnsiEncoding[code]
	if g == notdef {
		return ""
	}
	return g
}

// isStandard14 reports whether the given /BaseFont name is one of the 14
// PostScript names the PDF spec gives a synthetic AFM for (PDF 32000-1:2008
// §9.6.2.2). Used by the resolver's buildDecoder ladder to default an
// otherwise-unspecified /Encoding to StandardEncoding.
func isStandard14(baseFont string) bool {
	switch baseFont {
	case "Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique",
		"Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic",
		"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique",
		"Symbol", "ZapfDingbats":
		return true
	}
	return false
}
