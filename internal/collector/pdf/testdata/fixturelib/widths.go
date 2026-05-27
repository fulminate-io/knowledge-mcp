package fixturelib

import (
	"bufio"
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// standard14_widths.dat is sourced from collector/pdf/font/ — fixturelib
// embeds the SAME byte sequence the runtime path under collector/pdf/font/
// embeds (Option A layering, recorded in Phase 1 think for plan d76acb28).
// Both packages parse the .dat independently; neither imports the other,
// proving single-sourcing via the .dat byte sequence rather than a shared
// Go-level helper. Layering audit:
//
//	go list -deps github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/testdata/fixturelib
//	# MUST NOT show github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/font
//
//go:embed widths_data/standard14_widths.dat
var standard14WidthsDat string

var (
	standard14Widths     map[string]map[string]int // BaseFont → glyph name → width
	standard14WidthsErr  error
	standard14WidthsOnce sync.Once
)

// LoadStandard14Widths parses the embedded .dat (lazily, once). Returns the
// cached map and any first-encountered parse error. Exported for fixturelib
// callers (gen.go fixture builders that need widths to construct accurate
// /Widths arrays).
func LoadStandard14Widths() (map[string]map[string]int, error) {
	standard14WidthsOnce.Do(func() {
		m := make(map[string]map[string]int, 14)
		sc := bufio.NewScanner(strings.NewReader(standard14WidthsDat))
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

// Standard14Width returns the AFM advance width (1/1000 em) for the given
// Standard 14 BaseFont and glyph name. ok=false when the font isn't
// Standard 14 OR the glyph name isn't in the AFM tables for that font.
//
// Same semantics as font.standard14Width but in the fixturelib namespace.
// Used by fixturelib's FontSpec builders to generate accurate /Widths
// arrays for synthesized fonts.
func Standard14Width(baseFont, glyphName string) (int, bool) {
	widths, err := LoadStandard14Widths()
	if err != nil {
		// fixturelib is test-only; loud panic over silent skew.
		panic(fmt.Sprintf("fixturelib: standard14_widths.dat is malformed (build-time bug): %v", err))
	}
	fontMap, ok := widths[baseFont]
	if !ok {
		return 0, false
	}
	w, ok := fontMap[glyphName]
	return w, ok
}

// standard14WidthsFor returns the (FirstChar, Widths) pair to embed on a
// Standard 14 font dict. firstChar=0x20, widths covers codes 0x20..0xFF
// (the WinAnsi-mapped printable range), each entry the AFM width of the
// glyph at that code under WinAnsiEncoding. Returns ok=false for fonts
// not in the Standard 14 list.
//
// The encoding used to map code → glyph name is the WinAnsi mini-table
// declared inline below (rather than importing the runtime font/
// package, which would violate the layering audit). Symbol/ZapfDingbats
// have their own encodings and are skipped here — fixtures that need
// Symbol can supply explicit widths via FontSpec.Widths.
func standard14WidthsFor(baseFont string) (firstChar int, widths []int, ok bool) {
	switch baseFont {
	case "Symbol", "ZapfDingbats":
		// Symbol-encoded fonts; codes don't map through WinAnsi, so
		// the auto-population is skipped. Caller can set Widths
		// explicitly when needed.
		return 0, nil, false
	}
	w, err := LoadStandard14Widths()
	if err != nil {
		panic(fmt.Sprintf("fixturelib: standard14_widths.dat is malformed (build-time bug): %v", err))
	}
	fontMap, fontOK := w[baseFont]
	if !fontOK {
		return 0, nil, false
	}
	firstChar = 0x20
	const lastChar = 0xFF
	widths = make([]int, lastChar-firstChar+1)
	for code := firstChar; code <= lastChar; code++ {
		name := winAnsiCodeToGlyphName(code)
		if name == "" || name == ".notdef" {
			widths[code-firstChar] = 0
			continue
		}
		widths[code-firstChar] = fontMap[name] // 0 default when glyph absent from font
	}
	return firstChar, widths, true
}

// winAnsiCodeToGlyphName is fixturelib's local mini WinAnsiEncoding
// table, derived from PDF 32000-1:2008 Annex D Table D.2. Kept here
// (and not imported from font/) because fixturelib must remain
// independent of the runtime font/ package per the Option A layering
// audit (Phase 1 think note for plan d76acb28). Only the codes used
// by SimpleFontSpec auto-population matter; missing slots return ""
// and the corresponding width is set to 0.
func winAnsiCodeToGlyphName(code int) string {
	if code < 0 || code > 255 {
		return ""
	}
	if name, ok := winAnsiTable[code]; ok {
		return name
	}
	return ""
}

// winAnsiTable is the sparse code → glyph-name lookup. Only printable
// codes have entries; .notdef slots are absent. Entries cover the
// WinAnsi-mapped subset of Annex D Table D.2 — sufficient to populate
// /Widths arrays for the Standard 14 fonts under WinAnsiEncoding,
// which is what pdfcpu emits for non-Symbol core fonts.
var winAnsiTable = map[int]string{
	0x20: "space", 0x21: "exclam", 0x22: "quotedbl", 0x23: "numbersign",
	0x24: "dollar", 0x25: "percent", 0x26: "ampersand", 0x27: "quotesingle",
	0x28: "parenleft", 0x29: "parenright", 0x2A: "asterisk", 0x2B: "plus",
	0x2C: "comma", 0x2D: "hyphen", 0x2E: "period", 0x2F: "slash",
	0x30: "zero", 0x31: "one", 0x32: "two", 0x33: "three", 0x34: "four",
	0x35: "five", 0x36: "six", 0x37: "seven", 0x38: "eight", 0x39: "nine",
	0x3A: "colon", 0x3B: "semicolon", 0x3C: "less", 0x3D: "equal",
	0x3E: "greater", 0x3F: "question", 0x40: "at",
	0x41: "A", 0x42: "B", 0x43: "C", 0x44: "D", 0x45: "E", 0x46: "F",
	0x47: "G", 0x48: "H", 0x49: "I", 0x4A: "J", 0x4B: "K", 0x4C: "L",
	0x4D: "M", 0x4E: "N", 0x4F: "O", 0x50: "P", 0x51: "Q", 0x52: "R",
	0x53: "S", 0x54: "T", 0x55: "U", 0x56: "V", 0x57: "W", 0x58: "X",
	0x59: "Y", 0x5A: "Z",
	0x5B: "bracketleft", 0x5C: "backslash", 0x5D: "bracketright",
	0x5E: "asciicircum", 0x5F: "underscore", 0x60: "grave",
	0x61: "a", 0x62: "b", 0x63: "c", 0x64: "d", 0x65: "e", 0x66: "f",
	0x67: "g", 0x68: "h", 0x69: "i", 0x6A: "j", 0x6B: "k", 0x6C: "l",
	0x6D: "m", 0x6E: "n", 0x6F: "o", 0x70: "p", 0x71: "q", 0x72: "r",
	0x73: "s", 0x74: "t", 0x75: "u", 0x76: "v", 0x77: "w", 0x78: "x",
	0x79: "y", 0x7A: "z",
	0x7B: "braceleft", 0x7C: "bar", 0x7D: "braceright", 0x7E: "asciitilde",
	0x7F: "bullet", 0x80: "Euro", 0x82: "quotesinglbase", 0x83: "florin",
	0x84: "quotedblbase", 0x85: "ellipsis", 0x86: "dagger", 0x87: "daggerdbl",
	0x88: "circumflex", 0x89: "perthousand", 0x8A: "Scaron",
	0x8B: "guilsinglleft", 0x8C: "OE", 0x8E: "Zcaron",
	0x91: "quoteleft", 0x92: "quoteright", 0x93: "quotedblleft",
	0x94: "quotedblright", 0x95: "bullet", 0x96: "endash", 0x97: "emdash",
	0x98: "tilde", 0x99: "trademark", 0x9A: "scaron",
	0x9B: "guilsinglright", 0x9C: "oe", 0x9E: "zcaron", 0x9F: "Ydieresis",
	0xA0: "space", 0xA1: "exclamdown", 0xA2: "cent", 0xA3: "sterling",
	0xA4: "currency", 0xA5: "yen", 0xA6: "brokenbar", 0xA7: "section",
	0xA8: "dieresis", 0xA9: "copyright", 0xAA: "ordfeminine",
	0xAB: "guillemotleft", 0xAC: "logicalnot", 0xAD: "hyphen",
	0xAE: "registered", 0xAF: "macron", 0xB0: "degree", 0xB1: "plusminus",
	0xB2: "twosuperior", 0xB3: "threesuperior", 0xB4: "acute", 0xB5: "mu",
	0xB6: "paragraph", 0xB7: "periodcentered", 0xB8: "cedilla",
	0xB9: "onesuperior", 0xBA: "ordmasculine", 0xBB: "guillemotright",
	0xBC: "onequarter", 0xBD: "onehalf", 0xBE: "threequarters",
	0xBF: "questiondown",
	0xC0: "Agrave", 0xC1: "Aacute", 0xC2: "Acircumflex", 0xC3: "Atilde",
	0xC4: "Adieresis", 0xC5: "Aring", 0xC6: "AE", 0xC7: "Ccedilla",
	0xC8: "Egrave", 0xC9: "Eacute", 0xCA: "Ecircumflex", 0xCB: "Edieresis",
	0xCC: "Igrave", 0xCD: "Iacute", 0xCE: "Icircumflex", 0xCF: "Idieresis",
	0xD0: "Eth", 0xD1: "Ntilde", 0xD2: "Ograve", 0xD3: "Oacute",
	0xD4: "Ocircumflex", 0xD5: "Otilde", 0xD6: "Odieresis", 0xD7: "multiply",
	0xD8: "Oslash", 0xD9: "Ugrave", 0xDA: "Uacute", 0xDB: "Ucircumflex",
	0xDC: "Udieresis", 0xDD: "Yacute", 0xDE: "Thorn", 0xDF: "germandbls",
	0xE0: "agrave", 0xE1: "aacute", 0xE2: "acircumflex", 0xE3: "atilde",
	0xE4: "adieresis", 0xE5: "aring", 0xE6: "ae", 0xE7: "ccedilla",
	0xE8: "egrave", 0xE9: "eacute", 0xEA: "ecircumflex", 0xEB: "edieresis",
	0xEC: "igrave", 0xED: "iacute", 0xEE: "icircumflex", 0xEF: "idieresis",
	0xF0: "eth", 0xF1: "ntilde", 0xF2: "ograve", 0xF3: "oacute",
	0xF4: "ocircumflex", 0xF5: "otilde", 0xF6: "odieresis", 0xF7: "divide",
	0xF8: "oslash", 0xF9: "ugrave", 0xFA: "uacute", 0xFB: "ucircumflex",
	0xFC: "udieresis", 0xFD: "yacute", 0xFE: "thorn", 0xFF: "ydieresis",
}
