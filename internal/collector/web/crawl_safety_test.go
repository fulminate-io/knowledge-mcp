// SPDX-License-Identifier: Apache-2.0

package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCrawl_MaxPagesPerHostBoundsPerHost asserts that MaxPagesPerHost
// bounds the crawl for each host independently: with the cap set to 2,
// no host may contribute more than 2 pages to the result, even when the
// global MaxPages budget is generous.
//
// Cross-host links land in ExternalCites, not InternalLinks, so the
// BFS never follows them on its own. To produce a multi-host crawl we
// seed BOTH hosts directly; each seed drives its own host-local
// traversal and MaxPagesPerHost clips each one at its quota.
func TestCrawl_MaxPagesPerHostBoundsPerHost(t *testing.T) {
	t.Parallel()
	buildHostFixture := func(t *testing.T, tag string) *httptest.Server {
		t.Helper()
		mux := http.NewServeMux()
		for i := 1; i <= 4; i++ {
			body := fmt.Sprintf(`
<h1>Page %d on %s</h1>
<p>This page is one of several under the host's own root. Each page
links to the next on the same host so a BFS discovers them all in a
single host-local walk. The host tag %q is embedded per page so the
ContentHash is unique per (host, page) and dedup never collapses
distinct hosts. Enough prose here to keep readability from dropping
the body for being too short.</p>
<p><a href="/p%d.html">next</a></p>`, i, tag, tag, (i%4)+1)
			path := fmt.Sprintf("/p%d.html", i)
			pageBody := body
			mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprintf(w, `<!doctype html><html><body><article>%s</article></body></html>`, pageBody)
			})
		}
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return srv
	}

	hostA := buildHostFixture(t, "hostA")
	hostB := buildHostFixture(t, "hostB")

	opts := CrawlOptions{
		Source: "crawl-perhost",
		SeedURLs: []string{
			hostA.URL + "/p1.html",
			hostB.URL + "/p1.html",
		},
		MaxDepth:        10,
		MaxPages:        100,
		PolitenessMs:    0,
		MaxPagesPerHost: 2,
	}
	pages, _ := runCrawl(t, opts)

	perHost := map[string]int{}
	for _, p := range pages {
		u, err := urlHost(p.URL)
		require.NoError(t, err, "parse page URL %q", p.URL)
		perHost[u]++
	}
	for host, count := range perHost {
		assert.LessOrEqualf(t, count, opts.MaxPagesPerHost,
			"host %q produced %d pages; cap is %d", host, count, opts.MaxPagesPerHost)
	}
	assert.Len(t, perHost, 2,
		"expected exactly two distinct hosts in the result (got %v)", perHost)
	total := 0
	for _, n := range perHost {
		total += n
	}
	assert.Equal(t, 4, total,
		"expected cap*hosts = 4 total pages (got %d: %v)", total, perHost)
}

// urlHost returns the (lowercased) host portion of a raw URL for test
// bookkeeping. Uses net/url directly rather than the package-internal
// recordHost so the test stays decoupled from crawler internals.
func urlHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return strings.ToLower(u.Host), nil
}

// TestCrawl_ContentHashDedupSkipsDuplicate asserts that two distinct
// URLs that serve byte-identical HTML collapse to a single page node.
// The first-seen URL wins; the duplicate's URL is absent from urlToID,
// and its internal links are NOT enqueued (they would just rediscover
// the same DAG under a different prefix).
func TestCrawl_ContentHashDedupSkipsDuplicate(t *testing.T) {
	t.Parallel()
	f := &crawlFixture{}
	mux := http.NewServeMux()
	sameBody := `<!doctype html><html><body><article>
<h1>Shared body</h1>
<p>This body is byte-identical no matter which session-id the caller
passed in the query string. Readability should retain the body as an
article because it has plenty of prose to keep the extractor happy.</p>
<p>A second paragraph so the content hash is distinctive and not
trivially shared with other tiny pages in this test binary.</p>
</article></body></html>`
	mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.fetched = append(f.fetched, r.URL.Path+"?"+r.URL.RawQuery)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, sameBody)
	})
	mux.HandleFunc("/index.html", f.handle("index.html", `
<h1>Index</h1>
<p>The index links both session variants, both serving identical HTML
content. The crawler should dedup them by ContentHash and emit only one
page node for the shared body.</p>
<p><a href="/same?sid=1">variant 1</a> and
<a href="/same?sid=2">variant 2</a>.</p>`))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	opts := CrawlOptions{
		Source:       "crawl-hashdedup",
		SeedURLs:     []string{f.srv.URL + "/index.html"},
		MaxDepth:     3,
		MaxPages:     10,
		PolitenessMs: 0,
	}
	pages, urlToID := runCrawl(t, opts)

	require.Len(t, pages, 2, "index + one deduped /same variant")

	sameVariantsInMap := 0
	for u := range urlToID {
		if strings.Contains(u, "/same?sid=") {
			sameVariantsInMap++
		}
	}
	assert.Equal(t, 1, sameVariantsInMap,
		"exactly one /same variant should survive dedup (got %v)", urlToID)
}

// Path-segment cap and nofollow tests live in
// crawl_safety_paths_test.go so each file stays under the 300 LOC
// recommended cap.
