package font

import "testing"

// TestStandard14Widths_SingleSourcing asserts the runtime path reads the
// canonical pinned values from the embedded .dat. T=611 and o=556 are
// pinned per Requirement C′ (revision 4) and verified against pdfcpu's
// public `font.CharWidth` API (probe values matched orchestrator-supplied
// expectations during Phase 1 verification — see plan d76acb28).
func TestStandard14Widths_SingleSourcing(t *testing.T) {
	t.Parallel()
	widths, err := loadStandard14Widths()
	if err != nil {
		t.Fatalf("loadStandard14Widths: %v", err)
	}
	helv, ok := widths["Helvetica"]
	if !ok {
		t.Fatalf("standard14Widths missing Helvetica row")
	}
	if got := helv["T"]; got != 611 {
		t.Errorf("Helvetica T: got %d, want 611", got)
	}
	if got := helv["o"]; got != 556 {
		t.Errorf("Helvetica o: got %d, want 556", got)
	}
}

// TestStandard14Width_HelveticaCapitalT is the named test referenced by
// the manual mutation criterion (a8b57517048b8006cff613f72b169f52). When
// the .dat row "Helvetica\tT\t611" is mutated to "Helvetica\tT\t999",
// THIS TEST MUST FAIL — proving the runtime path actually reads the
// embedded .dat (and not some stale transcribed copy). Mutation+revert
// is performed once during Phase 1 implementation per the criterion.
func TestStandard14Width_HelveticaCapitalT(t *testing.T) {
	t.Parallel()
	w, ok := standard14Width("Helvetica", "T")
	if !ok {
		t.Fatalf("standard14Width(Helvetica, T): ok=false, want a width")
	}
	if w != 611 {
		t.Errorf("standard14Width(Helvetica, T): got %d, want 611", w)
	}
}

// TestStandard14Width_PinnedProbeRow covers the remaining pinned values
// from the Phase 1 probe so a regression on any one of them surfaces.
func TestStandard14Width_PinnedProbeRow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		font, glyph string
		want        int
	}{
		{"Helvetica", "T", 611},
		{"Helvetica", "o", 556},
		{"Helvetica", "F", 611},
		{"Helvetica", "space", 278},
		{"Times-Roman", "T", 611},
		{"Courier", "T", 600},
	}
	for _, c := range cases {
		t.Run(c.font+"/"+c.glyph, func(t *testing.T) {
			t.Parallel()
			got, ok := standard14Width(c.font, c.glyph)
			if !ok {
				t.Fatalf("standard14Width(%q, %q): ok=false", c.font, c.glyph)
			}
			if got != c.want {
				t.Errorf("standard14Width(%q, %q): got %d, want %d", c.font, c.glyph, got, c.want)
			}
		})
	}
}

// TestStandard14Width_UnknownFont verifies the (0, false) path for fonts
// not in the Standard 14 list — keeps the resolver ladder honest when
// the BaseFont is, e.g., a TrueType custom font.
func TestStandard14Width_UnknownFont(t *testing.T) {
	t.Parallel()
	if w, ok := standard14Width("CustomFont", "T"); ok {
		t.Errorf("standard14Width(CustomFont, T): ok=true want false (got width=%d)", w)
	}
}

// TestIsStandard14 spot-checks the helper used by the resolver's
// buildDecoder ladder for the implicit-StandardEncoding fallback.
func TestIsStandard14(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want bool
	}{
		{"Helvetica", true},
		{"Helvetica-Bold", true},
		{"Times-Roman", true},
		{"Courier-BoldOblique", true},
		{"Symbol", true},
		{"ZapfDingbats", true},
		{"AAAAAA+CustomFont", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := isStandard14(c.name); got != c.want {
				t.Errorf("isStandard14(%q): got %v, want %v", c.name, got, c.want)
			}
		})
	}
}
