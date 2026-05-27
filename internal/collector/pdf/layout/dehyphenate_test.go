package layout

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// makeLine builds a Line from a single Text string; used by
// dehyphenate tests to stay readable. BBox / WasDehyphenated default
// to zero; tests that need them override directly.
func makeLine(text_ ...string) Line {
	runs := make([]text.TextRun, 0, len(text_))
	for _, s := range text_ {
		runs = append(runs, text.TextRun{Text: s})
	}
	return Line{Runs: runs}
}

func TestDehyphenate_HyphenLowercase(t *testing.T) {
	t.Parallel()
	in := []Line{makeLine("inter-"), makeLine("national")}
	out := dehyphenateLines(in)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if got := out[0].Runs[0].Text; got != "inter" {
		t.Errorf("line0.run0 text = %q, want %q", got, "inter")
	}
	if !out[0].WasDehyphenated {
		t.Errorf("line0.WasDehyphenated = false, want true")
	}
	if got := out[1].Runs[0].Text; got != "national" {
		t.Errorf("line1.run0 text = %q, want %q (unchanged)", got, "national")
	}
	if out[1].WasDehyphenated {
		t.Errorf("line1.WasDehyphenated = true, want false")
	}
	// Input must not be mutated.
	if in[0].Runs[0].Text != "inter-" {
		t.Errorf("input mutated: %q", in[0].Runs[0].Text)
	}
}

func TestDehyphenate_HyphenUppercase(t *testing.T) {
	t.Parallel()
	in := []Line{makeLine("co-"), makeLine("Operative")}
	out := dehyphenateLines(in)
	if got := out[0].Runs[0].Text; got != "co-" {
		t.Errorf("uppercase next: line0 text = %q, want unchanged %q", got, "co-")
	}
	if out[0].WasDehyphenated {
		t.Errorf("uppercase next: WasDehyphenated should stay false")
	}
}

func TestDehyphenate_HyphenDigit(t *testing.T) {
	t.Parallel()
	in := []Line{makeLine("page-"), makeLine("12")}
	out := dehyphenateLines(in)
	if got := out[0].Runs[0].Text; got != "page-" {
		t.Errorf("digit next: line0 text = %q, want unchanged", got)
	}
	if out[0].WasDehyphenated {
		t.Errorf("digit next: WasDehyphenated should stay false")
	}
}

func TestDehyphenate_HyphenGreekLowercase(t *testing.T) {
	t.Parallel()
	// Q3 lock: Latin-only v1. Greek γ (U+03B3) is a lowercase letter
	// but outside the 0x024F bound; dehyphenation should NOT apply.
	in := []Line{makeLine("alpha-"), makeLine("γ-test")}
	out := dehyphenateLines(in)
	if got := out[0].Runs[0].Text; got != "alpha-" {
		t.Errorf("Greek next (out of Latin scope): line0 text = %q, want unchanged", got)
	}
	if out[0].WasDehyphenated {
		t.Errorf("Greek next: WasDehyphenated should stay false (Latin-only v1, Q3 lock)")
	}
}

func TestDehyphenate_HyphenLatinExtendedA(t *testing.T) {
	t.Parallel()
	// š (U+0161, "LATIN SMALL LETTER S WITH CARON") is in Latin
	// Extended-A (0x0100-0x017F), within the 0x024F bound. MUST
	// trigger dehyphenation.
	in := []Line{makeLine("inter-"), makeLine("škola")}
	out := dehyphenateLines(in)
	if got := out[0].Runs[0].Text; got != "inter" {
		t.Errorf("Latin Extended-A next: line0 text = %q, want %q", got, "inter")
	}
	if !out[0].WasDehyphenated {
		t.Errorf("Latin Extended-A next: WasDehyphenated = false, want true")
	}
}

func TestDehyphenate_NoHyphen(t *testing.T) {
	t.Parallel()
	in := []Line{makeLine("sentence"), makeLine("continues")}
	out := dehyphenateLines(in)
	if got := out[0].Runs[0].Text; got != "sentence" {
		t.Errorf("no-hyphen: line0 text = %q, want unchanged", got)
	}
	if out[0].WasDehyphenated {
		t.Errorf("no-hyphen: WasDehyphenated should stay false")
	}
}

func TestDehyphenate_TrailingWhitespaceBeforeHyphen(t *testing.T) {
	t.Parallel()
	// "inter- " (trailing space) / "national" → still dehyphenates.
	in := []Line{makeLine("inter- "), makeLine("national")}
	out := dehyphenateLines(in)
	if got := out[0].Runs[0].Text; got != "inter" {
		t.Errorf("trailing ws: line0 text = %q, want %q", got, "inter")
	}
	if !out[0].WasDehyphenated {
		t.Errorf("trailing ws: WasDehyphenated = false, want true")
	}
}

func TestDehyphenate_Empty(t *testing.T) {
	t.Parallel()
	out := dehyphenateLines(nil)
	if out != nil {
		t.Errorf("nil input: got %v, want nil", out)
	}
	out = dehyphenateLines([]Line{})
	if len(out) != 0 {
		t.Errorf("empty input: got %v, want len-0", out)
	}
}

func TestDehyphenate_SingleLine(t *testing.T) {
	t.Parallel()
	in := []Line{makeLine("solo-")}
	out := dehyphenateLines(in)
	if len(out) != 1 || out[0].Runs[0].Text != "solo-" {
		t.Errorf("single-line: got %v, want passthrough", out)
	}
	if out[0].WasDehyphenated {
		t.Errorf("single-line: WasDehyphenated should stay false (no next line)")
	}
}
