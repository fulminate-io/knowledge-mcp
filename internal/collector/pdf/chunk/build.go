// build.go — chunk.Build orchestration.
//
// Build walks the supplied Document, fetches per-page Blocks via the
// adapter, applies header/footer/footnote skip filters, runs cross-
// page continuity merge (Phase 3), dispatches on Mode (paragraph vs
// section), and applies the MinChunkChars post-filter.
//
// One-way dependency invariant: this package depends on layout +
// classify + stdlib only. The public collector/pdf.Document supplies
// a thin adapter satisfying the chunk.Document interface declared
// here so the layering points one way.
//
// Performance: two-phase. Phase A serializes pdfcpu access (one
// PageRuns call per page; pdfcpu's XRefTable mutates internal state on
// dereference and is not thread-safe). Phase B fans the pure-Go
// layout.Cluster + classify pass across runtime.NumCPU() workers —
// this is where the parallel speedup comes from. Phase C (chrome
// strip + cross-page merge + section build) is inherently serial
// because it consumes adjacent-page ordering.

package chunk

import (
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// Document is the minimal source surface chunk.Build pulls from. The
// public collector/pdf.Document supplies a documentAdapter satisfying
// this interface so the chunk package never imports the top-level
// package — the dependency points one way only.
//
// Two-phase pipeline: PageRuns is the only pdfcpu-touching method (text
// extraction + font decoding). Build calls PageRuns serially so the
// underlying pdfcpu XRefTable stays single-threaded, then runs
// layout.Cluster + classify across pages in parallel using runtime.NumCPU
// workers. HeadersFooters / Footnotes are also adapter-side reads —
// stubbed to ErrPageMethodNotImplemented in v1, so Build no-ops them.
type Document interface {
	// PageCount returns the number of pages in the source document.
	PageCount() int

	// PageRuns returns one page's decoded text runs + the page-info
	// envelope layout.ClusterWithParams needs. This is the
	// pdfcpu-touching half of per-page work; Build owns the cluster +
	// classify pass for parallelism.
	PageRuns(i int) (PageRuns, error)

	// PageHeadersFooters returns the page's header/footer blocks for
	// 0-indexed page i. May return ErrPageMethodNotImplemented when
	// the underlying source has not implemented this path yet —
	// Build treats that as "no headers/footers" and continues.
	PageHeadersFooters(i int) ([]layout.Block, error)

	// PageFootnotes returns the page's footnote blocks. Same
	// ErrPageMethodNotImplemented semantics as PageHeadersFooters.
	PageFootnotes(i int) ([]layout.Block, error)
}

// PageRuns is the per-page extraction handoff: the decoded text runs
// plus the page-info envelope layout.Cluster needs. Adapters return
// these from PageRuns; Build feeds them to layout.ClusterWithParams in
// the parallel phase. The Runs slice is owned by the producing
// adapter; Build does not retain references past the cluster + classify
// pass for any given page.
//
// Release is an optional teardown hook the adapter may set when it
// allocates pooled backing for the per-glyph slice fields on the
// runs (e.g. text.AcquireRunArena). Build invokes Release for every
// runset it received once the runs are no longer referenced — at the
// end of Build, after Chunks have been materialized. nil Release is
// tolerated (one-shot adapters that GC their arena).
type PageRuns struct {
	Runs     []text.TextRun
	PageInfo layout.PageInfo
	Release  func()
}

// BlockProvider is an optional interface that test fixtures can
// implement to inject pre-clustered, pre-classified layout blocks
// instead of going through the runs → cluster → classify pipeline.
// Build type-asserts the supplied Document and uses PageBlocks when
// the assertion succeeds; production adapters never implement this.
//
// PageBlocks returns the body blocks for page i; the chunker treats
// the result as already-classified (Block.Kind populated). Returning
// (nil, nil) means "empty page" — same as PageRuns returning a
// runset with zero runs.
type BlockProvider interface {
	PageBlocks(i int) ([]layout.Block, error)
}

// ErrPageMethodNotImplemented is the boundary sentinel adapters use
// to signal "the upstream source returned its own not-implemented
// error". Build honors this for SkipHeadersFooters and SkipFootnotes
// — both degrade to no-op when the underlying page method has not
// been wired up yet (T5 owns HeadersFooters / Footnotes; until T5
// ships, the public package's stub returns pdf.ErrNotImplemented and
// the adapter translates it to this sentinel).
var ErrPageMethodNotImplemented = errors.New("collector/pdf/chunk: page method not implemented")

// DefaultOptions holds the recommended Build configuration. Mode
// defaults to ModeSection (deliberately flipped from the ticket-
// pinned ModeParagraph): the primary downstream consumer is recipe-
// driven pattern extraction, where section-level context produces
// better signal than per-paragraph fragments. Recipe authors who
// want paragraph granularity can walk Children of section chunks, or
// override Mode explicitly.
var DefaultOptions = Options{
	Mode:               ModeSection,
	LayoutParams:       layout.DefaultLayoutParams,
	ClassifyParams:     classify.DefaultClassifyParams,
	SkipHeadersFooters: true,
	SkipFootnotes:      false,
	MinChunkChars:      0,
}

// Build runs the chunker over d. ModeParagraph emits a flat ordered
// slice (one Chunk per non-skipped block, post-merge); ModeSection
// emits a heading hierarchy with body blocks nested as Children of
// their enclosing heading.
//
// Headers/footers/footnotes filtering is best-effort — when the
// underlying Document.PageHeadersFooters or Document.PageFootnotes
// returns ErrPageMethodNotImplemented (T5 not yet shipped), Build
// silently treats the page as having no headers/footers/footnotes
// and continues. Locked Q1 / option (a): silent no-op until T5 ships.
//
// MinChunkChars semantics (locked Q4): chunks below the threshold
// are dropped entirely; merging into the next chunk is rejected
// because section-mode hierarchy makes "next chunk" ambiguous
// (siblings vs descendants). Drop semantics are predictable.
func Build(d Document, opts Options) ([]Chunk, error) {
	if d == nil {
		return nil, errors.New("collector/pdf/chunk: nil Document")
	}
	if opts.Mode == "" {
		opts.Mode = ModeSection
	}

	pageCount := d.PageCount()
	bp, hasBlockProvider := d.(BlockProvider)

	// Phase A — serial pdfcpu work (or BlockProvider direct fetch for
	// tests). Pull every page's runs + (stub) headers/footers/footnotes
	// while the pdfcpu XRefTable stays single-threaded.
	type pageInput struct {
		runs           PageRuns
		preClustered   []layout.Block // populated when source is BlockProvider
		headersFooters []layout.Block
		footnotes      []layout.Block
	}
	inputs := make([]pageInput, pageCount)

	// Release any pooled arenas the adapter attached to PageRuns once
	// Build returns. Chunks copy field-by-field into pure-Go strings +
	// Boxes (chunkFromMerged) and don't retain references to per-glyph
	// slice fields, so it's safe to recycle the arenas at this point.
	defer func() {
		for i := range inputs {
			if inputs[i].runs.Release != nil {
				inputs[i].runs.Release()
			}
		}
	}()
	for i := range pageCount {
		if hasBlockProvider {
			blocks, err := bp.PageBlocks(i)
			if err != nil {
				return nil, fmt.Errorf("collector/pdf/chunk: PageBlocks(%d): %w", i, err)
			}
			inputs[i].preClustered = blocks
		} else {
			runs, err := d.PageRuns(i)
			if err != nil {
				return nil, fmt.Errorf("collector/pdf/chunk: PageRuns(%d): %w", i, err)
			}
			inputs[i].runs = runs
		}
		if opts.SkipHeadersFooters {
			hf, err := d.PageHeadersFooters(i)
			if err == nil {
				inputs[i].headersFooters = hf
			} else if !errors.Is(err, ErrPageMethodNotImplemented) {
				return nil, fmt.Errorf("collector/pdf/chunk: PageHeadersFooters(%d): %w", i, err)
			}
		}
		if opts.SkipFootnotes {
			fn, err := d.PageFootnotes(i)
			if err == nil {
				inputs[i].footnotes = fn
			} else if !errors.Is(err, ErrPageMethodNotImplemented) {
				return nil, fmt.Errorf("collector/pdf/chunk: PageFootnotes(%d): %w", i, err)
			}
		}
	}

	// Phase B — parallel pure-Go cluster + classify per page. Workers
	// scale to runtime.NumCPU() but cap at pageCount for tiny docs.
	// BlockProvider sources skip the cluster + classify steps and feed
	// pre-clustered blocks directly.
	perPage := make([][]layout.Block, pageCount)
	clusterErrs := make([]error, pageCount)
	workers := runtime.NumCPU()
	if workers > pageCount {
		workers = pageCount
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int, pageCount)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				var blocks []layout.Block
				if hasBlockProvider {
					blocks = inputs[i].preClustered
				} else {
					b, err := layout.ClusterWithParams(inputs[i].runs.Runs, inputs[i].runs.PageInfo, opts.LayoutParams)
					if err != nil {
						clusterErrs[i] = err
						continue
					}
					blocks = classify.ClassifyWithParams(b, opts.ClassifyParams)
				}
				if len(inputs[i].headersFooters) > 0 {
					blocks = subtractBlocks(blocks, inputs[i].headersFooters)
				}
				if len(inputs[i].footnotes) > 0 {
					blocks = subtractBlocks(blocks, inputs[i].footnotes)
				}
				perPage[i] = blocks
			}
		}()
	}
	for i := range pageCount {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for i, err := range clusterErrs {
		if err != nil {
			return nil, fmt.Errorf("collector/pdf/chunk: cluster page %d: %w", i, err)
		}
	}

	if opts.SkipHeadersFooters {
		perPage = stripRepeatedChrome(perPage)
	}

	merged := mergeAcrossPages(perPage)

	var chunks []Chunk
	switch opts.Mode {
	case ModeParagraph:
		chunks = buildParagraphs(merged)
	default: // ModeSection (also catches the empty-mode normalization above)
		chunks = buildSections(merged)
	}

	if opts.MinChunkChars > 0 {
		chunks = filterByMinChars(chunks, opts.MinChunkChars)
	}
	return chunks, nil
}

// subtractBlocks returns body with any Block whose BBox+PageIndex
// matches a Block in skip removed. O(|body| × |skip|); typical skip
// count is 0..3 per page so the inner loop is trivial.
//
// PERF NOTE: if profiling shows |skip| growing >10 (multi-page
// running headers + footers + footnote section), swap to a
// map[layout.Rect]bool lookup for O(|body| + |skip|). Until then the
// linear inner loop wins (no map alloc per page, cache-friendly
// access pattern over a small slice).
func subtractBlocks(body, skip []layout.Block) []layout.Block {
	if len(skip) == 0 {
		return body
	}
	out := body[:0]
	for _, b := range body {
		drop := false
		for _, s := range skip {
			if b.PageIndex == s.PageIndex && b.BBox == s.BBox {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, b)
		}
	}
	return out
}

// filterByMinChars drops chunks whose Text length is below n.
// Recursively filters Children so section-mode hierarchy bodies are
// also scrubbed.
func filterByMinChars(in []Chunk, n int) []Chunk {
	out := in[:0]
	for _, c := range in {
		if len(c.Text) >= n {
			c.Children = filterByMinChars(c.Children, n)
			out = append(out, c)
		}
	}
	return out
}
