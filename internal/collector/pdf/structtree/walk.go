package structtree

import (
	"errors"
	"log/slog"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// structDepthCap bounds /K-recursion depth. Mirrors the
// text/content_stream_marked.go mcidDepthCap = 32 pattern, sized
// larger because structure trees nest more deeply than marked-content
// stacks (Document → Part → Sect → … → P chains are routine on
// technical PDFs). Defends against malformed PDFs with cyclic /K
// references or pathologically deep nesting.
const structDepthCap = 64

// ErrNotTagged is returned by Walk and Tree when the document has no
// /StructTreeRoot in its catalog. Use errors.Is to discriminate from
// other failures (encrypted, malformed, ...).
var ErrNotTagged = errors.New("collector/pdf/structtree: document is not tagged (no /StructTreeRoot)")

// Walk traverses the document's StructTreeRoot and emits Blocks in
// /K-array DFS order with semantic role pre-classified.
//
// pageFilter contract:
//   - pageFilter == -1: whole-document walk (Document.StructTree
//     consumer; every emit is appended to the result).
//   - pageFilter >= 0: per-page emit. The walker still descends every
//     subtree (a /Sect can contain MCRs from multiple pages), but
//     emitBlock skips emit when block.PageIndex != pageFilter.
//     Pruning at LEAF emit time (one int comparison) avoids the
//     quadratic walk-then-filter pattern at the routing layer.
//
// Returns ErrNotTagged when the document has no /StructTreeRoot.
func Walk(ctx *internalpdf.Context, pageFilter int) ([]layout.Block, error) {
	root, rf, err := initWalk(ctx)
	if err != nil {
		return nil, err
	}
	var blocks []layout.Block
	emit := func(b layout.Block) { blocks = append(blocks, b) }

	if err := walkRootKids(ctx, root, rf, func(s *internalpdf.StructElemRef) error {
		_, err := walkInternal(s, -1, 0, emit, rf, pageFilter)
		return err
	}); err != nil {
		return nil, err
	}
	return blocks, nil
}

// Tree returns the document's structure tree as an *Element root with
// every node's Type/Children/Page/MCIDs/Attrs/BBox populated. Returns
// ErrNotTagged for untagged documents.
//
// Element.BBox semantics: the union of the bboxes of every TextRun
// referenced by the element's MCIDs (transitive: an element's MCIDs
// are its OWN /K-array MCIDs, NOT its descendants'). For a pure
// walk-through container (no own MCIDs), BBox is the zero Rect; the
// zero Rect is the contract for "no spatial extent."
func Tree(ctx *internalpdf.Context) (*Element, error) {
	root, rf, err := initWalk(ctx)
	if err != nil {
		return nil, err
	}
	out := &Element{Page: -1}
	if err := walkRootKids(ctx, root, rf, func(s *internalpdf.StructElemRef) error {
		built, err := walkInternal(s, -1, 0, nil, rf, -1)
		if err != nil {
			return err
		}
		if built != nil {
			out.Children = append(out.Children, built)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// initWalk resolves the StructTreeRoot and constructs the run-cache
// closure shared by Walk and Tree. Returns ErrNotTagged when the
// catalog has no /StructTreeRoot.
func initWalk(ctx *internalpdf.Context) (*internalpdf.StructTreeRootRef, runFor, error) {
	if ctx == nil {
		return nil, nil, errors.New("collector/pdf/structtree: nil context")
	}
	root, err := ctx.StructTreeRoot()
	if err != nil {
		return nil, nil, err
	}
	if root == nil {
		return nil, nil, ErrNotTagged
	}
	rf := newRunFor(func(pageIndex int) ([]text.TextRun, error) {
		return extractRunsForPage(ctx, pageIndex)
	})
	return root, rf, nil
}

// walkRootKids iterates the root /K array, dereferences each entry to
// a StructElemRef, and dispatches to the supplied per-child callback.
// Shared by Walk + Tree to avoid duplicating root-array iteration.
func walkRootKids(ctx *internalpdf.Context, root *internalpdf.StructTreeRootRef, _ runFor, fn func(*internalpdf.StructElemRef) error) error {
	kArr, err := root.KArray()
	if err != nil {
		return err
	}
	for _, kObj := range kArr {
		child, err := ctx.ResolveStructElem(kObj)
		if err != nil {
			return err
		}
		if child == nil {
			continue
		}
		if err := fn(child); err != nil {
			return err
		}
	}
	return nil
}

// walkInternal is the SHARED DFS recursion driver used by both Walk
// and Tree. parentPageIndex is the /Pg inherited from the nearest
// ancestor with a /Pg entry; -1 when no ancestor pinned a page.
// depth tracks recursion for cycle protection.
//
// When emit != nil the walker calls emit on every RoleEmit / RoleFigure
// element it visits (Walk path). The returned *Element is also built
// in every case so Tree() can ignore emit and use the return value
// alone. Sharing the recursion eliminates the prior duplicate DFS
// implementation.
func walkInternal(s *internalpdf.StructElemRef, parentPageIndex int, depth int, emit func(layout.Block), rf runFor, pageFilter int) (*Element, error) {
	if depth >= structDepthCap {
		slog.Warn("pdf/structtree: depth cap exceeded; skipping cyclic /K",
			"page", parentPageIndex, "depth", depth, "cap", structDepthCap)
		return nil, nil
	}
	role := ResolveRole(s.Type())
	pageIndex := parentPageIndex
	if pi, ok := s.PageIndex(); ok {
		pageIndex = pi
	}
	kids, err := s.Kids()
	if err != nil {
		return nil, err
	}

	if emit != nil {
		switch role.Action {
		case RoleEmit:
			if err := emitBlock(s, role, pageIndex, kids, emit, rf, pageFilter); err != nil {
				return nil, err
			}
		case RoleFigure:
			if err := emitFigure(s, pageIndex, kids, emit, rf, pageFilter); err != nil {
				return nil, err
			}
		}
	}

	node, err := buildElementForKids(s, pageIndex, kids, rf)
	if err != nil {
		return nil, err
	}

	// Figures are leaves in v1; nothing to recurse into.
	if role.Action == RoleFigure {
		return node, nil
	}
	if err := recurseKids(s, kids, pageIndex, depth, role, emit, rf, pageFilter, node); err != nil {
		return nil, err
	}
	return node, nil
}

// recurseKids drives the per-kid recursion for walkInternal. Splits
// out so walkInternal stays simple (the lint nestif/gocognit rules
// flag the prior inline switch).
func recurseKids(s *internalpdf.StructElemRef, kids []internalpdf.Kid, pageIndex int, depth int, role RoleMapping, emit func(layout.Block), rf runFor, pageFilter int, node *Element) error {
	for _, k := range kids {
		switch v := k.(type) {
		case internalpdf.KidStructElem:
			if v.Ref == nil {
				continue
			}
			child, err := walkInternal(v.Ref, pageIndex, depth+1, emit, rf, pageFilter)
			if err != nil {
				return err
			}
			if node != nil && child != nil {
				node.Children = append(node.Children, child)
			}
		case internalpdf.KidMCID:
			if role.Action == RoleWalkThrough {
				slog.Debug("pdf/structtree: MCR under walk-through structure element",
					"role", s.Type(), "mcid", v.ID, "page", v.PageIndex)
			}
		}
	}
	return nil
}
