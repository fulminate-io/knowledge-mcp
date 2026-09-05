// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// This file holds the list-related DOM walkers (ul/ol/dl) split out
// from parse_dom_helpers.go so both files stay under the 300 LOC
// recommended cap. The handlers here are dispatched from
// handleStructural in parse_dom_helpers.go.

// handleList walks <ul>/<ol> and emits a listRecord with one
// listItemRecord per direct <li>, then classifies the list.
//
// classifyList runs AFTER the item loop because two of its four measurements
// are counts over the items this loop built.
func (w *walker) handleList(n *html.Node) {
	ordered := n.DataAtom == atom.Ol
	kind := "ul"
	if ordered {
		kind = "ol"
	}
	rec := listRecord{Ordered: ordered, Kind: kind, Attrs: extractCommonAttrs(n)}
	pos := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.DataAtom != atom.Li {
			continue
		}
		text := collectProseText(c)
		_, emphs := emitProseTextWithEmphasis(c)
		rec.Items = append(rec.Items, listItemRecord{
			Text:           text,
			Position:       pos,
			Attrs:          extractCommonAttrs(c),
			InlineEmphasis: emphs,
			LinkOnly:       itemLinkOnly(c),
		})
		pos++
	}
	rec.Signals = classifyList(n, rec.Items)
	w.append(rec)
	w.recordLinks(n)
}

// handleDL walks a <dl> description list emitting listItemRecord entries
// formatted as "<dt>: <dd>" strings, then classifies the list exactly as
// handleList does.
//
// IT TRACKS THE <dt> ELEMENT RATHER THAN ITS COLLECTED STRING, because the
// link-only measurement has to span the same nodes the item's Text does; see
// buildDDItem.
func (w *walker) handleDL(n *html.Node) {
	rec := listRecord{Kind: "dl", Attrs: extractCommonAttrs(n)}
	pos := 0
	var currentTerm *html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.DataAtom {
		case atom.Dt:
			currentTerm = c
		case atom.Dd:
			rec.Items = append(rec.Items, buildDDItem(c, currentTerm, pos))
			pos++
			currentTerm = nil
		}
	}
	rec.Signals = classifyList(n, rec.Items)
	w.append(rec)
	w.recordLinks(n)
}

// buildDDItem turns a <dd> node into a listItemRecord formatted as
// "term: body" when the preceding <dt> supplied a term. InlineEmphasis
// positions reference the <dd>'s own collapsed text, so they are shifted
// by len("term: ") when the term prefix is present.
//
// TERM IS THE <dt> ELEMENT, NOT ITS TEXT, so the link-only measurement spans
// the SAME nodes the item's Text does. A <dd> holding one bare anchor is
// link-only on its own, but its Text is "term: link" whenever a <dt> preceded
// it — measuring the <dd> alone would report a link-only item whose Content
// carries the term's prose.
func buildDDItem(dd *html.Node, term *html.Node, pos int) listItemRecord {
	body := collectProseText(dd)
	text := body
	termText := ""
	if term != nil {
		termText = collectProseText(term)
	}
	if termText != "" {
		text = termText + ": " + body
	}
	_, emphs := emitProseTextWithEmphasis(dd)
	if termText != "" && len(emphs) > 0 {
		shift := len(termText) + len(": ")
		for i := range emphs {
			emphs[i].Position += shift
		}
	}
	nodes := []*html.Node{dd}
	if term != nil {
		nodes = []*html.Node{term, dd}
	}
	return listItemRecord{
		Text:           text,
		Position:       pos,
		Attrs:          extractCommonAttrs(dd),
		InlineEmphasis: emphs,
		LinkOnly:       itemLinkOnly(nodes...),
	}
}

// listSignals is one list's VERDICT TOGETHER WITH THE MEASUREMENTS THAT
// PRODUCED IT, the list-side analog of tableSignals (parse_dom_blocks.go).
// Every field is an observation of the list, not an interpretation of it:
// Role is the raw role attribute lowercased and trimmed, Ancestry is the
// nearest nav/aside/header/footer ancestor's tag name or "", ItemCount and
// LinkOnlyItems are counts over the items the handler built, and Nav is the
// verdict the ordered rules reached.
//
// Carrying the readings is the whole point. A verdict alone cannot tell a list
// declared role="navigation" by its author from one inferred to be navigation
// because every item held nothing but a link.
type listSignals struct {
	Nav           bool
	Role          string
	Ancestry      string
	ItemCount     int
	LinkOnlyItems int
}

