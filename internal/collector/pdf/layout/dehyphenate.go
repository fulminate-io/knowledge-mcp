// dehyphenate.go: end-of-line hyphen heuristic.
//
// V1 SCOPE: Latin-only. The rule "ends with a hyphen, next line starts
// with lowercase, drop hyphen" assumes Latin script + Latin lowercase
// letters (rune <= 0x024F covering Basic Latin + Latin-1 Supplement +
// Latin Extended-A + Latin Extended-B). Greek, Cyrillic, and other
// lowercase-bearing scripts are intentionally NOT dehyphenated in v1.
// v2 should consider Unicode-aware lowercase detection for non-Latin
// scripts (broader unicode.IsLower coverage).

package layout

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// The four code points a typesetter can break a word on. Publishers do
// not agree on which: U+002D is what a keyboard produces, O'Reilly's
// PDFs break on U+2010, and U+00AD is what a word processor inserts
// when it hyphenates for you. Triggering on U+002D alone left words
// broken by the other three stranded mid-word with the hyphen intact.
//
// They are spelled as numeric code points rather than as character
// literals on purpose: U+00AD is invisible in an editor and U+2010 is
// indistinguishable from U+002D at most font sizes, so a literal would
// be both unreadable and impossible to grep for.
const (
	runeHyphenMinus       = rune(0x002D) // HYPHEN-MINUS
	runeHyphen            = rune(0x2010) // HYPHEN
	runeNonBreakingHyphen = rune(0x2011) // NON-BREAKING HYPHEN
	runeSoftHyphen        = rune(0x00AD) // SOFT HYPHEN
)

// IsHyphenRune reports whether r is one of the four end-of-line hyphen
// code points. Exported because the chunk package's line joiner has to
// recognize exactly the same set: layout strips the hyphen when the
// next line continues the word, and the joiner must not insert a space
// where a hyphen was removed OR where one was deliberately kept.
func IsHyphenRune(r rune) bool {
	switch r {
	case runeHyphenMinus, runeHyphen, runeNonBreakingHyphen, runeSoftHyphen:
		return true
	}
	return false
}

// DehyphenateLines applies the end-of-line hyphen heuristic to a slice
// of Lines that has just been assembled by a caller OUTSIDE the layout
// grouper.
//
// It exists because the grouper runs its own pass per block, at cluster
// time, and a later stage that CONCATENATES two blocks creates a line
// boundary that pass never saw. classify.MergeSplitHeadings is the
// case: a heading torn across two blocks at a hyphenated word arrives
// as "Concur-" and "rency", and without re-running the heuristic over
// the joined slice the emitted heading reads "Concur-rency".
//
// Idempotent on already-processed input: a line whose hyphen was
// stripped no longer ends in one, so the trigger cannot fire twice.
func DehyphenateLines(lines []Line) []Line {
	return dehyphenateLines(lines)
}

// dehyphenateLines applies the EOL-hyphen heuristic to a slice of
// Lines (the cluster.go orchestrator calls this AFTER Stage 2 block
// grouping so cross-block dehyphenation cannot happen by
// construction). For each adjacent pair (line N, line N+1):
//
//   - Trigger: line N's last run's Text (after trim of trailing
//     whitespace) ends with a hyphen-family rune (see IsHyphenRune).
//   - Match: line N+1's first run's Text (after trim of leading
//     whitespace) starts with a Latin lowercase letter
//     (unicode.IsLetter && unicode.IsLower && rune <= 0x024F).
//   - Action: strip the trailing hyphen from line N's last run's Text;
//     set lines[N].WasDehyphenated = true; line N+1 is unchanged.
//
// Lines remain distinct; the join is a downstream concern (the chunk
// package's normalizeBlockText reads WasDehyphenated to suppress the
// space when concatenating). Returns a NEW slice with copies of the
// Line structs; the input is not mutated.
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
		// Strip the trailing hyphen from the last run of line i. Copy
		// the Runs slice header so the in-place edit doesn't bleed
		// into the input slice (which shares the underlying array).
		stripped := stripTrailingHyphen(out[i].Runs)
		out[i].Runs = stripped
		out[i].WasDehyphenated = true
	}
	return out
}

// endsWithHyphen reports whether the line's last run's Text, after
// trimming trailing whitespace, ends with a hyphen-family rune. Empty /
// no-run lines return false.
func endsWithHyphen(l Line) bool {
	if len(l.Runs) == 0 {
		return false
	}
	last := strings.TrimRight(l.Runs[len(l.Runs)-1].Text, " \t")
	if last == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(last)
	return IsHyphenRune(r)
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

// stripTrailingHyphen returns a copy of the runs slice with the final
// run's Text rewritten to drop a trailing hyphen-family rune (and any
// trailing whitespace before it). The rune is removed by its own width
// via DecodeLastRuneInString, not by a fixed one-byte trim: U+2010,
// U+2011 and U+00AD are multi-byte in UTF-8, and lopping one byte off
// them would leave an invalid fragment behind. The runs slice header is
// always copied so callers don't mutate the original.
func stripTrailingHyphen(runs []text.TextRun) []text.TextRun {
	out := make([]text.TextRun, len(runs))
	copy(out, runs)
	idx := len(out) - 1
	t := strings.TrimRight(out[idx].Text, " \t")
	if r, size := utf8.DecodeLastRuneInString(t); IsHyphenRune(r) {
		t = t[:len(t)-size]
	}
	out[idx].Text = t
	return out
}
