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
// layout.Cluster pass and then the classify pass across
// runtime.NumCPU() workers — this is where the parallel speedup comes
// from. Between those two parallel stages sits one serial walk,
// classify.CalibrateDocument, because the body-size reference the
// classifier compares against is a property of the whole document;
// classify.AssignHeadingLevelsDocument closes the phase for the same
// reason. Phase C (chrome stamp + cross-page merge + section build) is
// inherently serial because it consumes adjacent-page ordering.

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
// layout.Cluster and classify across pages in parallel using
// runtime.NumCPU workers.
type Document interface {
	// PageCount returns the number of pages in the source document.
	PageCount() int

	// PageRuns returns one page's decoded text runs + the page-info
	// envelope layout.ClusterWithParams needs. This is the
	// pdfcpu-touching half of per-page work; Build owns the cluster +
	// classify pass for parallelism.
	PageRuns(i int) (PageRuns, error)
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

// TaggedBlockProvider is the optional interface a source implements
// when it can read blocks from the document's own STRUCTURE TREE — the
// author's declaration of what each region is, which beats every
// heuristic when it is present, and is the only way a table is
// recognizable at all.
//
// PageTaggedBlocks returns (blocks, true, nil) when page i was read
// from the structure tree, and (nil, false, nil) when it was not — an
// untagged document, or a caller that has asked not to prefer the
// tagged read. The bool is what lets Build fall through to PageRuns
// per page rather than per document.
//
// This is a THIRD source method rather than a reuse of BlockProvider,
// and the difference is the whole point: BlockProvider's branch bypasses
// classification entirely, on the assumption that a test fixture
// injects blocks that are already classified. Structure-tree blocks are
// not. They carry a StructRole and nothing else, and they need the same
// classification pass every other block gets — which is safe, because
// the classifier skips a block whose Kind is already set and treats
// StructRole as authoritative when it is not.
type TaggedBlockProvider interface {
	PageTaggedBlocks(i int) ([]layout.Block, bool, error)
}

// DefaultOptions holds the recommended Build configuration. Mode
// defaults to ModeSection (deliberately flipped from the ticket-
// pinned ModeParagraph): the primary downstream consumer is recipe-
// driven pattern extraction, where section-level context produces
// better signal than per-paragraph fragments. Recipe authors who
// want paragraph granularity can walk Children of section chunks, or
// override Mode explicitly.
var DefaultOptions = Options{
	Mode:           ModeSection,
	LayoutParams:   layout.DefaultLayoutParams,
	ClassifyParams: classify.DefaultClassifyParams,
	MinChunkChars:  0,
}

// Build runs the chunker over d. ModeParagraph emits a flat ordered
// slice (one Chunk per block, post-merge); ModeSection emits a heading
// hierarchy with body blocks nested as Children of their enclosing
// heading.
//
// Nothing is dropped. Running page chrome is STAMPED with the signals
// that identify it (chunk/chrome.go) and emitted like any other block,
// so a consumer that wants it gone filters on those signals and one
// that wants it kept simply keeps it.
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
	tp, hasTaggedProvider := d.(TaggedBlockProvider)

	// Phase A — serial pdfcpu work (or BlockProvider direct fetch for
	// tests). Pull every page's runs, or its structure-tree blocks,
	// while the pdfcpu XRefTable stays single-threaded. The tagged read
	// belongs here for the same reason PageRuns does: it touches pdfcpu.
	type pageInput struct {
		runs         PageRuns
		preClustered []layout.Block // populated when source is BlockProvider
		tagged       []layout.Block // populated when the page was read from the structure tree
		isTagged     bool
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
			continue
		}
		if hasTaggedProvider {
			blocks, ok, err := tp.PageTaggedBlocks(i)
			if err != nil {
				return nil, fmt.Errorf("collector/pdf/chunk: PageTaggedBlocks(%d): %w", i, err)
			}
			if ok {
				inputs[i].tagged = blocks
				inputs[i].isTagged = true
				continue
			}
		}
		runs, err := d.PageRuns(i)
		if err != nil {
			return nil, fmt.Errorf("collector/pdf/chunk: PageRuns(%d): %w", i, err)
		}
		inputs[i].runs = runs
	}

	// Phase B — parallel pure-Go cluster per page, then a serial
	// document-wide calibration, then a parallel per-page classify, then
	// the document-wide heading rank. Workers scale to runtime.NumCPU()
	// but cap at pageCount for tiny docs. BlockProvider sources skip the
	// cluster step and feed pre-clustered blocks directly.
	//
	// The calibration sits BETWEEN the two parallel stages because the
	// classifier's body-size reference is a property of the whole
	// document, not of one page: an 8pt page inside a 10pt book must be
	// classified against 10pt or its 9pt captions read as headings. It
	// is a single serial O(total runs) walk.
	perPage := make([][]layout.Block, pageCount)
	clusterErrs := make([]error, pageCount)
	runPerPage(pageCount, func(i int) {
		switch {
		case hasBlockProvider:
			perPage[i] = inputs[i].preClustered
		case inputs[i].isTagged:
			// The structure-tree read already did its own residue
			// clustering inside HybridFallback, and re-clustering would
			// discard the roles it recovered. Classification still runs
			// over these below, like every other block.
			perPage[i] = inputs[i].tagged
		default:
			b, err := layout.ClusterWithParams(inputs[i].runs.Runs, inputs[i].runs.PageInfo, opts.LayoutParams)
			if err != nil {
				clusterErrs[i] = err
				return
			}
			perPage[i] = b
		}
	})
	for i, err := range clusterErrs {
		if err != nil {
			return nil, fmt.Errorf("collector/pdf/chunk: cluster page %d: %w", i, err)
		}
	}

	// Stamp repeated chrome BEFORE classification: the stamp is an
	// input to the two stages below — the code merge refuses to absorb a
	// stamped block, and the document heading rank excludes stamped
	// headings from its ranking population. Its position relative to
	// clustering does not change the fingerprint index, which is
	// computed from block text.
	stampRepeatedChrome(perPage)

	if !hasBlockProvider {
		dc := classify.CalibrateDocument(perPage)
		runPerPage(pageCount, func(i int) {
			perPage[i] = classify.ClassifyPage(perPage[i], opts.ClassifyParams, dc)
		})
		classify.AssignHeadingLevelsDocument(perPage)
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

// runPerPage fans fn across runtime.NumCPU() workers, one call per
// page index, and returns once every page has been visited. Workers
// cap at pageCount so a two-page document does not spawn a worker per
// core. This is the pool shape Phase B's cluster stage and classify
// stage share; fn is responsible for its own per-index writes (each
// call touches only index i, so no locking is needed).
func runPerPage(pageCount int, fn func(i int)) {
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
				fn(i)
			}
		}()
	}
	for i := range pageCount {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
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
