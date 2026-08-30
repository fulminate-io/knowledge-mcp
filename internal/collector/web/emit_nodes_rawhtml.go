// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strconv"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// emitRawHTML appends the page's faithful-capture node: the EXACT response
// bytes, base64-encoded, retained in the raw graph so nothing the walker
// declined to emit is lost.
//
// THE NODE CARRIES NO SEARCHABLE TEXT, AND THAT IS THE POINT. SymbolName,
// Summary, Keywords, Description and Content are all left zero because those
// five scalars are precisely the fields the server composes into the BM25
// token index; a node with all five empty contributes nothing to it. Raw
// markup is noise in a token index — the emitted chunks are the search-facing
// text — so do NOT add a SymbolName "for readability" here. That single line
// would defeat the constraint this whole node is shaped around.
//
// FilePath is the page URL. It carries no filesystem meaning; it is the key
// collector/remote.BatchNodes groups by when it splits a collect into bounded
// upload frames, so setting it keeps each page's retained body in a frame with
// its own page rather than letting one unbounded blob decide the frame size
// for every page in the crawl.
//
// idx is the node's position under its page. emitFromPage passes
// len(TopSections), i.e. LAST, so every existing section's contains-position
// is untouched by this node's arrival.
//
// Emission is UNCONDITIONAL. There is deliberately no "skip when the body is
// empty" branch: a skip would make a page whose retention silently failed
// indistinguishable from a page that legitimately had nothing to retain.
// parsePage refuses an empty body outright, and the composition assertion is
// the catcher if that ever stops holding.
func (e *emitter) emitRawHTML(idx int) {
	id := stableID(e.page.URL, "raw_html", "", 0)
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:       id,
		Type:     "raw_html",
		FilePath: e.page.URL,
		Source:   "web-collect",
		Metadata: map[string]string{
			"uri":          e.pageURI,
			"html_base64":  e.page.RawHTMLBase64,
			"html_bytes":   strconv.Itoa(decodedBase64Len(e.page.RawHTMLBase64)),
			"content_hash": e.page.ContentHash,
		},
	})
	e.addContains(e.pageID, id, idx)
}

// decodedBase64Len returns the number of bytes s decodes to, derived from the
// encoding itself rather than by decoding: standard base64 emits four
// characters per three input bytes and pads the final quantum, so the decoded
// length is the quantum count times three, less the padding characters.
//
// Deriving it this way keeps html_bytes the DECODED length — the value its
// name promises — without materializing a second copy of the body, and
// without a decode step that could only fail in a state parsePage forbids.
func decodedBase64Len(s string) int {
	n := len(s)
	if n == 0 {
		return 0
	}
	pad := 0
	if s[n-1] == '=' {
		pad++
		if n >= 2 && s[n-2] == '=' {
			pad++
		}
	}
	return n/4*3 - pad
}
