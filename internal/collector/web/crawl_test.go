// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// newCrawlFixture spins up an httptest server that serves three
// interlinked pages:
//
//	/a.html  →  /b.html, /c.html
//	/b.html  →  /c.html
//	/c.html  →  (no internal links)
//
// Every page body is padded with filler prose so go-readability keeps
// the content (short fragments are rejected as chrome). The fixture
// tracks which URLs have been fetched so depth / budget / follow-pattern
// tests can assert on observed request paths directly.
type crawlFixture struct {
	srv     *httptest.Server
	mu      sync.Mutex
	fetched []string
}

// pathsFetched returns a copy of the observed request-path slice in
// fetch order.
func (f *crawlFixture) pathsFetched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.fetched))
	copy(out, f.fetched)
	return out
}

// newCrawlFixture constructs the shared 3-page test server.
func newCrawlFixture(t *testing.T) *crawlFixture {
	t.Helper()
	f := &crawlFixture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/a.html", f.handle("a.html", `
<h1>Page A — The Root Seed</h1>
<p>This is the canonical entry point of the crawl fixture. It contains
several sentences of substantive prose so that readability will not
reject the page as chrome or boilerplate.</p>
<p>Below we link out to <a href="/b.html">page B</a> and also to
<a href="/c.html">page C</a>; both are internal same-host links and should
therefore be enqueued by the BFS when the crawl depth allows it.</p>`))
	mux.HandleFunc("/b.html", f.handle("b.html", `
<h1>Page B — One Hop From The Seed</h1>
<p>This is the second page in the crawl fixture. It carries a single
internal link to <a href="/c.html">page C</a> which the BFS should visit
if the depth budget permits a second hop.</p>
<p>Additional prose keeps readability happy — the extractor bails on
trivially short bodies.</p>`))
	mux.HandleFunc("/c.html", f.handle("c.html", `
<h1>Page C — The Leaf</h1>
<p>This page has no outbound internal links. It is the terminus of any
crawl from the fixture seed and serves as a convenient target for the
link-resolution assertions.</p>
<p>A second paragraph supplies enough text for readability to retain
the body as the extracted article.</p>`))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// handle returns an http.HandlerFunc that records the path and renders
// a full HTML document wrapping body into a minimal <html>/<body>
// scaffold so go-readability has a real document to work with.
func (f *crawlFixture) handle(name, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.fetched = append(f.fetched, r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><title>%s</title></head>
<body><article>%s</article></body></html>`, name, body)
	}
}

// runCrawl is a thin wrapper that builds a fetchClient with zero
// politeness delay and invokes crawl. Used by the per-case tests below.
func runCrawl(t *testing.T, opts CrawlOptions) ([]*pageRecord, map[string]string) {
	t.Helper()
	fc := newFetchClient("", 0)
	pages, urlToID, _, _, err := crawl(context.Background(), fc, opts)
	require.NoError(t, err)
	return pages, urlToID
}

func TestCrawl_BFS_AllPagesWithinDepthBudget(t *testing.T) {
	t.Parallel()
	f := newCrawlFixture(t)

	opts := CrawlOptions{
		Source:       "crawl-depth2",
		SeedURLs:     []string{f.srv.URL + "/a.html"},
		MaxDepth:     2,
		MaxPages:     10,
		PolitenessMs: 0,
	}
	pages, urlToID := runCrawl(t, opts)

	require.Len(t, pages, 3, "MaxDepth=2 should reach all three pages (a→b→c)")
	// urlToID is keyed on record.URL (pre-redirect) for each fetched page;
	// every page we visited must be represented.
	for _, p := range []string{"/a.html", "/b.html", "/c.html"} {
		assert.Contains(t, urlToID, f.srv.URL+p, "urlToID missing entry for %s", p)
	}
	assert.ElementsMatch(t,
		[]string{"/a.html", "/b.html", "/c.html"},
		f.pathsFetched(),
		"exactly three request paths should have been observed")
}

func TestCrawl_MaxDepth1_OnlyCrawlsSeed(t *testing.T) {
	t.Parallel()
	f := newCrawlFixture(t)

	opts := CrawlOptions{
		Source:       "crawl-depth1",
		SeedURLs:     []string{f.srv.URL + "/a.html"},
		MaxDepth:     1,
		MaxPages:     10,
		PolitenessMs: 0,
	}
	pages, _ := runCrawl(t, opts)

	require.Len(t, pages, 1, "MaxDepth=1 should fetch only the seed")
	assert.Equal(t, []string{"/a.html"}, f.pathsFetched())
}

