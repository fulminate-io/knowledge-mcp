package structtree

import (
	"strings"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// pageRunIndex is the per-page MCID → []run-index map that powers
// fast lookup during the structure-tree walk. byMCID stores indices
// (not run slices) so a run associated with a given MCID is owned by
// the underlying runs slice rather than copied — Phase 4 hands the
// slice through to emitBlock, which does the bbox/text math on the
// in-place subset.
//
// The runs slice is shared with the caller; pageRunIndex does not
// take ownership and must not mutate it.
type pageRunIndex struct {
	byMCID map[int][]int
	runs   []text.TextRun

	// pageInfo is the page's own frame — media box and rotation.
	// It rides here rather than as another parameter threaded down
	// the walk because every caller that needs it already holds a
	// pageRunIndex, and the geometry is a property of the same page
	// the runs came from.
	pageInfo layout.PageInfo
}

// newPageRunIndex builds the MCID → []run-index map by scanning runs
// in source order. Only runs with MCID > 0 are indexed — MCID == 0 is
// the v1 contract for "outside any marked-content region" (untagged
// content even within a tagged document; common in HybridFallback).
//
// pageInfo IS STORED, and the assignment below is a repair rather than a
// tidy-up: the parameter was accepted and then dropped, so idx.pageInfo stayed
// the zero PageInfo for the life of every index this constructor built, and
// walk_emit.go's layout.LinesFromRuns call read that zero — laying out a tagged
// element with no media box and no rotation. unparam is what surfaced it,
// reporting the parameter as unused; the finding was a real defect wearing a
// style complaint's clothes, and the field's own doc comment already says the
// geometry is meant to ride here.
func newPageRunIndex(runs []text.TextRun, pageInfo layout.PageInfo) *pageRunIndex {
	idx := &pageRunIndex{
		byMCID:   make(map[int][]int),
		runs:     runs,
		pageInfo: pageInfo,
	}
	for i := range runs {
		mcid := runs[i].MCID
		if mcid <= 0 {
			continue
		}
		idx.byMCID[mcid] = append(idx.byMCID[mcid], i)
	}
	return idx
}

// RunsForMCIDs gathers all TextRuns associated with the given MCID
// set, deduped by run-index and returned in source order (the order
// runs were emitted, which is reading order within a content stream).
//
// The dedupe is defensive — the v1 walker stamps each run with the
// top-of-stack MCID, so no run appears under two MCIDs in byMCID.
// Future revisions that propagate ALL stack-frame MCIDs onto each run
// would still produce correct output here.
func (idx *pageRunIndex) RunsForMCIDs(ids []int) []text.TextRun {
	if idx == nil || len(ids) == 0 {
		return nil
	}
	seen := make(map[int]struct{})
	collected := make([]int, 0, len(ids))
	for _, id := range ids {
		for _, runIdx := range idx.byMCID[id] {
			if _, dup := seen[runIdx]; dup {
				continue
			}
			seen[runIdx] = struct{}{}
			collected = append(collected, runIdx)
		}
	}
	if len(collected) == 0 {
		return nil
	}
	// Sort by index ascending — preserves source order across MCIDs
	// emitted on the same page, even when ids was passed unsorted.
	sortInts(collected)
	out := make([]text.TextRun, 0, len(collected))
	for _, i := range collected {
		out = append(out, idx.runs[i])
	}
	return out
}

// sortInts is an inlined insertion sort sized for the small slices
// (typically 3-30 elements) RunsForMCIDs produces. Avoids the import
// of "sort" for this single use.
func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		x := a[i]
		j := i - 1
		for j >= 0 && a[j] > x {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = x
	}
}

// computeMCIDBBox returns the axis-aligned bbox enclosing every run
// in the slice. min/max over X, Y, X+Width, Y+Height. Returns the
// zero Rect when input is empty.
func computeMCIDBBox(runs []text.TextRun) Rect {
	if len(runs) == 0 {
		return Rect{}
	}
	r := runs[0]
	out := Rect{X0: r.X, Y0: r.Y, X1: r.X + r.Width, Y1: r.Y + r.Height}
	for i := 1; i < len(runs); i++ {
		r := runs[i]
		if r.X < out.X0 {
			out.X0 = r.X
		}
		if r.Y < out.Y0 {
			out.Y0 = r.Y
		}
		if r.X+r.Width > out.X1 {
			out.X1 = r.X + r.Width
		}
		if r.Y+r.Height > out.Y1 {
			out.Y1 = r.Y + r.Height
		}
	}
	return out
}

