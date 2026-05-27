package text

import (
	"math"
	"os"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/font"
	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// TestCharBounds_PdftotextCrossValidation cross-validates per-glyph
// CharBounds against a pre-baked poppler `pdftotext -bbox -layout`
// reference. The reference is committed to testdata so CI does NOT need
// poppler installed; this test never shells out at test time. Pattern
// mirror: collector/pdf/font/poppler_compat_test.go (T3 precedent).
//
// Comparison: poppler emits per-WORD bounds in a top-down y frame; our
// CharBounds are per-glyph in PDF user-space (+y up). For each
// reference word we union the CharBounds of the spatially-nearest
// matching glyph-run, flip y via pageHeight - yMax / yMin, and compare
// per-corner. Word-level match ratio must be ≥ 95%.
//
//	Regen: pdftotext -bbox -layout collector/pdf/testdata/corpus/rfc-7234-caching/source.pdf \
//		         collector/pdf/testdata/corpus/rfc-7234-caching/poppler-references/source.pdftotext-bbox.txt
//
// Corpus licensing: source.pdf is RFC 7234 (Fielding/Nottingham/Reschke,
// June 2014). IETF TLP; see poppler_compat_test.go for full attribution.
func TestCharBounds_PdftotextCrossValidation(t *testing.T) {
	// Not t.Parallel(): full doc-scope decode of a 17-page PDF; keep
	// serialized to avoid noise from concurrent stress.
	const (
		fixturePDF = "../testdata/corpus/rfc-7234-caching/source.pdf"
		fixtureRef = "../testdata/corpus/rfc-7234-caching/poppler-references/source.pdftotext-bbox.txt"
		// Axis-split tolerance. X matches at p50=0 across the corpus
		// (we and poppler both use cumulative-advance left edges); ±2pt
		// covers poppler's rasterization rounding. Y carries a
		// systematic font-metric divergence — we emit Y..Y+fontSize (the
		// geometrically correct text-space rectangle), poppler tightens
		// to cap-height (the inked extent). p50 Y offset is ~3.7pt
		// across RFC 7234; ±5pt is the smallest tolerance that holds.
		xTolerance = 2.0
		yTolerance = 5.0
		minRatio   = 0.95
	)

	refBytes, err := os.ReadFile(fixtureRef)
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}
	if len(refBytes) == 0 {
		t.Fatalf("reference fixture %s is empty", fixtureRef)
	}
	refDoc, err := parsePdftotextBBox(refBytes)
	if err != nil {
		t.Fatalf("parse reference: %v", err)
	}
	if len(refDoc.Pages) == 0 {
		t.Fatalf("reference has 0 pages")
	}

	ctx, err := internalpdf.LoadFile(fixturePDF)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	defer func() { _ = ctx.Close() }()

	if got, want := ctx.PageCount(), len(refDoc.Pages); got != want {
		t.Fatalf("page count mismatch: pdf has %d, reference has %d", got, want)
	}

	var totalRefWords, totalMatched int
	for pi := 0; pi < ctx.PageCount(); pi++ {
		page, err := ctx.Page(pi)
		if err != nil {
			t.Fatalf("Page(%d): %v", pi, err)
		}
		mb := page.MediaBox()
		pageHeight := mb.Y1 - mb.Y0

		runs, err := ExtractRuns(page)
		if err != nil {
			t.Fatalf("ExtractRuns(%d): %v", pi, err)
		}
		wrapped := make([]font.Run, len(runs))
		for j := range runs {
			wrapped[j] = charboundsRunAdapter{r: &runs[j]}
		}
		if err := font.Decode(wrapped, page); err != nil {
			t.Fatalf("font.Decode(%d): %v", pi, err)
		}

		// Build per-page word→bounds list from our runs. Each run's
		// Text is split on whitespace; glyph indices align word-by-word
		// via cumulative rune walk. Most RFC text is single-word runs
		// or contiguous space-separated words within a single run.
		ourWords := flattenRunsToWords(runs)

		refWords := refDoc.Pages[pi].Words
		totalRefWords += len(refWords)
		matched := 0
		for _, rw := range refWords {
			// Flip poppler y: y_pdf = pageHeight - y_poppler
			refY0 := pageHeight - rw.YMax
			refY1 := pageHeight - rw.YMin
			ow, ok := findWord(ourWords, rw.Text, rw.XMin, refY0)
			if !ok {
				continue
			}
			if math.Abs(ow.bounds.X0-rw.XMin) <= xTolerance &&
				math.Abs(ow.bounds.X1-rw.XMax) <= xTolerance &&
				math.Abs(ow.bounds.Y0-refY0) <= yTolerance &&
				math.Abs(ow.bounds.Y1-refY1) <= yTolerance {
				matched++
			}
		}
		totalMatched += matched
	}

	if totalRefWords == 0 {
		t.Fatalf("zero reference words extracted from %s", fixtureRef)
	}
	ratio := float64(totalMatched) / float64(totalRefWords)
	t.Logf("matched %d / %d words (ratio %.4f)", totalMatched, totalRefWords, ratio)
	if ratio < minRatio {
		t.Errorf("word-level bbox match ratio %.4f < %.2f (DoD floor)", ratio, minRatio)
	}
}

