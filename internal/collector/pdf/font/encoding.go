// Package font owns CMap, Encoding, and glyph→Unicode mapping (T3
// scope). Depends on internal/pdfcpu; consumed by text. The four
// predefined encoding tables in this file are PDF 32000-1:2008 Annex D
// data — they map a single byte (0..255) to a glyph name. Empty slots
// default to ".notdef" per §9.6.6.1.
//
// The tables are package-level [256]string vars rather than maps for
// allocation-free lookup: the resolver indexes them as `enc[code]`
// directly. They are exported so test/debug code can spot-check
// individual entries; the only true public API of this package is
// `Decode`, `NewResolver`, `NewDocResolver`, and `FontResolver` (see
// resolver.go).
package font

// notdef is the canonical fallback glyph name for unmapped code points
// per PDF 32000-1:2008 §9.6.6.1. The four predefined encoding tables
// initialize all 256 slots to .notdef and overwrite the spec-mapped
// codes; downstream consumers can compare strings directly.
const notdef = ".notdef"

// fillNotdef returns a [256]string pre-initialized to .notdef. Used by
// the per-encoding init func patterns below. Inlined to a single
// statement; the per-table init then sets the spec-defined codes.
func fillNotdef() [256]string {
	var t [256]string
	for i := range t {
		t[i] = notdef
	}
	return t
}

// encodingByName returns the predefined encoding table for the given
// /Encoding name. Recognized names per PDF 32000-1:2008 §9.6.6:
// "StandardEncoding", "WinAnsiEncoding", "MacRomanEncoding",
// "MacExpertEncoding". Returns (zero, false) for any other name —
// callers fall back to the resolver's 7-rung ladder.
//
// Package-private: the resolver is the only intended caller.
func encodingByName(name string) (*[256]string, bool) {
	switch name {
	case "WinAnsiEncoding":
		return &WinAnsiEncoding, true
	case "MacRomanEncoding":
		return &MacRomanEncoding, true
	case "MacExpertEncoding":
		return &MacExpertEncoding, true
	case "StandardEncoding":
		return &StandardEncoding, true
	}
	return nil, false
}
