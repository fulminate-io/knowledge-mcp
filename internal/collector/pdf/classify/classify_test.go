package classify

import (
	"regexp"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// Test fixtures (mkRun / body / mkBlock / mkMultiLineBlock / repeat)
// live in fixtures_test.go to hold this file under the 300-line cap.

func TestClassifyParams_FieldAssignment(t *testing.T) {
	t.Parallel()
	p := ClassifyParams{
		HeadingFontSizeRatio: 1.2, HeadingMinBoldOnly: false,
		CodeMonospaceRatio: 0.8, ListMarkerPattern: regexp.MustCompile(`^\s*[-*]\s+`),
	}
	if p.HeadingFontSizeRatio != 1.2 || p.CodeMonospaceRatio != 0.8 {
		t.Errorf("field assignment failed: %+v", p)
	}
	if !p.ListMarkerPattern.MatchString("- hello") {
		t.Error("ListMarkerPattern should match '- hello'")
	}
	if DefaultClassifyParams.HeadingFontSizeRatio == 0 ||
		!DefaultClassifyParams.HeadingMinBoldOnly ||
		DefaultClassifyParams.CodeMonospaceRatio == 0 ||
		DefaultClassifyParams.ListMarkerPattern == nil {
		t.Errorf("DefaultClassifyParams not populated: %+v", DefaultClassifyParams)
	}
}

func TestCalibrateBody_Empty(t *testing.T) {
	t.Parallel()
	cal := calibrateBody(nil)
	if cal.BodySize != 0 || cal.BodyFontName != "" || cal.BodyIsBold {
		t.Errorf("expected zero result, got %+v", cal)
	}
}

func TestCalibrateBody_ModalSize(t *testing.T) {
	t.Parallel()
	bodyBlk := mkMultiLineBlock(700, [][]text.TextRun{repeat("0123456789", 10)})
	hdrRun := mkRun(18, "Helvetica", false, true, false, "twentyglyphchars0123")
	cal := calibrateBody([]layout.Block{bodyBlk, mkBlock(750, 768, hdrRun)})
	if cal.BodySize != 12 || cal.BodyFontName != "Helvetica" || cal.BodyIsBold {
		t.Errorf("got %+v, want BodySize=12 BodyFontName=Helvetica BodyIsBold=false", cal)
	}
}

func TestIsHeadingCandidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		cal              calibrationResult
		blk              layout.Block
		gapAbove, avgGap float64
		want             bool
	}{
		{
			name: "LargeFont",
			cal:  calibrationResult{BodySize: 12},
			blk:  mkBlock(750, 768, mkRun(18, "Helvetica", false, true, false, "Heading")),
			want: true,
		},
		{
			name: "AllBold",
			cal:  calibrationResult{BodySize: 12, BodyIsBold: false},
			blk:  mkBlock(700, 712, mkRun(12, "Helvetica-Bold", false, true, false, "AllBold")),
			want: true,
		},
		{
			name: "AllBoldBodyDoc",
			cal:  calibrationResult{BodySize: 12, BodyIsBold: true},
			blk:  mkBlock(700, 712, mkRun(12, "Helvetica-Bold", false, true, false, "AllBold")),
			want: false,
		},
		{
			name:     "ShortGap",
			cal:      calibrationResult{BodySize: 12},
			blk:      mkMultiLineBlock(600, [][]text.TextRun{{mkRun(13, "Helvetica", false, false, false, "Short")}, {mkRun(13, "Helvetica", false, false, false, "Short")}}),
			gapAbove: 50, avgGap: 10, want: true,
		},
		{
			name:     "BodyNegative",
			cal:      calibrationResult{BodySize: 12},
			blk:      mkMultiLineBlock(600, [][]text.TextRun{{body("ordinary text")}, {body("ordinary text")}, {body("ordinary text")}, {body("ordinary text")}, {body("ordinary text")}, {body("ordinary text")}, {body("ordinary text")}, {body("ordinary text")}, {body("ordinary text")}, {body("ordinary text")}}),
			gapAbove: 5, avgGap: 10, want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isHeadingCandidate(tc.blk, tc.cal, tc.gapAbove, tc.avgGap, DefaultClassifyParams)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAssignHeadingLevels(t *testing.T) {
	t.Parallel()
	mkH := func(size float64, bold, italic bool) layout.Block {
		font := "Helvetica"
		if bold {
			font = "Helvetica-Bold"
		} else if italic {
			font = "Helvetica-Italic"
		}
		b := mkBlock(700, 712, mkRun(size, font, false, bold, italic, "H"))
		b.Kind = layout.BlockHeading
		return b
	}
	t.Run("Three", func(t *testing.T) {
		t.Parallel()
		blocks := []layout.Block{mkH(18, true, false), mkH(14, true, false), mkH(12, true, false)}
		AssignHeadingLevelsDocument([][]layout.Block{blocks})
		for i, want := range []int{1, 2, 3} {
			if blocks[i].HeadingLevel != want {
				t.Errorf("blocks[%d] = %d, want %d", i, blocks[i].HeadingLevel, want)
			}
		}
	})
	t.Run("Two_NoArtificialThird", func(t *testing.T) {
		t.Parallel()
		blocks := []layout.Block{mkH(18, true, false), mkH(14, true, false)}
		AssignHeadingLevelsDocument([][]layout.Block{blocks})
		if blocks[0].HeadingLevel != 1 || blocks[1].HeadingLevel != 2 {
			t.Errorf("got %d/%d, want 1/2", blocks[0].HeadingLevel, blocks[1].HeadingLevel)
		}
	})
	t.Run("BoldBump_ItalicDown", func(t *testing.T) {
		t.Parallel()
		blocks := []layout.Block{mkH(14, true, false), mkH(14, false, true)}
		AssignHeadingLevelsDocument([][]layout.Block{blocks})
		if blocks[0].HeadingLevel != 1 {
			t.Errorf("bold = %d, want 1", blocks[0].HeadingLevel)
		}
		if blocks[1].HeadingLevel != 2 {
			t.Errorf("italic = %d, want 2 (bumped DOWN one level)", blocks[1].HeadingLevel)
		}
	})
}

func TestIsCodeBlock(t *testing.T) {
	t.Parallel()
	mr := mkRun(12, "Courier", true, false, false, "x = 1")
	mrIndent := mkRun(12, "Courier", true, false, false, "    x = 1")
	mrShort := mkRun(12, "Courier", true, false, false, "f()")
	prose := mkBlock(686, 698, body("plain prose"))
	cases := []struct {
		name       string
		blk        layout.Block
		prev, next *layout.Block
		want       bool
	}{
		{"MultiLine", mkMultiLineBlock(700, [][]text.TextRun{{mr}, {mr}, {mr}, {mr}}), nil, nil, true},
		{"MixedBelowThreshold", mkBlock(700, 712, mr, body("def")), nil, nil, false},
		{"SingleLineProseSandwich", mkBlock(700, 712, mrShort), &prose, &prose, false},
		{"SingleLineIndented", mkBlock(700, 712, mrIndent), &prose, &prose, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isCodeBlock(tc.blk, tc.prev, tc.next, DefaultClassifyParams)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsListItem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		text       string
		wantMatch  bool
		wantMarker string
		wantIdx    int
	}{
		{"Decimal", "1. foo bar", true, "1.", 1},
		{"Bullet", "• foo", true, "•", 0},
		{"Alphabetic", "a) foo", true, "a)", 0},
		{"Negative", "plain prose without marker", false, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			blk := mkBlock(700, 712, body(tc.text))
			matched, marker, idx := isListItem(blk, defaultListMarkerPattern)
			if matched != tc.wantMatch || marker != tc.wantMarker || idx != tc.wantIdx {
				t.Errorf("got matched=%v marker=%q idx=%d, want %v %q %d", matched, marker, idx, tc.wantMatch, tc.wantMarker, tc.wantIdx)
			}
		})
	}
}

func TestClassify_Empty(t *testing.T) {
	t.Parallel()
	if out := Classify(nil); len(out) != 0 {
		t.Errorf("Classify(nil) returned %d blocks, want 0", len(out))
	}
}

func TestClassify_PreserveExistingKind(t *testing.T) {
	t.Parallel()
	blk := mkBlock(700, 712, body("table cell"))
	blk.Kind = layout.BlockTable
	out := Classify([]layout.Block{blk})
	if out[0].Kind != layout.BlockTable {
		t.Errorf("kind = %q, want %q (preserve)", out[0].Kind, layout.BlockTable)
	}
}

func TestClassify_StructRoleH2(t *testing.T) {
	t.Parallel()
	blk := mkBlock(700, 712, body("Section Heading"))
	blk.StructRole = "H2"
	out := Classify([]layout.Block{blk})
	if out[0].Kind != layout.BlockHeading || out[0].HeadingLevel != 2 {
		t.Errorf("kind=%q level=%d, want heading/2", out[0].Kind, out[0].HeadingLevel)
	}
}

func TestClassify_EndToEnd_Synthetic(t *testing.T) {
	t.Parallel()
	hdr := mkBlock(750, 768, mkRun(18, "Helvetica-Bold", false, true, false, "Heading"))
	para := mkMultiLineBlock(700, [][]text.TextRun{
		{body("first line of paragraph")},
		{body("second line of paragraph")},
		{body("third line of paragraph")},
	})
	mr := mkRun(12, "Courier", true, false, false, "code line")
	code := mkMultiLineBlock(600, [][]text.TextRun{{mr}, {mr}, {mr}})
	list := mkBlock(550, 562, body("1. list item"))
	blocks := Classify([]layout.Block{hdr, para, code, list})
	wantKinds := []layout.BlockKind{layout.BlockHeading, layout.BlockParagraph, layout.BlockCode, layout.BlockListItem}
	for i, want := range wantKinds {
		if blocks[i].Kind != want {
			t.Errorf("blocks[%d] = %q, want %q", i, blocks[i].Kind, want)
		}
	}
	if blocks[3].Metadata["list_marker"] != "1." || blocks[3].Metadata["list_index"] != "1" {
		t.Errorf("list metadata = %+v", blocks[3].Metadata)
	}
}

func TestClassify_InlineCodeMetadata_OneRun(t *testing.T) {
	t.Parallel()
	mono := mkRun(12, "Courier", true, false, false, "x")
	bodyBlk := mkMultiLineBlock(720, [][]text.TextRun{repeat("body text content", 5)})
	blk := mkBlock(700, 712, body("paragraph with one mono token: "), mono)
	out := Classify([]layout.Block{bodyBlk, blk})
	if out[1].Kind != layout.BlockParagraph {
		t.Errorf("kind = %q, want paragraph", out[1].Kind)
	}
	if out[1].Metadata["has_inline_code"] != "true" {
		t.Errorf("has_inline_code = %q, want 'true'", out[1].Metadata["has_inline_code"])
	}
}

func TestClassify_InlineCodeMetadata_NoMono(t *testing.T) {
	t.Parallel()
	bodyBlk := mkMultiLineBlock(720, [][]text.TextRun{repeat("body text content", 5)})
	prose := mkBlock(700, 712, body("paragraph with no mono runs at all"))
	out := Classify([]layout.Block{bodyBlk, prose})
	if out[1].Kind != layout.BlockParagraph {
		t.Errorf("kind = %q, want paragraph", out[1].Kind)
	}
	if _, ok := out[1].Metadata["has_inline_code"]; ok {
		t.Error("has_inline_code key present on pure-prose paragraph; should be absent")
	}
}
