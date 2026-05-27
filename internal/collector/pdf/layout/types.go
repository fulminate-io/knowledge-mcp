package layout

import "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"

// Rect is the layout-package's local copy of a PDF rectangle. The public
// pdf.Rect mirrors this struct field-for-field; the top-level package
// converts on the boundary. The duplication is deliberate — importing
// the top-level pdf package from a sub-package would create an import
// cycle.
type Rect struct {
	X0, Y0, X1, Y1 float64
}

// BlockKind tags the role of a layout block. T1 pins the 6-constant
// surface; T5 (classify) populates the values during heading / list /
// caption classification.
type BlockKind string

const (
	// BlockUnknown is the default — the classifier has not yet decided
	// (or could not decide) the role of the block.
	BlockUnknown BlockKind = ""

	// BlockHeading marks a heading line / paragraph (chapter, section,
	// subsection — heading level lives on Block.HeadingLevel).
	BlockHeading BlockKind = "heading"

	// BlockParagraph marks running prose.
	BlockParagraph BlockKind = "paragraph"

	// BlockCode marks a code block (monospace font, indent regularity).
	BlockCode BlockKind = "code_block"

	// BlockListItem marks a list item (bulleted, numbered, lettered).
	BlockListItem BlockKind = "list_item"

	// BlockTable marks a layout block that is part of a table cell or
	// table region. Full table-grid reconstruction is out of scope for
	// v1 — BlockTable signals "this region looks like a table" so the
	// chunker can flag it accordingly.
	BlockTable BlockKind = "table"
)

// Line is a single horizontal text line within a Block. It carries the
// raw TextRun sequence in left-to-right order plus the line bounding box
// and a dehyphenation flag for joins across a hyphenated line break.
type Line struct {
	// Runs holds the line's text runs in left-to-right reading order.
	Runs []text.TextRun

	// BBox is the line bounding box in user-space coordinates.
	BBox Rect

	// WasDehyphenated is true when the layout grouper merged a trailing
	// hyphenated word onto the next line; T4 sets, downstream readers
	// can use it to suppress the join hyphen if needed.
	WasDehyphenated bool
}

// Block is a layout-aware grouping of Lines that share role, font, and
// reading-order continuity. T1 pins the 11-field surface; T4 fills the
// geometric fields, T5 fills classify fields, T6 fills StructRole, T7
// reads HeadingLevel + Metadata.
type Block struct {
	// Kind identifies the block role (heading, paragraph, code, etc).
	// Empty BlockUnknown until T5 classifies.
	Kind BlockKind

	// BBox is the block bounding box in user-space coordinates.
	BBox Rect

	// Lines holds the block's lines in reading order.
	Lines []Line

	// PageIndex is the 0-indexed page the block was extracted from.
	PageIndex int

	// StructRole is the structure-tree role (e.g. "P", "H1", "Figure")
	// when the document is tagged. Populated by T6; empty when the
	// document is untagged or a tagged read isn't preferred.
	StructRole string

	// HeadingLevel is the heading depth (1 = top-level, 2 = subsection,
	// ...). Zero for non-heading blocks. Populated by T7's chunker (it
	// uses Kind + StructRole + size hierarchy).
	HeadingLevel int

	// Column is the 0-indexed column the block lives in (for
	// multi-column page layouts). Populated by T5's column detector.
	Column int

	// IsHeader is true when the classifier determined the block is a
	// page header (running title at the top of every page).
	IsHeader bool

	// IsFooter is true when the classifier determined the block is a
	// page footer (running title / page number at the bottom).
	IsFooter bool

	// IsFootnote is true when the classifier determined the block is a
	// footnote (smaller text below a separator line near the bottom).
	IsFootnote bool

	// Metadata is a free-form key/value map for any extra annotations
	// the chunker may attach (e.g. caption→figure ref, list-marker
	// type). Populated by T7.
	Metadata map[string]string
}

// LayoutParams tunes the layout grouper. T1 pinned the 9-field surface
// modeled on pdfminer.six's LAParams shape; T4 added the 10th field
// (ParagraphGapRatio) — pdfminer.six conflates line-clustering
// tolerance and paragraph-break threshold under a single line_margin,
// while T4 splits them, which empirically improves accuracy on
// real-world PDFs.
type LayoutParams struct {
	// LineMargin is the line-clustering Y-tolerance ratio applied
	// against the per-page medianHeight (yTolerance =
	// medianHeight × LineMargin). Default 0.4, empirically tuned
	// for the median-based denominator. pdfminer.six's line_margin
	// = 0.5 (layout.py:80) is intentionally rejected because it was
	// tuned for a per-line height denominator — a different basis
	// than the median-based normalization used here.
	LineMargin float64

	// CharMargin is the maximum horizontal gap (× avg_char_width)
	// between two adjacent items considered close enough to be in the
	// same word/run. pdfminer.six default 2.0 (layout.py:79). X-axis
	// threshold; per-line basis is intrinsic, so the median vs
	// per-line distinction does not apply.
	CharMargin float64

	// WordMargin is the horizontal gap (× avg_char_width) above which
	// two consecutive runs on the same line are treated as separate
	// words (and a space token is inserted between them). pdfminer.six
	// default 0.1 (layout.py:81).
	WordMargin float64

	// BoxesFlow tunes the reading-order trade-off between geometric
	// proximity and natural-reading ordering. -1.0 = pure geometric;
	// +1.0 = pure top-to-bottom-left-to-right. pdfminer.six default
	// 0.5 (layout.py:82). T5's reading-order pass consumes this; T4
	// stores it for forward compatibility.
	BoxesFlow float64

	// ParagraphGapRatio is the paragraph-break Y-gap ratio applied
	// against the per-page medianGap (paragraphGap =
	// medianGap × ParagraphGapRatio). Default 1.6, empirically
	// tuned for the median-based denominator. NEW field added by
	// T4 — pdfminer.six has no analog; pdfminer.six's line_margin
	// conflates line-clustering and block-break, while T4 splits
	// them.
	ParagraphGapRatio float64

	// DetectVertical enables detection of vertical text runs (CJK
	// scripts, rotated labels). pdfminer.six default false
	// (layout.py:83). T4 leaves false in v1 (Latin-script focus).
	DetectVertical bool

	// ColumnMinSeparation is the minimum horizontal whitespace (in
	// points) between two columns for them to be detected as separate.
	// Owned by T5; T4 leaves zero.
	ColumnMinSeparation float64

	// ColumnTolerance is the per-line jitter (in points) tolerated
	// when matching lines into the same column. Owned by T5; T4
	// leaves zero.
	ColumnTolerance float64

	// HeaderFooterMargin is the top/bottom margin (in points) within
	// which blocks are candidates for header/footer classification.
	// Owned by T5; T4 leaves zero.
	HeaderFooterMargin float64

	// DetectColumns enables the multi-column detection pass. Owned by
	// T5; T4 leaves false.
	DetectColumns bool
}

// DefaultLayoutParams holds the T4 defaults: pdfminer.six's LAParams
// structure with median-based threshold values. T5 fields
// (ColumnMinSeparation, ColumnTolerance, HeaderFooterMargin,
// DetectColumns) remain zero — T5 will populate them when its work
// lands.
var DefaultLayoutParams = LayoutParams{
	LineMargin:        0.4,
	CharMargin:        2.0, // pdfminer.six char_margin
	WordMargin:        0.1, // pdfminer.six word_margin
	BoxesFlow:         0.5, // pdfminer.six boxes_flow
	ParagraphGapRatio: 1.6,
	DetectVertical:    false,
}