func TestCrawl_MaxPages_TruncatesBeforeExhaustingFrontier(t *testing.T) {
	t.Parallel()
	f := newCrawlFixture(t)

	opts := CrawlOptions{
		Source:       "crawl-cap2",
		SeedURLs:     []string{f.srv.URL + "/a.html"},
		MaxDepth:     5,
		MaxPages:     2,
		PolitenessMs: 0,
	}
	pages, _ := runCrawl(t, opts)

	require.Len(t, pages, 2, "MaxPages=2 must cap the crawl at two fetched pages")
	// The BFS is deterministic: /a.html first, then whichever neighbor was
	// enqueued first. Our handler enqueues /b.html ahead of /c.html so the
	// second visit must be /b.html.
	assert.Equal(t, []string{"/a.html", "/b.html"}, f.pathsFetched())
}

func TestCrawl_FollowPatterns_FilterDiscoveredLinks(t *testing.T) {
	t.Parallel()
	f := newCrawlFixture(t)

	opts := CrawlOptions{
		Source:         "crawl-patterns",
		SeedURLs:       []string{f.srv.URL + "/a.html"},
		MaxDepth:       5,
		MaxPages:       10,
		PolitenessMs:   0,
		FollowPatterns: []string{`a\.html$`, `b\.html$`},
	}
	pages, urlToID := runCrawl(t, opts)

	// /c.html is filtered out by the allowlist; the crawl should stop at
	// /a.html + /b.html.
	require.Len(t, pages, 2, "FollowPatterns should drop /c.html")
	assert.Contains(t, urlToID, f.srv.URL+"/a.html")
	assert.Contains(t, urlToID, f.srv.URL+"/b.html")
	assert.NotContains(t, urlToID, f.srv.URL+"/c.html")
	assert.ElementsMatch(t, []string{"/a.html", "/b.html"}, f.pathsFetched())
}

// TestCrawl_InternalLinks_RewrittenToPageIDs exercises the
// resolveInternalLinks path by driving a full Collect() through the
// collector.Register pipeline and inspecting the edges in the captured
// CollectResult batch. The assertion is that every rel="internal"
// REFERENCES edge emitted from the /a.html page points at a real
// page-node ID (not at the "web:url:<absolute>" placeholder the emitter
// initially wrote). emitLinks writes these edges with string FromID/ToID
// (FromIdx/ToIdx = -1), so the captured batch.Edges carry the same
// endpoint IDs the persisted graph would — no store readback needed.
func TestCrawl_InternalLinks_RewrittenToPageIDs(t *testing.T) {
	sink := initWebTestSink(t)
	f := newCrawlFixture(t)

	opts := CrawlOptions{
		Source:       "crawl-resolve",
		SeedURLs:     []string{f.srv.URL + "/a.html"},
		MaxDepth:     2,
		MaxPages:     10,
		PolitenessMs: 0,
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	err := collector.Collect(ctx, "web", opts.Source, collector.CollectOptions{Force: true})
	require.NoError(t, err)

	batch := sink.last()
	require.NotNil(t, batch, "collector must hand a CollectResult to the sink")

	// Compute the expected page-node IDs for each fetched URL. stableID
	// with kind="page" is the emit-time derivation; urlToID uses the
	// same function so the same pair of URLs must yield the same IDs.
	wantByURL := map[string]string{
		f.srv.URL + "/a.html": stableID(f.srv.URL+"/a.html", "page", "", 0),
		f.srv.URL + "/b.html": stableID(f.srv.URL+"/b.html", "page", "", 0),
		f.srv.URL + "/c.html": stableID(f.srv.URL+"/c.html", "page", "", 0),
	}

	// Walk every REFERENCES edge out of /a.html's page node in the
	// captured batch and check that the rewrite happened: rel=internal
	// edges must now carry a real page-node ID as their ToID, matching
	// the wantByURL map.
	seenInternalTargets := map[string]struct{}{}
	aPageID := wantByURL[f.srv.URL+"/a.html"]
	for _, e := range batch.Edges {
		if e.Type != kgtypes.EdgeReferences || e.FromID != aPageID {
			continue
		}
		md := parseEdgeMeta(e.Evidence)
		if md["rel"] != "internal" {
			continue
		}
		seenInternalTargets[e.ToID] = struct{}{}
	}

	wantTargets := []string{
		wantByURL[f.srv.URL+"/b.html"],
		wantByURL[f.srv.URL+"/c.html"],
	}
	for _, want := range wantTargets {
		_, ok := seenInternalTargets[want]
		assert.True(t, ok, "internal-link edge from /a.html missing rewritten ToID %q (got targets %v)",
			want, seenInternalTargets)
	}
	// Placeholder targets must never survive the rewrite — this is the
	// Phase 6 invariant we care about.
	for tgt := range seenInternalTargets {
		assert.NotContainsf(t, tgt, "web:url:",
			"internal-link target still references placeholder: %q", tgt)
	}
}
