// body_calibration.go — per-document modal body-font detection.
//
// calibrateBody walks every TextRun in every Block.Lines[].Runs and
// returns the modal font size weighted by glyph count, plus the modal
// FontName at that size, plus whether the body cohort is dominantly
// bold. O(total-runs).
//
// Glyph weight (NOT rune count) is what was rendered; rune count would
// require a per-run UTF-8 walk. See plan open-question 1 (resolved).

package classify

import "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"

// calibrationResult carries per-document classifier reference values.
type calibrationResult struct {
	// BodySize is the modal font size in points. 0 when the document
	// has no text or calibrateBody could not determine a winner.
	BodySize float64

	// BodyFontName is the modal FontName among runs at BodySize. Empty
	// when BodySize is 0 or no run at BodySize carried a FontName.
	BodyFontName string

	// BodyIsBold is true when ≥ 50% of the modal-body-cohort glyphs
	// are bold. Body-bold documents (front-matter pages, marketing
	// PDFs typeset in a bold sans-serif) trip Rule #2 of
	// isHeadingCandidate without this gate.
	BodyIsBold bool
}

// calibrateBody returns the calibrationResult for blocks. Empty input
// or zero total glyph weight returns the zero calibrationResult.
func calibrateBody(blocks []layout.Block) calibrationResult {
	if len(blocks) == 0 {
		return calibrationResult{}
	}
	sizeWeight := sizeHistogram(blocks)
	if len(sizeWeight) == 0 {
		return calibrationResult{}
	}
	bodySize := pickModalSize(sizeWeight)
	if bodySize == 0 {
		return calibrationResult{}
	}
	fontWeight, boldGlyphs, totalGlyphs := bodyCohortStats(blocks, bodySize)
	return calibrationResult{
		BodySize:     bodySize,
		BodyFontName: pickModalFontName(fontWeight),
		// Integer-safe ≥ 50% bold-fraction test.
		BodyIsBold: totalGlyphs > 0 && boldGlyphs*2 >= totalGlyphs,
	}
}

// sizeHistogram builds a glyph-weighted size→weight map across every
// run in blocks. Runs with zero glyph weight are skipped.
func sizeHistogram(blocks []layout.Block) map[float64]int {
	out := make(map[float64]int, 8)
	for _, b := range blocks {
		for _, line := range b.Lines {
			for _, run := range line.Runs {
				if w := len(run.Glyphs); w > 0 {
					out[run.Size] += w
				}
			}
		}
	}
	return out
}

// pickModalSize returns the size with the largest accumulated weight.
// Tie-break: smaller size wins (body is usually the smaller contender;
// headings trend larger).
func pickModalSize(sizeWeight map[float64]int) float64 {
	var bodySize float64
	bestWeight := -1
	for sz, w := range sizeWeight {
		if w > bestWeight || (w == bestWeight && sz < bodySize) {
			bestWeight = w
			bodySize = sz
		}
	}
	if bestWeight <= 0 {
		return 0
	}
	return bodySize
}

// bodyCohortStats walks blocks once and returns the FontName→weight
// histogram, the bold-glyph count, and the total-glyph count for runs
// matching bodySize.
func bodyCohortStats(blocks []layout.Block, bodySize float64) (map[string]int, int, int) {
	fontWeight := make(map[string]int, 4)
	var boldGlyphs, totalGlyphs int
	for _, b := range blocks {
		for _, line := range b.Lines {
			for _, run := range line.Runs {
				if run.Size != bodySize {
					continue
				}
				w := len(run.Glyphs)
				if w == 0 {
					continue
				}
				totalGlyphs += w
				if run.Bold {
					boldGlyphs += w
				}
				if run.FontName != "" {
					fontWeight[run.FontName] += w
				}
			}
		}
	}
	return fontWeight, boldGlyphs, totalGlyphs
}

// pickModalFontName returns the most-weighted font name. Tie-break:
// lexically smaller name wins (deterministic for stability across
// runs).
func pickModalFontName(fontWeight map[string]int) string {
	var name string
	best := -1
	for n, w := range fontWeight {
		if w > best || (w == best && n < name) {
			best = w
			name = n
		}
	}
	return name
}
