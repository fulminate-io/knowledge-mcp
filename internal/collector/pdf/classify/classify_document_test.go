package classify

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// classify_document_test.go — the document-wide half of the
// classifier: one body calibration and one heading rank for the whole
// document rather than one of each per page.

// sized returns a plain (non-mono, non-bold, non-italic) run at size.
func sized(size float64, txt string) text.TextRun {
	return mkRun(size, "Helvetica", false, false, false, txt)
}

// boldSized returns a bold run at size. DefaultClassifyParams sets
// HeadingMinBoldOnly, so the size-only heading rule additionally
// requires at least one bold run.
func boldSized(size float64, txt string) text.TextRun {
	return mkRun(size, "Helvetica-Bold", false, true, false, txt)
}

// pageOf stacks one block per entry at 12pt intervals with a 2pt gap,
// stamping every block with pageIdx. Each entry is one line of runs.
func pageOf(pageIdx int, lines ...[]text.TextRun) []layout.Block {
	out := make([]layout.Block, 0, len(lines))
	y := 0.0
	for _, runs := range lines {
		b := mkBlock(y, y+10, runs...)
		b.PageIndex = pageIdx
		out = append(out, b)
		y += 12
	}
	return out
}

// classifyDocument runs the production document-wide sequence:
// calibrate once, classify every page against that calibration, then
// rank heading sizes across the whole document.
func classifyDocument(perPage [][]layout.Block) [][]layout.Block {
	dc := CalibrateDocument(perPage)
	for i := range perPage {
		perPage[i] = ClassifyPage(perPage[i], DefaultClassifyParams, dc)
	}
	AssignHeadingLevelsDocument(perPage)
	return perPage
}

// headingLevels returns the level of every heading block across pages,
// in page then block order.
func headingLevels(perPage [][]layout.Block) []int {
	var out []int
	for _, page := range perPage {
		for _, b := range page {
			if b.Kind == layout.BlockHeading {
				out = append(out, b.HeadingLevel)
			}
		}
	}
	return out
}

// twoPageTitleAndSubhead builds a 10pt-body document whose page 0
// carries a 24pt title and whose page 1 carries an 18pt subhead. Under
// a per-page rank each is the largest size on its own page and both
// rank 1; under a document-wide rank they are 1 and 2.
func twoPageTitleAndSubhead() [][]layout.Block {
	bodyLine := func(txt string) []text.TextRun { return []text.TextRun{sized(10, txt)} }
	page0 := pageOf(0,
		[]text.TextRun{boldSized(24, "The Document Title")},
		bodyLine("body text that establishes the document modal size at ten points"),
		bodyLine("more body text at ten points so the histogram winner is unambiguous"),
		bodyLine("still more ten point body text carrying the bulk of the glyph weight"),
	)
	page1 := pageOf(1,
		[]text.TextRun{boldSized(18, "A Later Subhead")},
		bodyLine("body text on the second page also at ten points for the same reason"),
		bodyLine("and another ten point line so page one cannot calibrate to anything else"),
		bodyLine("a third ten point line keeping the modal size stable across the document"),
	)
	return [][]layout.Block{page0, page1}
}

func TestHeadingLevel_IsADocumentRankNotAPerPageRank(t *testing.T) {
	perPage := classifyDocument(twoPageTitleAndSubhead())

	levels := headingLevels(perPage)
	if len(levels) != 2 {
		t.Fatalf("got %d headings, want 2 (a 24pt title on page 0 and an 18pt subhead on page 1); levels=%v", len(levels), levels)
	}
	if levels[0] != 1 || levels[1] != 2 {
		t.Errorf("24pt title on page 0 and 18pt subhead on page 1 got heading levels %v, want [1 2] - levels are ranked per page, not per document", levels)
	}
	for i, lv := range levels {
		if lv < 1 || lv > 6 {
			t.Errorf("heading %d landed at level %d, want 1..6", i, lv)
		}
	}
	t.Logf("document-wide heading levels = %v", levels)

	// DISCRIMINATING CONTROL, same run: ranking each page on its own —
	// which is what the per-page caller did — collapses both to level 1.
	// Without this the assertion above could pass for a reason unrelated
	// to the document-wide rank.
	perPageRanked := twoPageTitleAndSubhead()
	dc := CalibrateDocument(perPageRanked)
	for i := range perPageRanked {
		perPageRanked[i] = ClassifyPage(perPageRanked[i], DefaultClassifyParams, dc)
		AssignHeadingLevelsDocument([][]layout.Block{perPageRanked[i]})
	}
	control := headingLevels(perPageRanked)
	if len(control) != 2 || control[0] != 1 || control[1] != 1 {
		t.Fatalf("control: ranking each page alone gave %v, want [1 1] - the control does not reproduce the per-page defect, so the assertion above proves nothing", control)
	}
	t.Logf("per-page control heading levels = %v", control)
}

