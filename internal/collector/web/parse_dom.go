// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// parsePage walks the cleaned article HTML and produces a typed pageRecord.
// It never returns a nil *pageRecord on success — every field is populated
// with at least a zero value so downstream emission has a stable shape.
//
// The walker is a single recursive DFS over the html.Node tree. A section
// stack tracks open heading scopes so deeper headings nest beneath
// shallower ones (H3 under H2 under H1). Content records are appended to
// the top-of-stack section; when no section is open yet, a synthetic
// "" (empty-heading, depth 0) root is used so pre-heading prose still
// has a home.
func parsePage(p *fetchedPage, cleaned *cleanedArticle) (*pageRecord, error) {
	if p == nil {
		return nil, fmt.Errorf("parsePage: nil fetchedPage")
	}
	if cleaned == nil {
		return nil, fmt.Errorf("parsePage: nil cleanedArticle")
	}

	base, err := url.Parse(p.FinalURL)
	if err != nil || base == nil {
		return nil, fmt.Errorf("parsePage: invalid FinalURL %q: %w", p.FinalURL, err)
	}

	// Walk the RAW DOM, not readability's cleaned tree. Readability's
	// heuristic chrome-strip drops author-marked content (e.g. Hohpe's
	// <p class="pattern-solution"> paragraphs) that transformers rely on.
	// HTML5 semantic chrome tags (nav/header/footer/aside/script/style/
	// noscript/template) are honored as a generic skip list inside
	// walker.walk — not as pattern-catalog-specific logic. Readability is
	// still called upstream for title / byline / pubdate extraction via
	// cleaned.
	doc, err := html.Parse(bytes.NewReader(p.Body))
	if err != nil {
		return nil, fmt.Errorf("parsePage: html.Parse: %w", err)
	}

	// Strip elements browsers would never render — `hidden` attribute,
	// aria-hidden="true", inline display:none / visibility:hidden — before
	// the section walker sees them. Without this, chrome like Microsoft
	// Learn's `<div id="unsupported-browser" hidden>` "browser is no longer
	// supported" boilerplate ends up in page.Description and then in the
	// downstream practice-graph pattern nodes via recipe transformers.
	pruneHiddenNodes(doc)

	w := newWalker(base)
	seedRawLinks(w, p.Body, base)
	w.walk(doc)
	sections := w.finish()

	title := pickTitle(cleaned.Title, w.firstH1)

	return &pageRecord{
		URL:           p.URL,
		FinalURL:      p.FinalURL,
		Title:         title,
		Author:        cleaned.Byline,
		PubDate:       cleaned.PubDate,
		FetchedAt:     p.FetchedAt,
		ContentHash:   hashBody(p.Body),
		HTTPStatus:    p.Status,
		TopSections:   sections,
		InternalLinks: w.internalLinks,
		ExternalCites: w.externalCites,
		Attrs:         extractCommonAttrs(findBodyOrHTML(doc)),
	}, nil
}

