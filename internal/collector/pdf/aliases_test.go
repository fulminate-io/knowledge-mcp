package pdf

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/chunk"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/structtree"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// Compile-time type-alias identity assertions. If any pdf.X stops being
// a Go type alias (or starts pointing at the wrong sub-package type),
// these var declarations fail to compile loudly. There are no runtime
// checks here — the package init does nothing — but the package must
// compile, which is the whole point.
//
// Each line uses reverse-direction assignment (`var _ sub.X = pdf.X{}`)
// because that is the canonical Go idiom for proving alias identity:
// an unaliased named type would require an explicit conversion. The
// equivalent forward cast (`_ = pdf.X(sub.X{})`) is redundant — Go
// permits the conversion between identical underlying types regardless
// of aliasing — so we don't include it.
var (
	_ text.TextRun            = TextRun{}
	_ layout.Block            = Block{}
	_ layout.BlockKind        = BlockKind("")
	_ layout.Line             = Line{}
	_ layout.LayoutParams     = LayoutParams{}
	_ layout.PageInfo         = PageInfo{}
	_ classify.ClassifyParams = ClassifyParams{}
	_ chunk.Chunk             = Chunk{}
	_ chunk.Options           = ChunkOptions{}
	_ chunk.Mode              = ChunkMode("")
	_ structtree.Element      = StructElem{}
)

// BlockKind constant-identity assertions. The constants in aliases.go
// are value re-exports (`const BlockHeading = layout.BlockHeading`).
// The compiler guarantees equality at declaration time, but the
// tripwire below documents the contract explicitly and fails loudly
// if any future edit accidentally changes a binding.
var (
	_ = BlockUnknown == layout.BlockUnknown
	_ = BlockHeading == layout.BlockHeading
	_ = BlockParagraph == layout.BlockParagraph
	_ = BlockCode == layout.BlockCode
	_ = BlockListItem == layout.BlockListItem
	_ = BlockTable == layout.BlockTable
)

// ChunkMode constant-identity assertions, same rationale as the
// BlockKind block above.
var (
	_ = ChunkModeParagraph == chunk.ModeParagraph
	_ = ChunkModeSection == chunk.ModeSection
)
