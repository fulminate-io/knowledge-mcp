// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// v1FixtureHTML exercises every scraper v1 feature in one pass so a
// single regression surfaces exactly which feature broke. Attributes
// asserted on below are those go-readability PRESERVES: id, data-*,
// cite, rel, and the inline-emphasis tag names. class and role are
// stripped by readability's chrome-strip and do NOT survive to the
// emitted graph. The
// emitter-level unit tests (parse_dom_attrs_test.go, emit_nodes_test.go)
// verify class/role preservation against raw *html.Node trees, so v1's
// attribute-preservation contract is fully covered at the unit layer;
// this integration test's job is confirming the subset that survives
// readability actually reaches the graph.
const v1FixtureHTML = `<!doctype html>
<html>
<head><title>Pattern Page</title></head>
<body class="pattern-page" id="main" role="document" data-source="test-fixture">
<article>
<h1>Pattern Page Overview</h1>
<p>Introductory prose padded with enough real content that readability
retains this article body without dropping it as too-thin for extraction.
We add a second sentence so the extractor is comfortable keeping it.</p>
<h2 class="pattern-problem" id="context" data-kind="problem">Context</h2>
<p class="pattern-lead">The pattern applies when <strong>the system</strong>
has <code>high latency</code> in the <em>request path</em> and downstream
services must still meet the aggregated latency budget for every user
request that reaches the front-end tier.</p>
<ul class="applies-when" data-category="hot-path">
<li>Web requests under load that the front-end cannot keep buffered</li>
<li>API aggregation across two or more downstream services</li>
</ul>
<pre class="language-go"><code class="language-go">if cached { return }</code></pre>
<p>Here is an outbound link inside prose:
<a href="/followed.html">Followed</a> is part of the paragraph so it
gets classified as an in-prose link (internal, followed) and enqueued
for BFS on the seed.</p>
<nav class="outbound-nav">
<a href="/other.html" rel="nofollow">Skipped</a>
</nav>
<blockquote cite="https://example.org"><strong>Key insight:</strong>
patterns are discovered, not invented in isolation from the workloads
they describe.</blockquote>
</article>
<nav>
<a href="/nav-a.html">Nav A</a>
<a href="/nav-b.html">Nav B</a>
</nav>
</body>
</html>
`

// v1FollowupHTMLTmpl is a fmt.Sprintf template for the followup pages
// served at /followed.html, /nav-a.html, and /nav-b.html. The two %s
// format verbs consume the path twice so each body carries a distinct
// ContentHash — otherwise crawl_process.go's isContentAlias dedup
// would collapse them into a single crawled page (nav-a and nav-b
// would become unresolved reference placeholders).
const v1FollowupHTMLTmpl = `<!doctype html><html><body><article>
<h1>Followup %s</h1>
<p>This followup page exists so the BFS can actually enqueue and
fetch it after parsing the seed. Readability needs a bit of prose to
keep the body; two sentences is enough for the extractor to retain
this article content instead of dropping it as chrome. Path tag %s
is embedded twice so the ContentHash is distinct per followup.</p>
</article></body></html>`

