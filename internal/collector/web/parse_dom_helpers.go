// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// handleStructural dispatches an element to the appropriate per-tag
// handler. Returns true when the element was handled (and thus its
// children were processed by the handler); false signals walk() to
// recurse into children itself.
func (w *walker) handleStructural(n *html.Node) bool {
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		w.handleHeading(n)
		return true
	case atom.P:
		w.handleParagraph(n)
		return true
	case atom.Pre:
		w.handleCodeBlock(n)
		return true
	case atom.Ul, atom.Ol:
		w.handleList(n)
		return true
	case atom.Dl:
		w.handleDL(n)
		return true
	case atom.Table:
		w.handleTable(n)
		return true
	case atom.Img:
		w.handleImage(n, "")
		return true
	case atom.Figure:
		w.handleFigure(n)
		return true
	case atom.Blockquote:
		w.handleBlockquote(n)
		return true
	case atom.A:
		// Bare <a> outside of prose: still record the link.
		w.handleAnchor(n)
		return true
	}
	return false
}

// handleHeading converts an h1–h6 into a new sectionRecord and pushes it
// onto the stack. The section's heading text is the concatenated inline
// text; anchor id comes from the heading element's id attribute.
func (w *walker) handleHeading(n *html.Node) {
	depth := headingDepth(n.DataAtom)
	text := collectProseText(n)
	anchor := getAttr(n, "id")

	if depth == 1 && w.firstH1 == "" {
		w.firstH1 = text
	}

	sec := &sectionRecord{
		Heading: text,
		Depth:   depth,
		Anchor:  anchor,
		Attrs:   extractCommonAttrs(n),
	}
	w.pushSection(sec)
}

func headingDepth(a atom.Atom) int {
	switch a {
	case atom.H1:
		return 1
	case atom.H2:
		return 2
	case atom.H3:
		return 3
	case atom.H4:
		return 4
	case atom.H5:
		return 5
	case atom.H6:
		return 6
	}
	return 1
}

// handleParagraph emits a paragraphRecord. Inline <a href> inside the
// paragraph is recorded as a linkRecord attached to the current section
// in addition to being preserved as text. InlineEmphasis is populated
// alongside Text via a parallel walk that tracks offsets in the
// collapsed text (see emitProseTextWithEmphasis).
func (w *walker) handleParagraph(n *html.Node) {
	text := collectProseText(n)
	if strings.TrimSpace(text) != "" {
		_, emphs := emitProseTextWithEmphasis(n)
		w.append(paragraphRecord{
			Text:           text,
			Attrs:          extractCommonAttrs(n),
			InlineEmphasis: emphs,
		})
	}
	w.recordLinks(n)
}

// handleCodeBlock emits a codeBlockRecord. Language is extracted from a
// `language-xxx` or `lang-xxx` class on the <pre> or its <code> child.
func (w *walker) handleCodeBlock(n *html.Node) {
	lang, attrHint := "", ""
	if child := firstElementChild(n, atom.Code); child != nil {
		lang = langFromClass(getAttr(child, "class"))
		attrHint = getAttr(child, "class")
	}
	if lang == "" {
		lang = langFromClass(getAttr(n, "class"))
	}
	if attrHint == "" {
		attrHint = getAttr(n, "class")
	}
	src := collectText(n)
	w.append(codeBlockRecord{
		Language: lang,
		Source:   src,
		AttrHint: attrHint,
		Attrs:    extractCommonAttrs(n),
	})
}

func langFromClass(class string) string {
	for cls := range strings.FieldsSeq(class) {
		switch {
		case strings.HasPrefix(cls, "language-"):
			return strings.TrimPrefix(cls, "language-")
		case strings.HasPrefix(cls, "lang-"):
			return strings.TrimPrefix(cls, "lang-")
		}
	}
	return ""
}

// handleList / handleDL / buildDDItem live in parse_dom_list_helpers.go
// so each file stays under the 300 LOC recommended cap. They are
// dispatched from handleStructural above.

// handleTable emits a tableRecord.
func (w *walker) handleTable(n *html.Node) {
	headers, rows := extractTable(n)
	w.append(tableRecord{Headers: headers, Rows: rows, Attrs: extractCommonAttrs(n)})
	w.recordLinks(n)
}

// handleImage emits an imageRecord.
func (w *walker) handleImage(n *html.Node, caption string) {
	src := resolveHref(w.base, getAttr(n, "src"))
	alt := getAttr(n, "alt")
	if src == "" {
		return
	}
	w.append(imageRecord{
		URL:     src,
		Alt:     alt,
		Caption: caption,
		Attrs:   extractCommonAttrs(n),
	})
}

// handleFigure finds the first <img> and optional <figcaption> and emits
// a single paired imageRecord.
func (w *walker) handleFigure(n *html.Node) {
	var img *html.Node
	var caption string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.DataAtom {
		case atom.Img:
			if img == nil {
				img = c
			}
		case atom.Figcaption:
			caption = collectProseText(c)
		}
	}
	if img != nil {
		w.handleImage(img, caption)
	}
}

// handleBlockquote emits a quoteRecord. InlineEmphasis is populated from
// the same parallel walk used on paragraphs/list_items so downstream
// readers can find inline-tag spans inside the quote.
func (w *walker) handleBlockquote(n *html.Node) {
	text := collectProseText(n)
	cite := resolveHref(w.base, getAttr(n, "cite"))
	_, emphs := emitProseTextWithEmphasis(n)
	w.append(quoteRecord{
		Text:           text,
		CiteURL:        cite,
		Attrs:          extractCommonAttrs(n),
		InlineEmphasis: emphs,
	})
	w.recordLinks(n)
}

// handleAnchor records a bare <a href> (one outside prose).
func (w *walker) handleAnchor(n *html.Node) {
	lr := w.makeLink(n)
	if lr == nil {
		return
	}
	w.classifyLink(lr)
	w.append(*lr)
}

// recordLinks scans n for nested <a href> anchors and classifies them
// without adding duplicate records to the current section.
func (w *walker) recordLinks(n *html.Node) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode && n.DataAtom == atom.A {
		if lr := w.makeLink(n); lr != nil {
			w.classifyLink(lr)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.recordLinks(c)
	}
}
