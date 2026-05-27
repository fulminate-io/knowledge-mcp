package fixturelib

import (
	"bytes"
	"fmt"
)

// T3 fixture builders. Each returns a (PageSpec, FontSpec slice) pair
// or — for the multi-page caching fixture — the (specs, sharedFonts)
// the WriteMultiPagePDF entry point expects. Bodies are small and
// deterministic; tests assert decoded Text against pinned expectations.

// minimalToUnicodeCMap returns a tiny valid /ToUnicode CMap stream
// mapping the supplied (sourceCode, targetUTF16BE) pairs via bfchar.
// The CMap follows the Adobe-Identity-UCS template (PDF 32000-1:2008
// Annex D + Adobe technote 5099). codespaceLow/High give the byte-code
// range; for 1-byte/glyph fonts use 0x00..0xFF, for 2-byte/glyph use
// 0x0000..0xFFFF.
func minimalToUnicodeCMap(twoByteCodes bool, pairs map[string]string) []byte {
	var b bytes.Buffer
	b.WriteString("/CIDInit /ProcSet findresource begin\n")
	b.WriteString("12 dict begin\n")
	b.WriteString("begincmap\n")
	b.WriteString("/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n")
	b.WriteString("/CMapName /Adobe-Identity-UCS def\n")
	b.WriteString("/CMapType 2 def\n")
	b.WriteString("1 begincodespacerange\n")
	if twoByteCodes {
		b.WriteString("<0000> <FFFF>\n")
	} else {
		b.WriteString("<00> <FF>\n")
	}
	b.WriteString("endcodespacerange\n")
	fmt.Fprintf(&b, "%d beginbfchar\n", len(pairs))
	// Sort keys for deterministic output.
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "<%s> <%s>\n", k, pairs[k])
	}
	b.WriteString("endbfchar\n")
	b.WriteString("endcmap\n")
	b.WriteString("CMapName currentdict /CMap defineresource pop\n")
	b.WriteString("end\n")
	b.WriteString("end\n")
	return b.Bytes()
}

// ToUnicodeCleanFont is fixture 1's font: a TrueType subset with a
// minimal ToUnicode CMap mapping codes 0x41 ("A") and 0x42 ("B") to
// their Unicode codepoints. Content stream emits "<4142>".
func ToUnicodeCleanFont() FontSpec {
	return FontSpec{
		Key:      "F1",
		BaseFont: "AAAAAA+SubsetTimes",
		Subtype:  "TrueType",
		ToUnicodeBytes: minimalToUnicodeCMap(false, map[string]string{
			"41": "0041",
			"42": "0042",
		}),
	}
}

// ToUnicodeCleanBody emits a hex-string Tj at (100, 700) decoding to
// "AB" via the ToUnicodeCleanFont CMap.
func ToUnicodeCleanBody() string {
	return "BT /F1 12 Tf 100 700 Td <4142> Tj ET\n"
}

// NoToUnicodeWinAnsiFont is fixture 2: Helvetica with /Encoding
// /WinAnsiEncoding and NO /ToUnicode. Resolver path: encoding-table
// lookup (rung 4 of the buildDecoder ladder).
func NoToUnicodeWinAnsiFont() FontSpec {
	return SimpleFontSpec("F1", "Helvetica") // sets Encoding = WinAnsiEncoding
}

// NoToUnicodeWinAnsiBody emits literal "Hello" at (100, 700).
func NoToUnicodeWinAnsiBody() string {
	return "BT /F1 12 Tf 100 700 Td (Hello) Tj ET\n"
}

// DifferencesOverrideFont is fixture 3: a TrueType font whose
// /Encoding overrides one slot (0x41 → smileyface, a glyph name NOT
// in the AGL). Resolver should fall through to a replacement char or
// surface the unknown-glyph signal.
func DifferencesOverrideFont() FontSpec {
	return FontSpec{
		Key:      "F1",
		BaseFont: "AAAAAA+CustomFont",
		Subtype:  "TrueType",
		Encoding: "WinAnsiEncoding",
		Differences: []FontSpecDiff{
			{Code: 0x41, Names: []string{"smileyface"}},
		},
	}
}

