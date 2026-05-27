package font

import "testing"

// TestStandardEncoding_KnownEntries spot-checks Annex D Table D.2.
func TestStandardEncoding_KnownEntries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code  int
		glyph string
	}{
		{0x20, "space"},
		{0x41, "A"},
		{0x61, "a"},
		{0x7B, "braceleft"},
		{0x7E, "asciitilde"},
		{0xA1, "exclamdown"},
		{0xE1, "AE"},
		{0xF1, "ae"},
		// .notdef defaults
		{0x00, ".notdef"},
		{0x9F, ".notdef"},
		{0xFE, ".notdef"},
	}
	for _, c := range cases {
		if got := StandardEncoding[c.code]; got != c.glyph {
			t.Errorf("StandardEncoding[%#x]: got %q, want %q", c.code, got, c.glyph)
		}
	}
}

// TestWinAnsiEncoding_KnownEntries spot-checks the CP1252-mapped table.
// Includes the WinAnsi-specific quirks at 0x80-0x9F (Euro, OE, etc.).
func TestWinAnsiEncoding_KnownEntries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code  int
		glyph string
	}{
		{0x20, "space"},
		{0x41, "A"},
		{0x80, "Euro"},
		{0x91, "quoteleft"},
		{0x92, "quoteright"},
		{0x93, "quotedblleft"},
		{0x94, "quotedblright"},
		{0xA0, "space"},
		{0xC4, "Adieresis"},
		{0xE9, "eacute"},
		{0xFF, "ydieresis"},
		// .notdef slots in WinAnsi: 0x81, 0x8D, 0x8F, etc.
		{0x81, ".notdef"},
		{0x8D, ".notdef"},
	}
	for _, c := range cases {
		if got := WinAnsiEncoding[c.code]; got != c.glyph {
			t.Errorf("WinAnsiEncoding[%#x]: got %q, want %q", c.code, got, c.glyph)
		}
	}
}

// TestMacRomanEncoding_KnownEntries spot-checks Annex D Table D.2 for
// the Mac-specific high-half mappings.
func TestMacRomanEncoding_KnownEntries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code  int
		glyph string
	}{
		{0x20, "space"},
		{0x41, "A"},
		{0x80, "Adieresis"},
		{0xCA, "space"},
		{0xD1, "emdash"},
		{0xDB, "currency"},
		{0xDF, "fl"},
		{0xFE, "ogonek"},
		// .notdef defaults
		{0x00, ".notdef"},
		{0x9F, "udieresis"}, // verify NOT .notdef in MacRoman (it IS mapped)
	}
	for _, c := range cases {
		if got := MacRomanEncoding[c.code]; got != c.glyph {
			t.Errorf("MacRomanEncoding[%#x]: got %q, want %q", c.code, got, c.glyph)
		}
	}
}

// TestMacExpertEncoding_KnownEntries spot-checks the small-caps /
// expert-glyph table.
func TestMacExpertEncoding_KnownEntries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code  int
		glyph string
	}{
		{0x20, "space"},
		{0x21, "exclamsmall"},
		{0x46, ".notdef"}, // 0x46 is .notdef in MacExpert (NOT 'F')
		{0x47, "onequarter"},
		{0x48, "onehalf"},
		{0x57, "fi"},
		{0x58, "fl"},
		{0x61, "Asmall"},
		{0xC2, "commainferior"},
		{0xFB, "lsuperior"},
		{0xFF, "bsuperior"},
	}
	for _, c := range cases {
		if got := MacExpertEncoding[c.code]; got != c.glyph {
			t.Errorf("MacExpertEncoding[%#x]: got %q, want %q", c.code, got, c.glyph)
		}
	}
}

// TestEncodingByName_Lookup covers the lookup helper used by the
// resolver's buildDecoder ladder.
func TestEncodingByName_Lookup(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"WinAnsiEncoding", "MacRomanEncoding", "MacExpertEncoding", "StandardEncoding"} {
		enc, ok := encodingByName(name)
		if !ok {
			t.Errorf("encodingByName(%q): ok=false, want true", name)
			continue
		}
		if enc == nil {
			t.Errorf("encodingByName(%q): table=nil", name)
			continue
		}
		// Every encoding has space at 0x20.
		if got := enc[0x20]; got != "space" {
			t.Errorf("encodingByName(%q)[0x20]: got %q, want space", name, got)
		}
	}
	if _, ok := encodingByName("NotARealEncoding"); ok {
		t.Errorf("encodingByName(NotARealEncoding): ok=true, want false")
	}
}

// TestAllEncodings_NoEmptySlots verifies every slot in every encoding
// is non-empty. Catches transcription bugs that left a slot at the
// zero-value "" (which would compare unequal to ".notdef" downstream).
func TestAllEncodings_NoEmptySlots(t *testing.T) {
	t.Parallel()
	tables := map[string]*[256]string{
		"StandardEncoding":  &StandardEncoding,
		"WinAnsiEncoding":   &WinAnsiEncoding,
		"MacRomanEncoding":  &MacRomanEncoding,
		"MacExpertEncoding": &MacExpertEncoding,
	}
	for name, tab := range tables {
		for i, g := range tab {
			if g == "" {
				t.Errorf("%s[%#x] empty (must default to .notdef)", name, i)
			}
		}
	}
}
