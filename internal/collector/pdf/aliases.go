package pdf

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/chunk"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/structtree"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// Type aliases re-export the high-traffic sub-package types into the
// top-level pdf namespace so consumers can `import collector/pdf` and
// use pdf.TextRun, pdf.Block, etc. without pulling in every sub-package.
// Go's `type X = Y` preserves type identity — pdf.TextRun and
// text.TextRun are interchangeable at the type system level (verified
// by the compile-time identity assertions in aliases_test.go).
//
// Rect and Metadata are owned at the top level (types.go) rather than
// aliased; sub-packages duplicate the Rect shape locally to avoid the
// import cycle that would result from aliasing.
type (
	TextRun        = text.TextRun
	Block          = layout.Block
	BlockKind      = layout.BlockKind
	Line           = layout.Line
	LayoutParams   = layout.LayoutParams
	PageInfo       = layout.PageInfo
	ClassifyParams = classify.ClassifyParams
	Chunk          = chunk.Chunk
	ChunkOptions   = chunk.Options
	ChunkMode      = chunk.Mode
	StructElem     = structtree.Element
)

// BlockKind constant re-exports. Go aliases don't pull constants
// automatically, so each is re-declared. Resolves T1 open-question 4:
// all 6 BlockKind values are re-exported at the top level for
// consumer ergonomics (vs. only BlockUnknown).
const (
	BlockUnknown   = layout.BlockUnknown
	BlockHeading   = layout.BlockHeading
	BlockParagraph = layout.BlockParagraph
	BlockCode      = layout.BlockCode
	BlockListItem  = layout.BlockListItem
	BlockTable     = layout.BlockTable
)

// ChunkMode constant re-exports. Both ModeParagraph and ModeSection are
// re-exported at the top level — symmetry with the ChunkMode type
// alias and consistent ergonomics with the BlockKind re-exports.
const (
	ChunkModeParagraph = chunk.ModeParagraph
	ChunkModeSection   = chunk.ModeSection
)
