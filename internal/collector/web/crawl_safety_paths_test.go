// SPDX-License-Identifier: Apache-2.0

package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Path-segment cap and nofollow crawler-safety tests split out from
// crawl_safety_test.go so each file stays under the 300 LOC
// recommended cap.

// TestCrawl_MaxPathSegmentsEnforced asserts that a recursive-path trap
// (each page emits a link one segment deeper) is bounded by
// MaxPathSegments: the crawler refuses to enqueue URLs whose path
// segment count exceeds the cap.
func TestCrawl_MaxPathSegmentsEnforced(t *testing.T) {
	t.Parallel()
	f := &crawlFixture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.fetched = append(f.fetched, r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		next := r.URL.Path
		if next == "/" {
			next = "/a"
		} else {
			next += "/x"
		}
		fmt.Fprintf(w, `<!doctype html><html><body><article>
<h1>Depth %d</h1>
<p>This is a recursive-path trap. Every page renders a link one segment
deeper than itself, which would expand indefinitely without a cap.
Readability keeps pages with a few sentences of prose so the extractor
retains the body as article content rather than dropping it.</p>
<p><a href="%s">next</a></p>
</article></body></html>`, pathSegmentCount(next), next)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	opts := CrawlOptions{
		Source:          "crawl-pathcap",
		SeedURLs:        []string{f.srv.URL + "/"},
		MaxDepth:        50,
		MaxPages:        100,
		PolitenessMs:    0,
		MaxPathSegments: 3,
	}
	pages, _ := runCrawl(t, opts)

	for _, p := range pages {
		segs := pathSegmentCount(p.URL)
		assert.LessOrEqualf(t, segs, opts.MaxPathSegments,
			"page %q has %d segments; cap is %d", p.URL, segs, opts.MaxPathSegments)
	}
	for _, p := range f.pathsFetched() {
		segs := pathSegmentCount(f.srv.URL + p)
		assert.LessOrEqualf(t, segs, opts.MaxPathSegments,
			"fetched path %q has %d segments; cap is %d", p, segs, opts.MaxPathSegments)
	}
}

// TestCrawl_MaxPathSegmentsZeroMeansNoCap asserts that zero means "no
// cap" — the same recursive fixture is free to run until MaxPages bounds
// it rather than stopping at a segment count.
func TestCrawl_MaxPathSegmentsZeroMeansNoCap(t *testing.T) {
	t.Parallel()
	f := &crawlFixture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.fetched = append(f.fetched, r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		next := r.URL.Path
		if next == "/" {
			next = "/a"
		} else {
			next += "/x"
		}
		//nolint:gosec // controlled test fixture, no external taint
		fmt.Fprintf(w, `<!doctype html><html><body><article>
<h1>Depth page</h1>
<p>Filler prose line one of several to keep readability from dropping
the body as too-short. Short pages get rejected entirely.</p>
<p>Filler prose line two to give the extractor enough characters to
retain the body as an article rather than chrome.</p>
<p><a href="%s">next</a></p>
</article></body></html>`, next)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	opts := CrawlOptions{
		Source:          "crawl-pathcap-zero",
		SeedURLs:        []string{f.srv.URL + "/"},
		MaxDepth:        20,
		MaxPages:        5,
		PolitenessMs:    0,
		MaxPathSegments: 0, // explicit: no cap
	}
	pages, _ := runCrawl(t, opts)

	require.Len(t, pages, 5, "MaxPages should be the only bound")
	deep := 0
	for _, p := range pages {
		if pathSegmentCount(p.URL) > 3 {
			deep++
		}
	}
	assert.Positive(t, deep,
		"expected at least one page with segment count > 3 when cap is disabled")
}

// TestCrawl_NoFollowLinkNotFetched asserts that a same-host link with
// rel="nofollow" is never enqueued by the BFS, so the fixture server
// never receives a request for /trap. Other same-host links on the seed
// page are fetched normally.
func TestCrawl_NoFollowLinkNotFetched(t *testing.T) {
	t.Parallel()
	f := &crawlFixture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/seed.html", f.handle("seed.html", `
<h1>Seed</h1>
<p>Plenty of prose so readability retains this page. We have a link to
<a href="/ok.html">a normal page</a> and also a bait link to
<a href="/trap" rel="nofollow">the honeypot</a>. Only the former should
be crawled; the nofollow link must be dropped before BFS enqueue.</p>`))
	mux.HandleFunc("/ok.html", f.handle("ok.html", `
<h1>OK</h1>
<p>This page is reached by following the non-nofollow link from the
seed. Its body is padded so readability keeps it as an article.</p>`))
	mux.HandleFunc("/trap", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.fetched = append(f.fetched, "/trap")
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body>trapped</body></html>")
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	opts := CrawlOptions{
		Source:       "crawl-nofollow",
		SeedURLs:     []string{f.srv.URL + "/seed.html"},
		MaxDepth:     3,
		MaxPages:     10,
		PolitenessMs: 0,
	}
	pages, _ := runCrawl(t, opts)

	paths := f.pathsFetched()
	require.Len(t, pages, 2, "expected /seed.html + /ok.html only")
	assert.ElementsMatch(t, []string{"/seed.html", "/ok.html"}, paths,
		"nofollow link /trap must never be fetched (got %v)", paths)
	for _, p := range paths {
		assert.NotEqual(t, "/trap", p, "fixture observed a /trap request: %v", paths)
	}
}
