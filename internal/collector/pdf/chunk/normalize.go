// normalize.go — text normalization helpers.
//
// Per-block normalization is the bridge between the layout grouper's
// raw run text and the Chunk.Text field consumers expect. Three rules
// per the ticket spec:
//
//   - prose / heading / list-item / unknown blocks: trim + collapse
//     internal whitespace runs to a single space (paragraph reflow),
//     with the line-join space SUPPRESSED where the break fell inside a
//     word — either because layout stripped a hyphen there, or because
//     a compound word's own hyphen is still sitting at the line end;
//   - code blocks: preserve '\n' between lines (code structure
//     matters); within each line collapse internal whitespace; trim
//     at the block boundaries only;
//   - list-item blocks: same as prose. The layout grouper already
//     captures the marker as the first run's leading characters, so
//     no special prefix injection is needed here.
//
// The collapseWhitespace helper mirrors collector/web/parse_dom_emphasis.go's
// collapseText one-liner — same `strings.Join(strings.Fields(s), " ")`
// shape. The chunk package depends on layout + classify + stdlib only;
// pulling collector/web in for a 1-line helper would invert the
// dependency layering, so a local copy is justified.
//
// Performance: serial. Per-block work is O(total chars in block) with
// allocations bounded by the strings.Builder + strings.Fields slice.
// The chunker is a single-call, build-and-return API — caller does
// any fan-out at a higher level.

package chunk

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// normalizeBlockText reads block.Lines[*].Runs[*].Text and returns the
// Chunk-ready Text per the rules above. Idempotent.
func normalizeBlockText(b layout.Block) string {
	if b.Kind == layout.BlockCode {
		return normalizeCodeBlock(b)
	}
	// Prose / heading / list-item / unknown: join lines with a single
	// space then collapse all whitespace — EXCEPT where the previous
	// line ended in a word broken across the line break, where a space
	// is exactly wrong. See joinNeedsSpace.
	var sb strings.Builder
	for i, l := range b.Lines {
		if i > 0 && joinNeedsSpace(b.Lines[i-1]) {
			sb.WriteByte(' ')
		}
		sb.WriteString(joinLineRuns(l))
	}
	return collapseWhitespace(sb.String())
}

// joinNeedsSpace reports whether a space belongs between prev and the
// line that follows it. It is false in the two cases where the break
// falls INSIDE a word, and true everywhere else:
//
// CASE 1 — prev.WasDehyphenated. The layout pass already stripped the
// trailing hyphen because the next line continues the word in
// lowercase, so the two halves are one word: "sequen" + "tially". A
// space here produced "sequen tially", which is strictly worse than
// leaving the hyphen alone. This is the production reader
// layout.Line.WasDehyphenated was written for.
//
// CASE 2 — prev still ENDS in a hyphen-family rune. Layout deliberately
// leaves those alone when the continuation is not lowercase, which is
// what a COMPOUND word broken at its own hyphen looks like:
// "Event-" + "Driven". The hyphen is part of the word and must not be
// stripped, but a space after it would give "Event- Driven" instead of
// "Event-Driven".
//
// A block whose lines are empty falls through to true, which is the
// harmless direction: collapseWhitespace removes the redundant space.
func joinNeedsSpace(prev layout.Line) bool {
	if prev.WasDehyphenated {
		return false
	}
	r := lineTrailingRune(prev)
	if r == 0 {
		return true
	}
	return !layout.IsHyphenRune(r)
}

// lineTrailingRune returns the last non-whitespace rune on the line, or
// 0 when it has none. It walks the runs BACK TO FRONT and stops at the
// first one carrying a non-space rune, rather than concatenating the
// whole line to read one character off the end: this is called once per
// line boundary of every prose block in the document, and the
// concatenation it replaces allocated a string per call that nothing
// else ever read.
func lineTrailingRune(l layout.Line) rune {
	for _, v := range slices.Backward(l.Runs) {
		t := strings.TrimRight(v.Text, " \t")
		if t == "" {
			continue
		}
		r, _ := utf8.DecodeLastRuneInString(t)
		return r
	}
	return 0
}

// normalizeCodeBlock joins a code block's lines with '\n' and preserves
// per-line leading whitespace. The PDF text walker emits leading
// indentation as space characters in the first run's text (run.Text =
// "  file.each do |line|"), so we don't have to reconstruct it from
// X-positions — we only have to AVOID stripping it.
//
// Per-line behavior:
//   - leading whitespace: preserved verbatim (drives indentation)
//   - internal whitespace: runs of 2+ spaces/tabs collapsed to a single
//     space so accidental multi-space gaps inside a line don't bloat
//     output
//   - trailing whitespace: stripped (typically a result of right-padding
//     in monospace tables)
func normalizeCodeBlock(b layout.Block) string {
	if len(b.Lines) == 0 {
		return ""
	}
	lines := make([]string, 0, len(b.Lines))
	for _, l := range b.Lines {
		lines = append(lines, normalizeCodeLine(joinLineRuns(l)))
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// normalizeCodeLine preserves leading whitespace, strips trailing
// whitespace, and collapses runs of 2+ internal whitespace characters
// to a single space. Tabs in leading whitespace are preserved as-is;
// internal tabs are normalized to a space alongside multi-space runs.
func normalizeCodeLine(s string) string {
	leading := 0
	for leading < len(s) {
		c := s[leading]
		if c != ' ' && c != '\t' {
			break
		}
		leading++
	}
	indent := s[:leading]
	body := s[leading:]
	body = strings.TrimRight(body, " \t")
	// Collapse runs of internal whitespace to single space.
	if strings.ContainsAny(body, "  \t") {
		body = collapseInternalWhitespace(body)
	}
	return indent + body
}

// collapseInternalWhitespace replaces runs of spaces/tabs in s with a
// single space. Caller has already stripped leading + trailing
// whitespace, so s starts and ends with a non-whitespace rune.
func collapseInternalWhitespace(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' {
			if !prevSpace {
				sb.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		sb.WriteByte(c)
		prevSpace = false
	}
	return sb.String()
}

// joinLineRuns concatenates a Line's Runs[*].Text in order with no
// normalization (raw line text). Used as input to collapseWhitespace.
func joinLineRuns(l layout.Line) string {
	var b strings.Builder
	for _, r := range l.Runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

// collapseWhitespace trims s and collapses internal whitespace runs
// (any combination of spaces, tabs, newlines) to a single space.
// Single-pass — avoids the strings.Fields []string + strings.Join
// double allocation that the original mirror-of-collector/web shape
// paid on every block-text normalization.
func collapseWhitespace(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true // start true so leading whitespace is skipped
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		// Multi-byte UTF-8 whitespace (NBSP U+00A0, etc.) handled via
		// unicode.IsSpace would force a per-rune decode; the ASCII
		// fast path above covers the dominant case in PDF prose.
		// Fall through to writing the byte verbatim.
		b.WriteByte(c)
		prevSpace = false
	}
	out := b.String()
	if prevSpace && len(out) > 0 && out[len(out)-1] == ' ' {
		return out[:len(out)-1]
	}
	return out
}