// allChromeHeadingPages builds four pages whose ONLY heading-classified
// blocks are a repeated banner carrying a page-repeat stamp. Phase 4
// excludes chrome from the ranking population, so this document ranks
// over an EMPTY population and the placement rule has no scale to place
// anything on.
func allChromeHeadingPages() [][]layout.Block {
	perPage := make([][]layout.Block, 0, 4)
	for page := range 4 {
		banner := mkBlock(700, 718, boldSized(18, "Quarterly Operations Report"))
		banner.PageIndex = page
		banner.Metadata = map[string]string{ChromeStampKey: "4"}

		body := mkBlock(600, 610, sized(10, "ten point body prose with nothing bold or large anywhere in it"))
		body.PageIndex = page

		perPage = append(perPage, []layout.Block{banner, body})
	}
	return perPage
}

func TestHeadingRank_AllChromeHeadingsStillLandOnAScale(t *testing.T) {
	t.Parallel()
	perPage := classifyDocument(allChromeHeadingPages())

	headings := 0
	for page, blocks := range perPage {
		for _, b := range blocks {
			if b.Kind != layout.BlockHeading {
				continue
			}
			headings++
			t.Logf("page %d chrome heading level = %d", page, b.HeadingLevel)
			if b.HeadingLevel < 1 || b.HeadingLevel > 6 {
				t.Errorf("chrome heading left level-less: HeadingLevel = %d, want 1..6", b.HeadingLevel)
			}
		}
	}
	if headings != 4 {
		t.Fatalf("fixture produced %d heading blocks, want 4 - it does not exercise the empty-population case", headings)
	}

	// DISCRIMINATING CONTROL, same run: without the chrome stamp the
	// same banners ARE the ranking population and rank normally. That is
	// what makes the assertion above a statement about the empty-
	// population fallback rather than about heading ranking in general.
	unstamped := allChromeHeadingPages()
	for _, page := range unstamped {
		delete(page[0].Metadata, ChromeStampKey)
	}
	unstamped = classifyDocument(unstamped)
	for page, blocks := range unstamped {
		for _, b := range blocks {
			if b.Kind == layout.BlockHeading && b.HeadingLevel != 1 {
				t.Errorf("control: unstamped banner on page %d ranked %d, want 1", page, b.HeadingLevel)
			}
		}
	}
}

// eightPointPageWithNinePointBlock builds a document whose modal body
// size is 10pt but whose SECOND page is typeset at 8pt and carries one
// 9pt block with a wide gap above it. Against the page's own 8pt body
// that block clears rule #3 (>= 1.05x body, gap above > page average)
// and reads as a heading; against the document's 10pt body it does not.
func eightPointPageWithNinePointBlock() [][]layout.Block {
	bodyLine := func(txt string) []text.TextRun { return []text.TextRun{sized(10, txt)} }
	page0 := pageOf(0,
		bodyLine("ten point body text carrying the document modal glyph weight by a wide margin"),
		bodyLine("a second ten point line so the document body size is unambiguously ten points"),
		bodyLine("a third ten point line of body text for the same calibration reason as above"),
		bodyLine("a fourth ten point line so the eight point page cannot outweigh this one"),
		bodyLine("a fifth ten point line of ordinary body prose to settle the size histogram"),
	)

	small := func(txt string) []text.TextRun { return []text.TextRun{sized(8, txt)} }
	page1 := pageOf(1,
		small("eight point text on this page"),
		small("more eight point text here"),
		small("still more eight point text"),
		small("a fourth eight point line"),
	)
	// The 9pt block sits far below the last 8pt block, so its gap above
	// is much larger than the page's average inter-block gap.
	nine := mkBlock(90, 100, sized(9, "Nine Point Line"))
	nine.PageIndex = 1
	page1 = append(page1, nine)
	return [][]layout.Block{page0, page1}
}

func TestBodyCalibration_IsDocumentWide(t *testing.T) {
	perPage := eightPointPageWithNinePointBlock()
	dc := CalibrateDocument(perPage)
	if dc.BodySize != 10 {
		t.Fatalf("document calibration BodySize = %v, want 10 - the fixture's own premise is wrong", dc.BodySize)
	}
	perPage = classifyDocument(perPage)

	nine := perPage[1][len(perPage[1])-1]
	if nine.Kind == layout.BlockHeading {
		t.Errorf("the 9pt block on an all-8pt page classified as a heading; the page recalibrated to its own body size instead of the document's")
	}
	t.Logf("9pt block on the 8pt page classified as %v against document body %vpt", nine.Kind, dc.BodySize)

	// DISCRIMINATING CONTROL, same run: calibrating that page against
	// ITSELF — the per-page behaviour this step retires — does classify
	// the same block as a heading. Without a control that reads
	// differently, "not a heading" is indistinguishable from a fixture
	// that could never produce one.
	controlPages := eightPointPageWithNinePointBlock()
	pageAlone := [][]layout.Block{controlPages[1]}
	perPageCal := CalibrateDocument(pageAlone)
	if perPageCal.BodySize != 8 {
		t.Fatalf("control calibration BodySize = %v, want 8", perPageCal.BodySize)
	}
	controlBlocks := ClassifyPage(controlPages[1], DefaultClassifyParams, perPageCal)
	controlNine := controlBlocks[len(controlBlocks)-1]
	if controlNine.Kind != layout.BlockHeading {
		t.Fatalf("control: under the page's own 8pt calibration the 9pt block classified as %v, want heading - the control does not reproduce the per-page defect, so the assertion above proves nothing", controlNine.Kind)
	}
	t.Logf("control: under a per-page 8pt calibration the same block classifies as %v", controlNine.Kind)
}
