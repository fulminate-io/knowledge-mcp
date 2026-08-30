// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// makeLink builds a linkRecord from an <a> element; returns nil when
// href resolves to empty.
func (w *walker) makeLink(n *html.Node) *linkRecord {
	raw := getAttr(n, "href")
	abs := resolveHref(w.base, raw)
	if abs == "" {
		return nil
	}
	anchor := ""
	if parsed, err := url.Parse(abs); err == nil && parsed != nil {
		anchor = parsed.Fragment
	}
	return &linkRecord{
		URL:      abs,
		Text:     collectProseText(n),
		Anchor:   anchor,
		NoFollow: parseRelNoFollow(getAttr(n, "rel")),
		Attrs:    extractCommonAttrs(n),
	}
}

// classifyLink sets Rel and appends the link to internalLinks or
// externalCites as appropriate. Duplicates (same URL) are suppressed so
// pageRecord.InternalLinks is a unique set.
//
// rel="nofollow" links are classified (Rel set and dedup-tracked) but are
// NOT appended to w.internalLinks — the crawler's BFS (which enqueues from
// pageRecord.InternalLinks) therefore never fetches them. The link record
// itself still appears as a section-level emitted node so transformers can
// observe the nofollow reference.
func (w *walker) classifyLink(lr *linkRecord) {
	if _, seen := w.seenLinks[lr.URL]; seen {
		if sameHost(w.base, lr.URL) {
			lr.Rel = "internal"
		} else {
			lr.Rel = "external"
		}
		return
	}
	w.seenLinks[lr.URL] = struct{}{}

	if sameHost(w.base, lr.URL) {
		lr.Rel = "internal"
		if !lr.NoFollow {
			w.internalLinks = append(w.internalLinks, lr.URL)
		}
		return
	}
	lr.Rel = "external"
	cpy := *lr
	w.externalCites = append(w.externalCites, &cpy)
}

// parseRelNoFollow reports whether the given rel attribute value contains
// the token "nofollow". The token-list rationale now lives on hasToken,
// which this shares with the role="heading" reader.
func parseRelNoFollow(rel string) bool {
	return hasToken(rel, "nofollow")
}

// sameHost returns true when candidate resolves to the same host as base.
func sameHost(base *url.URL, candidate string) bool {
	if base == nil {
		return false
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed == nil {
		return false
	}
	if parsed.Host == "" {
		return true // relative → same host by resolution
	}
	return strings.EqualFold(parsed.Host, base.Host)
}

// resolveHref resolves a possibly-relative href against base and returns
// the absolute form. Empty input → empty output.
func resolveHref(base *url.URL, href string) string {
	if strings.TrimSpace(href) == "" {
		return ""
	}
	if base == nil {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

// getAttr returns the named attribute's value, or "" if missing.
func getAttr(n *html.Node, name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// firstElementChild returns the first direct child element with the
// given atom, or nil.
func firstElementChild(n *html.Node, a atom.Atom) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == a {
			return c
		}
	}
	return nil
}

// collectText concatenates every descendant text node's data preserving
// order. Whitespace is NOT normalized — callers needing a one-liner use
// collectProseText.
//
// Non-renderable subtrees are skipped: collectText backs handleCodeBlock's
// <pre> source extraction and emitProseText's backticked <code> text, and
// neither should ever carry script or style source.
func collectText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode && isNonRenderable(n.DataAtom) {
			return
		}
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

// collectProseText walks n preserving inline <code> as backtick-wrapped
// text and collapsing surrounding whitespace to single spaces.
func collectProseText(n *html.Node) string {
	var sb strings.Builder
	emitProseText(&sb, n)
	return strings.Join(strings.Fields(sb.String()), " ")
}

// collapseProseLines collapses intra-line whitespace to single spaces while
// PRESERVING author-declared line boundaries, then drops blank leading and
// trailing lines. It is the per-line form of the flat collapse
// collectProseText performs, and on single-line input the two are
// byte-identical.
//
// Its only consumer is flushInlineRun, which needs the <br> boundaries
// emitProseText writes to survive into a paragraph's Text — every other
// caller goes through collectProseText's flat Fields-join and sees the
// de-welding fix alone.
func collapseProseLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	start, end := 0, len(lines)
	for start < end && lines[start] == "" {
		start++
	}
	for end > start && lines[end-1] == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

func emitProseText(sb *strings.Builder, n *html.Node) {
	if n == nil {
		return
	}
	if n.Type == html.TextNode {
		sb.WriteString(n.Data)
		return
	}
	if n.Type == html.ElementNode && isNonRenderable(n.DataAtom) {
		return
	}
	// A <br> is a childless element, so the recursion below writes nothing
	// for it and tokens on either side weld together. Write a NEWLINE rather
	// than a space: collectProseText's flat Fields-join turns it straight
	// back into a single space for every existing caller, while
	// collapseProseLines preserves it as a line boundary.
	if n.Type == html.ElementNode && n.DataAtom == atom.Br {
		sb.WriteByte('\n')
		return
	}
	if n.Type == html.ElementNode && n.DataAtom == atom.Code {
		sb.WriteByte('`')
		sb.WriteString(collectText(n))
		sb.WriteByte('`')
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		emitProseText(sb, c)
	}
}

// extractTable walks a <table> and returns (headers, rows). If a <thead>
// is present, its first row supplies headers; otherwise the first <tr>
// under the table is treated as headers.
func extractTable(n *html.Node) ([]string, [][]string) {
	headers, bodyRows := gatherTableRows(n)
	if headers == nil && len(bodyRows) > 0 {
		headers = rowCells(bodyRows[0])
		bodyRows = bodyRows[1:]
	}
	rows := make([][]string, 0, len(bodyRows))
	for _, tr := range bodyRows {
		rows = append(rows, rowCells(tr))
	}
	return headers, rows
}

// gatherTableRows scans a <table>'s direct children and returns the
// headers row (from <thead>) plus the body row slice (from <tbody>,
// <tfoot>, or loose <tr>s).
func gatherTableRows(n *html.Node) ([]string, []*html.Node) {
	var headers []string
	var bodyRows []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.DataAtom {
		case atom.Thead:
			if headers == nil {
				headers = firstRowCells(c)
			}
		case atom.Tbody, atom.Tfoot:
			bodyRows = append(bodyRows, rowsIn(c)...)
		case atom.Tr:
			bodyRows = append(bodyRows, c)
		}
	}
	return headers, bodyRows
}

// firstRowCells returns rowCells of the first <tr> found under n.
func firstRowCells(n *html.Node) []string {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Tr {
			return rowCells(c)
		}
	}
	return nil
}

// rowsIn returns every <tr> direct child of n.
func rowsIn(n *html.Node) []*html.Node {
	var rows []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Tr {
			rows = append(rows, c)
		}
	}
	return rows
}

// rowCells returns the text content of each <td>/<th> direct child.
func rowCells(tr *html.Node) []string {
	var cells []string
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if c.DataAtom == atom.Td || c.DataAtom == atom.Th {
			cells = append(cells, collectProseText(c))
		}
	}
	return cells
}
