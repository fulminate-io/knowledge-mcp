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
//
// THE HEADING DISPATCH IS THREE ARMS AND THE ORDER IS THE REQUIREMENT:
// native h1-h6, then ARIA role="heading" with an explicit aria-level, then
// the calibrated presentation heuristic. Each arm runs only where the ones
// above it found no signal, which is this walker's form of the PDF
// classifier's `if blocks[i].HeadingLevel != 0 { continue }`
// (collector/pdf/classify/classify_heading.go:204): an upstream
// authoritative level is preserved, never overwritten by a heuristic one.
//
// Three arms is three pairwise orderings and each has its OWN subtest,
// because stating the order here gates none of them:
// native_heading_wins_over_aria_level, native_heading_wins_over_the_heuristic
// and aria_level_wins_over_the_heuristic.
func (w *walker) handleStructural(n *html.Node) bool {
	if isNativeHeading(n.DataAtom) {
		w.handleHeading(n, headingDepth(n.DataAtom), headingSourceNative, nil)
		return true
	}
	if depth, ok := ariaHeadingLevel(n); ok {
		w.handleHeading(n, depth, headingSourceAria, nil)
		return true
	}
	if sig, ok := w.heuristic[n]; ok {
		w.handleHeading(n, sig.level, headingSourceHeuristic, &sig)
		return true
	}
	switch n.DataAtom {
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
		// EVERY table produces a table record carrying its verdict and the
		// measurements behind it — the verdict is a SIGNAL, not a deletion.
		// A LAYOUT table additionally has its subtree walked, so the content
		// it wraps is emitted as its own records rather than swallowed; a
		// DATA table is terminal, as it always was.
		sig := classifyTable(n)
		w.handleTable(n, sig)
		if sig.Layout {
			w.walkChildren(n)
		}
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
		// HTML's content model for <a> is TRANSPARENT — an anchor may wrap
		// flow content. When it does, record the link AND walk the subtree,
		// so a masthead anchor wrapping an <h1> and a <p> yields a section
		// and a paragraph rather than one swallowed link. handleAnchor does
		// not recurse, so calling it first emits the link record without
		// double-walking. A bare <a> outside of prose takes the same path it
		// always did.
		if w.isBlockLevel(n) {
			w.handleAnchor(n)
			w.walkChildren(n)
			return true
		}
		w.handleAnchor(n)
		return true
	}
	return false
}

// The three values HeadingSource can take, one per dispatch arm. They are
// constants rather than literals at the call sites because they are stamped
// verbatim into the emitted node's `heading_source` metadata, where a consumer
// matches on the exact string.
const (
	headingSourceNative    = "native"
	headingSourceAria      = "aria"
	headingSourceHeuristic = "heuristic"
)

// handleHeading converts a heading element into a new sectionRecord and
// pushes it onto the stack. The section's heading text is the concatenated
// inline text; anchor id comes from the element's id attribute.
//
// depth is supplied by the caller rather than derived here, because the
// element is no longer necessarily an h1-h6: it may equally be an element
// carrying role="heading" with an explicit aria-level, or a presentation
// marker the heuristic pre-pass promoted. All three arms therefore inherit
// Anchor, Attrs and the firstH1 title fallback uniformly.
//
// source NAMES THE ARM THAT DECIDED, and sig carries the heuristic arm's
// measurements. sig is nil for the two authoritative arms and that nil is
// meaningful rather than incidental: a native or aria section has no
// calibration behind it, so it must carry no calibration keys. An
// implementation that stamped the heuristic inputs on every section would
// change nothing observable on the promoted ones, which is why the emitter's
// negative direction is gated as explicitly as its positive one.
func (w *walker) handleHeading(n *html.Node, depth int, source string, sig *headingSignal) {
	text := collectProseText(n)
	anchor := getAttr(n, "id")

	if depth == 1 && w.firstH1 == "" {
		w.firstH1 = text
	}

	sec := &sectionRecord{
		Heading:         text,
		Depth:           depth,
		Anchor:          anchor,
		Attrs:           extractCommonAttrs(n),
		HeadingSource:   source,
		HeuristicInputs: sig,
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
//
// AN IMAGE INSIDE A TERMINAL HANDLER IS DROPPED ENTIRELY, AND THAT IS AN
// EXPRESSLY APPROVED DECISION rather than an oversight. handleStructural
// returns true for atom.P — and likewise for the list, cell and blockquote
// handlers — so the walker never recurses into this element's children, and an
// <img> whose only ancestor is a paragraph produces NO image node at all.
// Measured: zero image nodes across a 27-page crawl of a site whose pages carry
// images, against a 47-image control on a site that places them outside
// terminal handlers.
//
// THE APPROVAL, in the user's terms: images are out of scope for this project,
// so the loss is accepted rather than repaired. WHAT IS LOST is the image node
// and its alt text for images nested inside prose containers; the page's full
// response bytes are retained regardless, so nothing is unrecoverable from the
// raw graph. Do not "fix" this by recursing from the terminal handlers without
// the same explicit approval — that changes what every prose record contains,
// not just whether images appear.
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

// handleTable emits a tableRecord carrying the classifier's signals. sig is
// passed in rather than recomputed because the dispatch arm above already
// needs the verdict to decide whether to walk the subtree, and classifying
// twice would read the same table's whole scope twice per table.
func (w *walker) handleTable(n *html.Node, sig tableSignals) {
	headers, rows := extractTable(n)
	w.append(tableRecord{Headers: headers, Rows: rows, Signals: sig, Attrs: extractCommonAttrs(n)})
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
