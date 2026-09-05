// classify_signals.go — the raw layout signals every classified block
// carries into node metadata.
//
// The classifier's job used to end at a verdict: this block is a
// heading, that one is code. Every input it weighed to reach the
// verdict — the block's font size against the document's body size,
// how much of it is bold, how far it sits below its predecessor — was
// computed and then discarded, so a downstream consumer that disagreed
// with the verdict had nothing to re-decide from and no choice but to
// accept a threshold baked into Go.
//
// StampRawSignals writes those inputs onto the block instead. They ride
// Block.Metadata → Chunk.Metadata → node metadata, which is the channel
// list_marker and has_inline_code already travel, so a recipe can
// filter on font_ratio_to_body or gap_above_pt and reach its own
// verdict. The verdict stays too; it is now one reading of the signals
// rather than the only reading anyone gets.

package classify

import (
	"strconv"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// Raw-signal metadata keys. Named as constants because the accuracy
// harness, the wire-byte gate and the recipe presets all have to agree
// with the emitter about the spelling.
const (
	SignalFontSizePt      = "font_size_pt"
	SignalBodyFontSizePt  = "body_font_size_pt"
	SignalFontRatioToBody = "font_ratio_to_body"
	SignalBoldFraction    = "bold_fraction"
	SignalItalicFraction  = "italic_fraction"
	SignalMonoFraction    = "monospace_fraction"
	SignalLineCount       = "line_count"
	SignalGapAbovePt      = "gap_above_pt"
	SignalPageAvgGapPt    = "page_avg_gap_pt"
)

// ChromeStampKey is the metadata key the chunk package's chrome
// detector writes onto every block whose text repeats across pages. It
// is declared HERE, in the lower package, because both packages have to
// agree on the spelling and the dependency points one way: chunk
// imports classify, never the reverse. chunk re-exports it under its
// own name.
//
// classify reads it to keep retained chrome out of two places it does
// not belong — the code-merge absorption and the document heading rank.
const ChromeStampKey = "page_repeat_count"

// carriesChromeStamp reports whether b was marked as repeating across
// pages by the chunk package's chrome detector.
func carriesChromeStamp(b layout.Block) bool {
	return b.Metadata[ChromeStampKey] != ""
}

// RawSignalKeys lists the nine always-on raw signals StampRawSignals
// writes onto every classified block. Chrome adds three further keys
// (chunk/chrome.go) that ride only the blocks they apply to.
var RawSignalKeys = []string{
	SignalFontSizePt,
	SignalBodyFontSizePt,
	SignalFontRatioToBody,
	SignalBoldFraction,
	SignalItalicFraction,
	SignalMonoFraction,
	SignalLineCount,
	SignalGapAbovePt,
	SignalPageAvgGapPt,
}

// StampRawSignals writes the nine raw layout signals onto every block
// in blocks. dc supplies the document-wide body reference and avgGap
// the page's own mean inter-block gap, so a consumer can compare a
// block against both its document and its page.
//
// Callers run this LAST, after the code-merge pass, so a block that
// absorbed its neighbors reports the geometry of what it actually
// became rather than of the fragment it started as. gap_above_pt is
// therefore measured against the merged predecessor.
func StampRawSignals(blocks []layout.Block, dc DocumentCalibration, avgGap float64) {
	bodySize := formatSignal(dc.BodySize)
	pageAvgGap := formatSignal(avgGap)
	for i := range blocks {
		size := blockMaxRunSize(blocks[i])
		var ratio float64
		if dc.BodySize > 0 {
			ratio = size / dc.BodySize
		}
		bold, italic, mono := styleFractions(blocks[i])

		ensureMetadata(&blocks[i])
		m := blocks[i].Metadata
		m[SignalFontSizePt] = formatSignal(size)
		m[SignalBodyFontSizePt] = bodySize
		m[SignalFontRatioToBody] = formatSignal(ratio)
		m[SignalBoldFraction] = formatSignal(bold)
		m[SignalItalicFraction] = formatSignal(italic)
		m[SignalMonoFraction] = formatSignal(mono)
		m[SignalLineCount] = strconv.Itoa(len(blocks[i].Lines))
		m[SignalGapAbovePt] = formatSignal(blockGapAbove(blocks, i))
		m[SignalPageAvgGapPt] = pageAvgGap
	}
}

// formatSignal renders a signal value as its shortest exact decimal
// representation. 'f' with precision -1 rather than 'g' or a fixed
// precision: every one of these keys rides every block-derived node, so
// the difference between "10" and "10.000000" is a wire-byte decision,
// and exponent notation would make a recipe's numeric comparison
// harder to write than it needs to be.
func formatSignal(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
