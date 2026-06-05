package structtree

import "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"

// RoleAction is the disposition the walker takes when it visits a
// structure-tree element with a given /S type.
type RoleAction int

const (
	// RoleEmit means produce one layout.Block from this element. The
	// Block aggregates the element's marked-content (referenced by
	// MCID) into a single Lines[0] entry; the walker does NOT recurse
	// into children when the action is RoleEmit (the element's own
	// MCIDs already cover the content).
	RoleEmit RoleAction = iota

	// RoleWalkThrough means produce no Block; recurse into the
	// element's children. Used for grouping nodes (Document, Part,
	// Sect, Div, L, TR, TD, TOC, TOCI, NonStruct, Private, ...) and
	// for vendor-specific / unknown types so children are still
	// extracted.
	RoleWalkThrough

	// RoleFigure means emit a placeholder BlockUnknown carrying a
	// Figure metadata flag — content extraction for figures is out of
	// v1 scope per the ticket; we only record their presence so
	// downstream consumers can count / position them.
	RoleFigure
)

// RoleMapping is the resolved disposition for one /S value. Kind is
// only consulted when Action == RoleEmit. Metadata, when non-nil,
// merges into the emitted Block.Metadata (e.g. is_quote=true,
// is_caption=true).
type RoleMapping struct {
	Kind     layout.BlockKind
	Action   RoleAction
	Metadata map[string]string
}

// roleTable is the canonical /S → RoleMapping table.
// Vendor-specific or unknown types miss the lookup
// and ResolveRole returns {Action: RoleWalkThrough}.
//
// Map values are pre-allocated; the metadata maps are shared rather
// than per-call to avoid allocation churn — callers who need to
// mutate must clone first.
var (
	mdQuote   = map[string]string{"is_quote": "true"}
	mdCaption = map[string]string{"is_caption": "true"}

	roleTable = map[string]RoleMapping{
		// Top-level grouping nodes — walk-through, no Block emitted.
		"Document": {Action: RoleWalkThrough},
		"Part":     {Action: RoleWalkThrough},
		"Sect":     {Action: RoleWalkThrough},
		"Div":      {Action: RoleWalkThrough},
		"Art":      {Action: RoleWalkThrough},

		// Heading: /H plain (HeadingLevel=0; T7 leveler decides) and
		// numbered /H1..H6 (level parsed by HeadingLevelFromType).
		"H":  {Kind: layout.BlockHeading, Action: RoleEmit},
		"H1": {Kind: layout.BlockHeading, Action: RoleEmit},
		"H2": {Kind: layout.BlockHeading, Action: RoleEmit},
		"H3": {Kind: layout.BlockHeading, Action: RoleEmit},
		"H4": {Kind: layout.BlockHeading, Action: RoleEmit},
		"H5": {Kind: layout.BlockHeading, Action: RoleEmit},
		"H6": {Kind: layout.BlockHeading, Action: RoleEmit},

		// Body prose.
		"P": {Kind: layout.BlockParagraph, Action: RoleEmit},

		// Lists. /L is a grouping node — walk-through into /LI
		// children. /LI / /Lbl / /LBody all surface as BlockListItem
		// (the leaf-content nodes inside a list item).
		"L":     {Action: RoleWalkThrough},
		"LI":    {Kind: layout.BlockListItem, Action: RoleEmit},
		"Lbl":   {Kind: layout.BlockListItem, Action: RoleEmit},
		"LBody": {Kind: layout.BlockListItem, Action: RoleEmit},

		// Code.
		"Code": {Kind: layout.BlockCode, Action: RoleEmit},

		// Quotes & captions: paragraph with metadata flag.
		"Quote":      {Kind: layout.BlockParagraph, Action: RoleEmit, Metadata: mdQuote},
		"BlockQuote": {Kind: layout.BlockParagraph, Action: RoleEmit, Metadata: mdQuote},
		"Caption":    {Kind: layout.BlockParagraph, Action: RoleEmit, Metadata: mdCaption},

		// Figure — record presence only (out-of-scope per ticket).
		"Figure": {Action: RoleFigure},

		// Tables. /Table emits one Block; cells flatten into it.
		"Table": {Kind: layout.BlockTable, Action: RoleEmit},
		"TR":    {Action: RoleWalkThrough},
		"TD":    {Action: RoleWalkThrough},
		"TH":    {Action: RoleWalkThrough},

		// Tables of contents / indices — walk-through.
		"TOC":   {Action: RoleWalkThrough},
		"TOCI":  {Action: RoleWalkThrough},
		"Index": {Action: RoleWalkThrough},

		// Container nodes that carry no semantic role themselves.
		"NonStruct": {Action: RoleWalkThrough},
		"Private":   {Action: RoleWalkThrough},
	}
)

// ResolveRole returns the disposition for /S value s. Unknown or
// vendor-specific types miss the table and resolve to walk-through —
// children are still extracted under their own /S classification.
func ResolveRole(s string) RoleMapping {
	if rm, ok := roleTable[s]; ok {
		return rm
	}
	return RoleMapping{Action: RoleWalkThrough}
}

// HeadingLevelFromType extracts the numeric heading level from /S
// values "H1".."H6". /H (plain, no suffix) returns 0 — the level is
// undetermined and T7's leveler will decide based on document-wide
// hierarchy. Anything else returns 0.
func HeadingLevelFromType(s string) int {
	if len(s) == 2 && s[0] == 'H' && s[1] >= '1' && s[1] <= '6' {
		return int(s[1] - '0')
	}
	return 0
}