// DifferencesOverrideBody emits one Tj of byte 0x41 → smileyface.
func DifferencesOverrideBody() string {
	return "BT /F1 12 Tf 100 700 Td <41> Tj ET\n"
}

// NoEncodingInfoFont is fixture 4: a TrueType font with NO encoding
// info at all (no /ToUnicode, no /Encoding, no /Differences). Forces
// the resolver to its bottom rung (replacement-char + warning).
func NoEncodingInfoFont() FontSpec {
	return FontSpec{
		Key:      "F1",
		BaseFont: "AAAAAA+UnknownFont",
		Subtype:  "TrueType",
	}
}

// NoEncodingInfoBody emits 3 raw bytes that should resolve to U+FFFD.
func NoEncodingInfoBody() string {
	return "BT /F1 12 Tf 100 700 Td <414243> Tj ET\n"
}

// LigaturesFont is fixture 5: Helvetica with a ToUnicode that maps
// CID 0x01 → "fi" (UTF-16BE 0066 0069) and CID 0x02 → "fl" (0066
// 006C). Tests the ligature decomposition path (multi-rune target
// per bfchar entry).
func LigaturesFont() FontSpec {
	return FontSpec{
		Key:      "F1",
		BaseFont: "Helvetica",
		Subtype:  "Type1",
		ToUnicodeBytes: minimalToUnicodeCMap(false, map[string]string{
			"01": "00660069",
			"02": "0066006C",
		}),
	}
}

// LigaturesBody emits CIDs 1 and 2.
func LigaturesBody() string {
	return "BT /F1 12 Tf 100 700 Td <0102> Tj ET\n"
}

// CIDFontIdentityHFont is fixture 6: a Type 0 font with Identity-H
// encoding, CIDFontType2 descendant, /CIDToGIDMap = /Identity, and a
// minimal ToUnicode mapping CIDs 0x0001 → "A", 0x0002 → "B".
func CIDFontIdentityHFont() FontSpec {
	return FontSpec{
		Key:               "F1",
		BaseFont:          "AAAAAA+TestCIDFont",
		Subtype:           "Type0",
		Encoding:          "Identity-H",
		DescendantSubtype: "CIDFontType2",
		CIDToGIDIdentity:  true,
		ToUnicodeBytes: minimalToUnicodeCMap(true, map[string]string{
			"0001": "0041",
			"0002": "0042",
		}),
	}
}

// CIDFontIdentityHBody emits 2 CIDs (0x0001 0x0002) via a hex string
// of 4 bytes (2 bytes per CID).
func CIDFontIdentityHBody() string {
	return "BT /F1 12 Tf 100 700 Td <00010002> Tj ET\n"
}

// MultiPageOneFontShared returns the FontSpec to register ONCE as a
// shared resource across multiple pages. Used by multipage_one_font.pdf
// to assert the document-scoped resolver caches font decoders by
// content (BaseFont + sha256(ToUnicodeBytes)) rather than by per-page
// indirection. The ToUnicode covers the ASCII bytes that appear in
// "page1"/"page2"/"page3" content streams.
func MultiPageOneFontShared() FontSpec {
	pairs := map[string]string{}
	for _, ch := range "page0123" {
		pairs[fmt.Sprintf("%02X", ch)] = fmt.Sprintf("%04X", ch)
	}
	return FontSpec{
		Key:            "F1",
		BaseFont:       "Helvetica",
		Subtype:        "Type1",
		ToUnicodeBytes: minimalToUnicodeCMap(false, pairs),
	}
}

// MultiPageOneFontBodies returns 3 page bodies, each emitting "page<N>"
// for N ∈ {1,2,3}. Each body shares the F1 resource registered via
// MultiPageOneFontShared.
func MultiPageOneFontBodies() []string {
	return []string{
		"BT /F1 12 Tf 100 700 Td (page1) Tj ET\n",
		"BT /F1 12 Tf 100 700 Td (page2) Tj ET\n",
		"BT /F1 12 Tf 100 700 Td (page3) Tj ET\n",
	}
}
