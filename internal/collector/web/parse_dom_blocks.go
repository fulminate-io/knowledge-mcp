// SPDX-License-Identifier: Apache-2.0

package web

import (
	"slices"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// This file carries the block/phrasing partition and the layout-table
// classifier. Both exist because the walker previously treated <table> as a
// terminal handler: a page laid out inside a wrapper table had its entire
// subtree consumed by handleTable, so nothing under it was ever emitted.
//
// THE PARTITION IS KEYED ON PHRASING, NOT ON BLOCK. The closed set is the
// phrasing one and everything else — including every element whose atom the
// parser does not know — is block. Keying it the other way round was tried
// and it collapsed every page to a single paragraph: an allow-list of known
// block atoms silently reads html/body/tbody/tr/td/th/li as inline, and
// reads every custom element as inline too, because golang.org/x/net/html
// assigns DataAtom == 0 to anything outside its table.

// isPhrasingAtom reports whether a is one of the 36 atoms that may appear
// inside a run of inline text. The set is CLOSED: any atom not named here —
// including atom.Atom(0), which is what the parser assigns to every custom
// element such as <x-widget> or <awsui-app-layout> — is block-level.
func isPhrasingAtom(a atom.Atom) bool {
	switch a {
	case atom.A, atom.Span, atom.Em, atom.Strong, atom.B, atom.I, atom.U,
		atom.Small, atom.Sub, atom.Sup, atom.Br, atom.Abbr, atom.Cite,
		atom.Code, atom.Kbd, atom.Mark, atom.Q, atom.S, atom.Samp,
		atom.Time, atom.Var, atom.Wbr, atom.Del, atom.Ins, atom.Bdi,
		atom.Bdo, atom.Data, atom.Dfn, atom.Rp, atom.Rt, atom.Ruby,
		atom.Font, atom.Big, atom.Strike, atom.Tt, atom.Nobr:
		return true
	}
	return false
}

// isBlockLevelNode reports whether n breaks an inline run. An element that
// is not a phrasing atom always does. A phrasing element does too when it
// CONTAINS block-level content — go101 wraps a <div><ul>...</ul></div>
// inside a <small>, and without the promotion that whole subtree is
// swallowed into a run and its list records are lost.
//
// Non-element nodes never break a run; they are the run's content.
func isBlockLevelNode(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	if !isPhrasingAtom(n.DataAtom) {
		return true
	}
	return containsBlockLevel(n)
}

// containsBlockLevel reports whether any descendant of n is block-level. It
// short-circuits on the first one found, so a phrasing subtree that wraps
// block content costs only the prefix up to that element.
func containsBlockLevel(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if isBlockLevelNode(c) {
			return true
		}
	}
	return false
}

// elementOwnScope visits every descendant ELEMENT of n without descending
// past a nested <table>: the nested table element itself is visited, its
// subtree is not. That boundary is what stops a data table nested inside a
// layout wrapper from lending the wrapper its header signal.
func elementOwnScope(n *html.Node, visit func(*html.Node)) {
	if n == nil {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		visit(c)
		if c.DataAtom == atom.Table {
			continue
		}
		elementOwnScope(c, visit)
	}
}

// tableOwnScope visits every element in t's own scope — every descendant
// element, stopping at (but including) any nested <table>.
func tableOwnScope(t *html.Node, visit func(*html.Node)) {
	elementOwnScope(t, visit)
}

// tableHasHeaderSignal reports whether t's own scope contains a <th>,
// <thead> or <caption>. Any of the three is an author's statement that the
// table holds data rather than layout.
func tableHasHeaderSignal(t *html.Node) bool {
	found := false
	tableOwnScope(t, func(n *html.Node) {
		switch n.DataAtom {
		case atom.Th, atom.Thead, atom.Caption:
			found = true
		}
	})
	return found
}

// tableRowShape returns t's own-scope <tr> count and whether every one of
// those rows carries the SAME number of <td>/<th> cells, that number being
// at least 2, over at least 2 rows.
//
// Uniformity is a POSITIVE data signal and it is not optional. Header-less
// label-value grids — two-column "Applicable Platforms" and "Vulnerability
// Mapping Notes" tables, which every CWE definition page carries — have no
// <th> and hold block content in their value cells, so without this rule
// they classify layout and their cell pairings flatten into loose prose.
func tableRowShape(t *html.Node) (rows int, uniform bool) {
	var trs []*html.Node
	tableOwnScope(t, func(n *html.Node) {
		if n.DataAtom == atom.Tr {
			trs = append(trs, n)
		}
	})
	if len(trs) < 2 {
		return len(trs), false
	}
	width := -1
	for _, tr := range trs {
		cells := rowCellCount(tr)
		if width < 0 {
			width = cells
			continue
		}
		if cells != width {
			return len(trs), false
		}
	}
	return len(trs), width >= 2
}

