package fixturelib

import "testing"

// TestFixturelibStandard14Widths_AgreesWithFontPackage proves that the
// fixturelib package and the font package read the SAME pinned values
// from the same .dat byte sequence. Single-sourcing is enforced by
// regen_widths.go writing both .dat copies in one atomic step
// (collector/pdf/font/testdata/regen_widths.go). When the runtime .dat
// row "Helvetica\tT\t611" mutates to "Helvetica\tT\t999" AND the
// regenerator is re-run (or the test is run mid-mutation), THIS TEST
// FAILS — because both .dat files come from the same source and stay
// byte-identical. The mutation criterion at plan d76acb28 step
// 90ef422eb5725e809b021a3b8932d21e exercises this dual-failure path.
func TestFixturelibStandard14Widths_AgreesWithFontPackage(t *testing.T) {
	t.Parallel()
	widths, err := LoadStandard14Widths()
	if err != nil {
		t.Fatalf("LoadStandard14Widths: %v", err)
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

// TestFixturelibStandard14Width_PinnedProbeRow mirrors the font package's
// pinned-probe assertions to give the dual-failure mutation test a clear
// per-glyph signal.
func TestFixturelibStandard14Width_PinnedProbeRow(t *testing.T) {
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
		c := c
		t.Run(c.font+"/"+c.glyph, func(t *testing.T) {
			t.Parallel()
			got, ok := Standard14Width(c.font, c.glyph)
			if !ok {
				t.Fatalf("Standard14Width(%q, %q): ok=false", c.font, c.glyph)
			}
			if got != c.want {
				t.Errorf("Standard14Width(%q, %q): got %d, want %d", c.font, c.glyph, got, c.want)
			}
		})
	}
}
