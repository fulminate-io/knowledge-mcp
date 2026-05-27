// classify_code.go — code block detection.
//
// Code is detected by glyph-weighted monospace fraction over the block,
// with a single-line edge case requiring non-mono neighbors and a
// ≥ 4-character leading indent.
//
// Inline-code metadata is a discrete signal: hasMonoRun(block) — true
// iff the block contains at least one TextRun whose Mono flag is set.
// The orchestrator (classify.go) uses hasMonoRun, NOT
// monospaceFraction, when setting Metadata["has_inline_code"] on
// paragraph blocks. See plan reviewer Tier-2 finding.

package classify

import (
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// monospaceFraction returns the glyph-weighted fraction of monospace
// glyphs in block. Returns 0 for empty blocks.
func monospaceFraction(block layout.Block) float64 {
	var total, monoTotal int
	for _, line := range block.Lines {
		for _, run := range line.Runs {
			w := len(run.Glyphs)
			total += w
			if run.Mono {
				monoTotal += w
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(monoTotal) / float64(total)
}

// hasMonoRun reports whether block contains at least one TextRun whose
// Mono flag is set. Discrete signal — no fraction or weight. Used by
// the orchestrator (Phase 6) to set the "has_inline_code" metadata key
// on paragraph blocks (resolved per plan reviewer Tier-2 finding: the
// trigger is ≥ 1 mono run, not a fraction > 0).
func hasMonoRun(block layout.Block) bool {
	for _, line := range block.Lines {
		for _, run := range line.Runs {
			if run.Mono {
				return true
			}
		}
	}
	return false
}

// isCodeBlock returns true when block qualifies as code:
//   - monospaceFraction(block) ≥ cp.CodeMonospaceRatio AND
//   - len(block.Lines) ≥ 2, OR
//   - single-line block whose neighbor on either side is itself a
//     monospace block (the single line is part of a multi-block code
//     sequence — e.g. one program split across blocks by blank lines or
//     annotation marks), OR
//   - single-line standalone snippet sandwiched between non-mono blocks
//     with ≥ 4-character leading indent, OR
//   - font-agnostic: hasNonMonoCodeShape(block) — catches code typeset
//     in a non-mono face (some publishers use sans-serif for Cypher /
//     SQL syntax-highlighted samples). High-precision composite of
//     punctuation density + indent-column variance; described in the
//     hasNonMonoCodeShape doc.
//
// The "adjacent mono" relaxation (vs. the original "both neighbors must
// be non-mono") fixes single-line code lines like Ruby's
// `counts = Hash.new(0)` that sit alone before a multi-line block —
// the next block is mono, so the previous "both-non-mono" rule rejected
// them as paragraph. Adjacent-mono is a stronger code signal than
// non-mono sandwich; the indent rule is preserved as a fallback for
// truly standalone snippets in prose.
func isCodeBlock(block layout.Block, prev, next *layout.Block, cp ClassifyParams) bool {
	if monospaceFraction(block) >= cp.CodeMonospaceRatio {
		if monoCodeMatches(block, prev, next, cp) {
			return true
		}
	}
	// Font-agnostic fallback: code in a non-mono face still has a
	// recognizable shape (high punctuation density across multiple
	// indent columns). Multi-line only — single-line non-mono "code"
	// has no signal that distinguishes it from a regular sentence.
	return hasNonMonoCodeShape(block)
}

// monoCodeMatches contains the original mono-based dispatch — kept as a
// helper so isCodeBlock's overall structure stays readable now that it
// also carries the font-agnostic branch.
func monoCodeMatches(block layout.Block, prev, next *layout.Block, cp ClassifyParams) bool {
	if len(block.Lines) >= 2 {
		return true
	}
	if len(block.Lines) != 1 {
		return false
	}
	prevMono := prev != nil && monospaceFraction(*prev) >= cp.CodeMonospaceRatio
	nextMono := next != nil && monospaceFraction(*next) >= cp.CodeMonospaceRatio
	if prevMono || nextMono {
		return true
	}
	// Page-edge case: classify operates per-page, so a single-line mono
	// block sitting alone at the start or end of a page has prev=nil or
	// next=nil even when its true neighbor on the adjacent page is also
	// mono code. Standalone monospace blocks at page boundaries are
	// overwhelmingly code.
	if prev == nil || next == nil {
		return true
	}
	// Standalone single-line snippet: non-mono on both sides, must be
	// indented ≥ 4 chars.
	return leadingIndentChars(block.Lines[0]) >= 4
}

// hasNonMonoCodeShape recognizes code blocks typeset in a non-monospace
// face. Composite signal — every axis must fire so a figure annotation
// or table-cell block can't trigger incidentally:
//
//  1. Block has ≥ codeShapeMinLines lines. Real code is rarely a
//     2-liner; figure annotations often are.
//  2. Aggregate punctuation density ≥ codeShapePunctRatio. Code shows
//     the `(){}[]:;=<>|&+*/@#$` family at much higher rates than prose
//     (typically 25-40% for code, 5-15% for prose).
//  3. ≥ codeShapeMinIndentColumns distinct line-start X positions
//     (within codeShapeIndentTolPt). Code uses nested indent levels;
//     prose has one left margin and figure annotations rarely cross
//     4 distinct columns.
//
// Tail-terminator gate (`.?!:`) excludes complete prose paragraphs
// that incidentally clear the other axes (an RFC-7234-style all-mono
// paragraph stream relies on this to stay in the prose lane).
func hasNonMonoCodeShape(block layout.Block) bool {
	if len(block.Lines) < codeShapeMinLines {
		return false
	}
	if punctuationDensity(block) < codeShapePunctRatio {
		return false
	}
	if distinctIndentColumns(block) < codeShapeMinIndentColumns {
		return false
	}
	if endsInProseTerminator(block) {
		return false
	}
	return true
}

const (
	// codeShapeMinLines — minimum block size for the non-mono branch
	// to consider firing. 3 lines is the smallest size at which
	// "nested indent" can manifest (block-open, body, block-close)
	// and rules out 2-line figure annotations.
	codeShapeMinLines = 3

	// codeShapePunctRatio — minimum aggregate fraction of
	// non-whitespace glyphs that must be code-shape punctuation for
	// the non-mono branch to fire. Empirically prose runs ~5-15%;
	// code (Cypher, SQL, Python, Go, JS) typically clears 20-40%.
	codeShapePunctRatio = 0.20

	// codeShapeMinIndentColumns — minimum distinct line-start X
	// positions (within codeShapeIndentTolPt). 4 columns implies real
	// nested indent levels (outer block, statement, expression body,
	// continuation) and rules out 2-3-column figure annotations.
	codeShapeMinIndentColumns = 4

	// codeShapeIndentTolPt — points within which two line X-starts
	// count as the same column. Sized to absorb typographic noise
	// without merging adjacent indent levels.
	codeShapeIndentTolPt = 2.0
)

// punctuationDensity returns the fraction of non-whitespace glyphs in
// block whose rune is one of the structural / operator chars that
// dominate code. Whitespace is excluded from the denominator so wide
// indentation doesn't dilute the signal.
func punctuationDensity(block layout.Block) float64 {
	var punct, total int
	for _, line := range block.Lines {
		for _, run := range line.Runs {
			for _, r := range run.Text {
				if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
					continue
				}
				total++
				if isCodeShapePunct(r) {
					punct++
				}
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(punct) / float64(total)
}

// isCodeShapePunct reports whether r is one of the structural /
// operator characters that distinguish code from prose. Includes
// brackets, separators, operators, and the comment / variable lead
// chars common across major languages. Sentence punctuation (`.,?!`)
// is excluded; `:` and `;` are included because code uses both as
// structural separators (typing, namespacing, statement terminators)
// while prose uses them sparingly.
func isCodeShapePunct(r rune) bool {
	switch r {
	case '{', '}', '[', ']', '(', ')',
		'=', '<', '>', '|', '&', '+', '*', '/', '\\',
		':', ';',
		'@', '#', '$', '%', '^', '~', '`':
		return true
	}
	return false
}

// distinctIndentColumns returns the number of distinct line-start X
// positions in block, with two X values within codeShapeIndentTolPt
// counted as the same column.
func distinctIndentColumns(block layout.Block) int {
	if len(block.Lines) == 0 {
		return 0
	}
	xs := make([]float64, 0, len(block.Lines))
	for _, l := range block.Lines {
		xs = append(xs, l.BBox.X0)
	}
	// Insertion sort — block.Lines is short (typical < 30).
	for i := 1; i < len(xs); i++ {
		v := xs[i]
		j := i - 1
		for j >= 0 && xs[j] > v {
			xs[j+1] = xs[j]
			j--
		}
		xs[j+1] = v
	}
	distinct := 1
	for i := 1; i < len(xs); i++ {
		if xs[i]-xs[i-1] > codeShapeIndentTolPt {
			distinct++
		}
	}
	return distinct
}

// endsInProseTerminator reports whether the block's last non-whitespace
// rune is a sentence-ending terminator (`.?!:`). Used by
// hasNonMonoCodeShape to reject prose paragraphs that incidentally
// score high on the punctuation/indent axes.
func endsInProseTerminator(block layout.Block) bool {
	for i := range slices.Backward(block.Lines) {
		for j := range slices.Backward(block.Lines[i].Runs) {
			t := block.Lines[i].Runs[j].Text
			for len(t) > 0 {
				last := t[len(t)-1]
				if last != ' ' && last != '\t' && last != '\n' {
					break
				}
				t = t[:len(t)-1]
			}
			if t == "" {
				continue
			}
			rs := []rune(t)
			r := rs[len(rs)-1]
			return r == '.' || r == '?' || r == '!' || r == ':'
		}
	}
	return false
}

// leadingIndentChars counts leading 0x20 (space) or 0x09 (tab) bytes in
// the first run's Text on line. Stops at the first non-whitespace byte.
// Tabs count as 1 character (not expanded). Returns 0 when the line has
// no runs or its first run has no text.
func leadingIndentChars(line layout.Line) int {
	if len(line.Runs) == 0 {
		return 0
	}
	t := line.Runs[0].Text
	count := 0
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c != ' ' && c != '\t' {
			break
		}
		count++
	}
	return count
}