// rowCellCount returns the number of <td>/<th> direct children of tr.
func rowCellCount(tr *html.Node) int {
	cells := 0
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if c.DataAtom == atom.Td || c.DataAtom == atom.Th {
			cells++
		}
	}
	return cells
}

// tableCellHasBlock reports whether any of t's own-scope cells holds
// block-level content in ITS own scope. A cell carrying headings,
// paragraphs, divs or a nested table is a layout slot, not a data value.
func tableCellHasBlock(t *html.Node) bool {
	var cells []*html.Node
	tableOwnScope(t, func(n *html.Node) {
		if n.DataAtom == atom.Td || n.DataAtom == atom.Th {
			cells = append(cells, n)
		}
	})
	for _, cell := range cells {
		found := false
		elementOwnScope(cell, func(n *html.Node) {
			if !found && isBlockLevelNode(n) {
				found = true
			}
		})
		if found {
			return true
		}
	}
	return false
}

// tableSignals is one table's VERDICT TOGETHER WITH THE MEASUREMENTS THAT
// PRODUCED IT. Every field is an observation of the table, not an
// interpretation of it: Role is the raw role attribute lowercased and trimmed,
// HeaderSignal / RowCount / Uniform / CellHasBlock are the four readings the
// ordered rules consult, and Layout is the verdict they reached.
//
// Carrying the readings is the whole point. The predecessor answered "layout"
// or "data" and threw away every number behind the answer, so a consumer could
// not tell a table declared role=presentation by its author from one inferred
// to be layout because a cell held a paragraph.
type tableSignals struct {
	Layout       bool
	Role         string
	HeaderSignal bool
	RowCount     int
	Uniform      bool
	CellHasBlock bool
}

// classifyTable decides whether n is page furniture rather than tabular data,
// and returns that verdict alongside every measurement it consulted.
//
// THE RULES ARE EVALUATED IN ORDER AND THE ORDER IS LOAD-BEARING:
//
//  1. role=presentation or role=none  -> LAYOUT (explicit author declaration, WAI-ARIA)
//  2. tableHasHeaderSignal            -> DATA
//  3. tableRowShape uniform           -> DATA
//  4. tableCellHasBlock               -> LAYOUT
//  5. otherwise                       -> DATA
//
// Uniformity must be tested BEFORE cellHasBlock: the header-less label-value
// grids described on tableRowShape have block content in their value cells, so
// rule 4 alone reads them as layout and destroys their pairings.
//
// IT COMPUTES ALL FOUR MEASUREMENTS UNCONDITIONALLY, where a verdict-only
// classifier would short-circuit after the first rule that decided. That is
// deliberate and it is the contract: the measurements are what the emitted node
// carries, so short-circuiting would leave a role=presentation table reporting
// a row count of zero and a header signal of false — figures that describe the
// classifier's laziness rather than the table. The cost is bounded by one
// table's own scope, which stops at a nested table.
func classifyTable(n *html.Node) tableSignals {
	sig := tableSignals{
		Role:         strings.ToLower(strings.TrimSpace(getAttr(n, "role"))),
		HeaderSignal: tableHasHeaderSignal(n),
		CellHasBlock: tableCellHasBlock(n),
	}
	sig.RowCount, sig.Uniform = tableRowShape(n)

	switch {
	case sig.Role == "presentation" || sig.Role == "none":
		sig.Layout = true
	case sig.HeaderSignal:
		sig.Layout = false
	case sig.Uniform:
		sig.Layout = false
	default:
		sig.Layout = sig.CellHasBlock
	}
	return sig
}

// isBlockLevel is the memoised form of isBlockLevelNode, scoped to one page
// walk. It is ONE implementation, not two — the answer is computed by
// isBlockLevelNode and only cached here. The cache lives on the walker
// rather than at package level because parsePage runs concurrently across
// the crawler's worker pool: a shared map would be a data race, and one
// keyed by *html.Node would pin every parsed DOM in memory for the life of
// the process.
//
// The repeat ask it removes is the anchor-transparency branch, which asks
// about an anchor's children and then hands the same anchor to walkChildren,
// which asks about them again.
func (w *walker) isBlockLevel(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	if v, ok := w.blockLevel[n]; ok {
		return v
	}
	v := isBlockLevelNode(n)
	w.blockLevel[n] = v
	return v
}

// walkChildren partitions n's children into INLINE RUNS separated by
// block-level children, and emits each run as its own record. This is the
// CSS anonymous-block-box rule: text sitting in a block box with no <p>
// wrapper is still a paragraph, and nothing in the walker emitted it before.
//
// Under the phrasing-keyed partition html, body, head, tbody, tr, td, th,
// li, dt, dd, caption, figcaption and every unknown or custom element are
// all block, so this descends the document rather than swallowing it.
func (w *walker) walkChildren(n *html.Node) {
	if n == nil {
		return
	}
	var run []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == html.CommentNode || c.Type == html.DoctypeNode:
			continue
		case c.Type == html.ElementNode && isNonRenderable(c.DataAtom):
			continue
		case w.isBlockLevel(c):
			w.flushInlineRun(n, run)
			run = nil
			w.walk(c)
		default:
			run = append(run, c)
		}
	}
	w.flushInlineRun(n, run)
}

