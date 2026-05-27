// continuity.go — cross-page paragraph continuity merge.
//
// A paragraph that ends a page without terminator punctuation and is
// followed on the next page by a lowercase-starting block of similar
// font and X-start is treated as a single logical block spanning both
// pages (PageRange=[i, i+1], Lines concatenated, BBox stays at the
// first page's BBox per Chunk.BBox documentation).
//
// Resolved Q2 (locked ALL-3): all three signals required —
// terminator punctuation, font mismatch, or X-start mismatch each
// block the merge. Fewer false-positive merges; the occasional
// false-negative cross-page paragraph is acceptable.
//
// Algorithm uses an INDEX into the output slice (sentinel -1 = "no
// prior tail"), NOT a *mergedBlock pointer. Pointers into a growable
// slice are a footgun: a future refactor that changes the
// preallocation could silently invalidate held pointers via append-
// realloc. The pre-allocated cap (totalBlockCount) makes realloc
// impossible today, but the index-based shape removes the load-
// bearing invariant from the algorithm's correctness proof.
//
// Performance: serial. Single-pass O(total blocks) over perPage;
// per-block O(text length) only when probing the first/last
// non-whitespace rune (early-exit). The 3-signal check is O(1) per
// page boundary.

package chunk

import (
	"strings"
	"unicode"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// mergeAcrossPages walks per-page block slices and emits a single
// flat slice of mergedBlock values in document order. When the last
// body block of page N and the first body block of page N+1 satisfy
// all 3 continuation signals (no terminator, lowercase head, similar
// font + X-start), they are merged into one mergedBlock with
// PageRange=[N, N+1] and concatenated Lines.
//
// Headings never participate in the merge (skipped on either side).
// Empty pages reset the tail tracker — continuation never spans an
// empty page.
func mergeAcrossPages(perPage [][]layout.Block) []mergedBlock {
	out := make([]mergedBlock, 0, totalBlockCount(perPage))
	prevTailIdx := -1
	prevPageIdx := -1
	for pageIdx, page := range perPage {
		for blkIdx, b := range page {
			mb := mergedBlock{Block: b, PageRange: [2]int{b.PageIndex, b.PageIndex}}

			if blkIdx == 0 && prevTailIdx >= 0 && pageIdx == prevPageIdx+1 &&
				out[prevTailIdx].Kind != layout.BlockHeading && b.Kind != layout.BlockHeading &&
				crossPageMatch(out[prevTailIdx].Block, b) {
				mergeInto(&out[prevTailIdx], b, pageIdx)
				continue
			}

			out = append(out, mb)
			prevTailIdx = len(out) - 1
		}
		// Empty page resets the tail. SOLE safeguard against merging
		// across an empty-page gap — the pageIdx==prevPageIdx+1 check
		// inside the loop fails to fire when prevPageIdx hasn't
		// advanced past the empty page, so without this reset a later
		// non-empty page would still see prevTailIdx pointing at the
		// page-before-empty's tail.
		if len(page) == 0 {
			prevTailIdx = -1
		}
		prevPageIdx = pageIdx
	}
	return out
}

// crossPageMatch dispatches between the prose 3-signal heuristic and
// the relaxed code-to-code branch. Both branches agree on font + X
// signals; the prose path additionally requires the tail to lack
// terminator punctuation and the head to start with lowercase, which
// are unsuitable signals for code (line of code can end in any
// punctuation; the next line can start with an uppercase identifier
// or a closing brace).
func crossPageMatch(tail, head layout.Block) bool {
	if tail.Kind == layout.BlockCode && head.Kind == layout.BlockCode {
		return codeContinuationMatches(tail, head)
	}
	return continuationMatches(tail, head)
}

// continuationMatches applies the 3-signal prose cross-page heuristic.
// Returns true iff ALL three signals match.
func continuationMatches(tail, head layout.Block) bool {
	if isTerminator(blockTrailingChar(tail)) {
		return false
	}
	if !unicode.IsLower(blockLeadingRune(head)) {
		return false
	}
	if !sameFont(tail, head) {
		return false
	}
	if absFloat(tail.BBox.X0-head.BBox.X0) > 2.0 {
		return false
	}
	return true
}

// codeContinuationMatches is the relaxed cross-page branch for
// code-to-code joins. Drops the lowercase-head + non-terminator rules
// (code lines violate both routinely — `counts = Hash.new(0)` ends in
// `)`, `File.open(...)` starts in `F`). Replaces the strict FontName
// match with mono-fraction sameness so the regular-mono → bold-mono
// keyword transition (Cypher MATCH/RETURN, SQL SELECT/WHERE/JOIN)
// stitches across page breaks instead of fragmenting. Mirrors the
// intra-page sameCodeFamily helper in classify/classify_merge.go.
func codeContinuationMatches(tail, head layout.Block) bool {
	if !sameMonoFamily(tail, head) {
		return false
	}
	return absFloat(tail.BBox.X0-head.BBox.X0) <= codeCrossPageXTolPt
}

// codeCrossPageXTolPt — slightly more generous than the prose 2.0pt
// tolerance because legitimate cross-page code joins occasionally jump
// indent (e.g. `counts = Hash.new(0)` at column 0 → `File.open(...)` at
// column 0 with a 1pt typographic offset). Sized to absorb that
// without bridging unrelated code areas.
const codeCrossPageXTolPt = 6.0

// codeMergeMonoRatio — minimum monospace glyph fraction required for a
// block to participate in code-merging. Set below CodeMonospaceRatio
// (0.8) so a code block with inline non-mono annotation runs can still
// merge. Mirrors the intra-page constant in classify/classify_merge.go.
const codeMergeMonoRatio = 0.5

// sameMonoFamily reports whether both blocks are dominantly monospace.
// Substitutes for FontName equality in the cross-page code branch —
// syntax-highlighted code mixes a regular-mono face with a bold-mono
// face for keywords (LiberationMono + LiberationMono-Bold), FontName
// equality rejects that case incorrectly. Prose blocks fail the mono
// threshold and remain on the strict prose path.
func sameMonoFamily(a, b layout.Block) bool {
	return blockMonoFraction(a) >= codeMergeMonoRatio &&
		blockMonoFraction(b) >= codeMergeMonoRatio
}

// blockMonoFraction returns the glyph-weighted fraction of monospace
// glyphs in b. Mirrors classify.monospaceFraction; duplicated here to
// keep the chunk package self-contained without exporting a primitive
// that has no other consumers.
func blockMonoFraction(b layout.Block) float64 {
	var total, mono int
	for _, line := range b.Lines {
		for _, run := range line.Runs {
			w := len(run.Glyphs)
			total += w
			if run.Mono {
				mono += w
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(mono) / float64(total)
}

func isTerminator(r rune) bool {
	switch r {
	case '.', '?', '!', ':':
		return true
	}
	return false
}

// blockTrailingChar returns the last non-whitespace rune in the
// block, walking Lines and Runs back-to-front. Returns 0 for an
// empty block (the zero rune fails isTerminator and isLower checks
// — defensive in either direction).
func blockTrailingChar(b layout.Block) rune {
	for i := len(b.Lines) - 1; i >= 0; i-- {
		for j := len(b.Lines[i].Runs) - 1; j >= 0; j-- {
			t := strings.TrimRight(b.Lines[i].Runs[j].Text, " \t\n")
			if t == "" {
				continue
			}
			rs := []rune(t)
			return rs[len(rs)-1]
		}
	}
	return 0
}

// blockLeadingRune returns the first non-whitespace rune in the
// block, walking Lines and Runs front-to-back.
func blockLeadingRune(b layout.Block) rune {
	for _, l := range b.Lines {
		for _, r := range l.Runs {
			t := strings.TrimLeft(r.Text, " \t")
			if t == "" {
				continue
			}
			for _, rr := range t {
				return rr
			}
		}
	}
	return 0
}

// sameFont returns true when the primary font of a and b match by
// FontName and have Size within ±5%. Tighter than the in-tree T4
// layout grouper's tolerances because false-positive merges across
// page breaks are costly (they conflate two paragraphs); T4's
// CharMargin tolerance applies within a single page where line
// grouping must absorb legitimate intra-paragraph font variation.
func sameFont(a, b layout.Block) bool {
	af, as := primaryFont(a)
	bf, bs := primaryFont(b)
	if af == "" || bf == "" || af != bf {
		return false
	}
	if as == 0 || bs == 0 {
		return false
	}
	delta := as - bs
	if delta < 0 {
		delta = -delta
	}
	return delta/as <= 0.05
}

// primaryFont returns the FontName + Size of the first non-empty
// run in the block. Empty block → ("", 0).
func primaryFont(b layout.Block) (string, float64) {
	for _, l := range b.Lines {
		for _, r := range l.Runs {
			if r.Text != "" {
				return r.FontName, r.Size
			}
		}
	}
	return "", 0
}

// mergeInto extends prev in place by appending head's Lines and
// updating PageRange[1] to head's page index. BBox stays at
// prev.BBox (first page's bbox per Chunk.BBox doc). Taking
// &out[prevTailIdx] in the caller is safe because the pointer is
// scoped to this synchronous call — never stored beyond it.
func mergeInto(prev *mergedBlock, head layout.Block, headPageIdx int) {
	prev.Lines = append(prev.Lines, head.Lines...)
	prev.PageRange[1] = headPageIdx
}

// totalBlockCount sums the per-page block counts so mergeAcrossPages
// can pre-allocate out[] with exact capacity.
func totalBlockCount(perPage [][]layout.Block) int {
	n := 0
	for _, page := range perPage {
		n += len(page)
	}
	return n
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
