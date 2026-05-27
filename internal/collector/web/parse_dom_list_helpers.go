// SPDX-License-Identifier: Apache-2.0

package web

import (
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// This file holds the list-related DOM walkers (ul/ol/dl) split out
// from parse_dom_helpers.go so both files stay under the 300 LOC
// recommended cap. The handlers here are dispatched from
// handleStructural in parse_dom_helpers.go.

// handleList walks <ul>/<ol> and emits a listRecord with one
// listItemRecord per direct <li>.
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
		})
		pos++
	}
	w.append(rec)
	w.recordLinks(n)
}

// handleDL walks a <dl> description list emitting listItemRecord entries
// formatted as "<dt>: <dd>" strings.
func (w *walker) handleDL(n *html.Node) {
	rec := listRecord{Kind: "dl", Attrs: extractCommonAttrs(n)}
	pos := 0
	var currentTerm string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.DataAtom {
		case atom.Dt:
			currentTerm = collectProseText(c)
		case atom.Dd:
			rec.Items = append(rec.Items, buildDDItem(c, currentTerm, pos))
			pos++
			currentTerm = ""
		}
	}
	w.append(rec)
	w.recordLinks(n)
}

// buildDDItem turns a <dd> node into a listItemRecord formatted as
// "term: body" when the preceding <dt> supplied a term. InlineEmphasis
// positions reference the <dd>'s own collapsed text, so they are shifted
// by len("term: ") when the term prefix is present.
func buildDDItem(dd *html.Node, term string, pos int) listItemRecord {
	body := collectProseText(dd)
	text := body
	if term != "" {
		text = term + ": " + body
	}
	_, emphs := emitProseTextWithEmphasis(dd)
	if term != "" && len(emphs) > 0 {
		shift := len(term) + len(": ")
		for i := range emphs {
			emphs[i].Position += shift
		}
	}
	return listItemRecord{
		Text:           text,
		Position:       pos,
		Attrs:          extractCommonAttrs(dd),
		InlineEmphasis: emphs,
	}
}
