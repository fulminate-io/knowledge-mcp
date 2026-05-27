package chunk

import (
	"errors"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// Test fixtures shared across the chunk_test.go suite. Kept in a
// separate file so chunk_test.go itself can hold the 300-line cap.

// mkRun builds a TextRun with the supplied text + style. The chunk
// package's tests do not exercise body-font calibration (that's
// classify's domain), so size/font are kept simple — set explicitly
// only when a test cares (e.g. continuity heuristic).
func mkRun(size float64, font string, txt string) text.TextRun {
	return text.TextRun{
		Text:     txt,
		X:        72,
		Y:        700,
		Width:    float64(len(txt)) * 6,
		Height:   size,
		Size:     size,
		FontKey:  font,
		FontName: font,
	}
}

// txtRun is the minimal-shape TextRun helper for tests that only care
// about decoded text content (normalize tests).
func txtRun(s string) text.TextRun {
	return mkRun(12, "Helvetica", s)
}

// mkBlock wraps runs into a single-line block on page 0.
func mkBlock(kind layout.BlockKind, runs ...text.TextRun) layout.Block {
	bbox := layout.Rect{X0: 72, Y0: 700, X1: 540, Y1: 712}
	return layout.Block{
		Kind:  kind,
		Lines: []layout.Line{{Runs: runs, BBox: bbox}},
		BBox:  bbox,
	}
}

// mkMultiLineBlock stacks one Line per supplied run-slice with each
// line 14pt below the previous (top-down). Useful for code-block
// normalization (multi-line, newlines preserved). Page 0.
func mkMultiLineBlock(kind layout.BlockKind, lines [][]text.TextRun) layout.Block {
	out := make([]layout.Line, len(lines))
	y0 := 700.0
	curY := y0
	for i, runs := range lines {
		out[i] = layout.Line{
			Runs: runs,
			BBox: layout.Rect{X0: 72, Y0: curY, X1: 540, Y1: curY + 12},
		}
		curY += 14
	}
	return layout.Block{
		Kind:  kind,
		Lines: out,
		BBox:  layout.Rect{X0: 72, Y0: y0, X1: 540, Y1: curY},
	}
}

// fakeDoc satisfies the chunk.Document interface for testing Build.
// pages is indexed by page number; headersFooters and footnotes are
// per-page maps (nil entry = empty); the *Err fields let tests
// inject error conditions (notably ErrPageMethodNotImplemented).
type fakeDoc struct {
	pages          [][]layout.Block
	headersFooters map[int][]layout.Block
	footnotes      map[int][]layout.Block
	headersErr     map[int]error
	footnotesErr   map[int]error
}

func (f *fakeDoc) PageCount() int { return len(f.pages) }

// PageBlocks is the BlockProvider-interface entry: tests inject
// pre-clustered, pre-classified blocks here so chunk.Build skips the
// runs → cluster → classify pipeline. Production adapters use
// PageRuns instead.
func (f *fakeDoc) PageBlocks(i int) ([]layout.Block, error) {
	if i < 0 || i >= len(f.pages) {
		return nil, errors.New("page out of range")
	}
	return f.pages[i], nil
}

// PageRuns is required by the chunk.Document interface but unused by
// fakeDoc — the BlockProvider type assertion makes Build pick
// PageBlocks instead. Returns an empty runset so misuse is caught
// (cluster on zero runs produces zero blocks).
func (f *fakeDoc) PageRuns(_ int) (PageRuns, error) {
	return PageRuns{}, nil
}

func (f *fakeDoc) PageHeadersFooters(i int) ([]layout.Block, error) {
	if err, ok := f.headersErr[i]; ok {
		return nil, err
	}
	return f.headersFooters[i], nil
}

func (f *fakeDoc) PageFootnotes(i int) ([]layout.Block, error) {
	if err, ok := f.footnotesErr[i]; ok {
		return nil, err
	}
	return f.footnotes[i], nil
}

// syntheticHierarchyFixture returns a single-page block stream with
// 1 H1 + 2 paragraphs + 1 H2 + 1 paragraph + 1 code block. Used by
// the section-mode round-trip property test.
func syntheticHierarchyFixture() []layout.Block {
	return []layout.Block{
		mkBlock(layout.BlockHeading, txtRun("Chapter 1")),
		mkBlock(layout.BlockParagraph, txtRun("First paragraph of chapter 1.")),
		mkBlock(layout.BlockParagraph, txtRun("Second paragraph of chapter 1.")),
		mkBlock(layout.BlockHeading, txtRun("Section 1.1")),
		mkBlock(layout.BlockParagraph, txtRun("Body of section 1.1.")),
		mkMultiLineBlock(layout.BlockCode, [][]text.TextRun{
			{txtRun("line1")},
			{txtRun("line2")},
			{txtRun("line3")},
		}),
	}
}

// bodyTextsFlat collects Text from non-heading non-empty chunks in
// the supplied flat slice. Drops headings (no body content) and
// any empty-Text entries (synthetic root in section mode).
func bodyTextsFlat(cs []Chunk) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if c.Kind == layout.BlockHeading || c.Text == "" {
			continue
		}
		out = append(out, c.Text)
	}
	return out
}

// bodyTextsRecursive walks Children depth-first; same filter as
// bodyTextsFlat. Used to flatten section-mode output for round-trip
// comparison against paragraph-mode output.
func bodyTextsRecursive(cs []Chunk) []string {
	var out []string
	var walk func(cs []Chunk)
	walk = func(cs []Chunk) {
		for _, c := range cs {
			if c.Kind != layout.BlockHeading && c.Text != "" {
				out = append(out, c.Text)
			}
			walk(c.Children)
		}
	}
	walk(cs)
	return out
}

// sameTextMultiset compares two []string ignoring order — sort both
// then compare element-wise.
func sameTextMultiset(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aSorted := append([]string(nil), a...)
	bSorted := append([]string(nil), b...)
	sort.Strings(aSorted)
	sort.Strings(bSorted)
	for i := range aSorted {
		if aSorted[i] != bSorted[i] {
			return false
		}
	}
	return true
}
