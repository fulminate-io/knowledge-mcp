package font

import "testing"

// TestLookupGlyph_SpotCheck verifies a representative slice of the
// Adobe Glyph List embedded in glyphlist.txt. Includes single-rune,
// ligature, and unknown-name cases.
func TestLookupGlyph_SpotCheck(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want []rune
		ok   bool
	}{
		// ASCII letters
		{"A", []rune{0x0041}, true},
		{"a", []rune{0x0061}, true},
		{"zero", []rune{0x0030}, true},
		{"space", []rune{0x0020}, true},
		// Punctuation
		{"period", []rune{0x002E}, true},
		{"comma", []rune{0x002C}, true},
		{"hyphen", []rune{0x002D}, true},
		{"emdash", []rune{0x2014}, true},
		{"endash", []rune{0x2013}, true},
		{"bullet", []rune{0x2022}, true},
		// Diacritics / accented
		{"Adieresis", []rune{0x00C4}, true},
		{"eacute", []rune{0x00E9}, true},
		{"ntilde", []rune{0x00F1}, true},
		// Ligatures: AGL maps these to single Unicode ligature codepoints
		// (U+FB01..FB03), not the decomposed pairs — the resolver emits
		// the ligature codepoint and downstream consumers can NFKC-fold
		// if they need the decomposed form.
		{"fi", []rune{0xFB01}, true},
		{"fl", []rune{0xFB02}, true},
		{"ffi", []rune{0xFB03}, true},
		// Multi-rune AGL entries: Hebrew dalet+hatafpatah → 05D3 05B2.
		{"dalethatafpatah", []rune{0x05D3, 0x05B2}, true},
		// Greek
		{"alpha", []rune{0x03B1}, true},
		// Omega: AGL maps to U+2126 (Ohm sign) for legacy Type 1 fonts;
		// Greek capital Omega proper is "Omega" → 2126 per AGL v2.0.
		{"Omega", []rune{0x2126}, true},
		// Currency
		{"Euro", []rune{0x20AC}, true},
		// Unknown name
		{"NotARealGlyph", nil, false},
		{".notdef", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, ok := lookupGlyph(c.name)
			if ok != c.ok {
				t.Fatalf("lookupGlyph(%q): ok=%v, want %v", c.name, ok, c.ok)
			}
			if !c.ok {
				return
			}
			if len(got) != len(c.want) {
				t.Fatalf("lookupGlyph(%q): got %d runes %v, want %d runes %v", c.name, len(got), got, len(c.want), c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("lookupGlyph(%q)[%d]: got %#x, want %#x", c.name, i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestLookupGlyph_AGLSize is a smoke check on the embedded data: the
// upstream AGL has ~4280 entries; we expect at least 4000 (catches a
// regression where the embed got truncated).
func TestLookupGlyph_AGLSize(t *testing.T) {
	t.Parallel()
	// Force lazy init by performing one lookup.
	_, _ = lookupGlyph("A")
	if got := len(aglMap); got < 4000 {
		t.Errorf("aglMap entries: got %d, want at least 4000 (full AGL parse)", got)
	}
}