// TestCollectorV1_AttrsAndEmphasis_IntegrationFixture runs the full
// collector pipeline (httptest → fetch → clean → parse → emit →
// CreateBatch) on a single realistic page that exercises every v1
// feature in one pass. Each assertion group is a t.Run subtest so a
// regression isolates the offending feature immediately.
//
// Readability strips class and role attributes from all elements and
// replaces the <body> wrapper entirely, so assertions below target
// the subset of attributes that SURVIVE the readability pass: id,
// data-*, cite, rel, and inline-emphasis tag names.
func TestCollectorV1_AttrsAndEmphasis_IntegrationFixture(t *testing.T) {
	sink := initWebTestSink(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/seed.html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(v1FixtureHTML))
	})
	// Each followup serves a BYTE-DISTINCT body so the content-hash
	// dedup in crawl_process.go:isContentAlias does not collapse them
	// into a single crawled page. A shared template would be rejected
	// as a content-alias after the first one.
	for _, p := range []string{"/followed.html", "/nav-a.html", "/nav-b.html"} {
		body := fmt.Sprintf(v1FollowupHTMLTmpl, p, p)
		mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(body))
		})
	}
	// /other.html is nofollow-excluded; installing a handler that
	// t.Errorf's on any request catches a regression where the
	// nofollow gate silently drops.
	mux.HandleFunc("/other.html", func(_ http.ResponseWriter, _ *http.Request) {
		t.Errorf("/other.html must never be fetched (rel=nofollow should exclude it)")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	opts := CrawlOptions{
		Source:       "v1-fixture",
		SeedURLs:     []string{srv.URL + "/seed.html"},
		MaxDepth:     2,
		MaxPages:     10,
		PolitenessMs: 0,
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	_, err := collector.Collect(ctx, "web", "v1-fixture", collector.CollectOptions{Force: true})
	require.NoError(t, err)

	batch := sink.last()
	require.NotNil(t, batch, "collector must hand a CollectResult to the sink")

	snap := v1CollectSnapshot(t, batch, srv.URL)

	t.Run("section_id_preserved", func(t *testing.T) {
		// readability strips class but preserves id on headings; the
		// section attrs test confirms id lands on the emitted section
		// node. The <h2 id="context"> fixture surfaces as the "Context"
		// section with id=context in metadata.
		sec := snap.findSectionByHeading("Context")
		require.NotNil(t, sec, "Context section must exist; sections=%v", snap.sectionsSummary())
		assert.Equal(t, "context", kgtypes.Value(sec, "id"), "section id preserved through readability")
	})

	t.Run("section_data_attr_preserved", func(t *testing.T) {
		// data-kind="problem" survives readability on the <h2>.
		sec := snap.findSectionByHeading("Context")
		require.NotNil(t, sec)
		data := parseJSONStringMap(t, kgtypes.Value(sec, "data"))
		assert.Equal(t, "problem", data["kind"],
			"section data-kind preserved through readability (got data=%q)",
			kgtypes.Value(sec, "data"))
	})

	t.Run("paragraph_inline_emphasis", func(t *testing.T) {
		// Inline emphasis tags survive readability. Find the paragraph
		// that contains all three spans (strong/code/em) in that order;
		// class is stripped so we can't key on .pattern-lead.
		raw := snap.paragraphInlineEmphasisWithTags([]string{"strong", "code", "em"})
		require.NotEmpty(t, raw, "no paragraph carries [strong,code,em] inline_emphasis list; paragraphs=%v", snap.paragraphsSummary())
		var emphs []struct {
			Tag      string `json:"tag"`
			Text     string `json:"text"`
			Position int    `json:"position"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &emphs))
		require.Len(t, emphs, 3, "want 3 emphasis spans (strong/code/em), got %v", emphs)
		assert.Equal(t, "strong", emphs[0].Tag)
		assert.Equal(t, "code", emphs[1].Tag)
		assert.Equal(t, "em", emphs[2].Tag)
		// Positions must be strictly ascending.
		assert.Less(t, emphs[0].Position, emphs[1].Position, "positions ascending 0→1")
		assert.Less(t, emphs[1].Position, emphs[2].Position, "positions ascending 1→2")
		assert.Equal(t, "the system", emphs[0].Text, "strong text")
		assert.Equal(t, "high latency", emphs[1].Text, "code text")
		assert.Equal(t, "request path", emphs[2].Text, "em text")
	})

	t.Run("list_data_attr_preserved", func(t *testing.T) {
		// data-category="hot-path" survives readability on <ul>.
		list := snap.findListWithDataKey("category")
		require.NotNil(t, list, "no list with data-category; lists=%v", snap.listsSummary())
		data := parseJSONStringMap(t, kgtypes.Value(list, "data"))
		assert.Equal(t, "hot-path", data["category"], "list data-category")
	})

	t.Run("code_block_emitted", func(t *testing.T) {
		// readability strips class from both <pre> and <code>, so the
		// `language-xxx` recovery hook in langFromClass receives an
		// empty class string and cannot recover the language. This is
		// the real integration truth — the <pre><code>...</code></pre>
		// block still emits a code_block node, but its language field
		// is empty. Unit tests in parse_dom_test.go (see
		// TestParsePage_PreCodeBlockLanguage) verify langFromClass
		// works against raw *html.Node trees; downstream transformers
		// that need language detection must use a different signal
		// (file extension, cue word frequency) when reading from
		// GraphWebRaw. Readability strips class and role from every
		// element; id, data-*, cite and rel survive.
		cb := snap.firstOfType("code_block")
		require.NotNil(t, cb, "no code_block emitted")
		assert.NotEmpty(t, cb.Content, "code_block must retain source content")
		assert.Contains(t, cb.Content, "if cached",
			"code_block source must contain the fixture code (got %q)", cb.Content)
	})

	t.Run("nofollow_link_recorded", func(t *testing.T) {
		// The /other.html link's rel=nofollow must still produce a
		// link node with nofollow=true metadata — the nofollow gate
		// excludes it from BFS enqueue, not from emission.
		link := snap.findLinkByURL(srv.URL + "/other.html")
		require.NotNil(t, link, "nofollow link /other.html must still be emitted as a link node")
		assert.Equal(t, "true", kgtypes.Value(link, "nofollow"), "nofollow=true on /other.html link")
	})

	t.Run("raw_link_nav_recovery", func(t *testing.T) {
		// <nav> anchors are stripped by readability but recovered by
		// the pre-readability seedRawLinks pass. They surface in
		// pageRecord.InternalLinks and land as rel=internal
		// EdgeReferences edges after resolveInternalLinks (which
		// rewrites the placeholder ToID to the crawled page ID —
		// rel=internal in Evidence is preserved).
		page := snap.requireSeedPage(t)
		internal := snap.referencedInternalURLs(page.Id)
		for _, must := range []string{"/followed.html", "/nav-a.html", "/nav-b.html"} {
			want := srv.URL + must
			assert.Containsf(t, internal, want,
				"page references must include %q (raw-link recovery/BFS); got %v",
				want, internal)
		}
		notWant := srv.URL + "/other.html"
		assert.NotContainsf(t, internal, notWant,
			"nofollow-excluded %q must not appear in internal references; got %v",
			notWant, internal)
	})

	t.Run("blockquote_cite_and_emphasis", func(t *testing.T) {
		// <blockquote cite> survives readability; the emitter maps it
		// to the cite_url metadata key. Inline <strong> inside the
		// blockquote becomes one inline_emphasis entry.
		bq := snap.firstOfType("blockquote")
		require.NotNil(t, bq, "no blockquote emitted")
		cite := kgtypes.Value(bq, "cite_url")
		if cite == "" {
			cite = kgtypes.Value(bq, "cite")
		}
		assert.Contains(t, cite, "example.org", "blockquote cite must carry example.org")
		raw := kgtypes.Value(bq, "inline_emphasis")
		require.NotEmptyf(t, raw, "blockquote must carry inline_emphasis for <strong>Key insight:</strong>")
		assert.Contains(t, raw, `"tag":"strong"`,
			"blockquote inline_emphasis must include the <strong> span: %q", raw)
	})
}

// v1Snapshot + helpers live in integration_v1_helpers_test.go so each
// file stays under the 300 LOC recommended cap.
