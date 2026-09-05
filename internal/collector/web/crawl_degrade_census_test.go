// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// censusCollect runs a real collect against mux and returns the composition
// the caller would see — the RENDERED response surface, not an internal field.
func censusCollect(t *testing.T, source string, mux *http.ServeMux, tune func(*CrawlOptions)) collector.CollectComposition {
	t.Helper()
	initWebTestSink(t)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Seeds are declared as REPO-RELATIVE PATHS and absolutised here, because
	// the server's address is not known until it starts. tune therefore also
	// sets paths, never absolute URLs.
	opts := CrawlOptions{
		Source:       source,
		SeedURLs:     []string{"/seed.html"},
		PolitenessMs: 0,
	}
	tune(&opts)
	for i, u := range opts.SeedURLs {
		opts.SeedURLs[i] = srv.URL + u
	}

	comp, err := collector.Collect(WithCrawlOptions(context.Background(), opts), "web", source, collector.CollectOptions{Force: true})
	require.NoError(t, err)
	return comp
}

// censusPage wraps a body fragment in a document the extractor will keep.
func censusPage(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><title>t</title></head>
<body><article>%s</article></body></html>`, body)
}

// TestCollect_DegradeCensusReachesTheCollectResponse asserts that work the
// crawl DROPPED is reported to the caller, per class, in the rendered collect
// response.
//
// ALL THREE CRAWLS ARE SUBTESTS OF THIS ONE FUNCTION, and that is a
// constraint rather than a style choice. The stored gate anchors its -run
// selector on this test's name alone: split into sibling top-level tests, the
// selector reaches only the first, the other two never execute, and deleting a
// counter bump they cover leaves the gate green over an unwired class.
func TestCollect_DegradeCensusReachesTheCollectResponse(t *testing.T) {
	// --- CRAWL 1: seven classes over plain HTTP, in one crawl ---------------
	t.Run("plain_http_classes", func(t *testing.T) {
		mux := http.NewServeMux()

		// fetch_failed: twenty URLs that hijack the connection and close it
		// with no response. They are linked AHEAD of everything else so their
		// reservations are taken first.
		const deadLinks = 20
		for i := range deadLinks {
			mux.HandleFunc(fmt.Sprintf("/dead%d.html", i), func(w http.ResponseWriter, _ *http.Request) {
				hj, ok := w.(http.Hijacker)
				if !ok {
					return
				}
				conn, _, err := hj.Hijack()
				if err != nil {
					return
				}
				_ = conn.Close()
			})
		}
		// clean_failed: a 200 with an EMPTY body, which is the only condition
		// the cleaner rejects.
		mux.HandleFunc("/empty.html", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		})
		// content_alias: two URLs serving byte-identical bodies.
		const twin = `<h1>Twin</h1><p>Two distinct URLs serve this identical body, so the
second one to arrive is a content-hash alias of the first and is dropped.</p>`
		mux.HandleFunc("/twin-a.html", func(w http.ResponseWriter, _ *http.Request) { censusPage(w, twin) })
		mux.HandleFunc("/twin-b.html", func(w http.ResponseWriter, _ *http.Request) { censusPage(w, twin) })
		// The deep path (path_segment_cap) and the unfollowed link
		// (link_downgraded_external) are never fetched, so they need no handler
		// beyond a catch-all for anything that does get requested.
		const goodPages = 10
		for i := range goodPages {
			// The body names the page's INDEX rather than echoing the request
			// path: it is what keeps every body distinct so no content-hash
			// alias collapses the corpus, without reflecting request data into
			// the response.
			body := fmt.Sprintf(`<h1>Good Page %d</h1>
<p>This page names its own number %d so its body is distinct from every sibling
and no content-hash alias collapses the corpus into a single record.</p>`, i, i)
			mux.HandleFunc(fmt.Sprintf("/good%d.html", i), func(w http.ResponseWriter, _ *http.Request) {
				censusPage(w, body)
			})
		}

		seed := &strings.Builder{}
		// hidden_pruned: a subtree the browser would never render, on the seed
		// page itself.
		seed.WriteString(`<h1>Census Seed</h1>
<div hidden><p>Unsupported browser boilerplate that no reader ever sees.</p></div>
<p>The seed page of the degrade census, carrying enough prose that the
extractor keeps it rather than discarding it as chrome.</p>`)
		for i := range deadLinks {
			fmt.Fprintf(seed, `<p>A dead link to <a href="/dead%d.html">target %d</a> that never answers.</p>`, i, i)
		}
		seed.WriteString(`<p>An <a href="/empty.html">empty-bodied page</a> the cleaner will reject.</p>
<p>The first <a href="/twin-a.html">twin</a> and the second <a href="/twin-b.html">twin</a>, byte-identical.</p>
<p>A <a href="/a/b/c/d/deep.html">deep path</a> beyond the segment cap.</p>
<p>An <a href="/excluded/page.html">excluded page</a> outside the follow allowlist.</p>`)
		for i := range goodPages {
			fmt.Fprintf(seed, `<p>A good link to <a href="/good%d.html">page %d</a> of the corpus.</p>`, i, i)
		}
		mux.HandleFunc("/seed.html", func(w http.ResponseWriter, _ *http.Request) { censusPage(w, seed.String()) })

		comp := censusCollect(t, "degrade-census", mux, func(o *CrawlOptions) {
			o.MaxDepth = 2
			o.MaxPages = 3
			o.MaxPathSegments = 3
			o.MaxConcurrency = 8
			// The allowlist is what makes /excluded/page.html unfollowed while
			// leaving every other fixture path reachable.
			o.FollowPatterns = []string{`^https?://[^/]+/(seed|dead[0-9]+|empty|twin-[ab]|good[0-9]+|a/b/c/d/deep)\.html$`}
		})

		rendered := comp.Render()
		// Logged, not asserted: a regression in the SHAPE of the census is far
		// easier to read from the rendered line than from a diff of counts.
		t.Logf("census crawl rendered: %s", rendered)
		for _, class := range []string{
			"fetch_failed", "clean_failed", "content_alias", "path_segment_cap",
			"link_downgraded_external", "hidden_pruned", "budget_declined",
		} {
			assert.Contains(t, rendered, class+" ",
				"the rendered census must name the %s class; a wired-but-unreported class is invisible to the caller: %s", class, rendered)
		}

		// PER-CLASS, NOT PER-URL. Twenty distinct failing URLs must produce ONE
		// entry carrying the count — that is the aggregation this measures, and
		// twenty is what makes it a measurement rather than an assertion about
		// a small number.
		assert.Equal(t, deadLinks, comp.Degraded["fetch_failed"],
			"twenty failing URLs must aggregate into one fetch_failed entry of 20, got %d", comp.Degraded["fetch_failed"])
		assert.LessOrEqual(t, len(comp.Degraded), 10,
			"the census is keyed by a fixed class vocabulary, so it cannot grow with the corpus; got %d entries: %v",
			len(comp.Degraded), comp.Degraded)
	})

	// --- CRAWL 2: the page gate, in BOTH directions, in one crawl -----------
	//
	// THIS SUBTEST LIVES INSIDE THIS FUNCTION rather than beside it, for the
	// same reason the function's own doc gives: the stored gate anchors its
	// -run selector on this test's name, so a sibling top-level test would
	// never execute and the gate would stay green over an unwired class.
	//
	// ONE CRAWL CARRIES BOTH DIRECTIONS, because a gate proven only on the
	// resources it must SKIP is satisfied by a gate that skips everything. Two
	// resources here must be declined and two must survive, and the page count
	// is what observes the surviving half.
	t.Run("not_a_page_both_directions", func(t *testing.T) {
		mux := http.NewServeMux()

		// The two ZIP bodies pad DIFFERENTLY on purpose: byte-identical bodies
		// would make one a content-hash alias of the other in the red
		// direction, muddying the signature the implementer reads.
		zipEpub := append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0x01}, 600)...)
		zipLying := append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0x02}, 600)...)

		// An HONEST BINARY: the origin declares a generic type and the bytes
		// are a ZIP. Declined on the bytes.
		mux.HandleFunc("/asset.epub", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(zipEpub)
		})
		// A LYING ORIGIN: declared text/html over ZIP bytes. Declined on the
		// disagreement — the direction a header-only gate loses.
		mux.HandleFunc("/lying.html", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(zipLying)
		})
		// A GENUINE PAGE UNDER A GENERIC DECLARATION. Must survive — the
		// direction a header-only gate also loses.
		mux.HandleFunc("/mystery.html", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, `<!doctype html><html><head><title>Mystery</title></head>
<body><article><h1>Declared A Generic Type, Served A Page</h1>
<p>This document is a genuine article whose origin declined to classify it, so
the bytes are what decide, and the bytes are unmistakably a page.</p>
<p>A second paragraph gives the extractor more than a trivial amount of text so
the cleaner keeps the article rather than discarding it as chrome.</p>
</article></body></html>`)
		})
		// A GENUINE PAGE WHOSE BYTES SNIFF AS text/plain, because its opening
		// tag falls outside the stdlib signature table. Must survive on its
		// DECLARATION — the direction a sniff-must-say-HTML gate loses.
		mux.HandleFunc("/fragment.html", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<article><h1>A Page That Sniffs As Plain Text</h1>
<p>This document opens with an article tag rather than a doctype, so the stdlib
content sniffer reads it as plain text even though it is a real page.</p>
<p>A second paragraph gives the extractor more than a trivial amount of text so
the cleaner keeps the article rather than discarding it as chrome.</p>
</article>`)
		})
		mux.HandleFunc("/seed.html", func(w http.ResponseWriter, _ *http.Request) {
			censusPage(w, `<h1>Page Gate Seed</h1>
<p>The seed page of the page-gate crawl, carrying enough prose that the
extractor keeps it rather than discarding it as chrome.</p>
<p>An honest <a href="/asset.epub">binary asset</a> declared as a generic type.</p>
<p>A <a href="/lying.html">lying origin</a> that declares HTML and serves a ZIP.</p>
<p>A <a href="/mystery.html">mystery page</a> declared as a generic type.</p>
<p>A <a href="/fragment.html">fragmentary page</a> whose bytes sniff as text.</p>`)
		})

		comp := censusCollect(t, "degrade-page-gate", mux, func(o *CrawlOptions) {
			o.MaxDepth = 2
			o.MaxPages = 10
			o.MaxConcurrency = 1
			o.FollowPatterns = []string{`^https?://[^/]+/(seed\.html|lying\.html|mystery\.html|fragment\.html|asset\.epub)$`}
		})

		rendered := comp.Render()
		t.Logf("page-gate crawl rendered: %s", rendered)

		// RAW LITERALS, not the new constant, so this subtest compiles against
		// the unfixed tree and can be run red first.
		assert.Equal(t, 2, comp.Degraded["not_a_page"],
			"the honest binary and the lying origin must each count once under not_a_page: %s", rendered)
		assert.Contains(t, rendered, "not_a_page ",
			"a counted-but-unreported class is invisible to the caller: %s", rendered)
		assert.Equal(t, 3, comp.NodesByType["page"],
			"the seed, the generically-declared page and the fragmentary page must all survive the gate: %s", rendered)
	})

	// --- CRAWL 3: the per-host cap, which needs its own crawl ---------------
	//
	// MaxPagesPerHost=1 would bound every other class in the crawl above, so
	// host_cap is driven here instead: three same-host seeds, one lands, the
	// rest are refused by the cap.
	t.Run("host_cap_needs_its_own_crawl", func(t *testing.T) {
		mux := http.NewServeMux()
		for _, p := range []string{"/seed.html", "/second.html", "/third.html"} {
			body := fmt.Sprintf(`<h1>Host Cap %s</h1>
<p>This page names its own path %s so the three seeds are not content-hash
aliases of one another, which would drop them for the wrong reason.</p>`, p, p)
			mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
				censusPage(w, body)
			})
		}
		comp := censusCollect(t, "degrade-host-cap", mux, func(o *CrawlOptions) {
			o.SeedURLs = []string{"/seed.html", "/second.html", "/third.html"}
			o.MaxPagesPerHost = 1
			o.MaxConcurrency = 1
		})

		t.Logf("host-cap crawl rendered: %s", comp.Render())
		assert.Contains(t, comp.Render(), "host_cap ",
			"the per-host cap must report itself in the rendered census: %s", comp.Render())
		assert.Equal(t, 1, comp.NodesByType["page"], "exactly one page may land under a per-host cap of 1")
	})

	// --- CONTROL: a clean harvest reports NOTHING -------------------------
	//
	// THE CONTROL PAGE CARRIES NO ANCHOR OF ANY KIND, and that is part of the
	// fixture rather than an accident. Two classes fire truthfully on an
	// otherwise perfectly healthy page that has even one link: an internal
	// link the crawl never visits is downgraded, and under a page budget the
	// queued link is declined. A control that merely avoids FAILING links reds
	// against correct work.
	t.Run("clean_control_reports_no_degradation", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/seed.html", func(w http.ResponseWriter, _ *http.Request) {
			censusPage(w, `<h1>A Clean Page</h1>
<p>This page carries substantive prose and no anchor at all, so nothing is
fetched that could fail, nothing is queued that could be declined, and no link
exists that could be downgraded.</p>
<p>A second paragraph gives the extractor more than a trivial amount of text.</p>`)
		})
		comp := censusCollect(t, "degrade-clean", mux, func(o *CrawlOptions) {
			o.MaxPages = 1
		})

		assert.Empty(t, comp.Degraded, "a clean harvest must report no degraded classes, got %v", comp.Degraded)
		assert.NotContains(t, comp.Render(), "degraded",
			"a clean harvest's response must be byte-identical to one with no census at all: %s", comp.Render())
	})
}
