// SPDX-License-Identifier: Apache-2.0

package web

import (
	"mime"
	"net/http"
)

// pageMediaTypes is the set of media types this collector will parse as a
// page. It is the SAME FAMILY the fetcher's Accept header prefers, and the
// difference between the two is the point of this file: the Accept header is a
// PREFERENCE a server may ignore — its trailing */*;q=0.8 accepts anything, so
// a site is free to answer a request for HTML with a ZIP — while this set is a
// DECISION about what arrived.
var pageMediaTypes = map[string]bool{
	"text/html":             true,
	"application/xhtml+xml": true,
	"application/xml":       true,
	"text/xml":              true,
}

// genericMediaTypes is the set of media types that carry no information about
// whether the bytes are a page. It has TWO ROLES and they are ASYMMETRIC.
//
// AS A DECLARATION a generic member says nothing: an origin answering
// application/octet-stream has declined to classify its own bytes, so the bytes
// decide instead.
//
// AS A SNIFF RESULT the role depends on whether a page DECLARATION is present.
// Against a page declaration a generic sniff is INCONCLUSIVE and never
// overrules it, because a real HTML page whose first tag falls outside the
// stdlib signature table sniffs as text/plain, and an empty body sniffs as
// text/plain too. But when the declaration is absent, unparseable or itself
// generic there is no page declaration to fall back on, and outcome 3 of
// classifyPage governs: only a PAGE sniff admits, so a generic sniff DECLINES
// there. The unqualified sentence "a generic sniff result is never a decline on
// its own" is false of outcome 3 and is deliberately not written here.
var genericMediaTypes = map[string]bool{
	"application/octet-stream": true,
	"text/plain":               true,
}

// pageVerdict is what classifyPage concluded and why. The reason and both
// observed media types are carried out so a declined resource is identifiable
// in the log without re-fetching it: the degrade census is a fixed vocabulary
// of counters and cannot carry a sub-reason.
type pageVerdict struct {
	isPage   bool
	reason   string
	declared string
	sniffed  string
}

// classifyPage decides whether a fetched response is a page worth parsing.
//
// IT READS BOTH SIGNALS ON EVERY RESPONSE — the origin's declared Content-Type
// and the first 512 bytes — because a gate consulting only one is wrong in one
// direction each way. Header-only parses a ZIP an origin mislabels text/html;
// sniff-only lets a crafted leading 512 bytes override an origin that answered
// the question honestly. A response from an upstream service is untrusted
// input, however much you trust the service, and the DECLARATION is one
// untrusted input among two rather than the decision.
//
// The three outcomes, in this order:
//
//  1. A parseable, non-generic declaration OUTSIDE pageMediaTypes is
//     authoritative and the resource is declined. An origin saying
//     application/epub+zip is answering the question, and the bytes do not get
//     to overrule it.
//  2. A declaration INSIDE pageMediaTypes is overridden only by a DECISIVE
//     sniff — one that parses and names something in neither pageMediaTypes nor
//     genericMediaTypes, which is the stdlib sniffer's vocabulary for a known
//     non-page container: application/zip, application/pdf, application/x-gzip,
//     the image/ family, application/wasm and the rest. This is the lying-origin
//     case and it is why the gate must sniff even when the header looks right.
//  3. An absent, unparseable or generic declaration leaves the bytes to decide,
//     and ONLY A PAGE SNIFF ADMITS IT — a generic or unparseable sniff declines
//     here, unlike outcome 2.
//
// TWO THINGS STAY PARSED, both of them outcome 2, and both depend on the PAGE
// declaration being present. An HTML fragment whose opening tag is outside the
// stdlib signature table sniffs as text/plain; declining it would lose real
// pages. An EMPTY body sniffs as text/plain too, and it is already classified —
// cleanArticle rejects it and it counts clean_failed, the class it has had all
// along. Neither survives the header going missing: that is outcome 3, and it
// is a decline.
func classifyPage(p *fetchedPage) pageVerdict {
	if p == nil {
		return pageVerdict{isPage: false, reason: "no response"}
	}
	declared := declaredMediaType(p)
	sniffed := sniffedMediaType(p)
	v := pageVerdict{declared: declared, sniffed: sniffed}

	// Outcome 1: the origin classified its own bytes as something other than a
	// page, and that answer is authoritative.
	if declared != "" && !genericMediaTypes[declared] && !pageMediaTypes[declared] {
		v.reason = "declared type is not a page type"
		return v
	}

	// Outcome 2: a page declaration, overruled only by a decisive non-page
	// sniff.
	if pageMediaTypes[declared] {
		if sniffed != "" && !pageMediaTypes[sniffed] && !genericMediaTypes[sniffed] {
			v.reason = "declared-vs-sniffed"
			return v
		}
		v.isPage = true
		v.reason = "declared page type"
		return v
	}

	// Outcome 3: no usable declaration, so only a page sniff admits.
	if pageMediaTypes[sniffed] {
		v.isPage = true
		v.reason = "sniffed bytes are a page"
		return v
	}
	v.reason = "sniffed bytes are not a page"
	return v
}

// declaredMediaType returns the response's Content-Type media type with its
// parameters stripped, or "" when the header map is nil, the header is absent
// or empty, or the value does not parse. An unparseable declaration is treated
// as no declaration rather than as an error: the bytes are still there to be
// read, and outcome 3 is a stricter disposition than erroring the fetch would
// be.
func declaredMediaType(p *fetchedPage) string {
	if p.Header == nil {
		return ""
	}
	raw := p.Header.Get("Content-Type")
	if raw == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return ""
	}
	return mt
}

// sniffedMediaType returns the media type of the response body as the stdlib
// content sniffer reads it, or "" when that rendering does not parse.
// http.DetectContentType reads at most the first 512 bytes itself, so the body
// is not pre-sliced.
func sniffedMediaType(p *fetchedPage) string {
	mt, _, err := mime.ParseMediaType(http.DetectContentType(p.Body))
	if err != nil {
		return ""
	}
	return mt
}
