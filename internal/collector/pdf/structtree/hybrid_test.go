package structtree

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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
// blocks survive the overlap filter and merge in READING ORDER.
//
// COORDINATE FRAME, named so the next reader does not re-invert it:
// both inputs are in PDF user space, where +y points UP. The block
// highest on the page therefore has the LARGEST Y1, and reading order
// is Y1 DESCENDING. This test previously asserted Y0 ascending, which
// is the page returned bottom-first — it pinned the defect rather than
// catching it, and the tagged path was unreachable in production so
// nothing else noticed.
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
	if !sort.SliceIsSorted(got, func(i, j int) bool {
		if got[i].BBox.Y1 != got[j].BBox.Y1 {
			return got[i].BBox.Y1 > got[j].BBox.Y1
		}
		return got[i].BBox.X0 < got[j].BBox.X0
	}) {
		t.Errorf("merged blocks not in reading order (Y1 descending, X0 ascending): %+v", got)
	}
	// The tagged H1 sits at the top of the page, so reading order puts
	// it FIRST. A bottom-first merge puts it last.
	if got[0].StructRole != "H1" {
		t.Errorf("first block in reading order has StructRole %q, want the top-of-page H1 - the page came back bottom-first", got[0].StructRole)
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
	runs, _, err := extractRunsForPage(ctx, 0)
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

// TestHybridFallback_ResidueIsGroupedAtElementScale pins the scale the
// residue clustering runs at.
//
// The runs no structure element claimed are, on a well-tagged page,
// one or two: a footer, a folio, a stray label. Clustering them with
// the PAGE-scale grouper applies its few-runs guard and emits one
// block per run, which splits a two-run footer in half. Measured
// through this same function before the fix, the fixture below came
// back as three blocks with the footer as "Chapter 3 | " and "42"
// separately; the chrome-shape detector requires a single line in a
// single block, so it could never fire on the running footers Phase 4
// exists to retain.
func TestHybridFallback_ResidueIsGroupedAtElementScale(t *testing.T) {
	t.Parallel()
	tagged := []layout.Block{makeBlock("P", 700, 400, 712, "1")}
	runs := []text.TextRun{
		makeRun("body", 1, 100, 700, 100, 12),
		// A two-run footer on ONE baseline.
		makeRun("Chapter 3 | ", 0, 100, 60, 70, 12),
		makeRun("42", 0, 172, 60, 12, 12),
	}

	got, err := HybridFallback(tagged, runs, defaultPageInfo())
	if err != nil {
		t.Fatalf("HybridFallback: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d blocks, want 2 (the tagged body plus ONE footer block) - the two-run footer was split", len(got))
	}
	var footer strings.Builder
	for _, l := range got[1].Lines {
		for _, r := range l.Runs {
			footer.WriteString(r.Text)
		}
	}
	if footer.String() != "Chapter 3 | 42" {
		t.Errorf("footer block text = %q, want %q", footer.String(), "Chapter 3 | 42")
	}
	if len(got[1].Lines) != 1 {
		t.Errorf("footer block has %d lines, want 1 - the chrome-shape detector requires a single line", len(got[1].Lines))
	}
	t.Logf("residue grouped at element scale: blocks=%d footer=%q lines=%d", len(got), footer.String(), len(got[1].Lines))

	// THE PLAUSIBLE-WRONG THIRD INPUT: the page-scale entry point on
	// the SAME two runs. A near-empty page really is a case where a
	// two-sample median cannot be trusted, so ClusterWithParams must
	// keep its guard and keep emitting one line per run. The two
	// entry points differing on identical input is what shows the fix
	// changed the SCALE rather than removing the guard everywhere.
	//
	// Measured, both directions, on the same-baseline pair used above:
	// page scale gives 2 blocks / 2 lines, element scale gives 1 / 1.
	pageRuns := []text.TextRun{
		makeRun("Chapter 3 | ", 0, 100, 60, 70, 12),
		makeRun("42", 0, 172, 60, 12, 12),
	}
	pageBlocks, err := layout.ClusterWithParams(pageRuns, defaultPageInfo(), layout.DefaultLayoutParams)
	if err != nil {
		t.Fatalf("ClusterWithParams: %v", err)
	}
	pageLines := 0
	for _, b := range pageBlocks {
		pageLines += len(b.Lines)
	}
	if pageLines != 2 {
		t.Errorf("page-scale clustering of 2 same-baseline runs produced %d lines, want 2 - the element-scale fix leaked into the page entry point", pageLines)
	}
	t.Logf("page-scale control intact: 2 same-baseline runs -> %d blocks / %d lines", len(pageBlocks), pageLines)
}
