package classify

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// classify_split_heading_test.go — the layout grouper splits a
// generously-leaded two-line title into two blocks, and both halves
// then emit as their own section. This drives the rejoin AND the case
// it must refuse.

// headingBlock builds an 18pt bold heading-shaped block occupying the
// vertical band [y0, y1] on page 0.
func headingBlock(y0, y1 float64, txt string) layout.Block {
	run := mkRun(18, "Helvetica-Bold", false, true, false, txt)
	bbox := layout.Rect{X0: 72, Y0: y0, X1: 540, Y1: y1}
	return layout.Block{
		Lines: []layout.Line{{Runs: []text.TextRun{run}, BBox: bbox}},
		BBox:  bbox,
	}
}

// blockText concatenates every run of every line in b.
func blockText(b layout.Block) string {
	var sb strings.Builder
	for _, l := range b.Lines {
		for _, r := range l.Runs {
			sb.WriteString(r.Text)
		}
	}
	return sb.String()
}

// headingTexts returns the concatenated text of every heading block.
func headingTexts(blocks []layout.Block) []string {
	var out []string
	for _, b := range blocks {
		if b.Kind == layout.BlockHeading {
			out = append(out, blockText(b))
		}
	}
	return out
}

// tornTitlePage builds a page carrying, in order:
//
//	the two halves of ONE 18pt title on consecutive baselines, which
//	verticalGapBlocks measures at 42pt (2.33x the face size);
//	a SEPARATE 18pt heading in the same face far below, measured at
//	148pt (8.2x);
//	body text establishing a 10pt document body.
//
// The two measurements straddle the rejoin threshold of 2.6x, so the
// fixture distinguishes a correctly-bounded rejoin from both an absent
// one and an unbounded one.
func tornTitlePage() [][]layout.Block {
	blocks := []layout.Block{
		headingBlock(700, 718, "Consistency and Concurrency in Event-"),
		headingBlock(676, 694, "Driven Systems"),
		headingBlock(546, 564, "A Separate Later Heading"),
	}
	y := 500.0
	for _, line := range []string{
		"ten point body text establishing the document modal size for calibration",
		"a second line of ten point body text so the histogram winner is unambiguous",
		"a third line of ordinary ten point body prose beneath the headings above",
		"a fourth line of ten point body prose to settle the body size for the page",
	} {
		b := mkBlock(y, y+10, mkRun(10, "Helvetica", false, false, false, line))
		blocks = append(blocks, b)
		y -= 14
	}
	return [][]layout.Block{blocks}
}

func TestSplitHeading_RejoinsAdjacentHalvesOfOneTitle(t *testing.T) {
	t.Parallel()
	perPage := tornTitlePage()
	dc := CalibrateDocument(perPage)
	perPage[0] = ClassifyPage(perPage[0], DefaultClassifyParams, dc)
	AssignHeadingLevelsDocument(perPage)

	got := headingTexts(perPage[0])
	if len(got) != 2 {
		t.Fatalf("got %d headings %q, want 2 - the two halves of one title must rejoin while the separate heading below stays its own block", len(got), got)
	}
	want0 := "Consistency and Concurrency in Event-Driven Systems"
	if got[0] != want0 {
		t.Errorf("rejoined heading = %q, want %q", got[0], want0)
	}
	if got[1] != "A Separate Later Heading" {
		t.Errorf("second heading = %q, want the separate heading %q - it was swallowed into the title above", got[1], "A Separate Later Heading")
	}
	t.Logf("headings after rejoin = %q", got)
}

// hyphenTornPage builds a page whose 18pt title is torn across two
// blocks between firstHalf and secondHalf, with body text below it
// establishing a 10pt document body.
func hyphenTornPage(firstHalf, secondHalf string) [][]layout.Block {
	blocks := []layout.Block{
		headingBlock(700, 718, firstHalf),
		headingBlock(676, 694, secondHalf),
	}
	y := 500.0
	for _, line := range []string{
		"ten point body text establishing the document modal size for calibration",
		"a second line of ten point body text so the histogram winner is unambiguous",
		"a third line of ordinary ten point body prose beneath the heading above",
	} {
		b := mkBlock(y, y+10, mkRun(10, "Helvetica", false, false, false, line))
		blocks = append(blocks, b)
		y -= 14
	}
	return [][]layout.Block{blocks}
}

