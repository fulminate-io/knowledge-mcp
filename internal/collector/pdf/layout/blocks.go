// blocks.go: Stage 2 of the T4 layout clusterer — group Lines into
// Blocks.
//
// Splits the line-clustering tolerance from the paragraph-break
// threshold (pdfminer.six conflates both under a single line_margin).
// Splitting them materially improves accuracy on real-world PDFs.
//
// Rule 2.0 — single-line short-circuit: when len(lines) < 2, emit
// one Block directly (no medianGap calc). Per Phase-4 step
// criterion 5b1b4d04.
// Rule 2.1 — X-start similarity check: |line.BBox.X0 -
// block.firstLine.BBox.X0| <= CharMargin × avgCharWidthAcrossLines.
// Rule 2.2 — mismatched X-start break: fail Rule 2.1 → new block.
// Rule 2.3 — paragraph-break threshold: (lineCenters[i] -
// lineCenters[i-1]) > medianGap × ParagraphGapRatio (default 1.6,
// empirically tuned for the median-based denominator).
// Rule 2.4 — block reading order: sort emitted Blocks by Y0
// ascending, X0 ascending tiebreak.
//
// medianGap is computed ONCE at function entry (NOT per-pair).
// Per-page work is serial; page-level fan-out is upstream.

package layout

import (
	"sort"
	"strings"
)

// groupLinesToBlocks is the Stage-2 entry point. lines MUST be the
// output of Stage 1 (already rotation-normalized + Y-flipped to
// top-down). pageIndex is stamped onto every emitted Block.
func groupLinesToBlocks(lines []Line, pageIndex int, lp LayoutParams) []Block {
	if len(lines) == 0 {
		return nil
	}

	// Sort lines top-down by Y0; tie-break by X0 (Rule 2.4 entry
	// ordering). Stable sort to preserve input order for ties.
	sorted := make([]Line, len(lines))
	copy(sorted, lines)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].BBox.Y0 != sorted[j].BBox.Y0 {
			return sorted[i].BBox.Y0 < sorted[j].BBox.Y0
		}
		return sorted[i].BBox.X0 < sorted[j].BBox.X0
	})

	// Rule 2.0 single-line short-circuit. When there is only one
	// line on the page, emit a single Block directly without
	// computing medianGap. Same-as-criterion 5b1b4d04 (Phase 4 step):
	// avoids a degenerate medianGap sort over an empty gap slice.
	if len(sorted) < 2 {
		return []Block{newBlock(sorted[0], pageIndex)}
	}

	// Compute lineCenters once (used both for medianGap and per-pair
	// gap break check below).
	lineCenters := make([]float64, len(sorted))
	for i, l := range sorted {
		lineCenters[i] = (l.BBox.Y0 + l.BBox.Y1) / 2.0
	}

	// Rule 2.0/2.3 — medianGap from line-to-line center-Y deltas.
	gaps := make([]float64, 0, len(sorted)-1)
	for i := 1; i < len(sorted); i++ {
		gaps = append(gaps, lineCenters[i]-lineCenters[i-1])
	}
	medianGap := medianFloat64s(gaps)
	paragraphGap := medianGap * lp.ParagraphGapRatio

	// Walk lines and assemble blocks.
	blocks := make([]Block, 0, 4)
	current := newBlock(sorted[0], pageIndex)
	state := newBlockExtensionState(sorted[0])
	for i := 1; i < len(sorted); i++ {
		line := sorted[i]
		if canExtendBlock(line, lineCenters[i], lineCenters[i-1], current, state, paragraphGap, lp) {
			extendBlock(&current, line)
			state.update(line)
			continue
		}
		blocks = append(blocks, current)
		current = newBlock(line, pageIndex)
		state = newBlockExtensionState(line)
	}
	blocks = append(blocks, current)

	// Rule 2.4 — sort emitted blocks by Y0 ascending, X0 ascending
	// tiebreak. Already in input order from the per-line greedy
	// walk, but stable-sort defensively.
	sort.SliceStable(blocks, func(i, j int) bool {
		if blocks[i].BBox.Y0 != blocks[j].BBox.Y0 {
			return blocks[i].BBox.Y0 < blocks[j].BBox.Y0
		}
		return blocks[i].BBox.X0 < blocks[j].BBox.X0
	})
	return blocks
}

