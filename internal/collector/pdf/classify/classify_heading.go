// classify_heading.go — heading detection and level assignment.
//
// Two responsibilities:
//   - isHeadingCandidate: per-block decision via three OR-joined rules
//     (large-font, all-bold gated by body-bold, short-block + vertical
//     gap).
//   - AssignHeadingLevelsDocument: distinct-size descending sort over
//     EVERY page → H1..H6 with bold>italic precedence at the same
//     size. The level is a document rank, not a per-page one.
//
// Pure-logic file — orchestration (calibration, gap computation, kind
// dispatch) lives in classify.go.

package classify

import (
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// isHeadingCandidate decides whether a single block (with calibrationResult
// already computed and the average inter-block gap already supplied)
// qualifies as a heading. Returns true when any of the three rules below
// fires; false when block is empty or no rule fires.
//
//	Rule #1 — size-only: every run has size ≥ cal.BodySize *
//	  cp.HeadingFontSizeRatio. When cp.HeadingMinBoldOnly is true, this
//	  rule additionally requires at least one bold run (suppresses
//	  pull-quotes / captions in large fonts).
//	Rule #2 — all-bold: every run is bold. INELIGIBLE when
//	  cal.BodyIsBold (the document body itself is dominantly bold).
//	Rule #3 — short-block + vertical-gap: 1 ≤ len(block.Lines) ≤ 3 AND
//	  maxRunSize ≥ cal.BodySize * 1.05 AND gapAbove > avgBlockGap.
func isHeadingCandidate(block layout.Block, cal calibrationResult, gapAbove, avgBlockGap float64, cp ClassifyParams) bool {
	if len(block.Lines) == 0 || cal.BodySize <= 0 {
		return false
	}
	if rule1LargeFont(block, cal, cp) {
		return true
	}
	if !cal.BodyIsBold && blockAllBold(block) {
		return true
	}
	return rule3ShortGap(block, cal, gapAbove, avgBlockGap)
}

// rule1LargeFont implements Rule #1: every run has size ≥
// cal.BodySize * cp.HeadingFontSizeRatio. When cp.HeadingMinBoldOnly
// is true, also requires at least one bold run.
func rule1LargeFont(block layout.Block, cal calibrationResult, cp ClassifyParams) bool {
	threshold := cal.BodySize * cp.HeadingFontSizeRatio
	runCount := 0
	allLarge := true
	anyBold := false
	for _, line := range block.Lines {
		for _, run := range line.Runs {
			runCount++
			if run.Size < threshold {
				allLarge = false
			}
			if run.Bold {
				anyBold = true
			}
		}
	}
	if runCount == 0 || !allLarge {
		return false
	}
	return !cp.HeadingMinBoldOnly || anyBold
}

// rule3ShortGap implements Rule #3: 1≤len(Lines)≤3, maxRunSize ≥
// 1.05× body, and gapAbove strictly > avgBlockGap.
func rule3ShortGap(block layout.Block, cal calibrationResult, gapAbove, avgBlockGap float64) bool {
	if n := len(block.Lines); n < 1 || n > 3 {
		return false
	}
	return blockMaxRunSize(block) >= cal.BodySize*1.05 && gapAbove > avgBlockGap
}

// blockMaxRunSize returns the largest run.Size across every line of
// block. Returns 0 for empty blocks.
func blockMaxRunSize(block layout.Block) float64 {
	var max float64
	for _, line := range block.Lines {
		for _, run := range line.Runs {
			if run.Size > max {
				max = run.Size
			}
		}
	}
	return max
}

// blockAllBold reports whether every run on every line is bold (and the
// block is non-empty).
func blockAllBold(block layout.Block) bool {
	hasRun := false
	for _, line := range block.Lines {
		for _, run := range line.Runs {
			hasRun = true
			if !run.Bold {
				return false
			}
		}
	}
	return hasRun
}

// blockAllItalic reports whether every run on every line is italic (and
// the block is non-empty). Used for the same-size bold>italic precedence
// at level-assignment time.
func blockAllItalic(block layout.Block) bool {
	hasRun := false
	for _, line := range block.Lines {
		for _, run := range line.Runs {
			hasRun = true
			if !run.Italic {
				return false
			}
		}
	}
	return hasRun
}

// AssignHeadingLevelsDocument ranks the distinct heading sizes across
// EVERY page of a document and writes the resulting level into every
// heading block on every page. Sizes sort descending and take their
// level from their index (1..6, capped at 6); when the document has
// only N distinct heading sizes (N < 6) the levels are 1..N, with no
// artificial expansion to 6. One shared size→level map means a 24pt
// title on page 0 and an 18pt subhead on page 12 rank 1 and 2 rather
// than both ranking 1 against their own page.
//
// Same-size precedence: when an all-bold heading and an
// all-italic-only heading share the same maxRunSize, the italic block
// is bumped DOWN one level (cap 6) so bold ranks higher than italic in
// the conventional typographic hierarchy. Mixed bold+italic is treated
// as bold for this rule.
//
// Callers run this once, after the last page has been through
// ClassifyPage — a rank cannot be computed from a page in isolation.
func AssignHeadingLevelsDocument(perPage [][]layout.Block) {
	// PAGE FURNITURE IS NOT DOCUMENT HIERARCHY. A running header
	// repeated on every page is not a level in the outline, and letting
	// its size into the ranking population compresses the real levels
	// below it. The chrome detector has already identified those blocks,
	// so excluding them costs nothing.
	sorted := documentHeadingSizesDesc(perPage, true)
	if len(sorted) == 0 {
		// EMPTY POPULATION. Every heading-classified block in this
		// document is chrome — a multi-page report with a repeated
		// banner and no other bold or large text. There is no scale to
		// place anything on, so the rank falls back to ranking over ALL
		// heading blocks, chrome included, and the banners land at
		// level 1 and downward on their own scale.
		//
		// Without this the placement rule below has nothing to place
		// against and every heading emerges at level 0, which
		// chunk/section.go reads as "not a heading": it pops the whole
		// heading stack and flattens the section tree, and the emitter
		// writes no heading_level key at all. The failure is silent,
		// which is why one line of fallback is worth having.
		sorted = documentHeadingSizesDesc(perPage, false)
	}
	if len(sorted) == 0 {
		return
	}
	sizeToLevel := make(map[float64]int, len(sorted))
	for idx, sz := range sorted {
		sizeToLevel[sz] = min(idx+1, 6)
	}
	style := documentStylePerSize(perPage)
	for _, page := range perPage {
		applyHeadingLevels(page, sizeToLevel, sorted, style)
	}
}

// documentHeadingSizesDesc unions the per-page distinct heading sizes
// into one descending document-wide ranking population. When
// excludeChrome is set, blocks carrying a page-repeat stamp are left
// out of the population — they are still LEVELED, by placement onto
// the resulting scale, they just do not define it.
func documentHeadingSizesDesc(perPage [][]layout.Block, excludeChrome bool) []float64 {
	sizes := make(map[float64]struct{}, 8)
	for _, page := range perPage {
		for _, sz := range distinctHeadingSizesDesc(page, excludeChrome) {
			sizes[sz] = struct{}{}
		}
	}
	if len(sizes) == 0 {
		return nil
	}
	out := make([]float64, 0, len(sizes))
	for sz := range sizes {
		out = append(out, sz)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// levelForSize returns the level a heading of size sz takes on the
// ranked scale. A size IN the population takes its own rank; a size
// outside it — which after chrome exclusion means a running header's
// size — is PLACED on the scale rather than left level-less: it takes
// the level of the first ranked size at or below its own, or the
// deepest level when it is smaller than every ranked size.
//
// Placement rather than exclusion matters because level 0 is not a
// neutral value downstream: chunk/section.go reads it as "not a
// heading" and pops the entire heading stack, and the emitter omits
// heading_level entirely. ranked must be sorted descending.
func levelForSize(sizeToLevel map[float64]int, ranked []float64, sz float64) int {
	if lvl, ok := sizeToLevel[sz]; ok {
		return lvl
	}
	for _, r := range ranked {
		if r <= sz {
			return sizeToLevel[r]
		}
	}
	return sizeToLevel[ranked[len(ranked)-1]]
}

// documentStylePerSize merges the per-page styleAtSize maps. A size is
// bold-bearing (or italic-bearing) for the document when any page
// carries that flavor at that size, so the bold>italic bump behaves
// the same way it did when one page was the whole world.
func documentStylePerSize(perPage [][]layout.Block) map[float64]styleAtSize {
	out := make(map[float64]styleAtSize, 8)
	for _, page := range perPage {
		for sz, s := range stylePerSize(page) {
			merged := out[sz]
			merged.hasBold = merged.hasBold || s.hasBold
			merged.hasItalic = merged.hasItalic || s.hasItalic
			out[sz] = merged
		}
	}
	return out
}

// distinctHeadingSizesDesc returns the distinct max-run sizes across
// heading blocks, sorted largest-first. With excludeChrome set, blocks
// carrying a page-repeat stamp are left out: page furniture is not
// document hierarchy, and a running header repeated on every page would
// otherwise occupy a level and compress the real ones below it.
func distinctHeadingSizesDesc(blocks []layout.Block, excludeChrome bool) []float64 {
	sizes := make(map[float64]struct{}, 4)
	for i := range blocks {
		if blocks[i].Kind != layout.BlockHeading {
			continue
		}
		if excludeChrome && carriesChromeStamp(blocks[i]) {
			continue
		}
		if sz := blockMaxRunSize(blocks[i]); sz > 0 {
			sizes[sz] = struct{}{}
		}
	}
	if len(sizes) == 0 {
		return nil
	}
	out := make([]float64, 0, len(sizes))
	for sz := range sizes {
		out = append(out, sz)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// styleAtSize tracks which boldness flavors exist at a single size.
type styleAtSize struct {
	hasBold   bool
	hasItalic bool // italic-only (not also bold)
}

// stylePerSize collects a per-size styleAtSize across heading blocks.
func stylePerSize(blocks []layout.Block) map[float64]styleAtSize {
	out := make(map[float64]styleAtSize, 4)
	for i := range blocks {
		if blocks[i].Kind != layout.BlockHeading {
			continue
		}
		sz := blockMaxRunSize(blocks[i])
		if sz == 0 {
			continue
		}
		s := out[sz]
		if blockAllBold(blocks[i]) {
			s.hasBold = true
		} else if blockAllItalic(blocks[i]) {
			s.hasItalic = true
		}
		out[sz] = s
	}
	return out
}

// applyHeadingLevels writes HeadingLevel into every heading block in
// blocks, preserving any non-zero level set upstream (StructRole). The
// bold>italic bump applies when both styles co-exist at the same size.
func applyHeadingLevels(blocks []layout.Block, sizeToLevel map[float64]int, ranked []float64, style map[float64]styleAtSize) {
	for i := range blocks {
		if blocks[i].Kind != layout.BlockHeading {
			continue
		}
		// Preserve HeadingLevel set by an upstream signal (StructRole
		// "H1".."H6"). The structure-tree tag is authoritative; do
		// not overwrite with the size-based heuristic.
		if blocks[i].HeadingLevel != 0 {
			continue
		}
		sz := blockMaxRunSize(blocks[i])
		if sz == 0 {
			continue
		}
		level := levelForSize(sizeToLevel, ranked, sz)
		s := style[sz]
		if s.hasBold && s.hasItalic && blockAllItalic(blocks[i]) && !blockAllBold(blocks[i]) {
			level = min(level+1, 6)
		}
		blocks[i].HeadingLevel = level
	}
}
