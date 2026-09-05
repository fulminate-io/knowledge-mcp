package chunk

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// reconstruction_test.go — the text layer put back together: a word
// broken at a line end rejoins as one word, and a record spanning a
// page break keeps what both halves knew.
//
// Both tests drive the REAL path — layout.Cluster for the line grouping
// and dehyphenation, then classify, then the chunk package's own
// normalization — rather than calling the join helper on hand-built
// Lines. The defect these cover was a disagreement BETWEEN two stages
// (layout stripped the hyphen, normalize then inserted a space where it
// had been), and a test that exercises either stage alone cannot see it.

// Hyphen code points, spelled numerically. U+00AD is invisible in an
// editor and U+2010 is indistinguishable from U+002D at most font
// sizes, so a literal character here would make the fixture unreadable
// and the file impossible to grep. hyph() renders one as a string.
const (
	cpHyphenMinus       = rune(0x002D)
	cpHyphen            = rune(0x2010)
	cpNonBreakingHyphen = rune(0x2011)
	cpSoftHyphen        = rune(0x00AD)
)

func hyph(cp rune) string { return string(cp) }

// reconstructionLeftMargin is the single left margin every fixture line
// in this file starts at. Lines sharing an X-start is what makes the
// clusterer group them into one block, which is the precondition for
// observing a line JOIN at all.
const (
	reconstructionLeftMargin = 72.0
	// reconstructionLineWidth was a per-call parameter until unparam observed
	// that every call site passed the same 180. It is a fixture constant, not a
	// dimension any of these cases varies.
	reconstructionLineWidth = 180.0
)

// mkLineRun builds a text run on baseline y at the shared fixture width, 12pt
// Helvetica at the shared left margin, with glyph weight equal to the
// byte length so body calibration has something to weigh.
func mkLineRun(y float64, txt string) text.TextRun {
	g := make([]uint16, len(txt))
	for i := range g {
		g[i] = uint16(txt[i])
	}
	return text.TextRun{
		Text: txt, Glyphs: g,
		X: reconstructionLeftMargin, Y: y, Width: reconstructionLineWidth, Height: 12, Size: 12,
		FontKey: "F1", FontName: "Helvetica",
	}
}

var reconstructionMediaBox = layout.Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}

// clusterAndNormalize runs the production path over one page of runs
// and returns the normalized text of every resulting block.
func clusterAndNormalize(t *testing.T, runs []text.TextRun) []string {
	t.Helper()
	blocks, err := layout.Cluster(runs, layout.PageInfo{MediaBox: reconstructionMediaBox})
	if err != nil {
		t.Fatalf("layout.Cluster: %v", err)
	}
	perPage := [][]layout.Block{blocks}
	dc := classify.CalibrateDocument(perPage)
	perPage[0] = classify.ClassifyPage(perPage[0], classify.DefaultClassifyParams, dc)
	classify.AssignHeadingLevelsDocument(perPage)

	out := make([]string, 0, len(perPage[0]))
	for _, b := range perPage[0] {
		out = append(out, normalizeBlockText(b))
	}
	return out
}

// TestDehyphenation_JoinsAcrossTheHyphenFamilyWithoutASpace drives a
// word broken at a line end by each of the four hyphen code points a
// typesetter can use, and asserts it emerges as ONE word with no space
// where the hyphen was.
//
// Both halves of the assertion matter. Before this work U+002D produced
// "sequen tially" — the hyphen stripped AND a space inserted, which is
// strictly worse than leaving the hyphen alone — while U+2010, U+2011
// and U+00AD were not recognized at all and kept the hyphen mid-word.
func TestDehyphenation_JoinsAcrossTheHyphenFamilyWithoutASpace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cp   rune
	}{
		{"hyphen_minus_U002D", cpHyphenMinus},
		{"hyphen_U2010", cpHyphen},
		{"nonbreaking_hyphen_U2011", cpNonBreakingHyphen},
		{"soft_hyphen_U00AD", cpSoftHyphen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Three baselines at the same left margin cluster into one
			// paragraph block; the word breaks between lines two and
			// three.
			runs := []text.TextRun{
				mkLineRun(700, "the records are written"),
				mkLineRun(686, "to the log sequen"+hyph(tc.cp)),
				mkLineRun(672, "tially by the broker"),
			}
			joined := strings.Join(clusterAndNormalize(t, runs), " ")

			if !strings.Contains(joined, "sequentially") {
				t.Errorf("normalized text %q does not contain the rejoined word %q", joined, "sequentially")
			}
			if strings.Contains(joined, "sequen tially") {
				t.Errorf("normalized text %q inserted a space where the hyphen was removed", joined)
			}
			if strings.Contains(joined, "sequen"+hyph(tc.cp)) {
				t.Errorf("normalized text %q still carries the line-break hyphen mid-word", joined)
			}
		})
	}

	// DISCRIMINATING CONTROL, same run: a line ending in a hyphen whose
	// continuation is NOT a lowercase Latin letter is a compound word,
	// not a broken one. Layout deliberately keeps its hyphen — and the
	// join must still not insert a space after it.
	t.Run("retained_compound_hyphen_gains_no_space", func(t *testing.T) {
		t.Parallel()
		h := hyph(cpHyphen)
		runs := []text.TextRun{
			mkLineRun(700, "a book about Event"+h),
			mkLineRun(686, "Driven Systems and logs"),
			mkLineRun(672, "written for practitioners"),
		}
		joined := strings.Join(clusterAndNormalize(t, runs), " ")
		if !strings.Contains(joined, "Event"+h+"Driven") {
			t.Errorf("normalized text %q lost the compound hyphen; want Event<U+2010>Driven intact", joined)
		}
		if strings.Contains(joined, "Event"+h+" Driven") {
			t.Errorf("normalized text %q inserted a space after a RETAINED hyphen", joined)
		}
	})
}

