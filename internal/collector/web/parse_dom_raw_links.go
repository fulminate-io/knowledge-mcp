// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"log/slog"
	"net/url"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// seedRawLinks parses the raw (pre-readability) page body and primes the
// walker's link bookkeeping with every <a href> present in the original
// document. The walker's subsequent pass over the cleaned tree will
// classify any surviving anchors — but because w.seenLinks is already
// populated from this pass, it will NOT re-emit duplicates. The net
// effect: pageRecord.InternalLinks / .ExternalCites end up as a strict
// superset of what the walker alone would produce.
//
// Silently no-ops on empty rawBody or html.Parse error — the walker pass
// is authoritative for structured content, so a pre-pass failure should
// never abort page processing.
func seedRawLinks(w *walker, rawBody []byte, base *url.URL) {
	if len(rawBody) == 0 || w == nil || base == nil {
		return
	}
	root, err := html.Parse(bytes.NewReader(rawBody))
	if err != nil {
		slog.Debug("web.parse: raw-link html.Parse failed, skipping pre-readability pass",
			"err", err)
		return
	}
	internal, external, seen := extractRawLinks(root, base)
	w.internalLinks = append(w.internalLinks, internal...)
	w.externalCites = append(w.externalCites, external...)
	for k := range seen {
		w.seenLinks[k] = struct{}{}
	}
}

// extractRawLinks walks a parsed but un-cleaned *html.Node tree and
// collects every <a href> anchor, regardless of parent container.
// Readability's cleanArticle strips nav / header / footer and other
// link-dense index chrome, zeroing out pageRecord.InternalLinks on pages
// like Fowler's eaaCatalog and Hohpe's messaging index. This helper runs
// BEFORE cleanArticle so those links survive.
//
// Classification rules mirror the post-readability walker exactly:
// resolveHref via url.ResolveReference, sameHost decides internal vs
// external, dedup by absolute URL string (first-occurrence-wins). The
// returned seen map lets callers prime the walker's seenLinks so it
// doesn't re-emit duplicates once readability-survived anchors are
// classified too. Attrs are left zero-value — that is the post-readability
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
