// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ctPage builds a fetched response carrying contentType and body. An EMPTY
// contentType leaves the header map nil, which is the "origin sent no
// Content-Type at all" case rather than "sent an empty one".
func ctPage(contentType string, body []byte) *fetchedPage {
	p := &fetchedPage{URL: "https://example.test/resource", Status: 200, Body: body}
	if contentType != "" {
		p.Header = http.Header{}
		p.Header.Set("Content-Type", contentType)
	}
	return p
}

// The four fixture bodies. Each one exists to produce a specific SNIFF result,
// and the test asserts that it really does before relying on it — a fixture
// that sniffed as something else would make whole rows prove nothing.
var (
	// ctHTMLDoc opens with a doctype, which is in the stdlib signature table.
	ctHTMLDoc = []byte(`<!doctype html><html><head><title>t</title></head>
<body><article><h1>A Real Page</h1><p>Substantive prose.</p></article></body></html>`)

	// ctFragment opens with <article>, which is NOT in the stdlib signature
	// table — "<A" only matches when the next byte terminates the tag — so a
	// genuine HTML document sniffs as text/plain.
	ctFragment = []byte(`<article><h1>A Real Fragment</h1>
<p>A genuine article whose opening tag falls outside the stdlib signature table.</p></article>`)

	// ctZIP and ctPDF are real container prefixes padded past the 512-byte
	// sniff window; a short body runs out of bytes and sniffs as something else.
	ctZIP = append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0x01}, 600)...)
	ctPDF = append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{0x02}, 600)...)
)

// TestClassifyPage_DecisionMatrix walks the cross of what an origin DECLARES
// and what the bytes SNIFF as, asserting both the verdict and the reason.
//
// THE REASON IS ASSERTED, NOT JUST THE VERDICT, because the census class is a
// fixed-vocabulary counter carrying no URLs: the reason string is the operator's
// only handle on why a particular resource was skipped.
//
// This level exists because the census level CANNOT see it. CollectComposition
// carries per-class counts and no URLs, so under a header-only implementation
// the census subtest renders byte-identically to the correct one and passes —
// the lying origin it wrongly admits and the generically-declared page it
// wrongly declines swap places in the totals. These rows are what reject that.
func TestClassifyPage_DecisionMatrix(t *testing.T) {
	// KNOWN NEGATIVE FIRST: pin what each fixture actually sniffs as, against an
	// expectation this file states rather than one classifyPage supplies, so a
	// fixture that drifted fails loudly instead of quietly weakening every row.
	require.Equal(t, "text/html", sniffedMediaType(ctPage("", ctHTMLDoc)), "the HTML fixture must sniff as text/html")
	require.Equal(t, "text/plain", sniffedMediaType(ctPage("", ctFragment)), "the fragment fixture must sniff as text/plain")
	require.Equal(t, "application/zip", sniffedMediaType(ctPage("", ctZIP)), "the ZIP fixture must sniff as application/zip")
	require.Equal(t, "application/pdf", sniffedMediaType(ctPage("", ctPDF)), "the PDF fixture must sniff as application/pdf")
	require.Equal(t, "text/plain", sniffedMediaType(ctPage("", nil)), "an empty body must sniff as text/plain")

	cases := []struct {
		name       string
		declared   string
		body       []byte
		wantIsPage bool
		wantReason string
	}{
		{
			name: "declared_html_over_html", declared: "text/html; charset=utf-8", body: ctHTMLDoc,
			wantIsPage: true, wantReason: "declared page type",
		},
		{
			// A sniff-must-say-HTML implementation loses this one.
			name: "declared_html_over_fragment_sniffing_as_text", declared: "text/html; charset=utf-8", body: ctFragment,
			wantIsPage: true, wantReason: "declared page type",
		},
		{
			name: "declared_html_over_zip_is_a_lying_origin", declared: "text/html; charset=utf-8", body: ctZIP,
			wantIsPage: false, wantReason: "declared-vs-sniffed",
		},
		{
			name: "declared_html_over_pdf_is_a_lying_origin", declared: "text/html; charset=utf-8", body: ctPDF,
			wantIsPage: false, wantReason: "declared-vs-sniffed",
		},
		{
			// Stays a page so it reaches cleanArticle and keeps counting
			// clean_failed, the class an empty 200 has always had.
			name: "declared_html_over_empty_body_is_a_page", declared: "text/html; charset=utf-8", body: nil,
			wantIsPage: true, wantReason: "declared page type",
		},
		{
			// A sniff-only implementation loses this one.
			name: "declared_epub_is_declined_on_the_declaration", declared: "application/epub+zip", body: ctHTMLDoc,
			wantIsPage: false, wantReason: "declared type is not a page type",
		},
		{
			name: "declared_generic_over_html_is_parsed", declared: "application/octet-stream", body: ctHTMLDoc,
			wantIsPage: true, wantReason: "sniffed bytes are a page",
		},
		{
			name: "declared_generic_over_zip_is_declined", declared: "application/octet-stream", body: ctZIP,
			wantIsPage: false, wantReason: "sniffed bytes are not a page",
		},
		{
			name: "absent_header_over_html_is_parsed", declared: "", body: ctHTMLDoc,
			wantIsPage: true, wantReason: "sniffed bytes are a page",
		},
		{
			name: "absent_header_over_zip_is_declined", declared: "", body: ctZIP,
			wantIsPage: false, wantReason: "sniffed bytes are not a page",
		},
		{
			// A malformed Content-Type falls through to the bytes rather than
			// erroring: there is no declaration to be authoritative.
			name: "unparseable_header_over_html_is_parsed", declared: "@@@ not a media type", body: ctHTMLDoc,
			wantIsPage: true, wantReason: "sniffed bytes are a page",
		},
		{
			// THE ASYMMETRY GATE. The same fragment body row two ADMITS is
			// DECLINED here, because there is no page declaration to fall back
			// on. Without this row an implementation reading "a generic sniff is
			// never a decline on its own" as universal passes every other leg
			// while admitting any headerless byte stream free of control
			// characters.
			name: "absent_header_over_fragment_sniffing_as_text_is_declined", declared: "", body: ctFragment,
			wantIsPage: false, wantReason: "sniffed bytes are not a page",
		},
		{
			// The same asymmetry with an explicit generic declaration rather
			// than an absent header, so both spellings of "no page declaration"
			// are covered.
			name: "declared_generic_over_fragment_sniffing_as_text_is_declined", declared: "text/plain; charset=utf-8", body: ctFragment,
			wantIsPage: false, wantReason: "sniffed bytes are not a page",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyPage(ctPage(tc.declared, tc.body))
			assert.Equal(t, tc.wantIsPage, got.isPage,
				"declared %q over bytes sniffing as %q: want isPage=%v, got %v (reason %q)",
				tc.declared, got.sniffed, tc.wantIsPage, got.isPage, got.reason)
			assert.Equal(t, tc.wantReason, got.reason,
				"declared %q over bytes sniffing as %q: the reason is the operator's only handle on the decline",
				tc.declared, got.sniffed)
		})
	}

	// A nil response is declined rather than dereferenced.
	nilVerdict := classifyPage(nil)
	assert.False(t, nilVerdict.isPage, "a nil response must be declined")
	assert.Equal(t, "no response", nilVerdict.reason, "a nil response must carry the no-response reason")
}
