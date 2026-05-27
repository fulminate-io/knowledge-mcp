// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"
	"testing"
)

// Link-related unit tests split out from parse_dom_test.go so each
// file stays under the 300 LOC recommended cap. Covers
// internal/external classification, relative-href resolution, and
// the rel=nofollow exclusion gate that keeps the BFS out of
// honeypots.

func TestParsePage_InternalVsExternalLinks(t *testing.T) {
	html := `<html><body>
<h1>T</h1>
<p>
  see <a href="/other">internal</a>
  and <a href="https://other.example.org/x">external</a>
  and <a href="https://example.com/same-host">also-internal</a>
</p>
</body></html>`
	p := fakeFetched("https://example.com/page", html)
	rec, err := parsePage(p, fakeCleaned("", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	if len(rec.InternalLinks) != 2 {
		t.Fatalf("want 2 internal links, got %d: %v", len(rec.InternalLinks), rec.InternalLinks)
	}
	if len(rec.ExternalCites) != 1 {
		t.Fatalf("want 1 external cite, got %d", len(rec.ExternalCites))
	}
	if rec.ExternalCites[0].Rel != "external" {
		t.Fatalf("external rel: %q", rec.ExternalCites[0].Rel)
	}
	if !strings.HasPrefix(rec.InternalLinks[0], "https://example.com/") {
		t.Fatalf("internal links not absolute: %v", rec.InternalLinks)
	}
}

func TestParsePage_RelativeHrefResolution(t *testing.T) {
	html := `<html><body>
<h1>T</h1>
<p><a href="../other.html">rel</a></p>
</body></html>`
	p := fakeFetched("https://example.com/docs/page.html", html)
	rec, err := parsePage(p, fakeCleaned("", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	if len(rec.InternalLinks) != 1 {
		t.Fatalf("want 1 internal link, got %v", rec.InternalLinks)
	}
	want := "https://example.com/other.html"
	if rec.InternalLinks[0] != want {
		t.Fatalf("resolved href: want %q got %q", want, rec.InternalLinks[0])
	}
}

// TestParsePage_NoFollowLinkExcludedFromInternalLinks asserts that a
// same-host <a rel="nofollow"> link is NOT appended to
// pageRecord.InternalLinks — the BFS crawler drives off that field, so
// this is how we ensure rel=nofollow links are never enqueued. A regular
// same-host link without rel=nofollow still appears in the slice.
func TestParsePage_NoFollowLinkExcludedFromInternalLinks(t *testing.T) {
	html := `<html><body>
<h1>T</h1>
<p>
  <a href="/ok">follow me</a>
  <a href="/trap" rel="nofollow">do not follow</a>
  <a href="/also-ok" rel="noopener">different token</a>
</p>
</body></html>`
	p := fakeFetched("https://example.com/page", html)
	rec, err := parsePage(p, fakeCleaned("", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	// Expected: /ok + /also-ok in InternalLinks; /trap excluded.
	wantSet := map[string]bool{
		"https://example.com/ok":      true,
		"https://example.com/also-ok": true,
	}
	for _, u := range rec.InternalLinks {
		if !wantSet[u] {
			t.Errorf("unexpected URL in InternalLinks: %q", u)
		}
		delete(wantSet, u)
	}
	for u := range wantSet {
		t.Errorf("missing URL in InternalLinks: %q (got %v)", u, rec.InternalLinks)
	}
	for _, u := range rec.InternalLinks {
		if u == "https://example.com/trap" {
			t.Fatalf("nofollow /trap leaked into InternalLinks: %v", rec.InternalLinks)
		}
	}
}

// TestParseRelNoFollow covers the tokenization rules: whitespace-split,
// case-insensitive match, substring non-match.
func TestParseRelNoFollow(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"nofollow", true},
		{"NOFOLLOW", true},
		{"NoFollow", true},
		{"noopener nofollow noreferrer", true},
		{"noopener  nofollow  noreferrer", true}, // extra whitespace
		{"noopener", false},
		{"nofollowup", false}, // substring must not match
		{"nofollowing", false},
	}
	for _, tc := range cases {
		got := parseRelNoFollow(tc.in)
		if got != tc.want {
			t.Errorf("parseRelNoFollow(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
