// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/web"
)

// These tests gate the seam between the collect tool's arguments and the web
// collector's CrawlOptions — withWebCrawlOptions (collect.go). The unmarshal
// tests next door stop at collectArgs and say so; this file is the other side.
//
// WHY NEITHER TEST READS THE OPTIONS BACK OUT OF THE CONTEXT. That would be the
// obvious shape, and it cannot be written from this package: crawlOptionsFrom is
// unexported and the context key is an unexported struct type, deliberately
// (options.go states the unexported-reader choice as a precedent it follows).
// So both tests below observe the assignment through something that CONSUMES the
// options instead — the validator, which names the field it rejected, and the
// collector itself, which turns the options into fetch behavior. Observing the
// effect is a stronger claim than observing the field anyway: a field that is
// populated but never consumed would satisfy a read-back and fail these.

// collectArgsFrom unmarshals a collect-tool JSON payload the way the tool
// dispatch does, so every test below starts from the wire shape rather than from
// a hand-built struct that could not catch a schema-key mismatch.
func collectArgsFrom(t *testing.T, payload string) collectArgs {
	t.Helper()
	var a collectArgs
	if err := json.Unmarshal([]byte(payload), &a); err != nil {
		t.Fatalf("unmarshal collect payload: %v", err)
	}
	return a
}

// TestWithWebCrawlOptions_CarriesEveryValidatedFieldIntoCrawlOptions covers the
// assignment for every field ValidateCrawlOptions guards, in ONE test rather than
// one per field: the struct literal is the unit that rots, and a per-field test
// would still leave a newly added field silently unassigned.
//
// THE LEVER IS THE VALIDATOR'S OWN FIELD-NAMED REJECTION. Each case sends a value
// the validator refuses and asserts the refusal names that field. If the literal
// dropped the assignment the field would hold its zero value, the validator would
// be satisfied, and no error would come back at all — so each case is red exactly
// when its assignment is missing, which is the property under test.
//
// The all-valid control is not decoration: without it, a withWebCrawlOptions that
// refused unconditionally would satisfy every case above it.
func TestWithWebCrawlOptions_CarriesEveryValidatedFieldIntoCrawlOptions(t *testing.T) {
	const validPayload = `{
		"type": "web",
		"id": "web/example",
		"seed_urls": ["https://example.com/"],
		"max_depth": 3,
		"max_pages": 40,
		"max_path_segments": 5,
		"max_pages_per_host": 12,
		"max_concurrency": 4,
		"politeness_ms": 25,
		"max_download_bytes": 1048576
	}`

	t.Run("control_all_valid_is_accepted", func(t *testing.T) {
		_, bail, res := withWebCrawlOptions(context.Background(), collectArgsFrom(t, validPayload))
		if bail {
			t.Fatalf("a fully valid payload was refused: %s", textBodyTools(res))
		}
	})

	// Each payload violates exactly one field. wantIn is a substring of the
	// validator message for that field, so a case cannot pass on a DIFFERENT
	// field's rejection — which is what would happen if two assignments were
	// swapped with each other.
	cases := []struct {
		name    string
		payload string
		wantIn  string
	}{
		{
			name:    "source_from_id",
			payload: `{"type":"web","id":"","seed_urls":["https://example.com/"]}`,
			wantIn:  "Source is required",
		},
		{
			name:    "seed_urls",
			payload: `{"type":"web","id":"web/example"}`,
			wantIn:  "SeedURLs is required",
		},
		{
			name:    "max_depth",
			payload: `{"type":"web","id":"web/example","seed_urls":["https://example.com/"],"max_depth":-1}`,
			wantIn:  "MaxDepth must be >= 0",
		},
		{
			name:    "max_pages",
			payload: `{"type":"web","id":"web/example","seed_urls":["https://example.com/"],"max_pages":-1}`,
			wantIn:  "MaxPages must be >= 0",
		},
		{
			name:    "politeness_ms",
			payload: `{"type":"web","id":"web/example","seed_urls":["https://example.com/"],"politeness_ms":-1}`,
			wantIn:  "PolitenessMs must be >= 0",
		},
		{
			name:    "max_path_segments",
			payload: `{"type":"web","id":"web/example","seed_urls":["https://example.com/"],"max_path_segments":-1}`,
			wantIn:  "MaxPathSegments must be >= 0",
		},
		{
			name:    "max_pages_per_host",
			payload: `{"type":"web","id":"web/example","seed_urls":["https://example.com/"],"max_pages_per_host":-1}`,
			wantIn:  "MaxPagesPerHost must be >= 0",
		},
		{
			name:    "max_concurrency",
			payload: `{"type":"web","id":"web/example","seed_urls":["https://example.com/"],"max_concurrency":-1}`,
			wantIn:  "MaxConcurrency must be >= 0",
		},
		{
			name:    "max_download_bytes",
			payload: `{"type":"web","id":"web/example","seed_urls":["https://example.com/"],"max_download_bytes":-2}`,
			wantIn:  "MaxDownloadBytes must be >= -1",
		},
		{
			// SeedURLs is a SLICE: a per-element rejection proves the whole
			// slice was carried across, not merely a non-empty one.
			name:    "seed_urls_element",
			payload: `{"type":"web","id":"web/example","seed_urls":["https://example.com/",""]}`,
			wantIn:  "SeedURLs[1] is empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, bail, res := withWebCrawlOptions(context.Background(), collectArgsFrom(t, tc.payload))
			if !bail {
				t.Fatalf("the %s violation was accepted; the field never reached CrawlOptions", tc.name)
			}
			if body := textBodyTools(res); !strings.Contains(body, tc.wantIn) {
				t.Errorf("refusal %q does not contain %q — a different field was rejected", body, tc.wantIn)
			}
		})
	}
}

