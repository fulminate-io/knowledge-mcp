// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// emphasisAtoms is the set of inline tags whose spans are preserved as
// inlineEmphasis entries on paragraph / list_item / blockquote records.
// Nested emphasis inside one of these tags is flattened to text — the
// outer tag wins and the inner tags contribute to the text only.
var emphasisAtoms = map[atom.Atom]string{
	atom.Strong: "strong",
	atom.Em:     "em",
	atom.Code:   "code",
	atom.B:      "b",
	atom.I:      "i",
	atom.Kbd:    "kbd",
}

// emitProseTextWithEmphasis walks n's children flattening inline content
// to whitespace-collapsed text while recording spans of inline emphasis
// tags (strong/em/code/b/i/kbd). Returned text matches the shape produced
// by collectProseText MINUS the backtick wrapping around <code> — the
// emphasis list's Tag="code" already carries that signal, and dropping
// the backticks keeps the Position offsets in sync with the final text
// characters.
//
// Position is the character offset into the returned (collapsed) text at
// which the emphasis span begins. Nested emphasis tags are flattened to
// text contributing to the span's Text but not emitted as their own
// entries.
func emitProseTextWithEmphasis(n *html.Node) (string, []inlineEmphasis) {
	var buf strings.Builder
	var emphs []inlineEmphasis
	pendingSpace := false
	walkEmphasis(n, &buf, &emphs, &pendingSpace)
	return buf.String(), emphs
}

// walkEmphasis is the recursive core of emitProseTextWithEmphasis. It
// threads a single collapsed-text builder and a pendingSpace flag so
// positions in the emphasis list line up with offsets in the final text.
func walkEmphasis(n *html.Node, buf *strings.Builder, emphs *[]inlineEmphasis, pendingSpace *bool) {
	if n == nil {
		return
	}
	if n.Type == html.TextNode {
		appendCollapsed(buf, n.Data, pendingSpace)
		return
	}
	if n.Type == html.ElementNode {
		if tag, ok := emphasisAtoms[n.DataAtom]; ok {
			emitEmphasisSpan(n, tag, buf, emphs, pendingSpace)
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkEmphasis(c, buf, emphs, pendingSpace)
	}
}

// emitEmphasisSpan flushes any pending inter-word space, records the
// emphasis entry's position at the current buf length, and appends the
// emphasis node's whitespace-collapsed inner text. If the inner text is
// empty, no entry is recorded and no state changes.
func emitEmphasisSpan(n *html.Node, tag string, buf *strings.Builder, emphs *[]inlineEmphasis, pendingSpace *bool) {
	inner := collapseText(collectText(n))
	if inner == "" {
		return
	}
	flushPendingSpace(buf, pendingSpace)
	position := buf.Len()
	buf.WriteString(inner)
	*emphs = append(*emphs, inlineEmphasis{
		Tag:      tag,
		Text:     inner,
		Position: position,
	})
}

// appendCollapsed writes s into buf honoring the collapsed-whitespace
// contract: runs of whitespace fold into a single space, and a leading
// space is emitted only when buf already has content.
func appendCollapsed(buf *strings.Builder, s string, pendingSpace *bool) {
	for _, r := range s {
		if isInlineSpace(r) {
			*pendingSpace = true
			continue
		}
		flushPendingSpace(buf, pendingSpace)
		buf.WriteRune(r)
	}
}

// flushPendingSpace emits one space if pendingSpace is set and buf
// already has content, then clears pendingSpace. Matches the behavior of
// strings.Fields + strings.Join(" ").
func flushPendingSpace(buf *strings.Builder, pendingSpace *bool) {
	if *pendingSpace {
		if buf.Len() > 0 {
			buf.WriteByte(' ')
		}
		*pendingSpace = false
	}
}

// isInlineSpace reports whether r counts as inline whitespace for the
// collapsing rules. Matches strings.Fields which treats unicode.IsSpace
// as the separator set.
func isInlineSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// collapseText applies the same whitespace-collapse rule used by
// collectProseText: strings.Fields splits on any whitespace, Join glues
// with a single space. Safe for empty input.
func collapseText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
