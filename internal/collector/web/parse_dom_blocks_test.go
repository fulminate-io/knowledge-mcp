// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// phrasingAtomSet is the 36-atom closed allow-list, restated here so the
// test is an INDEPENDENT statement of the contract rather than a read of
// the implementation's own switch.
var phrasingAtomSet = []atom.Atom{
	atom.A, atom.Span, atom.Em, atom.Strong, atom.B, atom.I, atom.U,
	atom.Small, atom.Sub, atom.Sup, atom.Br, atom.Abbr, atom.Cite,
	atom.Code, atom.Kbd, atom.Mark, atom.Q, atom.S, atom.Samp,
	atom.Time, atom.Var, atom.Wbr, atom.Del, atom.Ins, atom.Bdi,
	atom.Bdo, atom.Data, atom.Dfn, atom.Rp, atom.Rt, atom.Ruby,
	atom.Font, atom.Big, atom.Strike, atom.Tt, atom.Nobr,
}

// blockAtomSet names structural and document atoms that must NOT read as
// phrasing. The document/table/list ones are the load-bearing entries: an
// inverted partition that reads <body>, <td> or <li> as inline accumulates
// whole documents into a single run.
var blockAtomSet = []atom.Atom{
	atom.Html, atom.Head, atom.Body, atom.Div, atom.P, atom.Section,
	atom.Article, atom.Main, atom.Nav, atom.Header, atom.Footer,
	atom.Aside, atom.Table, atom.Tbody, atom.Thead, atom.Tfoot,
	atom.Tr, atom.Td, atom.Th, atom.Caption, atom.Ul, atom.Ol,
	atom.Li, atom.Dl, atom.Dt, atom.Dd, atom.H1, atom.H2, atom.H3,
	atom.H4, atom.H5, atom.H6, atom.Pre, atom.Blockquote, atom.Figure,
	atom.Figcaption, atom.Img, atom.Form, atom.Title,
}

// TestIsPhrasingAtom_ClosedSet is the executable statement that the declared
// allow-list reproduces the measured behaviour, including the case that
// silently degrades every Astro/Lit/Stencil/awsui docs site: a custom
// element, which golang.org/x/net/html assigns atom.Atom(0).
func TestIsPhrasingAtom_ClosedSet(t *testing.T) {
	t.Parallel()

	if got := len(phrasingAtomSet); got != 36 {
		t.Fatalf("phrasing allow-list has %d atoms, want exactly 36", got)
	}
	for _, a := range phrasingAtomSet {
		if !isPhrasingAtom(a) {
			t.Errorf("isPhrasingAtom(%s) = false, want true", a)
		}
	}
	for _, a := range blockAtomSet {
		if isPhrasingAtom(a) {
			t.Errorf("isPhrasingAtom(%s) = true, want false — a structural atom read as inline swallows its subtree into a run", a)
		}
	}

	if isPhrasingAtom(atom.Atom(0)) {
		t.Errorf("isPhrasingAtom(atom.Atom(0)) = true, want false — every custom element carries atom 0 and must read as BLOCK")
	}

	// The atom-level answer alone is not enough: isBlockLevelNode is what
	// walkChildren consults, so drive a real custom-element node through it.
	custom := parseFirstElement(t, `<x-widget><p>one</p></x-widget>`, "x-widget")
	if !isBlockLevelNode(custom) {
		t.Errorf("isBlockLevelNode(<x-widget>) = false, want true (DataAtom=%d)", custom.DataAtom)
	}
}

// TestIsBlockLevelNode_PhrasingContainingBlock pins the promotion clause: a
// phrasing element that WRAPS block content still breaks the run. Without
// it the go101 <small><div><ul> subtree is swallowed whole.
func TestIsBlockLevelNode_PhrasingContainingBlock(t *testing.T) {
	t.Parallel()

	plain := parseFirstElement(t, `<div><small>just words</small></div>`, "small")
	if isBlockLevelNode(plain) {
		t.Errorf("isBlockLevelNode(<small>just words</small>) = true, want false — a phrasing element with no block content belongs in the run")
	}

	wrapping := parseFirstElement(t, `<div><small><div><ul><li>one</li></ul></div></small></div>`, "small")
	if !isBlockLevelNode(wrapping) {
		t.Errorf("isBlockLevelNode(<small><div><ul>...) = false, want true — a phrasing element wrapping block content must break the run")
	}
}

