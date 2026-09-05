// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"

	"golang.org/x/net/html"
)

// pruneHiddenNodes walks the DOM tree from root and detaches every element
// that browsers would treat as not-displayed: the HTML5 `hidden` boolean
// attribute, `aria-hidden="true"`, or an inline `style` containing
// `display: none` / `visibility: hidden`.
//
// Browsers run this filter natively before any rendering or accessibility
// tree construction; our walker doesn't, which is why chrome blocks like
// Microsoft Learn's `<div id="unsupported-browser" hidden>` boilerplate were
// reaching pattern nodes. The route is now a CHUNK NODE rather than a page
// body — the page carries no body at all — so the boilerplate would be emitted
// as its own node and composed into a pattern by any recipe that gathers a
// page's chunks. Wikipedia
// uses the same pattern for many decorative spans (`aria-hidden="true"` on
// menu chevron icons, hidden language-list collapse toggles, etc.).
//
// Removal is structural — once a node is detached, its entire subtree is
// gone, so the section walker never sees the buried text. Hohpe's visible
// pattern-solution paragraphs are unaffected (no hidden markers).
//
// Pre-render filter only — does not handle CSS class-based hiding (`.sr-only`,
// `.visually-hidden`, etc.) because those depend on the page's stylesheet
// which we don't load. The HTML-level markers above cover the structural
// chrome that matters for the catalogs we scrape.
func pruneHiddenNodes(root *html.Node) int {
	if root == nil {
		return 0
	}
	// Two-pass traversal so removing children doesn't break the parent's
	// sibling-pointer walk. First collect every offender via DFS, then
	// detach each from its parent in a second loop.
	var victims []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode && elementIsHidden(n) {
			victims = append(victims, n)
			return // No need to recurse — entire subtree will go.
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	pruned := 0
	for _, v := range victims {
		if v.Parent != nil {
			v.Parent.RemoveChild(v)
			pruned++
		}
	}
	// The count is what reaches the collect response as the hidden_pruned
	// degrade class. It counts DETACHED subtrees, not elements: a hidden div
	// holding forty children is one loss of one subtree, which is the unit a
	// reader of the census is deciding about.
	return pruned
}

// elementIsHidden reports whether n carries any of the standard
// "browser does not display this" markers. Match list mirrors the
// HTML Living Standard's hidden-flag rules plus the conventional ARIA
// and inline-style overrides used by major sites.
func elementIsHidden(n *html.Node) bool {
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "hidden":
			// Boolean HTML attribute — presence alone hides the element.
			// Spec note: hidden="until-found" is technically findable, but
			// for catalog scraping we treat it the same as plain hidden.
			return true
		case "aria-hidden":
			if strings.EqualFold(strings.TrimSpace(a.Val), "true") {
				return true
			}
		case "style":
			if styleHidesElement(a.Val) {
				return true
			}
		}
	}
	return false
}

// styleHidesElement reports whether an inline style attribute contains a
// `display: none` or `visibility: hidden` declaration. Whitespace-tolerant
// across the colon and the value; case-insensitive on property + value
// (browsers don't care about case for either).
func styleHidesElement(style string) bool {
	lower := strings.ToLower(style)
	for decl := range strings.SplitSeq(lower, ";") {
		decl = strings.TrimSpace(decl)
		if decl == "" {
			continue
		}
		propRaw, valRaw, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		prop := strings.TrimSpace(propRaw)
		val := strings.TrimSpace(valRaw)
		switch prop {
		case "display":
			if val == "none" {
				return true
			}
		case "visibility":
			if val == "hidden" {
				return true
			}
		}
	}
	return false
}
