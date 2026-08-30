// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"
	"testing"
	"time"
)

// fakeCleaned builds a cleanedArticle stub. The walker parses the RAW
// response body (see parsePage), never any readability output, so the HTML
// argument is accepted for call-site symmetry with fakeFetched and read by
// nothing — hence the blank name.
func fakeCleaned(title string, _ string) *cleanedArticle {
	return &cleanedArticle{Title: title}
}

func fakeFetched(finalURL, body string) *fetchedPage {
	return &fetchedPage{
		URL:       finalURL,
		FinalURL:  finalURL,
		Status:    200,
		Body:      []byte(body),
		FetchedAt: time.Unix(0, 0).UTC(),
	}
}

// TestParsePage_EmptyBodyIsRefused pins the invariant the retention design
// rests on: a page with no body never becomes a pageRecord, so a raw_html node
// holding no HTML cannot be constructed through parsePage.
//
// The non-empty control in the same run is what keeps this from being
// satisfiable by a parsePage that refuses everything, and it also pins the
// positive half of the invariant — a real body yields a populated
// RawHTMLBase64 rather than an empty one.
func TestParsePage_EmptyBodyIsRefused(t *testing.T) {
	const pageURL = "https://example.com/empty"

	rec, err := parsePage(fakeFetched(pageURL, ""), fakeCleaned("", ""))
	if err == nil {
		t.Fatalf("empty body: want an error, got record %+v", rec)
	}
	if rec != nil {
		t.Errorf("empty body: want a nil record alongside the error, got %+v", rec)
	}
	if !strings.Contains(err.Error(), "parsePage") {
		t.Errorf("error %q does not name parsePage", err.Error())
	}
	if !strings.Contains(err.Error(), pageURL) {
		t.Errorf("error %q does not name the URL %q", err.Error(), pageURL)
	}

	// KNOWN POSITIVE, same function: a real body is accepted and retained.
	const body = `<html><body><h1>Kept</h1><p>a body with content</p></body></html>`
	ok, err := parsePage(fakeFetched("https://example.com/kept", body), fakeCleaned("Kept", body))
	if err != nil {
		t.Fatalf("non-empty body: unexpected error %v — the guard above is refusing everything", err)
	}
	if ok.RawHTMLBase64 == "" {
		t.Error("non-empty body: RawHTMLBase64 is empty, so the retention capture did not happen")
	}
}

