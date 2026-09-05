// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"log/slog"
	"net/url"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// seedRawLinks parses the page body and primes the walker's link bookkeeping
// with every <a href> present in it. The walker's own subsequent pass will
// classify the same anchors — but because w.seenLinks is already populated
// from this pass, it will NOT re-emit duplicates.
//
// BOTH PASSES READ THE SAME BYTES. This doc previously said the pre-pass
// existed because readability strips nav, header and footer and that the
// walker ran over a "cleaned tree"; neither is true. cleanArticle contributes
// title, byline and publication date only, and parsePage hands the walker the
// raw response body. The one real remaining difference is the HIDDEN-ELEMENT
// asymmetry recorded below: parsePage prunes hidden subtrees before the walk,
// and this pass does not.
//
// A parse failure returns true so fetchAndParse can count it as the
// raw_link_parse_failed degrade class; it still does not abort the page,
// because the walker pass is authoritative for structured content.
//
// THE HIDDEN-ELEMENT ASYMMETRY IS EXPRESSLY APPROVED, not accidental. parsePage
// runs pruneHiddenNodes over the cleaned tree before the walk, so content
// inside a hidden subtree is discarded — but THIS pass re-parses the UNPRUNED
// body, so links inside those same hidden subtrees are still crawled. A hidden
// nav therefore contributes its URLs to the frontier while contributing none of
// its text to the graph.
//
// THE APPROVAL: the pre-pass is kept because it is what makes InternalLinks a
// strict superset of the walker's own view, and the hiding policy is ruled out
// of scope for this ticket. WHAT IS LOST, stated so it is visible rather than
// discovered: the crawl may reach pages linked only from content no reader can
// see. A SECOND LIMITATION rides with it — the prune matches the `hidden`
// attribute, aria-hidden and inline display/visibility styles, so class-based
// hiding (a `.is-hidden` rule in a stylesheet) is invisible to it and that
// content is NOT pruned at all. The hidden_pruned degrade class counts what the
// prune did remove, so the size of the pruning is at least visible in the
// collect response. Do not narrow or widen either half without the same
// explicit approval.
func seedRawLinks(w *walker, rawBody []byte, base *url.URL) bool {
	if len(rawBody) == 0 || w == nil || base == nil {
		return false
	}
	root, err := html.Parse(bytes.NewReader(rawBody))
	if err != nil {
		slog.Warn("web.parse: raw-link html.Parse failed, pre-readability link pass skipped",
			"err", err)
		return true
	}
	internal, external, seen := extractRawLinks(root, base)
	w.internalLinks = append(w.internalLinks, internal...)
	w.externalCites = append(w.externalCites, external...)
	for k := range seen {
		w.seenLinks[k] = struct{}{}
	}
	return false
}

// extractRawLinks walks a parsed but un-cleaned *html.Node tree and
// collects every <a href> anchor, regardless of parent container.
// Readability's cleanArticle strips nav / header / footer and other
// link-dense index chrome, zeroing out pageRecord.InternalLinks on pages
// like Fowler's eaaCatalog and Hohpe's messaging index. This helper runs
// BEFORE cleanArticle so those links survive.
//
// Classification rules mirror the walker's own exactly: resolveHref via
// url.ResolveReference, sameHost decides internal vs external, dedup by
// absolute URL string (first-occurrence-wins). The returned seen map lets
// callers prime the walker's seenLinks so it does not re-emit duplicates when
// it reaches the same anchors. Attrs are left zero-value — that is the
// walker's job; this pass exists purely for crawl discovery.
//
// rel="nofollow" links are tracked in seen (so the walker's later pass
// doesn't duplicate them) but are NOT appended to internal. External
// nofollow links remain in external as reference cites — they would
// never be enqueued by the BFS either way since ExternalCites does not
// feed crawl discovery.
func extractRawLinks(root *html.Node, base *url.URL) (
	internal []string,
	external []*linkRecord,
	seen map[string]struct{},
) {
	seen = map[string]struct{}{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.A {
			if lr := rawLinkFor(n, base); lr != nil {
				if _, dup := seen[lr.URL]; !dup {
					seen[lr.URL] = struct{}{}
					switch {
					case !sameHost(base, lr.URL):
						external = append(external, lr)
					case !lr.NoFollow:
						internal = append(internal, lr.URL)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return internal, external, seen
}

// rawLinkFor builds a linkRecord from a raw-DOM <a> element during the
// pre-readability sweep. Returns nil when href is missing or resolves to
// empty. Rel is pre-classified ("internal" for same-host, "external"
// otherwise) so the caller doesn't need to re-run sameHost. NoFollow is
// set from the <a>'s rel attribute (see parseRelNoFollow).
func rawLinkFor(n *html.Node, base *url.URL) *linkRecord {
	abs := resolveHref(base, getAttr(n, "href"))
	if abs == "" {
		return nil
	}
	anchor := ""
	if parsed, err := url.Parse(abs); err == nil && parsed != nil {
		anchor = parsed.Fragment
	}
	rel := "external"
	if sameHost(base, abs) {
		rel = "internal"
	}
	return &linkRecord{
		URL:      abs,
		Text:     collectProseText(n),
		Rel:      rel,
		Anchor:   anchor,
		NoFollow: parseRelNoFollow(getAttr(n, "rel")),
	}
}
