// classify_heading.go — heading detection and level assignment.
//
// Two responsibilities:
//   - isHeadingCandidate: per-block decision via three OR-joined rules
//     (large-font, all-bold gated by body-bold, short-block + vertical
//     gap).
//   - assignHeadingLevels: distinct-size descending sort → H1..H6 with
//     bold>italic precedence at the same size.
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

// assignHeadingLevels mutates blocks in place: for every block where
// Kind == BlockHeading, set HeadingLevel by sorting all heading-candidate
// font sizes descending and assigning level by index (1..6, capped at
// 6). When the document has only N distinct heading sizes (N < 6),
// levels are 1..N — no artificial expansion to 6.
//
// Same-size precedence: when an all-bold heading and an all-italic-only
// heading share the same maxRunSize, the italic block is bumped DOWN one
// level (cap 6) so bold ranks higher than italic in the conventional
// typographic hierarchy. Mixed bold+italic is treated as bold for this
// rule. Refine in v2 if real-corpus tests expose additional edge cases.
func assignHeadingLevels(blocks []layout.Block) {
	sorted := distinctHeadingSizesDesc(blocks)
	if len(sorted) == 0 {
		return
	}
	sizeToLevel := make(map[float64]int, len(sorted))
	for idx, sz := range sorted {
		sizeToLevel[sz] = min(idx+1, 6)
	}
	style := stylePerSize(blocks)
	applyHeadingLevels(blocks, sizeToLevel, style)
}

// distinctHeadingSizesDesc returns the distinct max-run sizes across
// heading blocks, sorted largest-first.
func distinctHeadingSizesDesc(blocks []layout.Block) []float64 {
	sizes := make(map[float64]struct{}, 4)
	for i := range blocks {
		if blocks[i].Kind != layout.BlockHeading {
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
func applyHeadingLevels(blocks []layout.Block, sizeToLevel map[float64]int, style map[float64]styleAtSize) {
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
		level := sizeToLevel[sz]
		s := style[sz]
		if s.hasBold && s.hasItalic && blockAllItalic(blocks[i]) && !blockAllBold(blocks[i]) {
			level = min(level+1, 6)
		}
		blocks[i].HeadingLevel = level
	}
}