// findBodyOrHTML returns the first <body> element encountered in a DFS over
// doc, falling back to the first <html> element if no body is present, and
// finally nil. html.Parse always synthesizes html/head/body wrappers for
// well-formed input, so the body path is the common case; the html
// fallback is defensive against edge-case fragments.
func findBodyOrHTML(doc *html.Node) *html.Node {
	var htmlNode *html.Node
	var walk func(*html.Node) *html.Node
	walk = func(n *html.Node) *html.Node {
		if n == nil {
			return nil
		}
		if n.Type == html.ElementNode {
			if n.DataAtom == atom.Body {
				return n
			}
			if htmlNode == nil && n.DataAtom == atom.Html {
				htmlNode = n
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if b := walk(c); b != nil {
				return b
			}
		}
		return nil
	}
	if body := walk(doc); body != nil {
		return body
	}
	return htmlNode
}

// hashBody returns the hex-encoded sha256 of the raw body bytes.
func hashBody(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// pickTitle applies the fallback chain: cleaned title > first <h1> > "".
func pickTitle(cleaned, firstH1 string) string {
	if strings.TrimSpace(cleaned) != "" {
		return cleaned
	}
	if strings.TrimSpace(firstH1) != "" {
		return firstH1
	}
	return ""
}

// walker carries state during the DOM DFS: a section stack (top-of-stack
// is where new records land), the first-seen H1 for title fallback, and
// the accumulated internal/external link lists.
type walker struct {
	base          *url.URL
	root          *sectionRecord   // synthetic depth-0 sink for pre-heading content
	stack         []*sectionRecord // open section scopes, deepest last
	completed     []*sectionRecord // top-level (depth-1) sections closed out
	firstH1       string
	internalLinks []string
	externalCites []*linkRecord
	seenLinks     map[string]struct{}
}

func newWalker(base *url.URL) *walker {
	root := &sectionRecord{Depth: 0}
	return &walker{
		base:      base,
		root:      root,
		stack:     []*sectionRecord{root},
		seenLinks: map[string]struct{}{},
	}
}

// finish closes out every open section, promoting them to completed
// top-level sections as needed, and returns the final ordered slice of
// top-level sections.
//
// The synthetic root (depth=0, empty heading) captures pre-heading prose.
// It is included as the FIRST top-level section whenever it has children,
// regardless of whether subsequent headings opened additional sections.
// This preserves author-marked content that appears before any heading —
// e.g., Hohpe pattern pages place <p class="pattern-solution"> paragraphs
// before the "Integration Pattern Language" H2 sidebar.
func (w *walker) finish() []*sectionRecord {
	// Pop every section still open; nested sections were already attached
	// to their parents when they were pushed, so popping just drains the
	// stack. The depth-1 sections have already been appended to
	// w.completed when they were pushed.
	for len(w.stack) > 1 {
		w.stack = w.stack[:len(w.stack)-1]
	}

	out := make([]*sectionRecord, 0, len(w.completed)+1)
	if len(w.root.Children) > 0 {
		out = append(out, w.root)
	}
	out = append(out, w.completed...)
	if len(out) == 0 {
		return nil
	}
	return out
}

// top returns the innermost open section on the stack.
func (w *walker) top() *sectionRecord {
	return w.stack[len(w.stack)-1]
}

// pushSection closes out any open sections with depth >= newDepth and
// starts a new section at newDepth. Depth-1 sections get appended to
// w.completed as top-level sections; deeper sections get wrapped in a
// nestedSectionRecord and attached to their parent's Children slice.
func (w *walker) pushSection(s *sectionRecord) {
	for len(w.stack) > 1 && w.top().Depth >= s.Depth {
		w.stack = w.stack[:len(w.stack)-1]
	}
	if s.Depth <= 1 || len(w.stack) == 1 {
		w.completed = append(w.completed, s)
	} else {
		parent := w.top()
		parent.Children = append(parent.Children, nestedSectionRecord{Section: s})
	}
	w.stack = append(w.stack, s)
}

// append puts a contentRecord into the innermost open section.
func (w *walker) append(r contentRecord) {
	w.top().Children = append(w.top().Children, r)
}

// walk is the DFS entry; it dispatches each element to a per-tag handler
// and recurses otherwise. Non-element nodes contribute their text to the
// active paragraph or code block; handler functions are small so the
// combined cognitive complexity stays well under 30.
//
// Content from inside <script>, <style>, <noscript>, and <template> is
// not user-facing markup — skipping those four subtrees is not a chrome
// heuristic but a correctness decision (script/style text isn't prose,
// template content isn't rendered). Other semantic containers
// (nav/header/footer/aside) ARE walked per the locked generic-scraper
// principle; transformers filter them via tag/class/role metadata.
func (w *walker) walk(n *html.Node) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode && isNonRenderable(n.DataAtom) {
		return
	}
	if n.Type != html.ElementNode {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			w.walk(c)
		}
		return
	}
	if w.handleStructural(n) {
		return
	}
	// Unhandled containers: recurse into children.
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c)
	}
}

// isNonRenderable reports whether a is a tag whose subtree is not
// user-facing markup. Script/style contain code, not prose; noscript and
// template subtrees are not rendered. Walking into them would emit
// nonsense text as paragraphs. This is a correctness skip, not a chrome
// classification.
func isNonRenderable(a atom.Atom) bool {
	switch a {
	case atom.Script, atom.Style, atom.Noscript, atom.Template:
		return true
	}
	return false
}
