// classify.go — public orchestration for the heading / list / code /
// paragraph classifier.
//
// Two entry points:
//   - Classify uses DefaultClassifyParams.
//   - ClassifyWithParams accepts caller-supplied overrides and
//     substitutes per-field defaults for any zero-valued knob.
//
// Mutates blocks in place and returns the same slice (zero-copy).

package classify

import (
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// pickClassifyNeighbor returns the nearest neighbor of blocks[i] in the
// direction step (-1 or +1) on the same page, skipping over blocks that
// look like running-header/footer chrome. Chrome detection at the
// chunk layer happens AFTER classify, so during classify the chrome
// blocks are still present and would otherwise be selected as the
// immediate prev/next.
func pickClassifyNeighbor(blocks []layout.Block, i, step int) *layout.Block {
	page := blocks[i].PageIndex
	j := i + step
	for j >= 0 && j < len(blocks) && blocks[j].PageIndex == page {
		if !looksLikePageChrome(blocks[j]) {
			return &blocks[j]
		}
		j += step
	}
	return nil
}

// looksLikePageChrome reports whether b matches a running-header /
// footer pattern: single line, contains " | " separator, with one
// pipe-side consisting only of digits or short roman-numeral letters
// (e.g. "380 | Chapter 10: Batch Processing", "Maintainability | 17",
// "Table of Contents | vii"). Used by pickClassifyNeighbor to skip
// chrome when picking prev/next for code-block detection.
func looksLikePageChrome(b layout.Block) bool {
	if len(b.Lines) != 1 {
		return false
	}
	var sb strings.Builder
	for _, r := range b.Lines[0].Runs {
		sb.WriteString(r.Text)
	}
	text := strings.TrimSpace(sb.String())
	before, after, ok := strings.Cut(text, " | ")
	if !ok {
		return false
	}
	left := strings.TrimSpace(before)
	right := strings.TrimSpace(after)
	return isDigitsOrShortRoman(left) || isDigitsOrShortRoman(right)
}

// isDigitsOrShortRoman returns true for strings containing only ASCII
// digits, or up to 8 lowercase Roman-numeral letters (i, v, x, l, c,
// d, m). Empty returns false. Mirrors the chrome-detector's pipe-edge
// token rule.
func isDigitsOrShortRoman(s string) bool {
	if s == "" || len(s) > 8 {
		return false
	}
	allDigits := true
	allRoman := true
	for _, r := range s {
		if r < '0' || r > '9' {
			allDigits = false
		}
		switch r {
		case 'i', 'v', 'x', 'l', 'c', 'd', 'm', 'I', 'V', 'X', 'L', 'C', 'D', 'M':
			// roman
		default:
			allRoman = false
		}
		if !allDigits && !allRoman {
			return false
		}
	}
	return allDigits || allRoman
}

// Classify is the no-params entry point. Equivalent to
// ClassifyWithParams(blocks, DefaultClassifyParams).
func Classify(blocks []layout.Block) []layout.Block {
	return ClassifyWithParams(blocks, DefaultClassifyParams)
}

// ClassifyWithParams runs the classifier with caller-supplied params.
// Substitutes per-field defaults for any zero-valued knob, then walks
// blocks once in order and dispatches to the per-rule helpers.
func ClassifyWithParams(blocks []layout.Block, cp ClassifyParams) []layout.Block {
	if len(blocks) == 0 {
		return blocks
	}
	cp = applyParamDefaults(cp)

	cal := calibrateBody(blocks)
	avgGap := avgBlockGap(blocks)

	for i := range blocks {
		// 4a. Skip already-classified blocks (T6 may have set Kind via
		// the structure tree before reaching us).
		if blocks[i].Kind != layout.BlockUnknown {
			continue
		}

		// 4b. StructRole shortcut for Unknown blocks. The structtree
		// tag is the strongest signal we have when present.
		//
		// Exception: real-world tagged PDFs (e.g. DDIA) routinely tag
		// monospace code blocks as <P> rather than as a code-flavored
		// element. When the block is overwhelmingly monospace, skip the
		// shortcut so the heuristic dispatch can recognize it as code.
		// H1-H6 / L / LI remain authoritative — only "P" is overridden.
		if blocks[i].StructRole == "P" && monospaceFraction(blocks[i]) >= cp.CodeMonospaceRatio {
			// fall through to heuristic
		} else if applyStructRole(&blocks[i], cp) {
			continue
		}

		// Heuristic dispatch. Heading is checked first so a heading at
		// body size with bold runs is not misclassified as a list item
		// (rare but observed in real-world PDFs).
		gapAbove := blockGapAbove(blocks, i)
		if cal.BodySize > 0 && isHeadingCandidate(blocks[i], cal, gapAbove, avgGap, cp) {
			blocks[i].Kind = layout.BlockHeading
			continue
		}

		// 4e. Code (multi-line OR sandwiched single-line + indent).
		// Look past chrome-pattern blocks (running headers / footers
		// shaped like "<digits> | <text>" or "<text> | <digits>")
		// when picking neighbors — chrome runs at the page edges and
		// would otherwise mask an adjacent monospace neighbor on the
		// same page.
		prev := pickClassifyNeighbor(blocks, i, -1)
		next := pickClassifyNeighbor(blocks, i, +1)
		if isCodeBlock(blocks[i], prev, next, cp) {
			blocks[i].Kind = layout.BlockCode
			continue
		}

		// 4f. List item.
		if matched, marker, idx := isListItem(blocks[i], cp.ListMarkerPattern); matched {
			blocks[i].Kind = layout.BlockListItem
			ensureMetadata(&blocks[i])
			blocks[i].Metadata["list_marker"] = marker
			if idx > 0 {
				blocks[i].Metadata["list_index"] = strconv.Itoa(idx)
			}
			continue
		}

		// 4g. Default — paragraph; surface inline-code presence as
		// metadata when at least one fully-monospace run is present.
		blocks[i].Kind = layout.BlockParagraph
		if hasMonoRun(blocks[i]) {
			ensureMetadata(&blocks[i])
			blocks[i].Metadata["has_inline_code"] = "true"
		}
	}

	// 5. Second pass — assign HeadingLevel by distinct-size rank.
	assignHeadingLevels(blocks)

	// 6. Third pass — stitch adjacent code blocks the layout grouper
	// fragmented (bold-flip, paragraph-gap, SELECT/WHERE-style heading
	// misclassification). Same-page only; cross-page stitching belongs
	// to the chunk continuity pass.
	return mergeAdjacentCodeBlocks(blocks)
}

// applyParamDefaults substitutes per-field defaults for any zero-valued
// knob. Caller-supplied values are preserved.
func applyParamDefaults(cp ClassifyParams) ClassifyParams {
	if cp.HeadingFontSizeRatio == 0 {
		cp.HeadingFontSizeRatio = DefaultClassifyParams.HeadingFontSizeRatio
	}
	if cp.CodeMonospaceRatio == 0 {
		cp.CodeMonospaceRatio = DefaultClassifyParams.CodeMonospaceRatio
	}
	if cp.ListMarkerPattern == nil {
		cp.ListMarkerPattern = DefaultClassifyParams.ListMarkerPattern
	}
	// HeadingMinBoldOnly is a bool; the zero value (false) is a valid
	// caller choice ("don't enforce"). Don't overwrite.
	return cp
}

// applyStructRole maps a StructRole tag onto BlockKind + HeadingLevel
// when the role is recognized. Returns true when a mapping was applied
// (caller skips the heuristic dispatch).
//
// Recognized roles per the plan:
//
//	"P"        → BlockParagraph
//	"H1".."H6" → BlockHeading + HeadingLevel from the trailing digit
//	"L", "LI"  → BlockListItem (parseListMarker for index when present)
//
// Anything else (including empty StructRole) returns false so the
// heuristic path runs.
func applyStructRole(b *layout.Block, cp ClassifyParams) bool {
	switch b.StructRole {
	case "":
		return false
	case "P":
		b.Kind = layout.BlockParagraph
		return true
	case "L", "LI":
		b.Kind = layout.BlockListItem
		if matched, marker, idx := isListItem(*b, cp.ListMarkerPattern); matched {
			ensureMetadata(b)
			b.Metadata["list_marker"] = marker
			if idx > 0 {
				b.Metadata["list_index"] = strconv.Itoa(idx)
			}
		}
		return true
	}
	if len(b.StructRole) == 2 && b.StructRole[0] == 'H' {
		level := int(b.StructRole[1] - '0')
		if level >= 1 && level <= 6 {
			b.Kind = layout.BlockHeading
			b.HeadingLevel = level
			return true
		}
	}
	return false
}

// avgBlockGap returns the mean inter-block Y-gap across consecutive
// same-page block pairs. Returns 0 when fewer than two same-page
// neighbors exist.
func avgBlockGap(blocks []layout.Block) float64 {
	var sum float64
	var count int
	for i := 1; i < len(blocks); i++ {
		if blocks[i].PageIndex != blocks[i-1].PageIndex {
			continue
		}
		gap := blocks[i].BBox.Y0 - blocks[i-1].BBox.Y1
		if gap < 0 {
			gap = -gap
		}
		sum += gap
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// blockGapAbove returns the Y-gap between block i and its same-page
// predecessor. Returns 0 when block i is the first block on its page
// (no predecessor → no "above" gap signal available).
func blockGapAbove(blocks []layout.Block, i int) float64 {
	if i == 0 {
		return 0
	}
	if blocks[i-1].PageIndex != blocks[i].PageIndex {
		return 0
	}
	gap := blocks[i].BBox.Y0 - blocks[i-1].BBox.Y1
	if gap < 0 {
		gap = -gap
	}
	return gap
}

// ensureMetadata lazily allocates b.Metadata. Avoids per-block map
// allocation when no key is ever written.
func ensureMetadata(b *layout.Block) {
	if b.Metadata == nil {
		b.Metadata = make(map[string]string, 2)
	}
}
