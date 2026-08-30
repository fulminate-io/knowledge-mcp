// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// This file is the standing guard on a REMOVAL. The crawler used to fetch and
// honor /robots.txt; it no longer does, because the collector acts on behalf of
// a user asking for a specific document rather than as an automated scraper.
//
// A REMOVAL NEEDS A TEST THAT WOULD HAVE FAILED BEFORE IT, or deleting the old
// coverage and reporting green is indistinguishable from having removed nothing.
// Every assertion below is written against the gate's ABSENCE and was observed
// failing against the tree that still had it: the disallowed URL was skipped and
// /robots.txt was requested.
//
// It is deliberately BEHAVIORAL rather than a source census. A grep for the
// symbol names would pass the moment the files were deleted even if some other
// path still refused a URL; only driving the crawl shows what the crawler does.

// noRobotsFixture is a site that serves a restrictive /robots.txt and records
// every path requested. The robots.txt is served rather than omitted on purpose:
// the test's whole claim is that a PRESENT and RESTRICTIVE robots.txt changes
// nothing, which a fixture that 404s it could not distinguish from a crawler
// that reads robots.txt and finds no rules.
type noRobotsFixture struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
}

func newNoRobotsFixture(t *testing.T, pages map[string][]string) *noRobotsFixture {
	t.Helper()
	f := &noRobotsFixture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\nCrawl-delay: 90\n"))
	})
	for path, links := range pages {
		body := noRobotsPageHTML(path, links)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			f.record(r.URL.Path)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(body))
		})
	}
	// Unregistered paths are still recorded, so a test can tell "never attempted"
	// from "attempted and 404ed".
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		http.NotFound(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *noRobotsFixture) record(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
}

func (f *noRobotsFixture) sawPath(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.paths, path)
}

func (f *noRobotsFixture) url(path string) string { return f.srv.URL + path }

// noRobotsPageHTML renders a page with enough prose to survive the readability
// pass — a thin body is discarded as boilerplate and would take its links with
// it — plus one anchor per link.
func noRobotsPageHTML(title string, links []string) string {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html>\n<head><title>Page " + title + "</title></head>\n<body>\n<article>\n")
	b.WriteString("<h1>Page " + title + "</h1>\n")
	b.WriteString("<p>This is the intro paragraph for a crawl fixture page. It carries enough prose " +
		"that the readability pass keeps the article instead of discarding it as boilerplate.</p>\n")
	b.WriteString("<p>A second paragraph gives the extractor more than a trivial amount of text to " +
		"work with, because readability bails on very short bodies and would drop every link with them.</p>\n")
	for i, href := range links {
		b.WriteString("<p><a href=\"" + href + "\">Link " + strconv.Itoa(i) +
			" with enough anchor text to survive</a> plus additional surrounding context so this " +
			"paragraph is retained by the extractor along with the anchor it holds.</p>\n")
	}
	b.WriteString("</article>\n</body>\n</html>\n")
	return b.String()
}

// TestCrawl_RobotsTxtIsNeitherFetchedNorHonored drives the real collector against
// a site whose robots.txt forbids everything.
//
// THE KNOWN-POSITIVE IS THE PAGE NODE, not merely the request log. Asserting only
// that the disallowed path was requested would also pass for a crawl that fetched
// it and then discarded the result, so the emitted batch must contain the page.
func TestCrawl_RobotsTxtIsNeitherFetchedNorHonored(t *testing.T) {
	fixture := newNoRobotsFixture(t, map[string][]string{
		"/seed.html":         {"/blocked/page.html"},
		"/blocked/page.html": nil,
	})

	sink := initWebTestSink(t)
	opts := CrawlOptions{
		Source:       "no-robots-test",
		SeedURLs:     []string{fixture.url("/seed.html")},
		PolitenessMs: 0,
	}
	ctx := WithCrawlOptions(context.Background(), opts)
	if _, err := collector.Collect(ctx, "web", opts.Source, collector.CollectOptions{Force: true}); err != nil {
		t.Fatalf("collect: %v", err)
	}
	batch := sink.last()
	if batch == nil {
		t.Fatal("collector handed no CollectResult to the sink")
	}

	// The crawl must not have asked for robots.txt at all. A crawler that fetched
	// it and ignored the answer would still be spending a request per host on a
	// file it has no use for.
	if fixture.sawPath("/robots.txt") {
		t.Error("/robots.txt was requested; the crawler must not fetch it at all")
	}

	// The gate is gone: a path robots.txt disallows is followed like any other.
	if !fixture.sawPath("/blocked/page.html") {
		t.Error("a robots-disallowed URL was skipped; the robots gate is still in the discovery path")
	}
	if !pageNodeExistsForURL(batch.Nodes, fixture.url("/blocked/page.html")) {
		t.Error("no page node was emitted for the disallowed URL; it was fetched but not collected")
	}

	// KNOWN POSITIVE for both assertions above: the seed itself was crawled, so
	// "nothing was blocked" is not "nothing ran".
	if !fixture.sawPath("/seed.html") {
		t.Fatal("the seed was never fetched; this test proves nothing about the gate")
	}

	// No disclosure node survives, because there is no longer an outcome to
	// disclose. Guarded by the page-node assertion above, so this zero cannot be
	// satisfied by an empty batch.
	for _, n := range batch.Nodes {
		if n.Type == "robots_report" {
			t.Errorf("a robots_report node was emitted (%s); the disclosure must be gone with the gate", n.Id)
		}
	}
}

// pageNodeExistsForURL reports whether a page node was emitted for url.
func pageNodeExistsForURL(nodes []*knowledgev1.Node, url string) bool {
	for _, n := range nodes {
		if n.Type == "page" && (n.Metadata["url"] == url || n.Metadata["final_url"] == url) {
			return true
		}
	}
	return false
}
