// dehyphenate.go: end-of-line hyphen heuristic.
//
// V1 SCOPE: Latin-only. The rule "ends with '-', next line starts with
// lowercase, drop hyphen" assumes Latin script + Latin lowercase
// letters (rune <= 0x024F covering Basic Latin + Latin-1 Supplement +
// Latin Extended-A + Latin Extended-B). Greek (γ), Cyrillic (д), and
// other lowercase-bearing scripts are intentionally NOT dehyphenated
// in v1. v2 should consider Unicode-aware lowercase detection for
// non-Latin scripts (broader unicode.IsLower coverage).

package layout

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// dehyphenateLines applies the EOL-hyphen heuristic to a slice of
// Lines (the cluster.go orchestrator calls this AFTER Stage 2 block
// grouping so cross-block dehyphenation cannot happen by
// construction). For each adjacent pair (line N, line N+1):
//
//   - Trigger: line N's last run's Text (after trim of trailing
//     whitespace) ends with U+002D HYPHEN-MINUS '-'.
//   - Match: line N+1's first run's Text (after trim of leading
//     whitespace) starts with a Latin lowercase letter
//     (unicode.IsLetter && unicode.IsLower && rune <= 0x024F).
//   - Action: strip the trailing '-' from line N's last run's Text;
//     set lines[N].WasDehyphenated = true; line N+1 is unchanged.
//
// Lines remain distinct; the join is a downstream concern (T8
// chunker reads WasDehyphenated to suppress the gap when emitting
// markdown). Returns a NEW slice with copies of the Line structs;
// the input is not mutated.
func dehyphenateLines(lines []Line) []Line {
	if len(lines) < 2 {
		return lines
	}
	out := make([]Line, len(lines))
	copy(out, lines)
	for i := 0; i < len(out)-1; i++ {
		if !endsWithHyphen(out[i]) {
			continue
		}
		if !startsWithLatinLower(out[i+1]) {
			continue
		}
		// Strip the trailing '-' from the last run of line i. Copy
		// the Runs slice header so the in-place edit doesn't bleed
		// into the input slice (which shares the underlying array).
		stripped := stripTrailingHyphen(out[i].Runs)
		out[i].Runs = stripped
		out[i].WasDehyphenated = true
	}
	return out
}

// endsWithHyphen reports whether the line's last run's Text, after
// trimming trailing whitespace, ends with '-'. Empty / no-run lines
// return false.
func endsWithHyphen(l Line) bool {
	if len(l.Runs) == 0 {
		return false
	}
	last := strings.TrimRight(l.Runs[len(l.Runs)-1].Text, " \t")
	return strings.HasSuffix(last, "-")
}

// startsWithLatinLower reports whether the line's first run's Text,
// after trimming leading whitespace, begins with a rune that is
// (a) a letter, (b) lowercase, and (c) in the Latin Unicode range
// (≤ U+024F: Basic Latin, Latin-1 Supplement, Latin Extended-A,
// Latin Extended-B). Empty / no-run lines return false.
func startsWithLatinLower(l Line) bool {
	if len(l.Runs) == 0 {
		return false
	}
	first := strings.TrimLeft(l.Runs[0].Text, " \t")
	if first == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(first)
	if r == utf8.RuneError {
		return false
	}
	if r > 0x024F {
		return false
	}
	return unicode.IsLetter(r) && unicode.IsLower(r)
}

// stripTrailingHyphen returns a copy of the runs slice with the
// final run's Text rewritten to drop a trailing '-' (and any
// trailing whitespace before the hyphen). The runs slice header is
// always copied so callers don't mutate the original.
func stripTrailingHyphen(runs []text.TextRun) []text.TextRun {
	out := make([]text.TextRun, len(runs))
	copy(out, runs)
	idx := len(out) - 1
	t := out[idx].Text
	t = strings.TrimRight(t, " \t")
	t = strings.TrimSuffix(t, "-")
	out[idx].Text = t
	return out
}