// theRejoinedHeading classifies the page and returns the single heading
// block the rejoin produced.
func theRejoinedHeading(t *testing.T, perPage [][]layout.Block) layout.Block {
	t.Helper()
	dc := CalibrateDocument(perPage)
	perPage[0] = ClassifyPage(perPage[0], DefaultClassifyParams, dc)
	AssignHeadingLevelsDocument(perPage)
	var found []layout.Block
	for _, b := range perPage[0] {
		if b.Kind == layout.BlockHeading {
			found = append(found, b)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d heading blocks, want 1 rejoined heading", len(found))
	}
	return found[0]
}

// lineText concatenates one line's runs.
func lineText(l layout.Line) string {
	var sb strings.Builder
	for _, r := range l.Runs {
		sb.WriteString(r.Text)
	}
	return sb.String()
}

// TestSplitHeading_RejoinRunsTheHyphenHeuristicOverTheNewBoundary pins
// the boundary the layout grouper never saw.
//
// layout.Cluster dehyphenates per BLOCK. The join between the two
// halves of a torn heading does not exist at that point, so a title
// broken at a hyphenated word kept its hyphen: measured before this
// fix, the first fixture below emitted "Consistency and Concur-rency in
// Streaming" with WasDehyphenated false on the joined line.
//
// The assertions are on the LINES rather than on concatenated text,
// because the space between two joined lines is inserted downstream by
// the chunk package's normalizer, not here. The end-to-end spelling is
// pinned there, in TestDehyphenation_JoinsAcrossTheHyphenFamilyWithoutASpace.
func TestSplitHeading_RejoinRunsTheHyphenHeuristicOverTheNewBoundary(t *testing.T) {
	t.Parallel()

	// A word broken at a line end, lowercase Latin continuation: the
	// heuristic strips the hyphen and flags the join.
	broken := theRejoinedHeading(t, hyphenTornPage("Consistency and Concur-", "rency in Streaming"))
	if len(broken.Lines) != 2 {
		t.Fatalf("rejoined heading has %d lines, want 2", len(broken.Lines))
	}
	if got, want := lineText(broken.Lines[0]), "Consistency and Concur"; got != want {
		t.Errorf("first half after rejoin = %q, want %q - the hyphen heuristic did not run over the boundary the rejoin created", got, want)
	}
	if !broken.Lines[0].WasDehyphenated {
		t.Error("WasDehyphenated is false on the joined line; the downstream normalizer will insert a space mid-word")
	}
	t.Logf("hyphenated word break: first half %q WasDehyphenated=%v", lineText(broken.Lines[0]), broken.Lines[0].WasDehyphenated)

	// CONTROL ONE: a COMPOUND word's own hyphen, uppercase
	// continuation. The heuristic must leave it alone.
	compound := theRejoinedHeading(t, hyphenTornPage("Consistency and Concurrency in Event-", "Driven Systems"))
	if got, want := lineText(compound.Lines[0]), "Consistency and Concurrency in Event-"; got != want {
		t.Errorf("compound half after rejoin = %q, want %q - a compound hyphen must survive", got, want)
	}
	if compound.Lines[0].WasDehyphenated {
		t.Error("WasDehyphenated is true on a compound hyphen; the hyphen was wrongly treated as a word break")
	}
	t.Logf("compound hyphen kept: %q WasDehyphenated=%v", lineText(compound.Lines[0]), compound.Lines[0].WasDehyphenated)

	// CONTROL TWO: no hyphen at the tear at all. Nothing is stripped and
	// nothing is flagged, so the normalizer joins the halves with a
	// space — which is what proves the fix did not simply start
	// concatenating every boundary.
	plain := theRejoinedHeading(t, hyphenTornPage("Beyond Messaging: An Overview of the", "Kafka Broker"))
	if got, want := lineText(plain.Lines[0]), "Beyond Messaging: An Overview of the"; got != want {
		t.Errorf("word-boundary half after rejoin = %q, want %q", got, want)
	}
	if plain.Lines[0].WasDehyphenated {
		t.Error("WasDehyphenated is true at a plain word boundary; the halves will be run together without a space")
	}
	t.Logf("word boundary untouched: %q WasDehyphenated=%v", lineText(plain.Lines[0]), plain.Lines[0].WasDehyphenated)
}
