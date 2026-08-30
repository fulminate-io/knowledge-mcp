// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
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
// The walker is a single recursive DFS over the html.Node tree, preceded by
// one whole-document pre-pass for the presentation-heuristic heading arm. A
// section stack tracks open heading scopes so deeper headings nest beneath
// shallower ones (H3 under H2 under H1), and the end of a sectioning element
// closes every scope opened inside it. Content records are appended to
// the top-of-stack section; when no section is open yet, a synthetic
// "" (empty-heading, depth 0) root is used so pre-heading prose still
// has a home.
//
// A section is opened by any of THREE layered arms — native h1-h6, then
// role="heading" with an explicit aria-level, then the calibrated
// presentation heuristic. See handleStructural.
func parsePage(p *fetchedPage, cleaned *cleanedArticle) (*pageRecord, error) {
	if p == nil {
		return nil, fmt.Errorf("parsePage: nil fetchedPage")
	}
	if cleaned == nil {
		return nil, fmt.Errorf("parsePage: nil cleanedArticle")
	}
	// The retention design rests on this: a pageRecord that reached emission
	// with no body would produce a raw_html node holding no HTML. Refuse it
	// by name rather than emit an unfaithful capture.
	if len(p.Body) == 0 {
		return nil, fmt.Errorf("parsePage: empty body for %q", p.URL)
	}

	base, err := url.Parse(p.FinalURL)
	if err != nil || base == nil {
		return nil, fmt.Errorf("parsePage: invalid FinalURL %q: %w", p.FinalURL, err)
	}

	// Walk the RAW DOM, not readability's cleaned tree. Readability's
	// heuristic chrome-strip drops author-marked content (e.g. Hohpe's
	// <p class="pattern-solution"> paragraphs) that transformers rely on.
	// There is NO chrome skip list here: isNonRenderable covers script/style/noscript/template only,
	// and nav/header/footer/aside are walked like any other container per
	// the locked generic-scraper principle (see walker.walk). Readability is
	// still called upstream for title / byline / pubdate extraction via
	// cleaned. nav and aside do additionally END an open heading scope,
	// because both are sectioning content (see isSectionBoundary) — that is
	// section scoping, not a content skip: their subtrees are still walked
	// and still emitted.
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
	// The presentation-heuristic arm needs the WHOLE document before the walk
	// starts — repetition and calibration are both judgements about an
	// element's siblings — so its levels are computed here, once per page, and
	// consulted per element by handleStructural. It runs after
	// pruneHiddenNodes above, or a hidden repeated marker series would be
	// admitted into a group and inflate it.
	w.heuristic = w.heuristicHeadingLevels(doc)
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
		RawHTMLBase64: base64.StdEncoding.EncodeToString(p.Body),
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
	stack         []openSection    // open section scopes, deepest last
	completed     []*sectionRecord // top-level (depth-1) sections closed out
	firstH1       string
	internalLinks []string
	externalCites []*linkRecord
	seenLinks     map[string]struct{}

	// sectionSeq is the monotonic push counter openSection.seq is stamped
	// from. It only ever increases, so a section popped by a later heading
	// never leaves its number behind to be reused.
	sectionSeq int

	// heuristic maps a presentation marker to the heading level the
	// whole-document pre-pass assigned it. Populated once per page in
	// parsePage; nil for a page with no marker series.
	heuristic map[*html.Node]int

	// blockLevel memoises isBlockLevelNode for the duration of ONE page
	// walk. Scoped to the walker because parsePage runs concurrently across
	// the crawler's worker pool.
	blockLevel map[*html.Node]bool
}

// openSection is one entry on the section stack: the record itself plus the
// push-sequence number it was stamped with. The number is what
// closeSectionsOpenedSince keys on — see the comment there for why neither
// the stack's length nor a remembered pointer can stand in for it.
type openSection struct {
	sec *sectionRecord
	seq int
}

func newWalker(base *url.URL) *walker {
	root := &sectionRecord{Depth: 0}
	return &walker{
		base:       base,
		root:       root,
		stack:      []openSection{{sec: root}},
		seenLinks:  map[string]struct{}{},
		blockLevel: map[*html.Node]bool{},
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
	return w.stack[len(w.stack)-1].sec
}

// pushSection closes out any open sections with depth >= newDepth and
// starts a new section at newDepth. Depth-1 sections get appended to
// w.completed as top-level sections; deeper sections get wrapped in a
// nestedSectionRecord and attached to their parent's Children slice.
//
// This is no longer the only thing that closes a section: the end of a
// sectioning element closes every section opened inside it, via
// closeSectionsOpenedSince. The seq stamped here is what that pop keys on.
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
	w.sectionSeq++
	w.stack = append(w.stack, openSection{sec: s, seq: w.sectionSeq})
}

// append puts a contentRecord into the innermost open section.
func (w *walker) append(r contentRecord) {
	w.top().Children = append(w.top().Children, r)
}

// walk is the DFS entry; it dispatches each element to a per-tag handler
// and hands anything unhandled to walkChildren, which partitions that
// element's children into inline runs and emits each run as a record.
// Handler functions are small so the combined cognitive complexity stays
// well under 30.
//
// Content from inside <script>, <style>, <noscript>, and <template> is
// not user-facing markup — skipping those four subtrees is not a chrome
// heuristic but a correctness decision (script/style text isn't prose,
// template content isn't rendered). The <head> subtree is skipped for the
// same reason: under the run model its <title> text would otherwise be
// partitioned into a run and emitted as page content, while readability
// already supplies the title through cleaned.Title (see pickTitle). Other
// semantic containers (nav/header/footer/aside) ARE walked per the locked
// generic-scraper principle; transformers filter them via tag/class/role
// metadata.
func (w *walker) walk(n *html.Node) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode && isNonRenderable(n.DataAtom) {
		return
	}
	if n.Type != html.ElementNode {
		// Text sitting directly under the document or a fragment is
		// partitioned into runs too, not merely recursed past.
		w.walkChildren(n)
		return
	}
	if n.DataAtom == atom.Head {
		return
	}
	// A sectioning element ends the scope of every heading opened inside it.
	// The defer covers every return path out of walk, including the ones
	// handleStructural takes, and sits BELOW the isNonRenderable and Head
	// returns so a skipped subtree never arms it.
	if isSectionBoundary(n.DataAtom) {
		defer w.closeSectionsOpenedSince(w.sectionSeq)
	}
	if w.handleStructural(n) {
		return
	}
	// Unhandled containers: partition their children into inline runs.
	w.walkChildren(n)
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
