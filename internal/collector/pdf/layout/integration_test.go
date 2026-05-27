package layout_test

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
)

// fixtureBlocks opens testdata/<name>, gets page 0, and returns its
// Block slice. Fatals on any error.
func fixtureBlocks(t *testing.T, name string) (*pdf.Page, []pdf.Block) {
	t.Helper()
	doc, err := pdf.OpenFile("../testdata/" + name)
	if err != nil {
		t.Fatalf("OpenFile %s: %v", name, err)
	}
	t.Cleanup(func() { _ = doc.Close() })
	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page(0) %s: %v", name, err)
	}
	blocks, err := page.Blocks()
	if err != nil {
		t.Fatalf("Blocks %s: %v", name, err)
	}
	return page, blocks
}

// blockText concatenates every run's Text across every line of the
// given block, joined by a single space, with multi-space collapse
// for tolerance.
func blockText(b pdf.Block) string {
	parts := make([]string, 0, len(b.Lines)*4)
	for _, l := range b.Lines {
		for _, r := range l.Runs {
			if r.Text == "" {
				continue
			}
			parts = append(parts, r.Text)
		}
	}
	joined := strings.Join(parts, " ")
	for strings.Contains(joined, "  ") {
		joined = strings.ReplaceAll(joined, "  ", " ")
	}
	return strings.TrimSpace(joined)
}

func TestIntegration_T4Paragraph_OneBlockOneParagraph(t *testing.T) {
	t.Parallel()
	_, blocks := fixtureBlocks(t, "t4_paragraph_simple.pdf")
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1; got %+v", len(blocks), blocks)
	}
	if got := len(blocks[0].Lines); got != 3 {
		t.Errorf("len(block.Lines) = %d, want 3", got)
	}
	got := blockText(blocks[0])
	want := "The quick brown fox jumps over the lazy dog and runs away."
	if got != want {
		t.Errorf("blockText = %q, want %q", got, want)
	}
}

func TestIntegration_T4Hyphenated_DehyphenatedFlagSet(t *testing.T) {
	t.Parallel()
	_, blocks := fixtureBlocks(t, "t4_hyphenated_paragraph.pdf")
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if got := len(blocks[0].Lines); got != 2 {
		t.Fatalf("len(block.Lines) = %d, want 2", got)
	}
	if !blocks[0].Lines[0].WasDehyphenated {
		t.Errorf("Lines[0].WasDehyphenated = false, want true")
	}
	last := blocks[0].Lines[0].Runs[len(blocks[0].Lines[0].Runs)-1]
	if strings.HasSuffix(last.Text, "-") {
		t.Errorf("dehyphenated last run still ends with '-': %q", last.Text)
	}
	// Second line should start with 'national' (preserved unchanged).
	first := blocks[0].Lines[1].Runs[0]
	if !strings.HasPrefix(first.Text, "national") {
		t.Errorf("Lines[1].Runs[0].Text = %q, want prefix 'national'", first.Text)
	}
}

func TestIntegration_T4Rotated90_BlocksClusteredCorrectly(t *testing.T) {
	t.Parallel()
	page, blocks := fixtureBlocks(t, "t4_rotated90.pdf")
	if rot := page.Rotation(); rot != 90 {
		t.Fatalf("page.Rotation() = %d, want 90 (fixture sanity check)", rot)
	}
	if len(blocks) == 0 {
		t.Fatalf("rotated 90: blocks = 0, want >= 1")
	}
}

func TestCluster_MixedFontParagraph(t *testing.T) {
	t.Parallel()
	_, blocks := fixtureBlocks(t, "t4_mixed_font_paragraph.pdf")
	// Assert exactly 2 Blocks: body (3 lines) + caption (1 line).
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2 (median-norm split: body + caption); blocks=%+v", len(blocks), blocks)
	}
	// Identify the body block (3 lines) and caption block (1 line).
	var body, caption pdf.Block
	for _, b := range blocks {
		switch len(b.Lines) {
		case 3:
			body = b
		case 1:
			caption = b
		}
	}
	if len(body.Lines) != 3 {
		t.Fatalf("body block: len(Lines) = %d, want 3", len(body.Lines))
	}
	if len(caption.Lines) != 1 {
		t.Fatalf("caption block: len(Lines) = %d, want 1", len(caption.Lines))
	}
	bodyText := blockText(body)
	wantBody := "The quick brown fox jumps over the lazy dog and runs away."
	if bodyText != wantBody {
		t.Errorf("body text = %q, want %q", bodyText, wantBody)
	}
	captionText := blockText(caption)
	wantCaption := "Figure 1: caption text."
	if captionText != wantCaption {
		t.Errorf("caption text = %q, want %q", captionText, wantCaption)
	}
}