// TestIsLayoutTable_Classification runs every named case in
// table_shapes.html through the classifier. Each case is annotated with the
// rule it is there to exercise.
func TestIsLayoutTable_Classification(t *testing.T) {
	t.Parallel()
	doc := parseFixtureDoc(t, "table_shapes.html")

	cases := []struct {
		id       string
		want     bool // true == LAYOUT
		ruleNote string
	}{
		{"t-role-presentation", true, "rule 1 — explicit author declaration"},
		{"t-role-none", true, "rule 1 — explicit author declaration"},
		{"t-th", false, "rule 2 — header signal"},
		{"t-thead", false, "rule 2 — header signal"},
		{"t-caption", false, "rule 2 — header signal"},
		{"t-uniform-2x2", false, "rule 3 — uniform grid; rule 4 alone would say layout"},
		{"t-uniform-4x2", false, "rule 3 — uniform grid; rule 4 alone would say layout"},
		{"t-ragged-block", true, "rule 4 — the layout-wrapper shape"},
		{"t-single-row-wrapper", true, "rule 4 — the title/breadcrumb wrapper shape"},
		{"t-phrasing-only", false, "rule 3 — uniform, so it short-circuits BEFORE rule 5"},
		{"t-ragged-phrasing-only", false, "rule 5 — the ONLY case that reaches the fallthrough"},
		{"t-nested-data-in-layout", true, "rule 4 — the nested <th> must not leak in via own-scope"},
	}
	if len(cases) != 12 {
		t.Fatalf("case table has %d entries, want 12", len(cases))
	}

	for _, tc := range cases {
		host := elementByID(doc, tc.id)
		if host == nil {
			t.Errorf("case %s: no element with that id in table_shapes.html", tc.id)
			continue
		}
		tbl := firstDescendantTable(host)
		if tbl == nil {
			t.Errorf("case %s: no <table> inside the case wrapper", tc.id)
			continue
		}
		got := isLayoutTable(tbl)
		if got != tc.want {
			t.Errorf("case %s: isLayoutTable = %v, want %v (%s)", tc.id, verdict(got), verdict(tc.want), tc.ruleNote)
		}
	}
}

// TestIsLayoutTable_RaggedHeaderedTableIsData is the KILLER FOR RULE 2, and
// it exists because none of the twelve table_shapes.html cases is one.
// Measured: deleting the tableHasHeaderSignal rule outright leaves all
// twelve green, because t-th, t-thead and t-caption are uniform grids that
// rule 3 rescues before rule 5 is ever reached.
//
// The shape below is the one rule 2 actually protects on live pages: a
// genuine data table made RAGGED by a colspan, with block content in its
// cells. Rule 3 declines it (not uniform) and rule 4 would call it layout,
// so the header signal is the only thing classifying it DATA. A census over
// five CWE pages found 33 th-bearing tables of which 2 were ragged, and both
// were real data tables — the CWE-78 Content-History Submissions table
// (119 rows) among them.
func TestIsLayoutTable_RaggedHeaderedTableIsData(t *testing.T) {
	t.Parallel()

	const ragged = `<html><body><table id="ragged-headered">
<tr><th>Submitter</th><th>Date</th><th>Comment</th></tr>
<tr><td><p>A contributor</p></td><td><p>Spring</p></td><td><p>Clarified the description</p></td></tr>
<tr><td colspan="3"><p>A spanning note that makes this row narrower than the others</p></td></tr>
</table></body></html>`

	tbl := parseFirstElement(t, ragged, "table")

	// The premise: this table is ragged and block-bearing, so rules 3 and 4
	// alone would misclassify it. Asserting the premise keeps the test from
	// going vacuous if the fixture is ever edited into a uniform grid.
	rows, uniform := tableRowShape(tbl)
	if rows != 3 {
		t.Fatalf("premise broken: the fixture table has %d own-scope rows, want 3", rows)
	}
	if uniform {
		t.Fatalf("premise broken: the fixture table is uniform, so rule 3 would decide it and rule 2 would stay untested")
	}
	if !tableCellHasBlock(tbl) {
		t.Fatalf("premise broken: the fixture table has no block-bearing cell, so rule 4 would not misclassify it and rule 2 would stay untested")
	}

	if isLayoutTable(tbl) {
		t.Errorf("a ragged th-bearing data table classified LAYOUT — the header signal (rule 2) is the only rule that keeps it DATA, and its rows would be destroyed")
	}
}

func verdict(layout bool) string {
	if layout {
		return "LAYOUT"
	}
	return "DATA"
}