// newBlock constructs a single-Line Block stamped with pageIndex.
// Block.Kind defaults to BlockUnknown — T7 classifies later.
func newBlock(line Line, pageIndex int) Block {
	return Block{
		Kind:      BlockUnknown,
		BBox:      line.BBox,
		Lines:     []Line{line},
		PageIndex: pageIndex,
	}
}

// blockExtensionState holds the rolling block-level scalars consulted
// by canExtendBlock — maxRunSize, allBold, average per-glyph width.
// Maintained incrementally as lines are added to the current block,
// so canExtendBlock no longer rescans every line on each call. Without
// this cache the per-line scans were O(N) on each candidate-line check
// and O(N²) per block.
type blockExtensionState struct {
	maxRunSize    float64
	allBold       bool
	hasBoldSeen   bool
	charWidthSum  float64
	charWidthRuns int
}

// newBlockExtensionState seeds the rolling state from a block's first
// line.
func newBlockExtensionState(line Line) blockExtensionState {
	s := blockExtensionState{}
	s.update(line)
	return s
}

// update folds a newly-extended line into the rolling block state.
// Mirrors the per-line scans canExtendBlock used to do from scratch.
func (s *blockExtensionState) update(line Line) {
	if sz := lineMaxRunSize(line); sz > s.maxRunSize {
		s.maxRunSize = sz
	}
	lineBold, lineAny := lineBoldState(line)
	if lineAny {
		if !s.hasBoldSeen {
			s.allBold = lineBold
			s.hasBoldSeen = true
		} else if !lineBold {
			s.allBold = false
		}
	}
	for _, r := range line.Runs {
		if len(r.Glyphs) == 0 || r.Width <= 0 {
			continue
		}
		s.charWidthSum += r.Width / float64(len(r.Glyphs))
		s.charWidthRuns++
	}
}

// avgCharWidth returns the cached running average per-glyph width.
func (s *blockExtensionState) avgCharWidth() float64 {
	if s.charWidthRuns == 0 {
		return 0
	}
	return s.charWidthSum / float64(s.charWidthRuns)
}

// lineBoldState reports (allBold, anyNonWhitespace) — whether every
// non-whitespace run in line has Bold set, plus whether the line had
// any non-whitespace content at all (so update() can distinguish
// "blank line, ignore" from "non-bold content, flip allBold to false").
func lineBoldState(line Line) (allBold bool, any bool) {
	allBold = true
	for _, r := range line.Runs {
		if strings.TrimSpace(r.Text) == "" {
			continue
		}
		any = true
		if !r.Bold {
			allBold = false
		}
	}
	return allBold, any
}

// canExtendBlock returns true when line N fits the current block:
// (a) X-start similarity within CharMargin × avg_char_width AND
// (b) center-Y gap from line N-1 within paragraphGap (Rule 2.1+2.3
// combined). Failure of either starts a new block.
//
// Bullet hang-indent: when the block's first line starts with a list
// marker ("•", "-", "*", etc.), wrap lines typically align with the
// bullet body (after the marker) rather than the marker column itself.
// The X-similarity check accepts the wrap line if it matches EITHER
// the first line's X-start OR the body-after-marker X.
func canExtendBlock(line Line, lineCenterN, lineCenterPrev float64, block Block, state blockExtensionState, paragraphGap float64, lp LayoutParams) bool {
	// Rule 2.3: vertical gap test.
	if lineCenterN-lineCenterPrev > paragraphGap {
		return false
	}
	// Rule 2.1: X-start similarity.
	if len(block.Lines) == 0 {
		return true
	}
	// Style discontinuity: headings sit just below the trailing line of
	// the previous paragraph with a small Y gap, so the vertical-gap
	// test alone can't tell them apart. Two heuristics catch the
	// boundary:
	//
	//   - font-size jump: new line max size > 1.10 × block max
	//   - bold-state flip: new line all-bold while block is not all-bold
	//     (or vice versa). Heading sub-sections at body size are
	//     differentiated from body prose by bold weight; this check
	//     captures that transition.
	if state.maxRunSize > 0 {
		if newSize := lineMaxRunSize(line); newSize > state.maxRunSize*1.10 {
			return false
		}
	}
	if newAllBold, any := lineBoldState(line); any && state.hasBoldSeen && newAllBold != state.allBold {
		return false
	}
	avg := state.avgCharWidth()
	if avg <= 0 {
		// Without a per-line denominator we cannot scale the
		// CharMargin × avg gate; fall back to the absolute X-start
		// tolerance of CharMargin points (treat CharMargin as the
		// raw point tolerance — pdfminer.six fallback).
		avg = 1.0
	}
	tol := lp.CharMargin * avg

	firstX := block.Lines[0].BBox.X0
	dx := absFloat(line.BBox.X0 - firstX)
	if dx <= tol {
		return true
	}

	// Hang-indent fallback: if the first line starts with a list
	// marker, the wrap line might align with the body-after-marker.
	if bodyX, ok := bulletBodyX(block.Lines[0]); ok {
		if absFloat(line.BBox.X0-bodyX) <= tol {
			return true
		}
	}
	return false
}

