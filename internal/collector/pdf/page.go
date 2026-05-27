package pdf

import (
	"errors"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/font"
	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/structtree"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// textRunAdapter wraps a *text.TextRun so it satisfies the
// font.Run interface (font cannot import text — see resolver.go's
// Run interface for the layering rationale).
type textRunAdapter struct {
	r *text.TextRun
}

func (a textRunAdapter) GlyphsCopy() []uint16 { return a.r.Glyphs }
func (a textRunAdapter) FontKeyValue() string { return a.r.FontKey }
func (a textRunAdapter) FontResourcesHint() internalpdf.FormResources {
	return a.r.FontResourcesHint()
}
func (a textRunAdapter) SetText(s string) { a.r.Text = s }

// SetCharFlags merges resolver-supplied per-glyph flag bits into the
// run's existing CharFlags slice via bitwise-OR. The merge preserves
// flags T2 set at content-stream walk time (e.g. CharFlagMarkedContent
// from BMC/BDC regions) while letting the resolver layer add bits like
// CharFlagBadMap for rung-4 hits. When the run has no prior CharFlags
// (legacy or test-constructed runs), the supplied slice is assigned
// directly.
func (a textRunAdapter) SetCharFlags(f []uint8) {
	if len(f) == 0 {
		return
	}
	if len(a.r.CharFlags) == len(f) {
		for i, b := range f {
			a.r.CharFlags[i] |= b
		}
		return
	}
	a.r.CharFlags = f
}

// Page is a single PDF page, returned by Document.Page. T1 ships the
// working Number / MediaBox / Rotation accessors; the layout / text /
// reading-order entry points are stubs that return ErrNotImplemented
// until their owning ticket lands.
type Page struct {
	doc   *Document
	inner *internalpdf.PageObject
}

// newPage is the shared constructor used by Document.Page.
func newPage(doc *Document, inner *internalpdf.PageObject) *Page {
	return &Page{doc: doc, inner: inner}
}

// Number returns the 1-indexed page number (Page 1 is the first page),
// suitable for human display. The internal/pdfcpu wrapper indexes from
// 0; the +1 conversion happens here so the public API matches user
// expectations.
func (p *Page) Number() int {
	if p == nil || p.inner == nil {
		return 0
	}
	return p.inner.Index() + 1
}

// MediaBox returns the page's media box (the bounding rectangle of the
// physical page). Converts the wrapper's internal Rect to the public
// pdf.Rect on the boundary — internal/pdfcpu cannot import pdf (cycle),
// so the public package owns Rect and converts at the entry points.
func (p *Page) MediaBox() Rect {
	if p == nil || p.inner == nil {
		return Rect{}
	}
	mb := p.inner.MediaBox()
	return Rect{X0: mb.X0, Y0: mb.Y0, X1: mb.X1, Y1: mb.Y1}
}

// Rotation returns the page rotation in degrees (0 / 90 / 180 / 270).
func (p *Page) Rotation() int {
	if p == nil || p.inner == nil {
		return 0
	}
	return p.inner.Rotation()
}

// TextRuns returns the page's positioned text runs in content-stream
// order. Chains text.ExtractRuns (T2: content-stream walk) +
// font.Decode (T3: glyph→Unicode resolution via the 7-rung ladder).
// Each returned TextRun has its Text field populated with UTF-8
// extracted from the page's font dicts.
//
// One-shot path: each call allocates a private RunArena that is
// dropped to the GC when the returned runs go out of scope. Pipeline
// callers extracting many pages should use TextRunsInto with a pooled
// arena to keep per-glyph slice allocations off the heap.
func (p *Page) TextRuns() ([]TextRun, error) {
	return p.TextRunsInto(&text.RunArena{})
}

// TextRunsInto is the arena-aware variant. The supplied arena backs
// every returned run's Glyphs / CharBounds / CharFlags slice fields;
// callers MUST keep it live for as long as those slices are read,
// then release it via text.ReleaseRunArena once consumers (typically
// chunk.Build) are done.
func (p *Page) TextRunsInto(arena *text.RunArena) ([]TextRun, error) {
	if p == nil || p.inner == nil {
		return nil, ErrNotImplemented
	}
	runs, err := text.ExtractRunsInto(p.inner, text.ExtractOptions{}, arena)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return runs, nil
	}
	wrapped := make([]font.Run, len(runs))
	for i := range runs {
		wrapped[i] = textRunAdapter{r: &runs[i]}
	}
	resolver := p.doc.fontDecodeResolver()
	if err := resolver.DecodePage(wrapped, p.inner); err != nil {
		return nil, err
	}
	return runs, nil
}