// TestParsePage_TableLayoutPage_DataTableStillEmitsTable is the near-miss
// control for the classifier: widening it to swallow data tables would make
// the reproduction pass while destroying every table record on the page.
//
// The th-bearing taxonomy table and BOTH header-less label-value grids must
// still emit tableRecords with their cell pairings intact, while the
// MainPane wrapper — which holds the whole page — must emit none.
func TestParsePage_TableLayoutPage_DataTableStillEmitsTable(t *testing.T) {
	t.Parallel()
	got := collectRecords(parseFixture(t, "cwe_table_layout.html"))

	byID := map[string]tableRecord{}
	for _, tbl := range got.tables {
		byID[tbl.Attrs.ID] = tbl
	}

	if _, swallowed := byID["MainPane"]; swallowed {
		t.Errorf("the MainPane layout wrapper emitted a tableRecord — it holds the whole page and must be recursed into, not recorded")
	}

	taxonomy, ok := byID["RelatedWeaknesses"]
	if !ok {
		t.Fatalf("the th-bearing taxonomy table emitted no tableRecord; saw table ids %v", tableIDs(got.tables))
	}
	if len(taxonomy.Rows) < 2 {
		t.Errorf("taxonomy table kept %d rows, want at least 2", len(taxonomy.Rows))
	}

	// Both label-value grids are header-less and carry block content in their
	// value cells, so they classify DATA only on row uniformity. Their
	// pairings are the thing at risk.
	for _, spec := range []struct {
		id       string
		wantRows int
		pair     [2]string
	}{
		{"LabelValue2x2", 1, [2]string{"Prevalence", "Reported often in code that shells out to platform utilities"}},
		{"LabelValue4x2", 3, [2]string{"Reason", "The entry names a single, well bounded mistake"}},
	} {
		tbl, found := byID[spec.id]
		if !found {
			t.Errorf("%s emitted no tableRecord; saw table ids %v", spec.id, tableIDs(got.tables))
			continue
		}
		// extractTable promotes the first <tr> to Headers when no <thead>
		// exists, so an N-row label-value grid yields N-1 body rows.
		if len(tbl.Rows) != spec.wantRows {
			t.Errorf("%s kept %d body rows, want %d; rows=%v", spec.id, len(tbl.Rows), spec.wantRows, tbl.Rows)
		}
		if !hasPairing(tbl, spec.pair[0], spec.pair[1]) {
			t.Errorf("%s lost the pairing %q -> %q; headers=%v rows=%v", spec.id, spec.pair[0], spec.pair[1], tbl.Headers, tbl.Rows)
		}
	}
}

// hasPairing reports whether tbl carries a two-cell row (or header row)
// whose cells are exactly label and value.
func hasPairing(tbl tableRecord, label, value string) bool {
	rows := append([][]string{tbl.Headers}, tbl.Rows...)
	for _, row := range rows {
		if len(row) == 2 && row[0] == label && row[1] == value {
			return true
		}
	}
	return false
}

func tableIDs(tables []tableRecord) []string {
	out := make([]string, 0, len(tables))
	for _, tbl := range tables {
		out = append(out, tbl.Attrs.ID)
	}
	return out
}

// parseFixtureDoc parses a testdata fixture into an html document tree.
func parseFixtureDoc(t *testing.T, name string) *html.Node {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(loadFixture(t, name)))
	if err != nil {
		t.Fatalf("html.Parse(%s): %v", name, err)
	}
	return doc
}

// parseFirstElement parses a fragment and returns the first element whose
// tag name is want.
func parseFirstElement(t *testing.T, fragment, want string) *html.Node {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader([]byte(fragment)))
	if err != nil {
		t.Fatalf("html.Parse(%q): %v", fragment, err)
	}
	found := findNode(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == want
	})
	if found == nil {
		t.Fatalf("no <%s> in parsed fragment %q", want, fragment)
	}
	return found
}

// elementByID returns the element carrying the given id attribute, or nil.
func elementByID(root *html.Node, id string) *html.Node {
	return findNode(root, func(n *html.Node) bool {
		return n.Type == html.ElementNode && getAttr(n, "id") == id
	})
}

// firstDescendantTable returns the first <table> at or below n.
func firstDescendantTable(n *html.Node) *html.Node {
	return findNode(n, func(c *html.Node) bool {
		return c.Type == html.ElementNode && c.DataAtom == atom.Table
	})
}

// findNode returns the first node in a pre-order DFS from n satisfying pred.
func findNode(n *html.Node, pred func(*html.Node) bool) *html.Node {
	if n == nil {
		return nil
	}
	if pred(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := findNode(c, pred); got != nil {
			return got
		}
	}
	return nil
}