// flushInlineRun emits one closed inline run: a paragraphRecord for its text
// and a linkRecord for every anchor it contains.
//
// THE LINK RULE IS UNCONDITIONAL. Every anchor in the run produces a link
// record regardless of whether the run also carries prose, because
// handleAnchor is the only thing in this package that appends a linkRecord —
// any branch that reaches an anchor without calling it silently drops that
// anchor's node. recordLinks is deliberately NOT called here: handleAnchor
// already performs makeLink plus classifyLink, and classifyLink dedups the
// InternalLinks side, so a second pass would be redundant work with no
// effect.
//
// EVERY NON-EMPTY RUN IS RETAINED, INCLUDING A RUN OF NOTHING BUT LINKS.
// runHasNonAnchorText no longer decides whether the record exists; it decides
// what the record IS. A run with no text outside its anchors is a navigation
// strip, and it is emitted as a record carrying LinksOnly so a consumer can
// see the strip and decide about it — dropping it lost the fact that the page
// had navigation there at all, which no downstream reader could recover.
//
// The paragraph is appended BEFORE the link records so document order reads
// text-then-links deterministically.
//
// Attrs come from runAttrs, which splits their provenance: tag and dom_depth
// describe the run's immediate containing element, while class/id/role/data
// are climbed to the nearest classed ancestor because the immediate parent is
// frequently unclassed and taking attrs from it yields empty class and id on
// exactly the records a recipe needs to classify. When the two differ the
// climb is recorded on the record rather than left implicit.
//
// WHY STYLED-DIV CODE EXAMPLES ARE EMITTED AS PARAGRAPHS AND NOT AS
// code_block. There is no generic signal to key on: the pages that carry
// this shape have no <pre>, no <code>, no monospace styling and no
// white-space:pre, so a collector-side code detector would have nothing
// spec-defined to read. The signals that DO exist are site-authored
// vocabulary — a parenthesised label literal, a presentational class, an
// element id — and keying the collector on any of them is a per-site rule,
// which is out of scope by design and would not generalize to the next
// table-layout source. Classifying code is a RECIPE's job; the collector's
// obligation is to lose neither the text nor the attribute context, and that
// is what this function delivers: the author's line structure survives via
// collapseProseLines, the label text survives as its own adjacent record,
// and each record carries the nearest classed ancestor's class and id.
//
// A NOTE FOR WHOEVER EDITS THIS COMMENT. The scope-fence gate strips only
// line comments beginning with //, not /* */ blocks. Writing the literal
// w.append(*lr), or any of the per-site signals named above, inside a block
// comment turns that gate red against correct work. Keep this prose in //
// comments, as the rest of this package does.
func (w *walker) flushInlineRun(parent *html.Node, run []*html.Node) {
	if len(run) == 0 {
		return
	}
	var sb strings.Builder
	for _, n := range run {
		emitProseText(&sb, n)
	}
	if text := collapseProseLines(sb.String()); text != "" {
		w.append(paragraphRecord{
			Text:      text,
			LinksOnly: !runHasNonAnchorText(run),
			Attrs:     runAttrs(parent),
		})
	}
	for _, anchor := range anchorsIn(run) {
		w.handleAnchor(anchor)
	}
}

// runHasNonAnchorText reports whether the run carries any non-whitespace
// text OUTSIDE an anchor. A run of nothing but links is a navigation strip
// rather than prose, so its record is marked LinksOnly.
func runHasNonAnchorText(run []*html.Node) bool {
	return slices.ContainsFunc(run, hasNonAnchorText)
}

func hasNonAnchorText(n *html.Node) bool {
	if n == nil {
		return false
	}
	if n.Type == html.ElementNode && n.DataAtom == atom.A {
		return false
	}
	if n.Type == html.TextNode && strings.TrimSpace(n.Data) != "" {
		return true
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hasNonAnchorText(c) {
			return true
		}
	}
	return false
}

// anchorsIn returns every <a> element at or below the run's members, in
// document order.
func anchorsIn(run []*html.Node) []*html.Node {
	var out []*html.Node
	var collect func(*html.Node)
	collect = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.A {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	for _, n := range run {
		collect(n)
	}
	return out
}

// nearestAttrSource walks from n up through its ancestors and returns the
// first element carrying a non-empty class or id, or nil when none does.
func nearestAttrSource(n *html.Node) *html.Node {
	for cur := n; cur != nil; cur = cur.Parent {
		if cur.Type != html.ElementNode {
			continue
		}
		if getAttr(cur, "class") != "" || getAttr(cur, "id") != "" {
			return cur
		}
	}
	return nil
}
