// section.go — heading-stack hierarchy reconstruction (ModeSection).
//
// buildSections converts a flat post-continuity stream of mergedBlock
// values into a section-aware tree: each heading opens (or pops back
// to) its level on a stack and becomes the parent for subsequent
// body blocks; sub-headings nest under their enclosing heading until
// a same-or-higher-level heading closes the run.
//
// Heading-level-gap policy (locked Q5, option a): flat-into-nearest-
// ancestor. When the document jumps from H1 to H3 with no H2, the
// H3 nests under the H1 directly — no synthetic H2 is inserted.
// Stack-pop semantics yield this naturally: pop until stack-top's
// HeadingLevel < current heading's HeadingLevel, then attach.
//
// Orphan-body policy: body blocks that appear before the first
// heading are collected under a synthetic root (Kind=BlockUnknown,
// HeadingLevel=0, empty Text). When the document has no headings at
// all, all blocks land under that single synthetic root.
//
// Stack-pop pattern mirrors collector/web/parse_dom.go:198 pushSection
// in shape and intent — pop while stack-top.Depth >= current.Depth,
// push, attach. Differences: web walks DOM nodes with mixed-type
// children, chunk walks merged blocks where every child is a Chunk.
//
// Implementation note: the stack carries *sectionNode (a private
// wrapper with its own []*sectionNode children list), not *Chunk.
// A *Chunk pointer into a parent's []Chunk would become stale on
// every Children append because slices reallocate. Materialization
// converts the node tree to []Chunk on the boundary.
//
// Performance: serial. Single linear pass over merged blocks; stack
// ops are O(1) amortized; materialize is O(total nodes).

package chunk

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// sectionNode is the private tree node used during the walk. Children
// are *sectionNode so appends never invalidate sibling pointers.
// chunkFromMerged builds the Chunk; materializeNodes flattens the
// tree to []Chunk on the boundary.
type sectionNode struct {
	c        Chunk
	children []*sectionNode
}

// buildSections is the section-mode emitter. Walks merged blocks in
// document order, tracking a heading stack; body blocks attach to
// the current stack-top heading or to an orphan collector when the
// stack is empty.
func buildSections(blocks []mergedBlock) []Chunk {
	if len(blocks) == 0 {
		return nil
	}

	var roots []*sectionNode
	var stack []*sectionNode
	var orphans []*sectionNode

	for _, mb := range blocks {
		node := &sectionNode{c: chunkFromMerged(mb)}
		if mb.Kind == layout.BlockHeading {
			// Pop until stack-top's HeadingLevel < node's level.
			for len(stack) > 0 && stack[len(stack)-1].c.HeadingLevel >= node.c.HeadingLevel {
				stack = stack[:len(stack)-1]
			}
			if len(stack) == 0 {
				roots = append(roots, node)
			} else {
				stack[len(stack)-1].children = append(stack[len(stack)-1].children, node)
			}
			stack = append(stack, node)
			continue
		}
		// Body block.
		if len(stack) == 0 {
			orphans = append(orphans, node)
		} else {
			stack[len(stack)-1].children = append(stack[len(stack)-1].children, node)
		}
	}

	// Synthetic-root for orphans: prepended when orphans exist.
	// When no headings exist at all, this is the sole top-level
	// chunk and contains every block as a Child (ticket spec).
	if len(orphans) > 0 {
		syntheticRoot := &sectionNode{
			c:        Chunk{Kind: layout.BlockUnknown, Text: ""},
			children: orphans,
		}
		roots = append([]*sectionNode{syntheticRoot}, roots...)
	}
	return materializeNodes(roots)
}

// chunkFromMerged builds a leaf Chunk from a mergedBlock. Children
// stays nil here; the materialize pass populates Children from the
// sectionNode tree.
func chunkFromMerged(mb mergedBlock) Chunk {
	return Chunk{
		Kind:         mb.Kind,
		Text:         normalizeBlockText(mb.Block),
		BBox:         Rect{X0: mb.BBox.X0, Y0: mb.BBox.Y0, X1: mb.BBox.X1, Y1: mb.BBox.Y1},
		PageRange:    mb.PageRange,
		HeadingLevel: mb.HeadingLevel,
		Metadata:     copyMetadata(mb.Metadata),
		StructRole:   mb.StructRole,
	}
}

// materializeNodes recursively flattens a sectionNode tree onto
// []Chunk. The conversion happens at this boundary because Chunk's
// Children is []Chunk (value slice), which doesn't hold up under
// in-progress mutation; sectionNode's []*sectionNode does.
func materializeNodes(nodes []*sectionNode) []Chunk {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]Chunk, 0, len(nodes))
	for _, n := range nodes {
		c := n.c
		c.Children = materializeNodes(n.children)
		out = append(out, c)
	}
	return out
}