func TestParsePage_NestedHeadingHierarchy(t *testing.T) {
	html := `<html><body>
<h1 id="top">Top</h1>
<p>intro</p>
<h2>Middle</h2>
<p>middle prose</p>
<h3>Deep</h3>
<p>deep prose</p>
</body></html>`
	p := fakeFetched("https://example.com/p", html)
	rec, err := parsePage(p, fakeCleaned("", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	if len(rec.TopSections) != 1 {
		t.Fatalf("want 1 top section, got %d", len(rec.TopSections))
	}
	top := rec.TopSections[0]
	if top.Heading != "Top" || top.Depth != 1 || top.Anchor != "top" {
		t.Fatalf("bad top section: %+v", top)
	}

	h2, ok := findNestedSection(top)
	if !ok {
		t.Fatalf("expected H2 nested inside H1, children=%+v", top.Children)
	}
	if h2.Heading != "Middle" || h2.Depth != 2 {
		t.Fatalf("bad H2: %+v", h2)
	}
	h3, ok := findNestedSection(h2)
	if !ok {
		t.Fatalf("expected H3 nested inside H2, children=%+v", h2.Children)
	}
	if h3.Heading != "Deep" || h3.Depth != 3 {
		t.Fatalf("bad H3: %+v", h3)
	}
}

func TestParsePage_PreCodeBlockLanguage(t *testing.T) {
	html := `<html><body>
<h1>T</h1>
<pre><code class="language-go">func main() {}</code></pre>
</body></html>`
	p := fakeFetched("https://example.com/p", html)
	rec, err := parsePage(p, fakeCleaned("", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	cb := findCodeBlock(rec)
	if cb == nil {
		t.Fatalf("no codeBlockRecord; sections=%+v", rec.TopSections)
	}
	if cb.Language != "go" {
		t.Fatalf("want language go, got %q", cb.Language)
	}
	if !strings.Contains(cb.Source, "func main()") {
		t.Fatalf("source lost: %q", cb.Source)
	}
}

func TestParsePage_MixedChildrenDocumentOrder(t *testing.T) {
	html := `<html><body>
<h1>T</h1>
<p>para-a</p>
<ul><li>x</li><li>y</li></ul>
<pre><code>code</code></pre>
<p>para-b</p>
</body></html>`
	p := fakeFetched("https://example.com/p", html)
	rec, err := parsePage(p, fakeCleaned("", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	if len(rec.TopSections) != 1 {
		t.Fatalf("want 1 top section, got %d", len(rec.TopSections))
	}
	kinds := childKinds(rec.TopSections[0])
	want := []string{"paragraph", "list", "code_block", "paragraph"}
	if !equalStrings(kinds, want) {
		t.Fatalf("order mismatch: want %v got %v", want, kinds)
	}
}

// Link-related tests (InternalVsExternalLinks,
// RelativeHrefResolution, NoFollowLinkExcludedFromInternalLinks,
// ParseRelNoFollow) live in parse_dom_links_test.go so each file
// stays under the 300 LOC recommended cap.

func TestParsePage_TitleFallbackCleaned(t *testing.T) {
	html := `<html><body><h1>H-One</h1><p>x</p></body></html>`
	p := fakeFetched("https://example.com/p", html)
	rec, err := parsePage(p, fakeCleaned("Clean Title", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	if rec.Title != "Clean Title" {
		t.Fatalf("want cleaned title, got %q", rec.Title)
	}
}

func TestParsePage_TitleFallbackH1(t *testing.T) {
	html := `<html><body><h1>H-One</h1><p>x</p></body></html>`
	p := fakeFetched("https://example.com/p", html)
	rec, err := parsePage(p, fakeCleaned("", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	if rec.Title != "H-One" {
		t.Fatalf("want H-One, got %q", rec.Title)
	}
}

func TestParsePage_TitleFallbackEmpty(t *testing.T) {
	html := `<html><body><p>no heading</p></body></html>`
	p := fakeFetched("https://example.com/p", html)
	rec, err := parsePage(p, fakeCleaned("", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	if rec.Title != "" {
		t.Fatalf("want empty title, got %q", rec.Title)
	}
}

func TestParsePage_ContentHashDeterministic(t *testing.T) {
	html := `<html><body><h1>X</h1></body></html>`
	p1 := fakeFetched("https://example.com/p", "hello")
	p2 := fakeFetched("https://example.com/p", "hello")
	p3 := fakeFetched("https://example.com/p", "hello world")
	r1, _ := parsePage(p1, fakeCleaned("", html))
	r2, _ := parsePage(p2, fakeCleaned("", html))
	r3, _ := parsePage(p3, fakeCleaned("", html))
	if r1.ContentHash != r2.ContentHash {
		t.Fatalf("hash not deterministic: %q vs %q", r1.ContentHash, r2.ContentHash)
	}
	if r1.ContentHash == r3.ContentHash {
		t.Fatalf("different bodies produced same hash")
	}
	if len(r1.ContentHash) != 64 {
		t.Fatalf("want 64-hex sha256, got len %d", len(r1.ContentHash))
	}
}

// --- helpers ---

func findNestedSection(s *sectionRecord) (*sectionRecord, bool) {
	for _, c := range s.Children {
		if ns, ok := c.(nestedSectionRecord); ok {
			return ns.Section, true
		}
	}
	return nil, false
}

func findCodeBlock(rec *pageRecord) *codeBlockRecord {
	for _, s := range rec.TopSections {
		if cb := findCodeBlockIn(s); cb != nil {
			return cb
		}
	}
	return nil
}

func findCodeBlockIn(s *sectionRecord) *codeBlockRecord {
	for _, c := range s.Children {
		switch v := c.(type) {
		case codeBlockRecord:
			return &v
		case nestedSectionRecord:
			if cb := findCodeBlockIn(v.Section); cb != nil {
				return cb
			}
		}
	}
	return nil
}

func childKinds(s *sectionRecord) []string {
	kinds := make([]string, 0, len(s.Children))
	for _, c := range s.Children {
		kinds = append(kinds, c.recordKind())
	}
	return kinds
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