// classifyList decides whether n is page navigation rather than content, and
// returns that verdict alongside every measurement it consulted.
//
// THE RULES ARE EVALUATED IN ORDER AND THE ORDER IS LOAD-BEARING:
//
//  1. role=navigation/menu/menubar/tablist -> NAV (explicit author declaration, WAI-ARIA)
//  2. a <nav> ancestor                     -> NAV (the same declaration written as an element)
//  3. ItemCount == 0                       -> CONTENT (nothing was measured)
//  4. otherwise                            -> NAV exactly when every item is link-only
//
// Rule 1 does NOT include the roles list and listitem: those are a <ul>'s own
// implicit roles, so declaring one states nothing about navigation. Rule 2
// rests on <nav> carrying the implicit ARIA navigation role, which makes it the
// element form of rule 1's declaration.
//
// ONLY <nav> AMONG THE FOUR SECTIONING ANCESTORS DECIDES. aside, header and
// footer are complementary, banner and contentinfo respectively — each
// routinely holds a genuine bulleted list, so promoting them to a rule would
// refuse real content. Their reading is still recorded in Ancestry as a
// measurement, because what was observed is worth carrying even where it does
// not decide.
//
// IT COMPUTES ALL FOUR MEASUREMENTS UNCONDITIONALLY, where a verdict-only
// classifier would short-circuit after the first rule that decided. That is
// deliberate and it is the same contract classifyTable states: the measurements
// are what the emitted node carries, so short-circuiting would leave a
// role="navigation" list reporting an item count of zero — a figure describing
// the classifier's laziness rather than the list.
//
// ANCHOR DENSITY — how many links an item holds — is deliberately NOT among the
// measurements: link-only-ness is a yes-or-no reading of an item, and a count of
// anchors inside one decides nothing these rules consult.
func classifyList(n *html.Node, items []listItemRecord) listSignals {
	sig := listSignals{
		Role:      strings.ToLower(strings.TrimSpace(getAttr(n, "role"))),
		Ancestry:  sectioningAncestry(n),
		ItemCount: len(items),
	}
	for _, item := range items {
		if item.LinkOnly {
			sig.LinkOnlyItems++
		}
	}

	switch {
	case sig.Role == "navigation" || sig.Role == "menu" || sig.Role == "menubar" || sig.Role == "tablist":
		sig.Nav = true
	case sig.Ancestry == "nav":
		sig.Nav = true
	case sig.ItemCount == 0:
		sig.Nav = false
	default:
		sig.Nav = sig.LinkOnlyItems == sig.ItemCount
	}
	return sig
}

// sectioningAncestry climbs n's parent chain and returns the tag name of the
// first nav/aside/header/footer element enclosing it, or "" when none does.
//
// The walk is over the RAW parsed DOM — parsePage hands the walker the tree
// html.Parse produced (parse_dom.go) — so parent links are intact at handler
// time.
func sectioningAncestry(n *html.Node) string {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type != html.ElementNode {
			continue
		}
		switch p.DataAtom {
		case atom.Nav, atom.Aside, atom.Header, atom.Footer:
			return p.Data
		}
	}
	return ""
}

// itemLinkOnly reports whether the nodes making up one list item carry at
// least one link and no text outside a link.
//
// IT REUSES THE TWO HELPERS flushInlineRun ALREADY DECIDES LINK-ONLY-NESS
// WITH — anchorsIn and runHasNonAnchorText, both parse_dom_blocks.go — so a
// list item and a navigation strip are judged by ONE definition of "nothing but
// links" rather than by two that can drift apart.
func itemLinkOnly(nodes ...*html.Node) bool {
	return len(anchorsIn(nodes)) > 0 && !runHasNonAnchorText(nodes)
}