// Blocks returns the page's blocks. Routing:
//   - Tagged document with PreferStructTree=true: walks /StructTreeRoot
//     (per-page pageFilter) and merges with HybridFallback to capture
//     untagged residue (figures with surrounding prose, etc.).
//   - Otherwise: pure heuristic clustering via BlocksWithParams.
//
// The pageFilter passed to structtree.Walk prunes at LEAF emit time
// inside the walker — there is no post-walk filter loop. T6 reviewer
// fix T2.3.
func (p *Page) Blocks() ([]Block, error) {
	if p == nil || p.inner == nil {
		return nil, ErrNotImplemented
	}
	if p.doc != nil && p.doc.IsTagged() && p.doc.PreferStructTree() {
		return p.blocksFromStructTree()
	}
	return p.BlocksWithParams(layout.DefaultLayoutParams)
}

// blocksFromStructTree is the tagged-PDF Block-extraction path. It
// invokes structtree.Walk with the per-page pageFilter so the walker
// emits only this page's blocks, then merges with the page's residue
// runs through structtree.HybridFallback.
//
// Defensive ErrNotTagged path: when IsTagged() returned true but
// Walk surfaces ErrNotTagged (a desync between pdfcpu's Tagged flag
// and the catalog's /StructTreeRoot entry), surface a slog.Warn
// before falling back to the heuristic clusterer so the desync is
// observable. T6 reviewer fix T4.10.
func (p *Page) blocksFromStructTree() ([]Block, error) {
	pageIdx := p.inner.Index()
	pageBlocks, err := structtree.Walk(p.doc.ctx, pageIdx)
	if err != nil {
		if errors.Is(err, structtree.ErrNotTagged) {
			slog.Warn("pdf: blocksFromStructTree fell back to heuristic; IsTagged=true but Walk returned ErrNotTagged",
				"page", pageIdx)
			return p.BlocksWithParams(layout.DefaultLayoutParams)
		}
		return nil, err
	}
	runs, err := p.TextRuns()
	if err != nil {
		return nil, err
	}
	mb := p.MediaBox()
	info := layout.PageInfo{
		PageIndex: pageIdx,
		MediaBox:  layout.Rect{X0: mb.X0, Y0: mb.Y0, X1: mb.X1, Y1: mb.Y1},
		Rotation:  p.Rotation(),
	}
	return structtree.HybridFallback(pageBlocks, runs, info)
}

// BlocksWithParams is the parameterised Blocks variant. Same shape
// as Blocks but with a caller-supplied LayoutParams.
func (p *Page) BlocksWithParams(params LayoutParams) ([]Block, error) {
	if p == nil || p.inner == nil {
		return nil, ErrNotImplemented
	}
	runs, err := p.TextRuns()
	if err != nil {
		return nil, err
	}
	mb := p.MediaBox()
	page := layout.PageInfo{
		PageIndex: p.inner.Index(),
		MediaBox:  layout.Rect{X0: mb.X0, Y0: mb.Y0, X1: mb.X1, Y1: mb.Y1},
		Rotation:  p.Rotation(),
	}
	return layout.ClusterWithParams(runs, page, params)
}

// ReadingOrder returns the page's blocks ordered by reading-order
// heuristics (top-to-bottom, multi-column-aware). T5 fills; T1 stubs.
func (p *Page) ReadingOrder() ([]Block, error) {
	return nil, ErrNotImplemented
}

// HeadersFooters returns the page's header and footer blocks. T5
// fills; T1 stubs.
func (p *Page) HeadersFooters() ([]Block, error) {
	return nil, ErrNotImplemented
}

// Footnotes returns the page's footnote blocks. T5 fills; T1 stubs.
func (p *Page) Footnotes() ([]Block, error) {
	return nil, ErrNotImplemented
}
