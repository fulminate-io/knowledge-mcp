package structtree

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// loadFixture is the shared fixture loader for walk_test cases. The
// path is resolved relative to collector/pdf/testdata/ (the directory
// where Phase 7's gen.go writes synthetic tagged PDFs and where T1's
// onepage.pdf already lives).
func loadFixture(t *testing.T, name string) *internalpdf.Context {
	t.Helper()
	path := filepath.Join("..", "testdata", name)
	ctx, err := internalpdf.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = ctx.Close() })
	return ctx
}

// TestWalk_UntaggedDocument_ErrNotTagged asserts the basic
// untagged-document contract: testdata/onepage.pdf is the T1 baseline
// PDF (no /StructTreeRoot), so Walk must surface ErrNotTagged via
// errors.Is.
func TestWalk_UntaggedDocument_ErrNotTagged(t *testing.T) {
	t.Parallel()
	ctx := loadFixture(t, "onepage.pdf")

	if ctx.IsTagged() {
		t.Fatalf("onepage.pdf reports IsTagged=true; expected untagged baseline")
	}
	blocks, err := Walk(ctx, -1)
	if err == nil {
		t.Fatalf("Walk on untagged returned (%v, nil); want ErrNotTagged", blocks)
	}
	if !errors.Is(err, ErrNotTagged) {
		t.Errorf("Walk err = %v, want errors.Is(err, ErrNotTagged)", err)
	}
	if blocks != nil {
		t.Errorf("Walk on untagged returned non-nil blocks: %v", blocks)
	}
}

// blockText concats every TextRun in Lines[0] for an emit check.
func blockText(b layout.Block) string {
	if len(b.Lines) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, r := range b.Lines[0].Runs {
		sb.WriteString(r.Text)
	}
	return sb.String()
}

