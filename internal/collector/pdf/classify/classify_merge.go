// classify_merge.go — post-classification stitching of code blocks
// fragmented by the layout grouper.
//
// Three layout-grouper rules legitimately fragment a single program:
//
//   - bold-flip (canExtendBlock): a code line of language keywords
//     only (Ruby's `end`, Java's `if`) is all-bold while surrounding
//     code is mixed-weight. The heading-bleed gate fires.
//   - paragraph-gap (canExtendBlock): a single blank line within code
//     can exceed medianGap × ParagraphGapRatio.
//   - misclassification (isHeadingCandidate): a single-line all-bold
//     mono token (SQL's SELECT/WHERE/GROUP BY) trips the all-bold
//     heading rule.
//
// Once Block.Kind is known, adjacent code-blocks (or code-block triples
// straddling a misclassified bold-keyword middle) are unambiguously
// part of one logical program. mergeAdjacentCodeBlocks runs after
// assignHeadingLevels and reclaims them. Same-page only — cross-page
// stitching is the chunk continuity pass.

package classify

import (
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

const (
	// codeXTolPt — first-line X-start tolerance for treating two code
	// blocks as sharing the same code area's left margin.
	codeXTolPt = 6.0

	// codeVerticalGapPt — max gap (last-line of a → first-line of b,
	// in user-space points) below which two same-page code blocks are
	// considered part of one program. Empirically tuned: code with a
	// blank-line separator measures ~20pt (DDIA Ruby) and SQL
	// transactions measure ~30.3pt (DDIA p260 BEGIN/COMMIT). 31pt
	// absorbs both. The RFC-7234 plain-text fixture (whole-doc mono)
	// stays in bounds via the multi-line+terminator gate below.
	codeVerticalGapPt = 31.0

	// codeMergeMonoRatio — minimum monospace fraction for a block to
	// participate in code-merging. Below CodeMonospaceRatio so a code
	// block with inline non-mono annotation runs can still merge.
	codeMergeMonoRatio = 0.5
)

// mergeAdjacentCodeBlocks stitches consecutive same-page BlockCode
// blocks. Triples (code, heading-shaped keyword, code) are also
// merged when the middle is a short all-mono interloper (SELECT/
// WHERE/GROUP BY pattern); the middle is reclassified to BlockCode.
// Mutates entries in place when merging; returns the merged slice.
func mergeAdjacentCodeBlocks(blocks []layout.Block) []layout.Block {
	if len(blocks) < 2 {
		return blocks
	}
	out := make([]layout.Block, 0, len(blocks))
	consumed := make([]bool, len(blocks))
	for i := range blocks {
		if consumed[i] {
			continue
		}
		if blocks[i].Kind != layout.BlockCode {
			if !leadingCodeKeyword(blocks, i, consumed) {
				out = append(out, blocks[i])
				continue
			}
			blocks[i].Kind = layout.BlockCode
		}
		anchor := blocks[i]
		j := i + 1
		for j < len(blocks) {
			if consumed[j] {
				j++
				continue
			}
			if blocks[j].PageIndex != anchor.PageIndex {
				break
			}
			if blocks[j].Kind == layout.BlockCode {
				if !shouldMergeCode(anchor, blocks[j]) {
					break
				}
				mergeCodeBlock(&anchor, blocks[j])
				consumed[j] = true
				j++
				continue
			}
			if k, ok := codeAfterInterloper(blocks, j, anchor); ok {
				blocks[j].Kind = layout.BlockCode
				mergeCodeBlock(&anchor, blocks[j])
				mergeCodeBlock(&anchor, blocks[k])
				consumed[j] = true
				consumed[k] = true
				j = k + 1
				continue
			}
			break
		}
		out = append(out, anchor)
	}
	return out
}

// leadingCodeKeyword reports whether blocks[i] is a misclassified code
// keyword sitting just above a code block (e.g. SQL's `SELECT`
// preceding the column list). Reclassifying lets the anchor-merge
// loop fold the program into one block.
func leadingCodeKeyword(blocks []layout.Block, i int, consumed []bool) bool {
	if blocks[i].Kind != layout.BlockHeading || !looksLikeCodeKeyword(blocks[i]) {
		return false
	}
	j := i + 1
	if j >= len(blocks) || consumed[j] {
		return false
	}
	if blocks[j].Kind != layout.BlockCode || blocks[j].PageIndex != blocks[i].PageIndex {
		return false
	}
	if !sameCodeColumn(blocks[i], blocks[j]) {
		return false
	}
	return verticalGapBlocks(blocks[i], blocks[j]) <= codeVerticalGapPt
}

// codeAfterInterloper returns the index of the next code-block after a
// short bold-keyword interloper at j when (anchor, j, k) qualifies.
//
// Font check is relaxed for the interloper itself — SQL keywords like
// SELECT/WHERE/GROUP BY are routinely typeset in a bold-mono variant
// while the surrounding code is in the regular face. Both are mono,
// treating them as one program is correct. The outer anchor →
// blocks[k] check still requires the strict shouldMergeCode gate.
func codeAfterInterloper(blocks []layout.Block, j int, anchor layout.Block) (int, bool) {
	if blocks[j].Kind != layout.BlockHeading || !looksLikeCodeKeyword(blocks[j]) {
		return 0, false
	}
	k := j + 1
	if k >= len(blocks) {
		return 0, false
	}
	if blocks[k].Kind != layout.BlockCode || blocks[k].PageIndex != anchor.PageIndex {
		return 0, false
	}
	if anchor.PageIndex != blocks[j].PageIndex || !sameCodeColumn(anchor, blocks[j]) {
		return 0, false
	}
	if verticalGapBlocks(anchor, blocks[j]) > codeVerticalGapPt {
		return 0, false
	}
	if !shouldMergeCode(anchor, blocks[k]) {
		return 0, false
	}
	return k, true
}

// shouldMergeCode reports whether b can be appended to a as a
// continuation of one same-page code block. Required: same page, both
// dominantly monospace, same code column (within codeXTolPt), gap
// within codeVerticalGapPt. Mono-fraction sameness substitutes for a
// strict FontName match — syntax-highlighted code mixes regular-mono
// with bold-mono variants, both are mono, treating them as one family
// is correct (prose blocks fail the mono threshold and are
// unaffected). When BOTH blocks are multi-line, additionally require
// that a does NOT end in a sentence-final terminator (`.?!:`) — that
// gate keeps an RFC-7234-style all-mono document's prose paragraphs
// separate (each ends in `.`) without affecting code (which ends in
// `;`, `}`, `)`, identifiers, etc.).
func shouldMergeCode(a, b layout.Block) bool {
	if a.PageIndex != b.PageIndex {
		return false
	}
	if !sameCodeFamily(a, b) || !sameCodeColumn(a, b) {
		return false
	}
	if verticalGapBlocks(a, b) > codeVerticalGapPt {
		return false
	}
	if len(a.Lines) >= 2 && len(b.Lines) >= 2 && isProseTerminator(blockTrailingChar(a)) {
		return false
	}
	return true
}

// isProseTerminator matches the prose continuation rule's terminator
// set (chunk/continuity.go). Duplicated here to keep classify free of
// a chunk-package dependency — the boundary is one-way.
func isProseTerminator(r rune) bool {
	switch r {
	case '.', '?', '!', ':':
		return true
	}
	return false
}

// blockTrailingChar returns the last non-whitespace rune in b, walking
// Lines + Runs back-to-front. Mirrors the chunk-package helper of the
// same shape; duplicated to preserve the layering boundary.
func blockTrailingChar(b layout.Block) rune {
	for i := range slices.Backward(b.Lines) {
		for j := range slices.Backward(b.Lines[i].Runs) {
			t := b.Lines[i].Runs[j].Text
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
			return rs[len(rs)-1]
		}
	}
	return 0
}

func sameCodeFamily(a, b layout.Block) bool {
	return monospaceFraction(a) >= codeMergeMonoRatio &&
		monospaceFraction(b) >= codeMergeMonoRatio
}

// looksLikeCodeKeyword reports whether b is a short all-mono single-
// line block likely to be a misclassified language keyword (SELECT,
// WHERE, GROUP BY, end, ...). The "1 line + ≥ CodeMonospaceRatio mono"
// combo is narrow enough that real headings (Helvetica-Bold prose)
// won't qualify.
func looksLikeCodeKeyword(b layout.Block) bool {
	if len(b.Lines) != 1 {
		return false
	}
	return monospaceFraction(b) >= DefaultClassifyParams.CodeMonospaceRatio
}

// sameCodeColumn reports whether a and b share the same code area's
// left margin within codeXTolPt (compared on first-line BBox.X0).
func sameCodeColumn(a, b layout.Block) bool {
	if len(a.Lines) == 0 || len(b.Lines) == 0 {
		return false
	}
	return absFloat(a.Lines[0].BBox.X0-b.Lines[0].BBox.X0) <= codeXTolPt
}

// verticalGapBlocks returns the user-space gap from a's last line
// bottom to b's first line top. Returns a large sentinel for empty
// blocks so callers reject the merge.
func verticalGapBlocks(a, b layout.Block) float64 {
	if len(a.Lines) == 0 || len(b.Lines) == 0 {
		return 1e9
	}
	gap := b.Lines[0].BBox.Y0 - a.Lines[len(a.Lines)-1].BBox.Y1
	if gap < 0 {
		gap = -gap
	}
	return gap
}

// mergeCodeBlock appends b's lines to a in place, expanding a.BBox to
// enclose b. b is logically consumed by the caller after this call.
func mergeCodeBlock(a *layout.Block, b layout.Block) {
	a.Lines = append(a.Lines, b.Lines...)
	a.BBox = unionRect(a.BBox, b.BBox)
}

func unionRect(a, b layout.Rect) layout.Rect {
	if (a == layout.Rect{}) {
		return b
	}
	if (b == layout.Rect{}) {
		return a
	}
	out := a
	if b.X0 < out.X0 {
		out.X0 = b.X0
	}
	if b.Y0 < out.Y0 {
		out.Y0 = b.Y0
	}
	if b.X1 > out.X1 {
		out.X1 = b.X1
	}
	if b.Y1 > out.Y1 {
		out.Y1 = b.Y1
	}
	return out
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
