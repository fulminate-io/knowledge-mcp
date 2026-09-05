package structtree

import (
	"reflect"
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

func mkRun(text_ string, mcid int, x, y, w, h float64) text.TextRun {
	return text.TextRun{Text: text_, MCID: mcid, X: x, Y: y, Width: w, Height: h}
}

// TestNewPageRunIndex_BucketsByMCID asserts the indexer buckets runs
// by MCID and skips MCID == 0 (untagged residue).
func TestNewPageRunIndex_BucketsByMCID(t *testing.T) {
	t.Parallel()
	runs := []text.TextRun{
		mkRun("a", 5, 0, 0, 10, 12),  // idx 0 → MCID 5
		mkRun("b", 5, 10, 0, 10, 12), // idx 1 → MCID 5
		mkRun("c", 0, 0, 20, 10, 12), // idx 2 → untagged, skipped
		mkRun("d", 7, 0, 30, 10, 12), // idx 3 → MCID 7
	}
	idx := newPageRunIndex(runs, layout.PageInfo{})
	if got, want := len(idx.byMCID[5]), 2; got != want {
		t.Errorf("byMCID[5] len = %d, want %d", got, want)
	}
	if got, want := len(idx.byMCID[7]), 1; got != want {
		t.Errorf("byMCID[7] len = %d, want %d", got, want)
	}
	if _, present := idx.byMCID[0]; present {
		t.Errorf("byMCID[0] should not be populated (untagged)")
	}
}

// TestRunsForMCIDs_PreservesSourceOrder asserts that querying MCIDs in
// any order yields runs in their source-emission order.
func TestRunsForMCIDs_PreservesSourceOrder(t *testing.T) {
	t.Parallel()
	runs := []text.TextRun{
		mkRun("first", 5, 0, 0, 10, 12),
		mkRun("second", 5, 10, 0, 10, 12),
		mkRun("third", 7, 0, 30, 10, 12),
	}
	idx := newPageRunIndex(runs, layout.PageInfo{})
	// Query MCIDs in reverse order to confirm sorted-source-order.
	out := idx.RunsForMCIDs([]int{7, 5})
	got := make([]string, 0, len(out))
	for _, r := range out {
		got = append(got, r.Text)
	}
	want := []string{"first", "second", "third"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RunsForMCIDs = %v, want %v", got, want)
	}
}

// TestRunsForMCIDs_DedupesRunIndices asserts duplicate MCID lookups
// don't yield duplicate runs.
func TestRunsForMCIDs_DedupesRunIndices(t *testing.T) {
	t.Parallel()
	runs := []text.TextRun{
		mkRun("a", 5, 0, 0, 10, 12),
		mkRun("b", 5, 10, 0, 10, 12),
	}
	idx := newPageRunIndex(runs, layout.PageInfo{})
	out := idx.RunsForMCIDs([]int{5, 5, 5})
	if len(out) != 2 {
		t.Errorf("RunsForMCIDs len = %d, want 2", len(out))
	}
}

// TestComputeMCIDBBox_MinMaxOverRuns asserts the bbox unions the
// run extents on every axis.
func TestComputeMCIDBBox_MinMaxOverRuns(t *testing.T) {
	t.Parallel()
	runs := []text.TextRun{
		mkRun("", 0, 10, 20, 30, 12), // X: 10..40, Y: 20..32
		mkRun("", 0, 0, 50, 25, 14),  // X: 0..25, Y: 50..64
		mkRun("", 0, 60, 5, 10, 8),   // X: 60..70, Y: 5..13
	}
	got := computeMCIDBBox(runs)
	want := Rect{X0: 0, Y0: 5, X1: 70, Y1: 64}
	if got != want {
		t.Errorf("computeMCIDBBox = %+v, want %+v", got, want)
	}
}

// TestComputeMCIDBBox_EmptyInput asserts empty input → zero rect.
func TestComputeMCIDBBox_EmptyInput(t *testing.T) {
	t.Parallel()
	if got := computeMCIDBBox(nil); got != (Rect{}) {
		t.Errorf("computeMCIDBBox(nil) = %+v, want zero Rect", got)
	}
	if got := computeMCIDBBox([]text.TextRun{}); got != (Rect{}) {
		t.Errorf("computeMCIDBBox([]) = %+v, want zero Rect", got)
	}
}

// TestExtractMCIDText_ConcatInOrder asserts text is concatenated in
// source order with sensible separators (no extra spaces when runs
// already carry their own boundary whitespace).
func TestExtractMCIDText_ConcatInOrder(t *testing.T) {
	t.Parallel()
	// Same line: separator = single space (or none if either side
	// already carries whitespace).
	runs := []text.TextRun{
		mkRun("hello", 1, 0, 100, 30, 12),
		mkRun(" ", 1, 30, 100, 5, 12),
		mkRun("world", 1, 35, 100, 30, 12),
	}
	if got := extractMCIDText(runs); got != "hello world" {
		t.Errorf("extractMCIDText = %q, want %q", got, "hello world")
	}
	// Without explicit space run, helper inserts one between
	// adjacent same-line runs.
	runs2 := []text.TextRun{
		mkRun("hello", 1, 0, 100, 30, 12),
		mkRun("world", 1, 35, 100, 30, 12),
	}
	if got := extractMCIDText(runs2); got != "hello world" {
		t.Errorf("extractMCIDText (auto-space) = %q, want %q", got, "hello world")
	}
}

// TestApplyActualText_OverrideWhenNonempty asserts non-empty
// actualText replaces the fallback.
func TestApplyActualText_OverrideWhenNonempty(t *testing.T) {
	t.Parallel()
	if got := applyActualText("override", "hello"); got != "override" {
		t.Errorf("applyActualText = %q, want %q", got, "override")
	}
}

// TestApplyActualText_FallbackWhenEmpty asserts empty actualText
// returns the fallback.
func TestApplyActualText_FallbackWhenEmpty(t *testing.T) {
	t.Parallel()
	if got := applyActualText("", "hello"); got != "hello" {
		t.Errorf("applyActualText = %q, want %q", got, "hello")
	}
}

// TestCollectMCIDsFromKids_FlattenAndFlagObjref asserts MCID kids
// flatten to a list and ObjRef kids surface a true flag.
func TestCollectMCIDsFromKids_FlattenAndFlagObjref(t *testing.T) {
	t.Parallel()
	kids := []internalpdf.Kid{
		internalpdf.KidMCID{ID: 1, PageIndex: 0},
		internalpdf.KidMCID{ID: 2, PageIndex: 0},
		internalpdf.KidObjRef{},
		internalpdf.KidMCID{ID: 5, PageIndex: 0},
	}
	mcids, hasObj := collectMCIDsFromKids(kids)
	if !reflect.DeepEqual(mcids, []int{1, 2, 5}) {
		t.Errorf("mcids = %v, want [1 2 5]", mcids)
	}
	if !hasObj {
		t.Errorf("hasObjref = false, want true")
	}
}

// TestNewRunFor_CachesPageIndex asserts the closure cache means a
// second call for the same page does not re-invoke the extractor.
func TestNewRunFor_CachesPageIndex(t *testing.T) {
	t.Parallel()
	calls := 0
	rf := newRunFor(func(pi int) ([]text.TextRun, layout.PageInfo, error) {
		calls++
		return []text.TextRun{mkRun("x", 1, 0, 0, 10, 12)}, layout.PageInfo{}, nil
	})
	if _, err := rf(0); err != nil {
		t.Fatalf("rf(0) err: %v", err)
	}
	if _, err := rf(0); err != nil {
		t.Fatalf("rf(0) cached err: %v", err)
	}
	if _, err := rf(1); err != nil {
		t.Fatalf("rf(1) err: %v", err)
	}
	if calls != 2 {
		t.Errorf("extractor calls = %d, want 2 (one per unique page)", calls)
	}
}