// extractMCIDText concatenates run.Text values in source order with
// single-space separators between runs that aren't adjacent on the
// same Y line. v1 emits the raw concat — the layout-package's
// dehyphenateLines helper operates on Lines, not raw runs, so
// integrating it here would force a Line-level grouping pass we
// don't otherwise need. T7's chunker can dehyphenate the resulting
// Block.Lines[0] if downstream consumers demand it.
//
// Adjacency rule: two runs are "adjacent on the same line" when
// abs(Y_b - Y_a) < max(H_a, H_b) * 0.5 — half a line height. Far
// apart on Y (different lines) → join with a newline.
func extractMCIDText(runs []text.TextRun) string {
	if len(runs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(runs[0].Text)
	for i := 1; i < len(runs); i++ {
		prev, cur := runs[i-1], runs[i]
		if sameLine(prev, cur) {
			// Insert a space if the previous run didn't already end
			// in whitespace and the current doesn't start with it.
			if !endsWS(prev.Text) && !startsWS(cur.Text) {
				sb.WriteByte(' ')
			}
		} else {
			sb.WriteByte('\n')
		}
		sb.WriteString(cur.Text)
	}
	return sb.String()
}

// sameLine reports whether two runs sit on the same baseline within a
// half-line tolerance. Uses the larger of the two heights as the
// scale to handle mixed-font lines (e.g. a superscript next to body
// prose).
func sameLine(a, b text.TextRun) bool {
	h := a.Height
	if b.Height > h {
		h = b.Height
	}
	if h == 0 {
		h = 1
	}
	dy := a.Y - b.Y
	if dy < 0 {
		dy = -dy
	}
	return dy < h*0.5
}

// endsWS reports whether s ends in an ASCII whitespace byte.
func endsWS(s string) bool {
	if s == "" {
		return false
	}
	c := s[len(s)-1]
	return c == ' ' || c == '\t' || c == '\n'
}

// startsWS reports whether s starts with an ASCII whitespace byte.
func startsWS(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c == ' ' || c == '\t' || c == '\n'
}

// applyActualText returns actualText when non-empty, otherwise the
// fallback. Used by Tree()'s element-attr population path where the
// caller wants the resolved-text-only form. Phase 4 walk_emit.go's
// Block synthesis path constructs a replacement TextRun directly
// rather than going through this helper.
func applyActualText(actualText, fallback string) string {
	if actualText != "" {
		return actualText
	}
	return fallback
}

// collectMCIDsFromKids flattens the /K array's Kid slice into the
// MCID list and a hasObjref flag. KidStructElem entries are NOT
// flattened — those are the recursion targets, traversed separately
// by walk.go. Only KidMCID and KidObjRef are inspected here.
func collectMCIDsFromKids(kids []internalpdf.Kid) (mcids []int, hasObjref bool) {
	for _, k := range kids {
		switch v := k.(type) {
		case internalpdf.KidMCID:
			mcids = append(mcids, v.ID)
		case internalpdf.KidObjRef:
			hasObjref = true
		}
	}
	return mcids, hasObjref
}

// runFor is the closure shape walk.go consumes to lazily build (or
// reuse) the per-page MCID index. The first call for a given
// pageIndex builds the index; subsequent calls return the cached
// reference.
type runFor func(pageIndex int) (*pageRunIndex, error)

// newRunFor returns a runFor that lazily builds + caches a
// pageRunIndex per page. The cache lives in the closure so walkers
// at different recursion depths share it transparently.
//
// extractRuns is injected by the caller (Phase 6's wire-up supplies
// a Page-bound extractor; tests can supply a stub). When extractRuns
// returns an error, the closure surfaces it to the caller without
// caching, so a transient failure can be retried on a subsequent
// call.
func newRunFor(extractRuns func(pageIndex int) ([]text.TextRun, layout.PageInfo, error)) runFor {
	cache := make(map[int]*pageRunIndex)
	return func(pageIndex int) (*pageRunIndex, error) {
		if idx, ok := cache[pageIndex]; ok {
			return idx, nil
		}
		runs, pageInfo, err := extractRuns(pageIndex)
		if err != nil {
			return nil, err
		}
		idx := newPageRunIndex(runs, pageInfo)
		cache[pageIndex] = idx
		return idx, nil
	}
}