// TestWalk_SimpleTagged_HPP loads testdata/simple_tagged.pdf and
// asserts the Document → H1 → P → P shape produces 3 blocks with
// the correct StructRole + HeadingLevel pre-classification.
func TestWalk_SimpleTagged_HPP(t *testing.T) {
	t.Parallel()
	ctx := loadFixture(t, "simple_tagged.pdf")

	blocks, err := Walk(ctx, -1)
	if err != nil {
		t.Fatalf("Walk err: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("Walk produced %d blocks, want 3 — got %+v", len(blocks), blocks)
	}
	wantRoles := []string{"H1", "P", "P"}
	wantKinds := []layout.BlockKind{layout.BlockHeading, layout.BlockParagraph, layout.BlockParagraph}
	wantLevels := []int{1, 0, 0}
	for i, b := range blocks {
		if b.StructRole != wantRoles[i] {
			t.Errorf("blocks[%d].StructRole = %q, want %q", i, b.StructRole, wantRoles[i])
		}
		if b.Kind != wantKinds[i] {
			t.Errorf("blocks[%d].Kind = %q, want %q", i, b.Kind, wantKinds[i])
		}
		if b.HeadingLevel != wantLevels[i] {
			t.Errorf("blocks[%d].HeadingLevel = %d, want %d", i, b.HeadingLevel, wantLevels[i])
		}
		if b.PageIndex != 0 {
			t.Errorf("blocks[%d].PageIndex = %d, want 0", i, b.PageIndex)
		}
	}
}

// TestWalk_PageFilter_OnlyMatchingPage verifies the pageFilter contract
// (T2.3): pruning happens at LEAF emit time inside the walker; every
// returned Block on a per-page call has PageIndex == pageFilter.
func TestWalk_PageFilter_OnlyMatchingPage(t *testing.T) {
	t.Parallel()
	ctx := loadFixture(t, "simple_tagged.pdf")
	// simple_tagged.pdf is single-page (page 0); requesting page 0
	// returns all 3 blocks; requesting page 1 returns 0 blocks.
	allOnPage := func(blocks []layout.Block, want int) {
		t.Helper()
		for i, b := range blocks {
			if b.PageIndex != want {
				t.Errorf("blocks[%d].PageIndex = %d, want %d", i, b.PageIndex, want)
			}
		}
	}
	got, err := Walk(ctx, 0)
	if err != nil {
		t.Fatalf("Walk(ctx, 0) err: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Walk(ctx, 0) len = %d, want 3", len(got))
	}
	allOnPage(got, 0)

	got1, err := Walk(ctx, 1)
	if err != nil {
		t.Fatalf("Walk(ctx, 1) err: %v", err)
	}
	if len(got1) != 0 {
		t.Errorf("Walk(ctx, 1) on single-page fixture should be empty, got %d blocks", len(got1))
	}
}

// TestWalk_NestedWalkthrough_PartSectH2P loads
// testdata/nested_tagged.pdf (Document → Part → Sect → H2 → P) and
// asserts walk-through skips Document/Part/Sect; Walk emits 2 blocks.
func TestWalk_NestedWalkthrough_PartSectH2P(t *testing.T) {
	t.Parallel()
	ctx := loadFixture(t, "nested_tagged.pdf")

	blocks, err := Walk(ctx, -1)
	if err != nil {
		t.Fatalf("Walk err: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("len = %d, want 2 (H2+P after walk-through), got %+v", len(blocks), blocks)
	}
	if blocks[0].StructRole != "H2" || blocks[0].HeadingLevel != 2 {
		t.Errorf("blocks[0] = %+v, want StructRole=H2 HeadingLevel=2", blocks[0])
	}
	if blocks[1].StructRole != "P" {
		t.Errorf("blocks[1].StructRole = %q, want P", blocks[1].StructRole)
	}
}

// TestWalk_ListNesting_LLILBody loads testdata/list_tagged.pdf and
// asserts the L → LI → LBody chain emits at least one BlockListItem.
func TestWalk_ListNesting_LLILBody(t *testing.T) {
	t.Parallel()
	ctx := loadFixture(t, "list_tagged.pdf")

	blocks, err := Walk(ctx, -1)
	if err != nil {
		t.Fatalf("Walk err: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("len = %d, want 2 (LI+LI), got %+v", len(blocks), blocks)
	}
	for i, b := range blocks {
		if b.Kind != layout.BlockListItem {
			t.Errorf("blocks[%d].Kind = %q, want %q", i, b.Kind, layout.BlockListItem)
		}
		if b.StructRole != "LI" {
			t.Errorf("blocks[%d].StructRole = %q, want LI", i, b.StructRole)
		}
	}
}

// TestWalk_ActualTextOverride (T2.1 reviewer fix) — load
// testdata/actualtext_tagged.pdf where /P carries
// /A << /ActualText (replaced text) >> over runs reading "original".
// Assert Lines[0].Runs is a single synthesized TextRun whose Text is
// the override, BBox-derived X/Y/W/H, and FontKey/Size inherited
// from runs[0].
func TestWalk_ActualTextOverride(t *testing.T) {
	t.Parallel()
	ctx := loadFixture(t, "actualtext_tagged.pdf")

	blocks, err := Walk(ctx, -1)
	if err != nil {
		t.Fatalf("Walk err: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("len = %d, want 1", len(blocks))
	}
	b := blocks[0]
	if got := blockText(b); got != "replaced text" {
		t.Errorf("Lines[0] text = %q, want %q (synthesized override)", got, "replaced text")
	}
	if len(b.Lines) != 1 || len(b.Lines[0].Runs) != 1 {
		t.Fatalf("Lines/Runs shape = %+v, want single Line with single synthesized Run", b.Lines)
	}
	r := b.Lines[0].Runs[0]
	// BBox-derived geometry.
	if r.X != b.BBox.X0 || r.Y != b.BBox.Y0 {
		t.Errorf("synthesized run X/Y = (%v,%v), want bbox.X0/Y0 (%v,%v)", r.X, r.Y, b.BBox.X0, b.BBox.Y0)
	}
	if got, want := r.Width, b.BBox.X1-b.BBox.X0; got != want {
		t.Errorf("synthesized run Width = %v, want %v", got, want)
	}
	// FontKey/Size inherited from underlying run; non-empty/non-zero.
	if r.FontKey == "" {
		t.Errorf("synthesized run FontKey is empty; want inherited from first underlying run")
	}
	if r.Size == 0 {
		t.Errorf("synthesized run Size = 0; want inherited from first underlying run")
	}
}

// TestWalk_VendorRole_WalkthroughChildrenExtracted asserts unknown
// /S values walk-through and the wrapped /P emits a paragraph.
func TestWalk_VendorRole_WalkthroughChildrenExtracted(t *testing.T) {
	t.Parallel()
	ctx := loadFixture(t, "vendor_tagged.pdf")

	blocks, err := Walk(ctx, -1)
	if err != nil {
		t.Fatalf("Walk err: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("len = %d, want 1 (P inside MyCorp::Custom walks through to inner /P)", len(blocks))
	}
	if blocks[0].StructRole != "P" {
		t.Errorf("StructRole = %q, want P", blocks[0].StructRole)
	}
}