// TestCrossPageMerge_CarriesBothHalvesMetadataAndThePageSpan asserts a
// paragraph continuing across a page break emerges carrying metadata
// keys from BOTH halves plus a page_span of 2.
//
// Before this, mergeInto appended Lines and rewrote PageRange but never
// touched Metadata, so every key the continuing half carried was
// dropped on the floor without a word.
func TestCrossPageMerge_CarriesBothHalvesMetadataAndThePageSpan(t *testing.T) {
	t.Parallel()

	// A tail that does not end in terminator punctuation, and a head
	// starting lowercase in the same font at the same X — the three
	// signals mergeAcrossPages requires.
	tail := mkBlock(layout.BlockParagraph, mkRun(12, "Helvetica", "the broker writes each record"))
	tail.PageIndex = 0
	tail.Metadata = map[string]string{"tail_only": "TAIL", "shared": "FROM_TAIL"}

	head := mkBlock(layout.BlockParagraph, mkRun(12, "Helvetica", "sequentially to the log"))
	head.PageIndex = 1
	head.Metadata = map[string]string{"head_only": "HEAD", "shared": "FROM_HEAD"}

	merged := mergeAcrossPages([][]layout.Block{{tail}, {head}})
	if len(merged) != 1 {
		t.Fatalf("mergeAcrossPages produced %d records, want 1 - the two halves did not merge, so this test cannot observe the merge", len(merged))
	}
	got := merged[0]

	if got.PageRange != [2]int{0, 1} {
		t.Errorf("merged PageRange = %v, want [0 1]", got.PageRange)
	}
	if v, ok := got.Metadata["head_only"]; !ok || v != "HEAD" {
		t.Errorf("merged record lost metadata key %q; got %v", "head_only", got.Metadata)
	}
	if v, ok := got.Metadata["tail_only"]; !ok || v != "TAIL" {
		t.Errorf("merged record lost metadata key %q; got %v", "tail_only", got.Metadata)
	}
	// The tail's value wins a collision: its geometry describes the
	// block the reader meets first.
	if got.Metadata["shared"] != "FROM_TAIL" {
		t.Errorf("colliding key %q = %q, want the TAIL's value %q", "shared", got.Metadata["shared"], "FROM_TAIL")
	}
	if got.Metadata[MetaKeyPageSpan] != "2" {
		t.Errorf("merged record %s = %q, want %q", MetaKeyPageSpan, got.Metadata[MetaKeyPageSpan], "2")
	}
	t.Logf("merged record metadata = %v pageRange=%v", got.Metadata, got.PageRange)

	// DISCRIMINATING CONTROL, same run: the source blocks' own maps must
	// be untouched. mergedBlock embeds the Block by value, which shares
	// the map header, so a careless in-place merge would reach back into
	// the caller's page slices.
	if _, leaked := tail.Metadata["head_only"]; leaked {
		t.Errorf("the merge wrote through into the source tail block's map: %v", tail.Metadata)
	}
	if _, leaked := head.Metadata[MetaKeyPageSpan]; leaked {
		t.Errorf("the merge wrote through into the source head block's map: %v", head.Metadata)
	}
}
