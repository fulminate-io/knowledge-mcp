// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// concurrencyFixture serves a small linked corpus and tracks the HIGH-WATER
// MARK of simultaneously in-flight requests, which is the only way to observe
// a worker count from outside the collector.
//
// Each handler holds the connection briefly so overlap is observable at all: a
// handler that returns instantly finishes before the next request begins, and
// the high-water mark reads 1 whatever the worker count — a timing artifact
// that once produced exactly the wrong conclusion about this seam.
type concurrencyFixture struct {
	srv   *httptest.Server
	meter *overlapMeter
}

// overlapMeter is the shared in-flight counter. It is SHARED ACROSS FIXTURES on
// purpose: the question is whether the crawl had several requests in flight AT
// THE SAME INSTANT, and per-host peaks summed after the fact would report 2 for
// two hosts that were each served alone at different moments.
type overlapMeter struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	served   int
}

func (m *overlapMeter) enter() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlight++
	m.served++
	if m.inFlight > m.peak {
		m.peak = m.inFlight
	}
}

func (m *overlapMeter) leave() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlight--
}

func (m *overlapMeter) stats() (peak, served int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peak, m.served
}

// newConcurrencyFixture starts one server. Callers seed as many of these as
// they need hosts: httptest hands out a distinct 127.0.0.1 port per server, and
// the crawler's politeness mutex is keyed per host:port, so separate servers
// are separate hosts as far as the crawl is concerned.
func newConcurrencyFixture(t *testing.T, meter *overlapMeter, links int) *concurrencyFixture {
	t.Helper()
	f := &concurrencyFixture{meter: meter}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		meter.enter()
		defer meter.leave()
		time.Sleep(40 * time.Millisecond)

		body := &strings.Builder{}
		fmt.Fprintf(body, `<h1>Page %s</h1>
<p>This page of the concurrency fixture is served from path %s, and naming the
path here keeps every body distinct so no content-hash alias collapses the
corpus into one record.</p>`, r.URL.Path, r.URL.Path)
		for i := range links {
			fmt.Fprintf(body, `<p>A link onward to <a href="/p%d.html">page %d</a> with
enough surrounding prose that the paragraph holding it survives extraction.</p>`, i, i)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><title>c</title></head>
<body><article>%s</article></body></html>`, body.String())
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// TestCollect_MaxConcurrencyIsCallerControlled has five legs. Legs 2 and 3 are
// a PROPERTY PAIR: leg 3 alone would be satisfied by an implementation that
// ignored the parameter entirely and always ran the default 8 workers, and leg 2
// alone would be satisfied by one that hardcoded a single worker.
//
// LEGS 4 AND 5 ARE THE CEILING, and they are a property pair for the same
// reason. The knob is caller-controlled but not without limit: it counts worker
// goroutines issuing simultaneous requests at hosts that never consented to the
// number, so the validator refuses a value above the cap rather than clamping
// it. Leg 4 fails an off-by-one ceiling that every other assertion in this file
// survives; leg 5 fails a clamp, a refusal message that omits the cap, and a
// validator that never refuses at all.
func TestCollect_MaxConcurrencyIsCallerControlled(t *testing.T) {
	// LEG 1 — the validator's field-named refusal. It is the lever this file
	// already uses, and it proves the value was READ: a dropped assignment
	// leaves the zero value, which the validator accepts, so nothing at all is
	// refused and the negative payload sails through.
	t.Run("negative_value_is_refused_by_name", func(t *testing.T) {
		payload := `{"type":"web","id":"web/concurrency","seed_urls":["https://example.com/"],"max_concurrency":-1}`
		_, bail, res := withWebCrawlOptions(context.Background(), collectArgsFrom(t, payload))
		if !bail {
			t.Fatal("a negative max_concurrency must be refused; the tool layer never read the field")
		}
		if body := textBodyTools(res); !strings.Contains(body, "MaxConcurrency") {
			t.Errorf("the refusal must name MaxConcurrency so it cannot pass on another field's rejection, got: %s", body)
		}
	})

	// LEG 4 — AT the cap is a legal value. The refusal is strictly greater
	// than, so this is the leg that reds an off-by-one ceiling written as >=.
	t.Run("at_the_cap_is_admitted", func(t *testing.T) {
		payload := `{"type":"web","id":"web/concurrency","seed_urls":["https://example.com/"],"max_concurrency":32}`
		_, bail, res := withWebCrawlOptions(context.Background(), collectArgsFrom(t, payload))
		if bail {
			t.Fatalf("max_concurrency at the cap is a legal value and must be admitted, got: %s", textBodyTools(res))
		}
	})

	// LEG 5 — above the cap is REFUSED, never clamped, and the message names
	// BOTH numbers. The requested value proves the refusal is about this field
	// rather than about another field's rejection; the cap is what lets the
	// caller pick a legal value without reading the source.
	t.Run("above_the_cap_is_refused_naming_value_and_cap", func(t *testing.T) {
		payload := `{"type":"web","id":"web/concurrency","seed_urls":["https://example.com/"],"max_concurrency":33}`
		_, bail, res := withWebCrawlOptions(context.Background(), collectArgsFrom(t, payload))
		if !bail {
			t.Fatal("a max_concurrency above the cap must be REFUSED, never clamped; the tool layer accepted it")
		}
		body := textBodyTools(res)
		for _, want := range []string{"MaxConcurrency", "33", "32"} {
			if !strings.Contains(body, want) {
				t.Errorf("the refusal must name %q — both the requested value and the cap — got: %s", want, body)
			}
		}
	})

	// LEG 2 — one worker is strictly serial.
	t.Run("one_worker_never_overlaps", func(t *testing.T) {
		meter := &overlapMeter{}
		f := newConcurrencyFixture(t, meter, 6)
		runWebCollectFromPayload(t, fmt.Sprintf(
			`{"type":"web","id":"web/conc-serial","seed_urls":["%s/seed.html"],"max_depth":2,"max_pages":6,"max_concurrency":1,"politeness_ms":0}`,
			f.srv.URL))

		peak, served := meter.stats()
		// KNOWN POSITIVE, in the same run: "never more than one in flight" is
		// trivially true of a crawl that issued no request at all.
		if served < 2 {
			t.Fatalf("the crawl issued only %d request(s); the serial claim below would be vacuous", served)
		}
		if peak != 1 {
			t.Errorf("max_concurrency=1 must produce a strictly serial crawl, observed %d in flight", peak)
		}
	})

	// LEG 3 — several workers across SEVERAL HOSTS actually overlap.
	//
	// THREE HOSTS, NOT ONE, and the reason is measured rather than assumed:
	// per-host politeness spaces request STARTS to a host, so a single-host
	// crawl's overlap depends on request latency against that spacing. Seeding
	// distinct hosts makes the parallelism a function of the worker count
	// alone, which is the parameter under test.
	t.Run("several_workers_across_hosts_overlap", func(t *testing.T) {
		meter := &overlapMeter{}
		a := newConcurrencyFixture(t, meter, 4)
		b := newConcurrencyFixture(t, meter, 4)
		c := newConcurrencyFixture(t, meter, 4)

		runWebCollectFromPayload(t, fmt.Sprintf(
			`{"type":"web","id":"web/conc-parallel","seed_urls":["%s/seed.html","%s/seed.html","%s/seed.html"],"max_depth":1,"max_pages":12,"max_concurrency":6,"politeness_ms":0}`,
			a.srv.URL, b.srv.URL, c.srv.URL))

		peak, served := meter.stats()
		if served < 3 {
			t.Fatalf("the crawl issued only %d request(s) across three hosts; the overlap claim would be vacuous", served)
		}
		// The meter is shared by all three servers, so this peak is genuine
		// SIMULTANEITY rather than three per-host maxima that happened at
		// different moments and were added together afterwards.
		if peak < 2 {
			t.Errorf("max_concurrency=6 must overlap fetches, observed only %d in flight", peak)
		}
	})
}
