package chunk

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// Rect is the chunk-package's local copy of a PDF rectangle. Like the
// layout / structtree / internal/pdfcpu copies, it duplicates pdf.Rect
// to avoid importing the top-level pdf package (which would cycle
// through aliases.go).
type Rect struct {
	X0, Y0, X1, Y1 float64
}

// Mode picks the chunker's grouping granularity.
type Mode string

const (
	// ModeParagraph emits one Chunk per layout block (paragraph,
	// heading, code, list-item). Default for retrieval-augmented
	// generation.
	ModeParagraph Mode = "paragraph"

	// ModeSection groups every block under its enclosing heading into
	// a single Chunk per section. Children carry the per-block
	// breakdown. Useful when you want section-aware embeddings.
	ModeSection Mode = "section"
)

// Chunk is a single output record produced by the chunker. T1 pins the
// 8-field surface; T7 fills the values during chunking.
type Chunk struct {
	// Kind mirrors the layout block kind (heading, paragraph, code,
	// list-item, table, unknown). Empty BlockUnknown when the chunker
	// could not classify.
	Kind layout.BlockKind

	// Text is the decoded plain-text body of the chunk.
	Text string

	// BBox is the bounding box of the chunk in the first page's user
	// space. For multi-page chunks, this is the bbox on PageRange[0].
	BBox Rect

	// PageRange is [first, last] 0-indexed page range the chunk spans.
	// Both equal for single-page chunks.
	PageRange [2]int

	// HeadingLevel is the heading depth (1 = top-level) for heading
	// chunks. 0 for non-heading chunks.
	//
	// The level is a RANK of the DOCUMENT's distinct heading sizes, so
	// it is comparable across pages: the largest heading size anywhere
	// in the document is level 1 whatever page it appears on, and a
	// smaller size on an earlier page ranks below it. It is not a
	// within-page rank, and it is not an absolute point size.
	HeadingLevel int

	// Children are sub-chunks (e.g. paragraphs nested under a section
	// heading in ModeSection). Nil for leaf chunks.
	Children []Chunk

	// Metadata is a free-form key/value map of chunker annotations
	// (caption→figure ref, list-marker type, etc.).
	Metadata map[string]string

	// StructRole is the structure-tree role (e.g. "P", "H1", "Figure")
	// when the source document is tagged. Empty for untagged docs or
	// when a tagged read isn't preferred.
	StructRole string
}

// Options tunes the chunker. Mode picks the grouping granularity;
// LayoutParams + ClassifyParams flow through to the layout grouper +
// classifier; MinChunkChars is a post-process filter applied during
// chunk emission.
type Options struct {
	// Mode picks paragraph- vs section-grouping (default ModeParagraph
	// when zero — empty Mode is treated as ModeParagraph by T7).
	Mode Mode

	// LayoutParams tunes the layout grouper's geometric grouping.
	LayoutParams layout.LayoutParams

	// ClassifyParams tunes the heading / list / code classifier.
	ClassifyParams classify.ClassifyParams

	// MinChunkChars drops chunks shorter than this many characters
	// after concatenation. 0 disables the filter.
	MinChunkChars int
}
