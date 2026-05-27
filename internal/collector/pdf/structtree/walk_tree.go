package structtree

import internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"

// buildElementForKids packs a single structure element's data into an
// *Element value (no recursion — walkInternal owns the DFS, this
// helper is the per-node packer it calls on every visit). Resolves
// MCIDs / Attrs / BBox into the returned node; the caller appends
// child *Elements onto Children.
//
// Sharing this helper between Walk and Tree means both APIs build the
// same per-element shape (so Tree() output mirrors what Walk() would
// have emitted, just packed as a tree instead of a flat block list).
func buildElementForKids(s *internalpdf.StructElemRef, pageIndex int, kids []internalpdf.Kid, rf runFor) (*Element, error) {
	mcids, hasObjref := collectMCIDsFromKids(kids)

	var bbox Rect
	if len(mcids) > 0 {
		idx, err := rf(pageIndex)
		if err != nil {
			return nil, err
		}
		runs := idx.RunsForMCIDs(mcids)
		bbox = computeMCIDBBox(runs)
	}

	attrs := s.Attributes()
	if hasObjref {
		if attrs == nil {
			attrs = make(map[string]string)
		}
		attrs["has_objref"] = "true"
	}
	if at := s.ActualText(); at != "" {
		if attrs == nil {
			attrs = make(map[string]string)
		}
		attrs["ActualText"] = at
	}

	return &Element{
		Type:  s.Type(),
		Page:  pageIndex,
		MCIDs: mcids,
		BBox:  bbox,
		Attrs: attrs,
	}, nil
}
