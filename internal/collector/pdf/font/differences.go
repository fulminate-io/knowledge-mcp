package font

import (
	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// applyDifferences returns a copy of base with each /Differences entry's
// glyph-name overrides applied. PDF 32000-1:2008 §9.6.6.1: an entry's
// Code resets the cursor; subsequent Names assign to cursor, cursor+1,
// cursor+2, ... So `[ 32 /space /exclam ]` writes "space" at code 32
// and "exclam" at code 33. Codes outside [0, 255] are silently skipped
// (PDF only addresses single-byte codes via /Encoding tables).
func applyDifferences(base [256]string, diffs []internalpdf.DifferenceEntry) [256]string {
	out := base
	for _, e := range diffs {
		cursor := e.Code
		for _, name := range e.Names {
			if cursor >= 0 && cursor < 256 {
				out[cursor] = name
			}
			cursor++
		}
	}
	return out
}
