package pdf

import (
	"errors"
	"io"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/chunk"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/font"
	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/structtree"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// Document is the public consumer entry point for a loaded PDF. T1 ships
// the working open / page-count / page-metadata path; StructTree, Chunks
// and the Classify hooks are stubbed per the per-subsystem ownership
// rule and return ErrNotImplemented (or pass through) until their
// owning ticket lands.
type Document struct {
	ctx              *internalpdf.Context
	preferStructTree bool
	fontResolver     *font.FontResolver // lazy doc-scoped, shared across pages
	pageCache        []*Page            // index → Page; nil entry = not yet constructed
	runArena         *text.RunArena     // doc-scoped arena backing all pages' run slices; reset per Chunks
}

// Open parses a PDF from r. Non-seekable readers are buffered in memory
// by the wrapper. Returns ErrEncrypted when the input is
// password-protected.
func Open(r io.Reader) (*Document, error) {
	ctx, err := internalpdf.Load(r)
	if err != nil {
		return nil, mapErr(err)
	}
	return newDocument(ctx), nil
}

// OpenFile opens path and parses it as a PDF. Returns ErrEncrypted when
// the input is password-protected, or any underlying open / parse error
// from the file system or pdfcpu.
func OpenFile(path string) (*Document, error) {
	ctx, err := internalpdf.LoadFile(path)
	if err != nil {
		return nil, mapErr(err)
	}
	return newDocument(ctx), nil
}

// newDocument is the shared constructor for Document. preferStructTree
// defaults to true — when a PDF is tagged, prefer the structure tree
// over geometric layout. T6 wires this into Chunks().
func newDocument(ctx *internalpdf.Context) *Document {
	return &Document{ctx: ctx, preferStructTree: true}
}

// fontDecodeResolver returns the lazily-allocated doc-scoped
// FontResolver. The cache is keyed on (BaseFont, ToUnicode hash) so
// page N's fonts are reused on pages N+1, N+2, ... without re-parsing
// the CMap or rebuilding the decoder. Avoids the 491-pages × per-page
// rebuild that dominated CPU + heap on large documents.
func (d *Document) fontDecodeResolver() *font.FontResolver {
	if d.fontResolver == nil {
		d.fontResolver = font.NewDocResolver(d.ctx)
	}
	return d.fontResolver
}

// mapErr translates internal/pdfcpu sentinels onto public sentinels at
// the package boundary. Anything else is returned verbatim.
func mapErr(err error) error {
	if errors.Is(err, internalpdf.ErrEncrypted) {
		return ErrEncrypted
	}
	return err
}

// Close releases the underlying pdfcpu context. The doc-scoped run
// arena is dropped to the GC; pre-sizing on construction means we
// don't need cross-document pooling (the grow loop is amortized
// within one Chunks call, not across documents).
func (d *Document) Close() error {
	if d == nil {
		return nil
	}
	d.runArena = nil
	if d.ctx == nil {
		return nil
	}
	return d.ctx.Close()
}

// PageCount returns the number of pages in the document.
func (d *Document) PageCount() int {
	if d == nil || d.ctx == nil {
		return 0
	}
	return d.ctx.PageCount()
}

// Page returns a Page handle for the 0-indexed page i. Cached:
// chunk.Build's documentAdapter can call Page(i) more than once per
// page, and each call previously triggered a fresh pdfcpu page-tree
// walk in internalpdf.Context.Page. The first construction caches the
// *Page keyed by index; subsequent calls return the cached instance.
func (d *Document) Page(i int) (*Page, error) {
	if d == nil || d.ctx == nil {
		return nil, errors.New("cmd/knowledge/internal/collector/pdf: nil document")
	}
	if d.pageCache == nil {
		d.pageCache = make([]*Page, d.ctx.PageCount())
	}
	if i >= 0 && i < len(d.pageCache) && d.pageCache[i] != nil {
		return d.pageCache[i], nil
	}
	inner, err := d.ctx.Page(i)
	if err != nil {
		return nil, err
	}
	p := newPage(d, inner)
	if i >= 0 && i < len(d.pageCache) {
		d.pageCache[i] = p
	}
	return p, nil
}

// IsTagged reports whether the document declares a PDF structure tree
// (tagged PDF). Untagged documents force the geometric layout path; T6
// uses this together with SetPreferStructTree to decide which path to
// use during Chunks().
func (d *Document) IsTagged() bool {
	if d == nil || d.ctx == nil {
		return false
	}
	return d.ctx.IsTagged()
}

// Metadata returns the document Info-dict metadata. Missing fields
// return their zero value (empty string / zero time.Time).
func (d *Document) Metadata() Metadata {
	if d == nil || d.ctx == nil {
		return Metadata{}
	}
	return Metadata{
		Title:        d.ctx.Title(),
		Author:       d.ctx.Author(),
		Subject:      d.ctx.Subject(),
		Keywords:     d.ctx.Keywords(),
		Producer:     d.ctx.Producer(),
		Creator:      d.ctx.Creator(),
		CreationDate: d.ctx.CreationDate(),
		ModDate:      d.ctx.ModDate(),
	}
}

// SetPreferStructTree configures whether Chunks() prefers the PDF
// structure tree (tagged PDFs) over geometric layout. Default true. T6
// reads this when wiring the structtree-vs-layout selection.
func (d *Document) SetPreferStructTree(prefer bool) {
	if d == nil {
		return
	}
	d.preferStructTree = prefer
}

// PreferStructTree reports the current SetPreferStructTree configuration.
// Internal helper exposed primarily for T6 testing.
func (d *Document) PreferStructTree() bool {
	if d == nil {
		return false
	}
	return d.preferStructTree
}

// StructTree returns the document's PDF structure-tree root. T6 fills
// this; T1 stubs return ErrNotImplemented.
// StructTree returns the document's tagged-PDF structure tree as a
// structtree.Element root. Returns structtree.ErrNotTagged when the
// catalog has no /StructTreeRoot. T6 fills the body — earlier
// revisions stubbed ErrNotImplemented.
//
// The implementation delegates to structtree.Tree (whole-document
// view, no per-page filter). Page-scoped Block extraction lives on
// Page.Blocks via structtree.Walk(ctx, pageIdx).
func (d *Document) StructTree() (*structtree.Element, error) {
	if d == nil || d.ctx == nil {
		return nil, errors.New("cmd/knowledge/internal/collector/pdf: nil document")
	}
	return structtree.Tree(d.ctx)
}

// Chunks runs the chunker over the document, returning a slice of
// Chunk values per opts. Wired via an unexported documentAdapter
// that satisfies chunk.Document.
//
// The doc-scoped run arena is acquired lazily and reset to len 0 at
// the start of every Chunks call so successive calls don't accumulate
// stale glyph/bound/flag entries from previous runs. The arena
// returns to its sync.Pool on Document.Close.
func (d *Document) Chunks(opts ChunkOptions) ([]Chunk, error) {
	if d == nil {
		return nil, errors.New("cmd/knowledge/internal/collector/pdf: nil document")
	}
	if d.runArena == nil {
		d.runArena = text.NewRunArenaForPages(d.PageCount())
	} else {
		text.ResetRunArena(d.runArena)
	}
	return chunk.Build(documentAdapter{d: d, opts: opts}, opts)
}

// documentAdapter satisfies chunk.Document by calling through to
// *Document's existing methods. Unexported because the chunk
// package's Build expects an interface; concrete adapter shape is
// internal to the pdf package.
//
// The adapter pairs with chunk.Build's two-phase pipeline: PageRuns
// is the only pdfcpu-touching call (Phase A, serial); cluster +
// classify happen inside Build's Phase B worker pool.
type documentAdapter struct {
	d    *Document
	opts ChunkOptions
}

func (a documentAdapter) PageCount() int { return a.d.PageCount() }

func (a documentAdapter) PageRuns(i int) (chunk.PageRuns, error) {
	p, err := a.d.Page(i)
	if err != nil {
		return chunk.PageRuns{}, err
	}
	// Doc-scoped arena: every page's runs append into the same backing
	// slabs, so the pool only sees one acquire/release per Document
	// (vs one per page). Owned by Document.Chunks; PageRuns called
	// outside that path (e.g. ad-hoc adapter use in tests) gets a
	// lazy on-demand arena that lives until Document.Close.
	if a.d.runArena == nil {
		a.d.runArena = text.NewRunArenaForPages(a.d.PageCount())
	}
	runs, err := p.TextRunsInto(a.d.runArena)
	if err != nil {
		return chunk.PageRuns{}, err
	}
	mb := p.MediaBox()
	return chunk.PageRuns{
		Runs: runs,
		PageInfo: layout.PageInfo{
			PageIndex: p.inner.Index(),
			MediaBox:  layout.Rect{X0: mb.X0, Y0: mb.Y0, X1: mb.X1, Y1: mb.Y1},
			Rotation:  p.Rotation(),
		},
		// Release intentionally nil — the arena is owned by the
		// Document and is reset/released in (*Document).Chunks /
		// (*Document).Close, not per-page.
	}, nil
}

// PageTaggedBlocks implements chunk.TaggedBlockProvider. It reports
// ok=false — meaning "read this page from PageRuns instead" — unless
// the document is tagged AND the caller prefers the structure-tree
// read; otherwise it returns the structure-tree walk merged with its
// clustered residue, in reading order.
//
// The runs it needs go through the DOCUMENT-scoped arena rather than a
// private per-page one, so a tagged document costs the pool a single
// acquire like an untagged one does.
func (a documentAdapter) PageTaggedBlocks(i int) ([]Block, bool, error) {
	if a.d == nil || !a.d.IsTagged() || !a.d.PreferStructTree() {
		return nil, false, nil
	}
	p, err := a.d.Page(i)
	if err != nil {
		return nil, false, err
	}
	if a.d.runArena == nil {
		a.d.runArena = text.NewRunArenaForPages(a.d.PageCount())
	}
	blocks, err := p.blocksFromStructTreeInto(a.d.runArena)
	if err != nil {
		return nil, false, err
	}
	return blocks, true, nil
}

// Classify applies the default heading / list / code classifier to the
// supplied layout blocks and returns them annotated. It annotates the
// supplied blocks in place, but the RETURNED slice is not necessarily
// the one passed in: the classifier stitches fragmented code blocks and
// rejoins torn headings, so the result can be shorter. Use the return
// value; do not assume the argument now holds the answer.
func (d *Document) Classify(blocks []Block) []Block {
	return classify.Classify(blocks)
}

// ClassifyWithParams is the parameterised Classify; per-field zero
// values are substituted with DefaultClassifyParams entries.
func (d *Document) ClassifyWithParams(blocks []Block, params ClassifyParams) []Block {
	return classify.ClassifyWithParams(blocks, params)
}
