package structtree

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// TestResolveRole_Walkthrough covers every walk-through entry in the
// ticket table. Each must report Action == RoleWalkThrough and an
// empty Kind (Kind is consulted only on RoleEmit).
func TestResolveRole_Walkthrough(t *testing.T) {
	t.Parallel()
	walkthroughs := []string{
		"Document", "Part", "Sect", "Div", "Art",
		"L",
		"TR", "TD", "TH",
		"TOC", "TOCI", "Index",
		"NonStruct", "Private",
	}
	for _, in := range walkthroughs {
		got := ResolveRole(in)
		if got.Action != RoleWalkThrough {
			t.Errorf("ResolveRole(%q).Action = %v, want RoleWalkThrough", in, got.Action)
		}
		if got.Kind != "" {
			t.Errorf("ResolveRole(%q).Kind = %q, want empty (Kind ignored on walk-through)", in, got.Kind)
		}
	}
}

// TestResolveRole_Headings asserts all heading types resolve to
// BlockHeading + RoleEmit.
func TestResolveRole_Headings(t *testing.T) {
	t.Parallel()
	headings := []string{"H", "H1", "H2", "H3", "H4", "H5", "H6"}
	for _, in := range headings {
		got := ResolveRole(in)
		if got.Action != RoleEmit {
			t.Errorf("ResolveRole(%q).Action = %v, want RoleEmit", in, got.Action)
		}
		if got.Kind != layout.BlockHeading {
			t.Errorf("ResolveRole(%q).Kind = %q, want %q", in, got.Kind, layout.BlockHeading)
		}
	}
}

// TestResolveRole_Paragraphs covers P (plain paragraph) and the two
// metadata-flagged variants Quote/BlockQuote → is_quote=true and
// Caption → is_caption=true.
func TestResolveRole_Paragraphs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		in        string
		wantKind  layout.BlockKind
		wantMetaK string
		wantMetaV string
	}{
		{name: "P", in: "P", wantKind: layout.BlockParagraph},
		{name: "Quote", in: "Quote", wantKind: layout.BlockParagraph, wantMetaK: "is_quote", wantMetaV: "true"},
		{name: "BlockQuote", in: "BlockQuote", wantKind: layout.BlockParagraph, wantMetaK: "is_quote", wantMetaV: "true"},
		{name: "Caption", in: "Caption", wantKind: layout.BlockParagraph, wantMetaK: "is_caption", wantMetaV: "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveRole(tc.in)
			if got.Action != RoleEmit {
				t.Errorf("Action = %v, want RoleEmit", got.Action)
			}
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if tc.wantMetaK != "" {
				if got.Metadata == nil || got.Metadata[tc.wantMetaK] != tc.wantMetaV {
					t.Errorf("Metadata[%q] = %q, want %q", tc.wantMetaK, got.Metadata[tc.wantMetaK], tc.wantMetaV)
				}
			}
		})
	}
}

// TestResolveRole_ListItems asserts the three list-item leaf types
// all surface as BlockListItem + RoleEmit.
func TestResolveRole_ListItems(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"LI", "Lbl", "LBody"} {
		got := ResolveRole(in)
		if got.Action != RoleEmit {
			t.Errorf("ResolveRole(%q).Action = %v, want RoleEmit", in, got.Action)
		}
		if got.Kind != layout.BlockListItem {
			t.Errorf("ResolveRole(%q).Kind = %q, want %q", in, got.Kind, layout.BlockListItem)
		}
	}
}

// TestResolveRole_Code asserts /Code emits a BlockCode.
func TestResolveRole_Code(t *testing.T) {
	t.Parallel()
	got := ResolveRole("Code")
	if got.Action != RoleEmit || got.Kind != layout.BlockCode {
		t.Errorf("ResolveRole(Code) = %+v, want {RoleEmit, BlockCode}", got)
	}
}

// TestResolveRole_Table asserts /Table emits BlockTable; TR/TD/TH all
// walk through (cells flatten into the parent Table block).
func TestResolveRole_Table(t *testing.T) {
	t.Parallel()
	tbl := ResolveRole("Table")
	if tbl.Action != RoleEmit || tbl.Kind != layout.BlockTable {
		t.Errorf("ResolveRole(Table) = %+v, want {RoleEmit, BlockTable}", tbl)
	}
	for _, in := range []string{"TR", "TD", "TH"} {
		got := ResolveRole(in)
		if got.Action != RoleWalkThrough {
			t.Errorf("ResolveRole(%q).Action = %v, want RoleWalkThrough", in, got.Action)
		}
	}
}

// TestResolveRole_Figure asserts /Figure resolves to RoleFigure.
func TestResolveRole_Figure(t *testing.T) {
	t.Parallel()
	got := ResolveRole("Figure")
	if got.Action != RoleFigure {
		t.Errorf("ResolveRole(Figure).Action = %v, want RoleFigure", got.Action)
	}
}

// TestResolveRole_VendorUnknown asserts vendor-specific or unknown
// /S values fall through to walk-through. Children are still
// extracted under their own classification.
func TestResolveRole_VendorUnknown(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"MyCorp::Custom", "", "FooBar", "AnnotatedText"} {
		got := ResolveRole(in)
		if got.Action != RoleWalkThrough {
			t.Errorf("ResolveRole(%q).Action = %v, want RoleWalkThrough", in, got.Action)
		}
	}
}

// TestHeadingLevelFromType covers the H<N> suffix parser. Only
// H1..H6 produce non-zero levels; H (plain), P, H7, longer strings
// all return 0.
func TestHeadingLevelFromType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
	}{
		{"H1", 1}, {"H2", 2}, {"H3", 3}, {"H4", 4}, {"H5", 5}, {"H6", 6},
		{"H", 0}, {"P", 0}, {"H7", 0}, {"H0", 0}, {"H1Extra", 0}, {"", 0},
	}
	for _, tc := range cases {
		if got := HeadingLevelFromType(tc.in); got != tc.want {
			t.Errorf("HeadingLevelFromType(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
