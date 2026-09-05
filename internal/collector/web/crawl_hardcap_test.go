// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hardCapGoodPages is how many healthy interlinked pages the fixture serves.
const hardCapGoodPages = 40

// hardCapDeadLinks is how many connection-hijacking URLs the seed emits AHEAD
// of the good ones.
//
// THE DEAD BLOCK IS WHAT MAKES THIS FIXTURE ABLE TO SEE ITS OWN DEFECT, and it
// is mandated rather than incidental. On a fixture of healthy pages alone, no
// reservation is ever consumed without producing a page — so an implementation
// that reserves at dequeue and NEVER RELEASES behaves identically to a correct
// one, and the gate reads green against the very defect it names. These links
// are emitted first so their reservations are taken first.
const hardCapDeadLinks = 20

// hardCapFixture serves one seed page linking to hardCapDeadLinks dead URLs
// followed by hardCapGoodPages healthy pages, each of which links onward to the
// next so an unbounded crawl reaches all of them.
//
// EACH GOOD PAGE'S BODY IS DISTINCT. Identical bodies are dropped as
// content-hash aliases, which would make the page count measure deduplication
// rather than the budget — the assertion would then hold for entirely the
// wrong reason.
type hardCapFixture struct {
	srv *httptest.Server
	mu  sync.Mutex
	hit map[string]int
}

func newHardCapFixture(t *testing.T) *hardCapFixture {
	t.Helper()
	f := &hardCapFixture{hit: map[string]int{}}
	mux := http.NewServeMux()

	// The dead URLs: the handler hijacks the connection and closes it with no
	// response at all, so fetchAndParse fails and its reservation must come
	// back.
	for i := range hardCapDeadLinks {
		mux.HandleFunc("/dead"+strconv.Itoa(i)+".html", func(w http.ResponseWriter, r *http.Request) {
			f.record(r.URL.Path)
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

	var seedBuf strings.Builder
	for i := range hardCapDeadLinks {
		fmt.Fprintf(&seedBuf, `<p>A dead link to <a href="/dead%d.html">target %d</a> that never answers.</p>`, i, i)
	}
	for i := range hardCapGoodPages {
		fmt.Fprintf(&seedBuf, `<p>A live link to <a href="/good%d.html">page %d</a> of the fixture corpus.</p>`, i, i)
	}
	seed := seedBuf.String()
	mux.HandleFunc("/seed.html", f.page("seed", `<h1>Seed</h1>
<p>The seed page of the hard-cap fixture. It carries substantive prose so that
readability retains it, and it links out to every other page in the corpus.</p>`+seed))

	for i := range hardCapGoodPages {
		body := fmt.Sprintf(`<h1>Good Page %d</h1>
<p>This is fixture page number %d, and this sentence names that number so the
page body is distinct from every sibling and no content-hash alias collapses
the corpus into a single record.</p>
<p>It links onward to <a href="/good%d.html">the next page</a> so an unbounded
crawl reaches the whole corpus by following links rather than by reading the
seed alone.</p>`, i, i, (i+1)%hardCapGoodPages)
		mux.HandleFunc("/good"+strconv.Itoa(i)+".html", f.page("good"+strconv.Itoa(i), body))
	}

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *hardCapFixture) record(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hit[path]++
}

func (f *hardCapFixture) page(name, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><title>%s</title></head>
<body><article>%s</article></body></html>`, name, body)
	}
}

// capturingHandler is a slog.Handler that keeps every record it is given, so a
// test can read the attributes of the truncation warning rather than only its
// presence.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler            { return h }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

// attr returns the first value seen for key across every captured record whose
// message contains want, and whether such a record existed at all.
func (h *capturingHandler) attr(want, key string) (slog.Value, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if !containsSubstring(r.Message, want) {
			continue
		}
		var out slog.Value
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				out, found = a.Value, true
				return false
			}
			return true
		})
		if found {
			return out, true
		}
	}
	return slog.Value{}, false
}

func containsSubstring(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestCrawl_MaxPagesIsAHardCapUnderConcurrency asserts BOTH directions of the
// cap, and the pair is the point.
//
// RESPECTED: the crawl must land no more than MaxPages, where the pre-fix
// behaviour landed maxPages + workers - 1 because the budget was tested
// against pages already recorded while up to W-1 items were still in flight.
//
// REACHED: it must land no FEWER than MaxPages on a site that has the pages,
// which is what fails an implementation that reserves a slot and never returns
// it — the twenty dead links ahead of the good ones consume twenty
// reservations, so a no-release variant lands 1 of 4.
//
// The warning is read as a third leg: a truncation notice reporting a fetched
// count above the cap it names is reporting on the defect rather than on the
// crawl.
func TestCrawl_MaxPagesIsAHardCapUnderConcurrency(t *testing.T) {
	f := newHardCapFixture(t)

	capture := &capturingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const maxPages, workers = 4, 8
	opts := CrawlOptions{
		Source:         "hard-cap",
		SeedURLs:       []string{f.srv.URL + "/seed.html"},
		MaxDepth:       3,
		MaxPages:       maxPages,
		MaxConcurrency: workers,
		PolitenessMs:   0,
	}
	pages, _ := runCrawl(t, opts)

	assert.LessOrEqual(t, len(pages), maxPages,
		"MaxPages=%d with %d workers must be a hard cap; %d page nodes landed", maxPages, workers, len(pages))
	assert.Len(t, pages, maxPages,
		"the cap must be REACHED as well as respected — %d of %d pages landed", len(pages), maxPages)

	fetched, ok := capture.attr("MaxPages budget exhausted", "fetched")
	require.True(t, ok, "a truncated crawl must warn, and the warning must carry a fetched count")
	assert.LessOrEqual(t, int(fetched.Int64()), maxPages,
		"the exhaustion warning reported fetched=%d above the cap it names", fetched.Int64())
}

// TestCrawl_UnboundedCrawlStillReachesEveryPage is the CHARACTERIZATION GUARD
// on the other side of the fix. It passes on the pre-fix tree by design: it
// exists to fail an OVER-CLAMPING repair — one that holds the cap by refusing
// to start more than one worker, or by closing the crawl the moment the budget
// is touched — not to fail the code the ticket is replacing.
func TestCrawl_UnboundedCrawlStillReachesEveryPage(t *testing.T) {
	f := newHardCapFixture(t)

	opts := CrawlOptions{
		Source:         "hard-cap-unbounded",
		SeedURLs:       []string{f.srv.URL + "/seed.html"},
		MaxDepth:       3,
		MaxPages:       0, // unbounded
		MaxConcurrency: 8,
		PolitenessMs:   0,
	}
	pages, _ := runCrawl(t, opts)

	// The seed plus every good page; the dead links contribute nothing.
	assert.Len(t, pages, hardCapGoodPages+1,
		"an unbounded crawl of the same site must still reach every page, got %d", len(pages))
}
