package structtree

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// makeBlock is a tiny constructor for hand-built layout.Block fixtures.
// X0 is fixed to 0 since every test starts blocks at the left margin —
// linter flags the unused parameter.
func makeBlock(structRole string, y0, x1, y1 float64, mcidsCSV string) layout.Block {
	b := layout.Block{
		StructRole: structRole,
		BBox:       layout.Rect{X0: 0, Y0: y0, X1: x1, Y1: y1},
	}
	if mcidsCSV != "" {
		b.Metadata = map[string]string{"mcids": mcidsCSV}
	}
	return b
}

func makeRun(t string, mcid int, x, y, w, h float64) text.TextRun {
	return text.TextRun{Text: t, MCID: mcid, X: x, Y: y, Width: w, Height: h}
}

func defaultPageInfo() layout.PageInfo {
	return layout.PageInfo{
		PageIndex: 0,
		MediaBox:  layout.Rect{X0: 0, Y0: 0, X1: 612, Y1: 792},
		Rotation:  0,
	}
}

// TestHybridFallback_NoResidue_ReturnsTaggedUnchanged asserts that
// when every run is reachable via tagged blocks, HybridFallback
// returns the tagged blocks untouched.
func TestHybridFallback_NoResidue_ReturnsTaggedUnchanged(t *testing.T) {
	t.Parallel()
	tagged := []layout.Block{makeBlock("P", 700, 200, 720, "1,2")}
	runs := []text.TextRun{
		makeRun("a", 1, 0, 700, 100, 12),
		makeRun("b", 2, 100, 700, 100, 12),
	}
	got, err := HybridFallback(tagged, runs, defaultPageInfo())
	if err != nil {
		t.Fatalf("HybridFallback err: %v", err)
	}
	if !reflect.DeepEqual(got, tagged) {
		t.Errorf("got %d blocks, want tagged unchanged (%d)", len(got), len(tagged))
	}
}

// TestHybridFallback_PureResidue_NoTagged asserts that when there
// are no tagged blocks at all, HybridFallback returns layout's
// clustering of the residue.
func TestHybridFallback_PureResidue_NoTagged(t *testing.T) {
	t.Parallel()
	runs := []text.TextRun{
		makeRun("hello", 0, 0, 700, 30, 12),
		makeRun("world", 0, 35, 700, 30, 12),
	}
	got, err := HybridFallback(nil, runs, defaultPageInfo())
	if err != nil {
		t.Fatalf("HybridFallback err: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("expected non-empty residue clustering, got 0 blocks")
	}
	for _, b := range got {
		if b.StructRole != "" {
			t.Errorf("residue block has non-empty StructRole %q (want empty heuristic-origin marker)", b.StructRole)
		}
	}
}

// TestHybridFallback_PartitionByReachability asserts orphan-MCID
// runs (MCID > 0 but unreferenced by any tagged block) and untagged
// runs (MCID == 0) both partition into residue.
func TestHybridFallback_PartitionByReachability(t *testing.T) {
	t.Parallel()
	tagged := []layout.Block{makeBlock("P", 700, 200, 720, "5,7")}
	runs := []text.TextRun{
		makeRun("a", 5, 0, 700, 50, 12),  // reachable; partitioned out
		makeRun("b", 7, 50, 700, 50, 12), // reachable; partitioned out
		makeRun("c", 0, 0, 600, 50, 12),  // untagged residue (different Y)
		makeRun("d", 9, 0, 500, 50, 12),  // orphan-MCID residue (different Y)
	}
	got, err := HybridFallback(tagged, runs, defaultPageInfo())
	if err != nil {
		t.Fatalf("HybridFallback err: %v", err)
	}
	// Result should include the tagged block + at least 1 residue
	// block from runs c+d (not overlapping with tagged at y=700).
	if len(got) < 2 {
		t.Errorf("expected ≥2 blocks (tagged + residue), got %d", len(got))
	}
}

// TestHybridFallback_DropsResidueOverlap verifies the tagged-wins
// overlap rule (Q2 locked option a).
func TestHybridFallback_DropsResidueOverlap(t *testing.T) {
	t.Parallel()
	tagged := []layout.Block{makeBlock("P", 0, 100, 50, "1")}
	// Hand-craft residue that would cluster into a block intersecting
	// the tagged bbox. We use untagged runs all at one Y so layout
	// produces one block at roughly (50, 25, 75, 40).
	runs := []text.TextRun{
		makeRun("o", 1, 0, 0, 30, 12),   // tagged region; partitioned out
		makeRun("p", 0, 50, 25, 25, 15), // residue inside tagged bbox
	}
	got, err := HybridFallback(tagged, runs, defaultPageInfo())
	if err != nil {
		t.Fatalf("HybridFallback err: %v", err)
	}
	for _, b := range got {
		if b.StructRole == "" && bboxIntersects(b.BBox, tagged[0].BBox) {
			t.Errorf("residue block %+v overlaps tagged bbox %+v but was kept; tagged-wins violation",
				b.BBox, tagged[0].BBox)
		}
	}
}

// TestHybridFallback_KeepsResidueDisjoint asserts disjoint residue
// blocks survive the overlap filter and merge in Y order.
func TestHybridFallback_KeepsResidueDisjoint(t *testing.T) {
	t.Parallel()
	tagged := []layout.Block{makeBlock("H1", 700, 200, 720, "1")}
	runs := []text.TextRun{
		makeRun("hdr", 1, 0, 700, 100, 12),
		makeRun("body1", 0, 0, 600, 50, 12),
		makeRun("body2", 0, 60, 600, 50, 12),
	}
	got, err := HybridFallback(tagged, runs, defaultPageInfo())
	if err != nil {
		t.Fatalf("HybridFallback err: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("expected ≥2 blocks, got %d", len(got))
	}
	// Confirm blocks are Y0-ascending after merge.
	if !sort.SliceIsSorted(got, func(i, j int) bool {
		if got[i].BBox.Y0 != got[j].BBox.Y0 {
			return got[i].BBox.Y0 < got[j].BBox.Y0
		}
		return got[i].BBox.X0 < got[j].BBox.X0
	}) {
		t.Errorf("merged blocks not Y-ascending: %+v", got)
	}
}

// TestBboxIntersects_AABBCases pins the AABB-overlap helper's edge
// cases — strict-inequality (touching edges do NOT overlap), nested
// containment, partial overlap, disjoint.
func TestBboxIntersects_AABBCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		a, b    layout.Rect
		overlap bool
	}{
		{"sameRect", layout.Rect{X0: 0, Y0: 0, X1: 10, Y1: 10}, layout.Rect{X0: 0, Y0: 0, X1: 10, Y1: 10}, true},
		{"touchingRight", layout.Rect{X0: 0, Y0: 0, X1: 10, Y1: 10}, layout.Rect{X0: 10, Y0: 0, X1: 20, Y1: 10}, false},
		{"touchingTop", layout.Rect{X0: 0, Y0: 0, X1: 10, Y1: 10}, layout.Rect{X0: 0, Y0: 10, X1: 10, Y1: 20}, false},
		{"nestedInner", layout.Rect{X0: 0, Y0: 0, X1: 10, Y1: 10}, layout.Rect{X0: 2, Y0: 2, X1: 8, Y1: 8}, true},
		{"disjoint", layout.Rect{X0: 0, Y0: 0, X1: 10, Y1: 10}, layout.Rect{X0: 50, Y0: 50, X1: 60, Y1: 60}, false},
		{"partial", layout.Rect{X0: 0, Y0: 0, X1: 10, Y1: 10}, layout.Rect{X0: 5, Y0: 5, X1: 15, Y1: 15}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bboxIntersects(tc.a, tc.b); got != tc.overlap {
				t.Errorf("bboxIntersects(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.overlap)
			}
		})
	}
}

