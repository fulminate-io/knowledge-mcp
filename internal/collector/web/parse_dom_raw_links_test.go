// SPDX-License-Identifier: Apache-2.0

package web

import "testing"

// TestParsePage_RawLinksSurviveReadabilityStrip verifies the Phase 5 fix:
// readability's chrome stripper eats <nav> and <footer> on link-dense
// index pages (Fowler eaaCatalog, Hohpe messaging index), zeroing out
// InternalLinks. The pre-readability raw-DOM sweep runs first and
// populates the link set so readability's stripping no longer drops them.
// The test simulates this by passing a *different* cleaned body to
// parsePage than the raw body — cleaned has no <a>, raw has several.
func TestParsePage_RawLinksSurviveReadabilityStrip(t *testing.T) {
	rawBody := `<!doctype html>
<html><body>
<nav><a href="/a.html">A</a><a href="/b.html">B</a></nav>
<div class="site-footer">
  <a href="/c.html">C</a>
  <a href="/d.html">D</a>
  <a href="https://external.test/ext">External</a>
</div>
<main><p>Short prose that won't trigger content detection.</p></main>
</body></html>`
	// Simulate what readability would emit after stripping nav/footer:
	// the cleaned view has no anchors at all.
	cleanedHTML := `<html><body><main><p>Short prose that won't trigger content detection.</p></main></body></html>`

	p := fakeFetched("https://example.test/index.html", rawBody)
	rec, err := parsePage(p, fakeCleaned("", cleanedHTML))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}

	assertRawInternals(t, rec.InternalLinks)
	assertRawExternal(t, rec.ExternalCites)
	assertCleanedOnlyControl(t, cleanedHTML)
}

// assertRawInternals checks the four /a.html../d.html slugs were recovered
// by the raw-DOM sweep as absolute internal URLs.
func assertRawInternals(t *testing.T, got []string) {
	t.Helper()
	wantInternal := map[string]bool{
		"https://example.test/a.html": false,
		"https://example.test/b.html": false,
		"https://example.test/c.html": false,
		"https://example.test/d.html": false,
	}
	for _, u := range got {
		if _, ok := wantInternal[u]; ok {
			wantInternal[u] = true
		}
	}
	for u, seen := range wantInternal {
		if !seen {
			t.Errorf("raw-link sweep missed internal %q; got %v", u, got)
		}
	}
	if len(got) < 4 {
		t.Errorf("want >=4 internal links, got %d: %v", len(got), got)
	}
}

// assertRawExternal checks the single external cite was recovered with
// rel="external".
func assertRawExternal(t *testing.T, cites []*linkRecord) {
	t.Helper()
	for _, ext := range cites {
		if ext.URL == "https://external.test/ext" {
			if ext.Rel != "external" {
				t.Errorf("external rel: want %q got %q", "external", ext.Rel)
			}
			return
		}
	}
	t.Errorf("raw-link sweep missed external https://external.test/ext; got %+v", cites)
}

// assertCleanedOnlyControl is the null-comparison: if the body passed in
// is ONLY the cleaned HTML (no nav/footer anchors anywhere), the
// post-readability walker alone produces zero links. Proves the fix is
// load-bearing — the anchors come from the raw pre-readability pass, not
// from the walker.
func assertCleanedOnlyControl(t *testing.T, cleanedHTML string) {
	t.Helper()
	cleanedOnlyPage := fakeFetched("https://example.test/index.html", cleanedHTML)
	cleanedOnly, err := parsePage(cleanedOnlyPage, fakeCleaned("", cleanedHTML))
	if err != nil {
		t.Fatalf("parsePage (cleaned-only control): %v", err)
	}
	if len(cleanedOnly.InternalLinks) != 0 || len(cleanedOnly.ExternalCites) != 0 {
		t.Fatalf("control: cleaned-only body should have zero links; got int=%v ext=%v",
			cleanedOnly.InternalLinks, cleanedOnly.ExternalCites)
	}
}