// absFloat returns the absolute value of x.
func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// bulletBodyX returns the X coordinate just after the list-marker
// run on line, when the first run's text starts with a list marker.
// The marker set tracks the chunker's defaultListMarkerPattern
// without coupling layout → classify (no import).
func bulletBodyX(line Line) (float64, bool) {
	if len(line.Runs) == 0 {
		return 0, false
	}
	first := line.Runs[0]
	t := first.Text
	if t == "" {
		return 0, false
	}
	// Trim leading whitespace before checking for the marker so
	// "  • foo" still qualifies.
	i := 0
	for i < len(t) && (t[i] == ' ' || t[i] == '\t') {
		i++
	}
	if i >= len(t) {
		return 0, false
	}
	if !isListMarkerByte(t[i]) {
		// Multi-byte marker (e.g., U+2022 "•"). Check for it.
		if !startsWithBulletRune(t[i:]) {
			return 0, false
		}
	}
	// If the marker fits within the first run, body X is just past
	// the marker. Otherwise fall back to the second run's X.
	if len(line.Runs) >= 2 {
		return line.Runs[1].X, true
	}
	if len(first.Glyphs) > 0 && first.Width > 0 {
		// One run with marker + body; estimate body X as first.X plus
		// marker width (~one char of advance).
		perGlyph := first.Width / float64(len(first.Glyphs))
		return first.X + perGlyph, true
	}
	return 0, false
}

func isListMarkerByte(c byte) bool {
	return c == '-' || c == '*' || c == '+'
}

// startsWithBulletRune reports whether s begins with a Unicode bullet
// character (U+2022 "•" / 0xE2 0x80 0xA2 in UTF-8) or a few common
// variants. Avoids unicode.Is and a regexp dependency for the fast
// hot path.
func startsWithBulletRune(s string) bool {
	if len(s) >= 3 && s[0] == 0xE2 && s[1] == 0x80 {
		switch s[2] {
		case 0xA2, 0x93, 0x94, 0xA3, 0xA4: // •  – — ‣ •
			return true
		}
	}
	return false
}

// lineMaxRunSize returns the largest run.Size across line.Runs. Zero
// when the line has no runs or all runs have zero size.
func lineMaxRunSize(line Line) float64 {
	var m float64
	for _, r := range line.Runs {
		if r.Size > m {
			m = r.Size
		}
	}
	return m
}

// extendBlock appends line to block and grows BBox to enclose it.
func extendBlock(b *Block, line Line) {
	b.Lines = append(b.Lines, line)
	b.BBox = bboxUnion(b.BBox, line.BBox)
}

// medianFloat64s returns the median of xs (sort + middle-index;
// average of middle pair on even length). Returns 0 on empty input.
// NEW helper — no in-tree analog under collector/pdf/*. Internal
// only; if another package needs medians later it gets promoted.
func medianFloat64s(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := make([]float64, len(xs))
	copy(sorted, xs)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2.0
}
