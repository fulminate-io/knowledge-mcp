package font

import (
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// TestApplyDifferences_CursorAdvance covers the basic case from PDF
// 32000-1:2008 §9.6.6.1: `[ 32 /space /exclam ]` writes "space" at
// code 32 and "exclam" at code 33.
func TestApplyDifferences_CursorAdvance(t *testing.T) {
	t.Parallel()
	base := fillNotdef()
	diffs := []internalpdf.DifferenceEntry{
		{Code: 32, Names: []string{"space", "exclam"}},
	}
	out := applyDifferences(base, diffs)
	if out[32] != "space" {
		t.Errorf("out[32]: got %q, want space", out[32])
	}
	if out[33] != "exclam" {
		t.Errorf("out[33]: got %q, want exclam", out[33])
	}
	if out[34] != ".notdef" {
		t.Errorf("out[34]: got %q, want .notdef (untouched)", out[34])
	}
}

// TestApplyDifferences_MultipleEntries covers the case where two
// entries occur with non-adjacent starting codes — each entry's
// cursor resets independently.
func TestApplyDifferences_MultipleEntries(t *testing.T) {
	t.Parallel()
	base := fillNotdef()
	diffs := []internalpdf.DifferenceEntry{
		{Code: 65, Names: []string{"A", "B"}},
		{Code: 100, Names: []string{"d"}},
	}
	out := applyDifferences(base, diffs)
	if out[65] != "A" {
		t.Errorf("out[65]: got %q, want A", out[65])
	}
	if out[66] != "B" {
		t.Errorf("out[66]: got %q, want B", out[66])
	}
	if out[100] != "d" {
		t.Errorf("out[100]: got %q, want d", out[100])
	}
	if out[67] != ".notdef" {
		t.Errorf("out[67] gap: got %q, want .notdef", out[67])
	}
}

// TestApplyDifferences_OutOfRange covers the defensive branch: codes
// outside [0, 255] are silently skipped (no panic, no out-of-bounds).
func TestApplyDifferences_OutOfRange(t *testing.T) {
	t.Parallel()
	base := fillNotdef()
	diffs := []internalpdf.DifferenceEntry{
		{Code: 254, Names: []string{"A", "B", "C", "D"}}, // C/D run past 255
		{Code: -5, Names: []string{"X"}},
	}
	out := applyDifferences(base, diffs)
	if out[254] != "A" {
		t.Errorf("out[254]: got %q, want A", out[254])
	}
	if out[255] != "B" {
		t.Errorf("out[255]: got %q, want B", out[255])
	}
	// out-of-range writes (code 256, 257) silently dropped — no panic.
}

// TestApplyDifferences_EmptyOverridesUnchanged: empty diffs returns
// base unchanged.
func TestApplyDifferences_EmptyOverridesUnchanged(t *testing.T) {
	t.Parallel()
	base := WinAnsiEncoding
	out := applyDifferences(base, nil)
	for i := range 256 {
		if out[i] != base[i] {
			t.Errorf("out[%d]: got %q, want %q (unchanged)", i, out[i], base[i])
		}
	}
}

// TestApplyDifferences_DoesNotMutateBase: applying diffs returns a
// new array; base remains unchanged.
func TestApplyDifferences_DoesNotMutateBase(t *testing.T) {
	t.Parallel()
	base := fillNotdef()
	diffs := []internalpdf.DifferenceEntry{
		{Code: 65, Names: []string{"A"}},
	}
	out := applyDifferences(base, diffs)
	if base[65] != ".notdef" {
		t.Errorf("base[65] mutated: got %q, want .notdef", base[65])
	}
	if out[65] != "A" {
		t.Errorf("out[65]: got %q, want A", out[65])
	}
}