// TestMCIDsFromBlock_CSVRoundTrip pins the CSV parsing of the
// Block.Metadata["mcids"] stamp Phase 4 emits.
func TestMCIDsFromBlock_CSVRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want []int
	}{
		{"three", "5,7,9", []int{5, 7, 9}},
		{"single", "42", []int{42}},
		{"empty", "", nil},
		{"missingMeta", "absent", []int{}}, // sentinel: see below
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := layout.Block{}
			if tc.raw != "absent" {
				b.Metadata = map[string]string{"mcids": tc.raw}
			}
			got := mcidsFromBlock(b)
			if tc.want == nil && got != nil {
				t.Errorf("got %v, want nil", got)
			}
			if tc.name == "missingMeta" {
				if got != nil {
					t.Errorf("missing metadata → got %v, want nil", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) && !(tc.want == nil && len(got) == 0) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHybridFallback_PartialTaggedFixture is the Phase 7 integration
// test: load testdata/hybrid_partial.pdf (one tagged /P at y=700 +
// one untagged paragraph at y=600). Walk the tree to gather tagged
// blocks; pull all runs; pass through HybridFallback. Result must
// contain both blocks Y-ordered (the untagged one above the tagged
// one when sorted Y0 ascending).
func TestHybridFallback_PartialTaggedFixture(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "testdata", "hybrid_partial.pdf")
	ctx, err := internalpdf.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	defer ctx.Close()
	if !ctx.IsTagged() {
		t.Fatalf("hybrid_partial.pdf reports IsTagged=false")
	}
	tagged, err := Walk(ctx, 0)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(tagged) != 1 {
		t.Fatalf("Walk produced %d tagged blocks, want 1", len(tagged))
	}
	runs, err := extractRunsForPage(ctx, 0)
	if err != nil {
		t.Fatalf("extractRunsForPage: %v", err)
	}
	page, _ := ctx.Page(0)
	mb := page.MediaBox()
	info := layout.PageInfo{
		PageIndex: 0,
		MediaBox:  layout.Rect{X0: mb.X0, Y0: mb.Y0, X1: mb.X1, Y1: mb.Y1},
		Rotation:  page.Rotation(),
	}
	merged, err := HybridFallback(tagged, runs, info)
	if err != nil {
		t.Fatalf("HybridFallback: %v", err)
	}
	if len(merged) < 2 {
		t.Fatalf("HybridFallback produced %d blocks, want ≥2 (tagged + residue)", len(merged))
	}
	// Confirm at least one tagged block (with non-empty StructRole)
	// AND at least one residue block (empty StructRole = heuristic
	// origin) made it through.
	var taggedFound, residueFound bool
	for _, b := range merged {
		if b.StructRole != "" {
			taggedFound = true
		} else {
			residueFound = true
		}
	}
	if !taggedFound {
		t.Errorf("merged result has no tagged block; want one carrying StructRole=P")
	}
	if !residueFound {
		t.Errorf("merged result has no residue block; want untagged paragraph in residue")
	}
}