// crawlOptsFixture is a one-host site for the end-to-end assertion below. It
// records the User-Agent of every request alongside the path, because UserAgent
// is invisible to the validator and can only be observed behaviorally — what
// header actually arrived.
type crawlOptsFixture struct {
	srv    *httptest.Server
	mu     sync.Mutex
	paths  []string
	agents []string
}

func newCrawlOptsFixture(t *testing.T) *crawlOptsFixture {
	t.Helper()
	f := &crawlOptsFixture{}
	page := `<!doctype html><html><head><title>p</title></head><body><article>` +
		`<h1>Seed</h1>` +
		`<p>This seed page carries enough prose that the readability pass keeps the ` +
		`article instead of discarding it as boilerplate and taking its links along.</p>` +
		`<p>A second paragraph gives the extractor more than a trivial amount of text, ` +
		`because readability bails on very short bodies.</p>` +
		`<p><a href="/second/page.html">A link with enough anchor text to survive</a> plus ` +
		`surrounding context so the paragraph holding it is retained.</p>` +
		`</article></body></html>`
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *crawlOptsFixture) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	f.agents = append(f.agents, r.Header.Get("User-Agent"))
}

func (f *crawlOptsFixture) sawPath(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.paths, path)
}

func (f *crawlOptsFixture) sawAgent(agent string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.agents, agent)
}

// runWebCollectFromPayload drives the WHOLE seam: a collect-tool JSON payload
// becomes collectArgs, withWebCrawlOptions turns it into the context the web
// collector reads, and WebCollector.Collect runs the real crawl against fixture.
//
// Going through WebCollector.Collect rather than a read-back is what makes this
// a test of the assignment's EFFECT. Collect calls crawlOptionsFrom itself, so a
// field the tool layer dropped is a field the crawl never sees.
func runWebCollectFromPayload(t *testing.T, payload string) {
	t.Helper()
	ctx, bail, res := withWebCrawlOptions(context.Background(), collectArgsFrom(t, payload))
	if bail {
		t.Fatalf("withWebCrawlOptions refused a valid payload: %s", textBodyTools(res))
	}
	if _, err := (&web.WebCollector{}).Collect(ctx, "", collector.CollectOptions{}); err != nil {
		t.Fatalf("web collect: %v", err)
	}
}

// TestWithWebCrawlOptions_UserAgentReachesTheCrawl covers the one assigned field
// the validator cannot see. Every other field in the struct literal is either
// rejected by value (the table above) or has no observable effect here.
//
// THE ASSERTION IS ON THE WIRE, not on the struct. A UserAgent that were assigned
// but never reached the request would satisfy any structural check and fail this.
func TestWithWebCrawlOptions_UserAgentReachesTheCrawl(t *testing.T) {
	const agent = "crawlopts-seam-probe/1.0"
	f := newCrawlOptsFixture(t)

	runWebCollectFromPayload(t, `{"type":"web","id":"web/crawlopts","seed_urls":["`+
		f.srv.URL+`/seed.html"],"user_agent":"`+agent+`","politeness_ms":1}`)

	// KNOWN POSITIVE: the crawl actually issued a request. Without it, "the
	// header was not some other value" would be satisfied by no requests at all.
	if !f.sawPath("/seed.html") {
		t.Fatal("the seed was never fetched; this test proves nothing about UserAgent")
	}
	if !f.sawAgent(agent) {
		t.Errorf("no request carried the payload's user_agent %q; the UserAgent assignment "+
			"did not reach the crawl (agents seen: %v)", agent, f.agents)
	}
}
