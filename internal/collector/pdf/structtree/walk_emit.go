package structtree

import (
	"maps"
	"strconv"
	"strings"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// emitBlock produces one layout.Block for an element classified as
// RoleEmit. The Block aggregates all marked-content the element owns
// (via /K-array MCRs), with a run-derived bbox, and splits those runs
// into ONE LINE PER RENDERED LINE through layout.LinesFromRuns — the
// same grouper the untagged path uses. A tagged element is a semantic
// unit that may span many rendered lines; it is not one line.
//
// Empty-MCID guard (T3.6 reviewer fix): if the element has no MCID
// kids of its own, no Block is emitted. /K children that are
// nested StructElems handle their own emits via walkInternal's
// recursion. This prevents zero-bbox phantom blocks for pure
// container nodes that happen to carry an emittable role.
//
// ActualText override (T2.1 reviewer fix): when the element carries
// /A << /ActualText (...) >>, emitBlock synthesizes a single
// replacement TextRun whose Text is the override, BBox-derived X/Y/
// W/H, and inherited FontKey/Size from runs[0]; being one run it
// groups to one line. Otherwise the element's raw extracted runs are
// grouped into lines by geometry.
//
// pageFilter pruning (T2.3 reviewer fix): when the caller passes
// pageFilter >= 0 and this element's resolved pageIndex doesn't
// match, the emit is skipped. The walker still descends — a
// /Sect can contain MCRs from multiple pages — but only matching
// emits land in the result.
func emitBlock(s *internalpdf.StructElemRef, role RoleMapping, pageIndex int, kids []internalpdf.Kid, emit func(layout.Block), rf runFor, pageFilter int) error {
	mcids, hasObjref := collectMCIDsFromKids(kids)
	if len(mcids) == 0 {
		return nil
	}
	if pageFilter >= 0 && pageIndex != pageFilter {
		return nil
	}
	idx, err := rf(pageIndex)
	if err != nil {
		return err
	}
	runs := idx.RunsForMCIDs(mcids)
	bbox := computeMCIDBBox(runs)

	actualText := s.ActualText()
	var lineRuns []text.TextRun
	if actualText != "" {
		// Synthesize one replacement run carrying the override.
		// Inherit FontKey/Size from the first underlying run when
		// available; for the degenerate case (ActualText set on an
		// element with no MCID-derived runs) emit a zero-font run —
		// the override text still surfaces.
		var fontKey string
		var size float64
		if len(runs) > 0 {
			fontKey = runs[0].FontKey
			size = runs[0].Size
		}
		synthetic := text.TextRun{
			Text:    actualText,
			X:       bbox.X0,
			Y:       bbox.Y0,
			Width:   bbox.X1 - bbox.X0,
			Height:  bbox.Y1 - bbox.Y0,
			FontKey: fontKey,
			Size:    size,
		}
		lineRuns = []text.TextRun{synthetic}
	} else {
		lineRuns = runs
	}

	// One layout.Line per RENDERED line, through the SAME grouper the
	// untagged path uses. A tagged element is a semantic unit that may
	// span many rendered lines; collapsing them into one Line lost
	// every boundary the downstream dehyphenation and normalization
	// passes work across. Routing through layout keeps the per-line X
	// sort, the Y-center banding that places a raised run, space-token
	// insertion and rotation handling — a local re-implementation
	// dropped all four and emitted superscripts ahead of their words.
	elementLines, err := layout.LinesFromRuns(lineRuns, idx.pageInfo, layout.DefaultLayoutParams)
	if err != nil {
		return err
	}

	block := layout.Block{
		Kind:         role.Kind,
		StructRole:   s.Type(),
		HeadingLevel: HeadingLevelFromType(s.Type()),
		PageIndex:    pageIndex,
		BBox: layout.Rect{
			X0: bbox.X0, Y0: bbox.Y0, X1: bbox.X1, Y1: bbox.Y1,
		},
		Lines:    elementLines,
		Metadata: cloneMetadata(role.Metadata),
	}
	if hasObjref {
		ensureMetadata(&block)
		block.Metadata["has_objref"] = "true"
	}
	// Phase 5 amendment: stamp Block.Metadata["mcids"] so HybridFallback
	// can identify which page-runs were already covered by the tagged
	// path and partition the residue accordingly.
	ensureMetadata(&block)
	block.Metadata["mcids"] = formatMCIDs(mcids)

	emit(block)
	return nil
}

// emitFigure emits a Figure-flagged BlockUnknown for an element
// classified as RoleFigure. Content extraction for figures is out of
// v1 scope; this records presence and bbox only.
func emitFigure(s *internalpdf.StructElemRef, pageIndex int, kids []internalpdf.Kid, emit func(layout.Block), rf runFor, pageFilter int) error {
	if pageFilter >= 0 && pageIndex != pageFilter {
		return nil
	}
	mcids, _ := collectMCIDsFromKids(kids)
	var bbox Rect
	if len(mcids) > 0 {
		idx, err := rf(pageIndex)
		if err != nil {
			return err
		}
		runs := idx.RunsForMCIDs(mcids)
		bbox = computeMCIDBBox(runs)
	}
	block := layout.Block{
		Kind:       layout.BlockUnknown,
		StructRole: s.Type(),
		PageIndex:  pageIndex,
		BBox: layout.Rect{
			X0: bbox.X0, Y0: bbox.Y0, X1: bbox.X1, Y1: bbox.Y1,
		},
		Metadata: map[string]string{"is_figure": "true"},
	}
	if len(mcids) > 0 {
		block.Metadata["mcids"] = formatMCIDs(mcids)
	}
	emit(block)
	return nil
}

// cloneMetadata returns a copy of m (so callers can mutate the
// returned map without disturbing the shared role-table entry).
// Returns nil for nil input — keeps the Block.Metadata zero value
// when no metadata applies.
func cloneMetadata(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	maps.Copy(out, m)
	return out
}

// ensureMetadata makes Block.Metadata non-nil so callers can write
// keys without an extra map check at every call site.
func ensureMetadata(b *layout.Block) {
	if b.Metadata == nil {
		b.Metadata = make(map[string]string)
	}
}

// formatMCIDs joins the mcid slice as a comma-separated decimal
// string for Block.Metadata["mcids"]. HybridFallback parses this back
// out to identify which page-runs the tagged path already covered.
func formatMCIDs(mcids []int) string {
	if len(mcids) == 0 {
		return ""
	}
	parts := make([]string, len(mcids))
	for i, v := range mcids {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}
