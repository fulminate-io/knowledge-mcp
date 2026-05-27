// paragraph.go — flat per-block emit (ModeParagraph).
//
// buildParagraphs walks pre-flattened post-continuity blocks and emits
// one Chunk per block, preserving Kind / HeadingLevel / PageRange /
// BBox and applying text normalization via normalize.go.
//
// mergedBlock is the post-continuity carrier defined here — Phase 2
// ships a passthrough producer (one mergedBlock per layout.Block,
// PageRange=[i,i]); Phase 3 extends mergeAcrossPages so a single
// mergedBlock can span 2 pages with merged Lines and a 2-page
// PageRange. The shape stays binary-stable: PageRange is added in
// Phase 2, not introduced/renamed in Phase 3.
//
// Performance: serial. Single linear pass; pre-allocated output slice.
// Per-block work is O(block char count) dominated by normalization.
// The chunker is a single-call API; caller fans out at a higher level
// if needed.

package chunk

import (
	"maps"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// mergedBlock is the carrier between continuity (Phase 3) and the
// per-mode emit. PageRange tracks the [first,last] pages a block
// spans — equal for single-page blocks, distinct for cross-page
// merges produced in Phase 3.
//
// The embedded layout.Block carries Kind / BBox / Lines / PageIndex /
// HeadingLevel / Metadata / StructRole. PageRange is the only added
// field; mergeAcrossPages may also rewrite Lines (concatenation) and
// BBox (union) during a real merge.
type mergedBlock struct {
	layout.Block
	PageRange [2]int
}

// buildParagraphs converts a post-continuity slice of mergedBlock
// values into a flat slice of Chunks per ModeParagraph rules: one
// Chunk per Block, Children left nil. Headings emit a Chunk with
// Kind=BlockHeading and HeadingLevel populated; the section-mode
// hierarchy is built by buildSections, not here.
//
// chunkFromMerged is the shared layout.Block→Chunk mapping (also
// used by buildSections). Single source of truth for the per-block
// Chunk shape.
//
// MinChunkChars filtering happens AFTER this call, in Build.
func buildParagraphs(blocks []mergedBlock) []Chunk {
	out := make([]Chunk, 0, len(blocks))
	for _, mb := range blocks {
		out = append(out, chunkFromMerged(mb))
	}
	return out
}

// copyMetadata defensively copies a Block.Metadata map onto the
// emitted Chunk so downstream consumers can mutate without reaching
// back into the layout result. Nil-safe.
func copyMetadata(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	maps.Copy(out, m)
	return out
}