// charboundsRunAdapter is the local font.Run adapter for this test
// file. Mirrors charflagsRunAdapter (collector/pdf/text/charflags_test.go)
// — kept separate to avoid cross-test coupling.
type charboundsRunAdapter struct{ r *TextRun }

func (a charboundsRunAdapter) GlyphsCopy() []uint16 { return a.r.Glyphs }
func (a charboundsRunAdapter) FontKeyValue() string { return a.r.FontKey }
func (a charboundsRunAdapter) FontResourcesHint() internalpdf.FormResources {
	return a.r.FontResourcesHint()
}
func (a charboundsRunAdapter) SetText(s string) { a.r.Text = s }
func (a charboundsRunAdapter) SetCharFlags(f []uint8) {
	if len(f) == 0 {
		return
	}
	if len(a.r.CharFlags) == len(f) {
		for i, b := range f {
			a.r.CharFlags[i] |= b
		}
		return
	}
	a.r.CharFlags = f
}

// ourWord is a single space-delimited token plus the unioned bbox of
// the glyphs that produced it.
type ourWord struct {
	text   string
	bounds Rect
}

// flattenRunsToWords walks decoded TextRuns, splits each run's Text on
// whitespace, and unions the parallel CharBounds rectangles for the
// glyphs that contributed to each word. Glyph→rune alignment uses a
// simple rune-index walk: we assume one glyph per rune in the run's
// decoded text, which holds for the RFC 7234 corpus (Helvetica /
// Courier / Times Roman with /WinAnsiEncoding, no ligatures). Runs
// where len(CharBounds) != rune-length-of(Text) are skipped (they
// can't supply per-word bounds).
func flattenRunsToWords(runs []TextRun) []ourWord {
	out := make([]ourWord, 0, len(runs))
	for _, r := range runs {
		if r.Text == "" || len(r.CharBounds) == 0 {
			continue
		}
		runes := []rune(r.Text)
		if len(runes) != len(r.CharBounds) {
			continue
		}
		// Walk runes; each whitespace boundary closes the current
		// word. Track the rune index range that contributed to it.
		start := -1
		for i, rn := range runes {
			isSpace := isASCIISpace(rn)
			if !isSpace && start < 0 {
				start = i
			}
			if (isSpace || i == len(runes)-1) && start >= 0 {
				end := i
				if !isSpace {
					end = i + 1
				}
				word := string(runes[start:end])
				if word != "" {
					out = append(out, ourWord{
						text:   word,
						bounds: unionBounds(r.CharBounds[start:end]),
					})
				}
				start = -1
			}
		}
	}
	return out
}

// findWord returns the ourWord with exact text match whose bbox is
// spatially nearest the target reference bbox (refX, refY in PDF
// user-space). The RFC 7234 corpus has many duplicate words ("the",
// "a", "of", etc.); first-match would always pick the first
// occurrence and the bbox comparison would fail for the other ~12k
// duplicates. Spatial-nearest matching is the standard pattern used
// by pdftotext-vs-extractor cross-validators (see pdfminer.six's
// dumpdata.py and poppler-utils' qa scripts).
func findWord(words []ourWord, text string, refX, refY float64) (ourWord, bool) {
	bestIdx := -1
	bestDist := math.Inf(1)
	for i, w := range words {
		if w.text != text {
			continue
		}
		dx := w.bounds.X0 - refX
		dy := w.bounds.Y0 - refY
		d := dx*dx + dy*dy
		if d < bestDist {
			bestDist = d
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return ourWord{}, false
	}
	return words[bestIdx], true
}

// unionBounds returns the smallest rect containing every input rect.
// Empty input → zero rect (caller is responsible for not consuming
// zero-rect output as a valid bound).
func unionBounds(rs []Rect) Rect {
	if len(rs) == 0 {
		return Rect{}
	}
	out := rs[0]
	for _, r := range rs[1:] {
		if r.X0 < out.X0 {
			out.X0 = r.X0
		}
		if r.Y0 < out.Y0 {
			out.Y0 = r.Y0
		}
		if r.X1 > out.X1 {
			out.X1 = r.X1
		}
		if r.Y1 > out.Y1 {
			out.Y1 = r.Y1
		}
	}
	return out
}

func isASCIISpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}
