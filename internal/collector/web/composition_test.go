// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// compFrom builds a CollectComposition from a by-type map, deriving TotalNodes
// from the map so a fixture cannot claim a count its own type census contradicts.
func compFrom(name string, byType map[string]int) collector.CollectComposition {
	total := 0
	for _, n := range byType {
		total += n
	}
	return collector.CollectComposition{
		GraphName:   name,
		TotalNodes:  total,
		NodesByType: byType,
	}
}

// TestWebCollector_AssertComposition_FiresWhenNoSubstantiveContent drives the
// MEASURED CWE composition — the harvest this ticket exists for. Both zero terms
// are stated explicitly, because the predicate is paragraph+code_block == 0 and a
// fixture that left either implicit would not say which term it exercises.
func TestWebCollector_AssertComposition_FiresWhenNoSubstantiveContent(t *testing.T) {
	c := &WebCollector{}

	cwe := compFrom("cwe", map[string]int{
		"page": 4, "section": 4, "list_item": 24, "link": 16, "table": 12, "list": 4,
	})
	require.Equal(t, 0, cwe.NodesByType["paragraph"], "the measured CWE harvest has zero paragraph")
	require.Equal(t, 0, cwe.NodesByType["code_block"], "the measured CWE harvest has zero code_block")

	err := c.AssertComposition(cwe)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harvest captured nothing usable")
	assert.Contains(t, err.Error(), "cwe", "the error names the source")
	assert.Contains(t, err.Error(), "nodes 64", "the error embeds what WAS captured")
}

// TestWebCollector_AssertComposition_SilentOnNonPageHarvests guards the PAGE
// GATE against the two MEASURED non-page shapes. Both are correct harvests today
// and all three github-mode e2e tests depend on them staying silent.
//
// What makes them legitimate is that they emit NO page node — github URLs
// short-circuit into materializeGithub, which records no pageRecord — not merely
// that they have zero paragraphs.
func TestWebCollector_AssertComposition_SilentOnNonPageHarvests(t *testing.T) {
	c := &WebCollector{}

	// github tree-mode; blob-mode has the same type set.
	tree := compFrom("test-gh-tree", map[string]int{
		"github_repo": 1, "file": 3, "function_declaration": 3, "language": 1,
	})
	require.Equal(t, 0, tree.NodesByType["page"], "github mode emits no page node")
	require.NoError(t, c.AssertComposition(tree))

	// The size-cap shape: a single warning document, still no page node.
	sizeCap := compFrom("test-gh-sizecap", map[string]int{"document": 1})
	require.Equal(t, 0, sizeCap.NodesByType["page"])
	require.NoError(t, c.AssertComposition(sizeCap))

	// KNOWN POSITIVE, same asserter and same substantive-content shape: add a
	// page node to the size-cap map and it FIRES. Without this the two silences
	// above are indistinguishable from an asserter that never fires at all.
	gated := compFrom("test-gh-sizecap", map[string]int{"document": 1, "page": 1})
	require.Error(t, c.AssertComposition(gated),
		"the page gate is what silences the harvests above, not a dead asserter")
}

// TestWebCollector_AssertComposition_FiresOnZeroNodes covers the leg the page
// gate structurally cannot: zero nodes means zero pages, so the gate would not
// apply. The state is reachable today — collector/web/collector.go returns a nil
// node slice and no error when every fetch fails.
func TestWebCollector_AssertComposition_FiresOnZeroNodes(t *testing.T) {
	c := &WebCollector{}

	empty := collector.CollectComposition{GraphName: "all-fetches-failed"}
	require.Equal(t, 0, empty.TotalNodes)
	require.Equal(t, 0, empty.NodesByType["page"], "zero nodes means the page gate cannot apply")

	err := c.AssertComposition(empty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harvest captured nothing usable")
	assert.Contains(t, err.Error(), "all-fetches-failed")
	assert.Contains(t, err.Error(), "no nodes at all")
}

// TestWebCollector_AssertComposition_FiresWhenRetentionMissing drives the
// RETENTION leg specifically. The map carries substantive content so the
// chrome leg above cannot fire — without that, a passing test would not
// distinguish "the retention leg fired" from "the chrome leg fired first".
func TestWebCollector_AssertComposition_FiresWhenRetentionMissing(t *testing.T) {
	c := &WebCollector{}

	// Four pages' worth of real prose, and only one page's HTML retained.
	unretained := compFrom("retention-lost", map[string]int{
		"page": 4, "section": 4, "paragraph": 22, "code_block": 3, "raw_html": 1,
	})
	require.Positive(t, unretained.NodesByType["paragraph"]+unretained.NodesByType["code_block"],
		"the fixture must carry substantive content so the chrome leg cannot fire")
	require.Less(t, unretained.NodesByType["raw_html"], unretained.NodesByType["page"])

	err := c.AssertComposition(unretained)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "page bodies were not retained",
		"the retention leg must carry its OWN message, not the chrome leg's")
	assert.NotContains(t, err.Error(), "harvest captured nothing usable",
		"reusing the chrome leg's locked substring would make the landed gates pass for the wrong reason")
	assert.Contains(t, err.Error(), "retention-lost", "the error names the source")
	assert.Contains(t, err.Error(), "only 1 raw_html", "the error embeds what WAS retained")

	// SILENCE, same asserter and the same shape one node different: retention
	// complete means no error. Without this the assertion above is
	// indistinguishable from a leg that fires on every harvest.
	retained := compFrom("retention-complete", map[string]int{
		"page": 4, "section": 4, "paragraph": 22, "code_block": 3, "raw_html": 4,
	})
	require.NoError(t, c.AssertComposition(retained))
}

// --- The three CONTENT axes, as REAL CRAWLS ----------------------------------
//
// Each test below serves a fixture over httptest, runs the real crawl through
// collector.Collect, ASSERTS ITS COMPOSITION EXPLICITLY, and only then asserts
// the CheckComposition verdict.
//
// WHY THE EXPLICIT COMPOSITION ASSERTIONS ARE THE POINT, not decoration: a test
// that only checked the verdict cannot distinguish "the fixture produced the
// intended shape and the guard behaved" from "the fixture produced something
// else that happens to give the same verdict". It also keeps the failure
// DIAGNOSTIC — if extraction ever changes what these fixtures emit, the failure
// names the node type that appeared instead of showing an unexplained verdict
// flip.

// serveCrawl starts an httptest server for one HTML body and runs a real
// single-page crawl of it, returning BOTH the composition collector.Collect
// reports and the CollectResult the capturing sink received — one run observed
// two ways. Mirrors the httptest + CrawlOptions + collector.Collect shape at
// integration_single_test.go:56-77 rather than standing up a new harness.
func serveCrawl(t *testing.T, source, body string) (collector.CollectComposition, *collectorwire.CollectResult) {
	t.Helper()
	sink := initWebTestSink(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	opts := CrawlOptions{
		Source:       source,
		SeedURLs:     []string{srv.URL},
		MaxDepth:     0,
		MaxPages:     1,
		PolitenessMs: 0,
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	comp, err := collector.Collect(ctx, "web", source, collector.CollectOptions{Force: true})
	require.NoError(t, err)

	batch := sink.last()
	require.NotNil(t, batch, "collector must hand a CollectResult to the sink")
	return comp, batch
}

// serveComposition is serveCrawl for the callers that only need the
// composition verdict.
func serveComposition(t *testing.T, source, body string) collector.CollectComposition {
	t.Helper()
	comp, _ := serveCrawl(t, source, body)
	return comp
}

// chromeOnlyHTML is a navigation menu and nothing else: a <nav> holding a single
// <ul> of <li> elements, each with ONE bare <a href>. No <p>, no <pre>, NO
// LAYOUT TABLE, no free text outside anchors, no prose-bearing <div>s.
//
// THE SHAPE IS THE POINT, and it is deliberately NOT the CWE-modeled fixture
// this replaced. That one carried a 3-row table plus presentational divs, and
// its zero-paragraph property held only because prose currently comes solely
// from atom.P. The sibling extraction lane exists precisely to remove that: it
// makes in-table content walkable and emits prose from block boxes with no <p>
// wrapper. A table is chrome only until someone teaches the walker to read
// tables — so that fixture would have started emitting paragraphs, the guard
// would stop firing on it, and this test would go red against CORRECT sibling
// work. Anchors under a nav list are chrome in ANY regime: handleList claims the
// whole <ul> subtree and the walker never recurses into <li> children.
const chromeOnlyHTML = `<!doctype html>
<html>
<head><title>Weakness Catalog Navigation</title></head>
<body>
<nav>
<ul>
<li><a href="/catalog/one">Improper Input Validation</a></li>
<li><a href="/catalog/two">Improper Neutralization of Special Elements</a></li>
<li><a href="/catalog/three">Buffer Copy without Checking Size of Input</a></li>
<li><a href="/catalog/four">Improper Restriction of Operations within Bounds</a></li>
<li><a href="/catalog/five">Use After Free in Memory Management Routines</a></li>
<li><a href="/catalog/six">Improper Limitation of a Pathname to a Directory</a></li>
<li><a href="/catalog/seven">Exposure of Sensitive Information to an Actor</a></li>
<li><a href="/catalog/eight">Incorrect Permission Assignment for Critical Resource</a></li>
</ul>
</nav>
</body>
</html>
`

// TestCollect_ChromeOnlyHarvest_ReportsFailure is the KNOWN-POSITIVE CONTROL for
// the entire guard. If it stops firing, the guard is vacuous and nothing else in
// this plan detects that.
//
// DO NOT RELAX THE paragraph == 0 && code_block == 0 ASSERTION to make this pass.
// Weakening it hollows out the one control that makes the whole guard
// non-vacuous.
func TestCollect_ChromeOnlyHarvest_ReportsFailure(t *testing.T) {
	comp := serveComposition(t, "chrome-only", chromeOnlyHTML)

	assert.Positive(t, comp.NodesByType["page"], "the crawl must emit a page node — this is what arms the page gate")
	assert.Equal(t, 0, comp.NodesByType["paragraph"], "a nav list of bare anchors emits no paragraph")
	assert.Equal(t, 0, comp.NodesByType["code_block"], "a nav list of bare anchors emits no code_block")

	err := collector.CheckComposition("web", comp)
	require.Error(t, err, "a chrome-only harvest must not report plain success")
	assert.Contains(t, err.Error(), "harvest captured nothing usable")
}

// proseOnlyHTML is several <p> paragraphs and NO <pre>. Modeled on the
// prose-bearing fixture already crawled at crawl_test.go:210.
const proseOnlyHTML = `<!doctype html>
<html>
<head><title>An Ordinary Prose Page</title></head>
<body>
<article>
<h1>An Ordinary Prose Page</h1>
<p>This is the canonical entry point of the prose fixture. It contains several sentences of substantive prose so that readability will not reject the page as chrome or boilerplate.</p>
<p>A second paragraph ensures the extractor has more than a trivial amount of text to work with, because readability bails on very short bodies and would leave nothing to emit.</p>
<p>A third paragraph continues the same theme, supplying enough ordinary running text that the cleaned article is unambiguously an article rather than a navigation shell.</p>
<h2>A Second Heading</h2>
<p>The body of the second section carries yet more prose, so the extracted document keeps a shape a reader would recognize as a page of writing.</p>
<p>Another paragraph in the second section, again with enough real text content that readability retains the body rather than discarding it as boilerplate.</p>
<p>A final paragraph closes the fixture with more substantive sentences, keeping the total comfortably above any minimum-length heuristic in the extractor.</p>
</article>
</body>
</html>
`

// TestCollect_ProseOnlyHarvest_ReportsSuccess is THE PARAGRAPH-BLINDNESS KILLER.
//
// WHAT IT CATCHES: an implementation written as `if page >= 1 && codeBlock == 0
// { fire }` — one that adds the code_block term but DROPS the paragraph term —
// passes every other control in this plan, and then fires on every ordinary
// prose page with no code, which is most of the web. No other control on the web
// leg varies paragraph-presence.
func TestCollect_ProseOnlyHarvest_ReportsSuccess(t *testing.T) {
	comp := serveComposition(t, "prose-only", proseOnlyHTML)

	assert.Positive(t, comp.NodesByType["page"], "the crawl must emit a page node")
	assert.Positive(t, comp.NodesByType["paragraph"], "the prose fixture must emit paragraphs")
	assert.Equal(t, 0, comp.NodesByType["code_block"], "the prose fixture has no <pre> and must emit no code_block")

	assert.NoError(t, collector.CheckComposition("web", comp),
		"an ordinary prose page with no code is a real harvest and must stay silent")
}

// codeOnlyHTML is headings plus <pre> blocks and NO <p>.
const codeOnlyHTML = `<!doctype html>
<html>
<head><title>API Reference Snippets</title></head>
<body>
<article>
<h1>API Reference Snippets</h1>
<h2>Opening A Client</h2>
<pre><code class="language-go">package main

import "example.com/client"

func main() {
	c, err := client.Open("example.com:443")
	if err != nil {
		panic(err)
	}
	defer c.Close()
}
</code></pre>
<h2>Issuing A Request</h2>
<pre><code class="language-go">resp, err := c.Do(ctx, client.Request{
	Method: "GET",
	Path:   "/v1/resources",
})
if err != nil {
	return err
}
</code></pre>
<h2>Handling The Response</h2>
<pre><code class="language-go">for _, item := range resp.Items {
	if err := handle(item); err != nil {
		return fmt.Errorf("handle %s: %w", item.ID, err)
	}
}
</code></pre>
</article>
</body>
</html>
`

// TestCollect_CodeOnlyHarvest_ReportsSuccess is the code-only arm as a REAL
// CRAWL. A map arm would exercise AssertComposition alone and would stay green
// even if <pre> emission broke; this arm is what proves the real path actually
// produces code_block nodes from <pre>.
func TestCollect_CodeOnlyHarvest_ReportsSuccess(t *testing.T) {
	comp := serveComposition(t, "code-only", codeOnlyHTML)

	assert.Positive(t, comp.NodesByType["page"], "the crawl must emit a page node")
	assert.Equal(t, 0, comp.NodesByType["paragraph"], "the code fixture has no <p> and must emit no paragraph")
	assert.Positive(t, comp.NodesByType["code_block"], "the code fixture must emit code_block nodes from its <pre> blocks")

	assert.NoError(t, collector.CheckComposition("web", comp),
		"a page made entirely of code is a real harvest and must stay silent")
}

// --- The per-node uri census, as a REAL CRAWL --------------------------------

// TestCollect_URICensus_EveryNodeCarriesURI proves the `uri` stamp survives the
// whole real path — HTTP fetch, readability, DOM walk, emission — rather than
// only the hand-built pageRecord that emit_nodes_uri_test.go drives.
//
// It is a CENSUS, not a spot check: the ticket's clause is "stamp uri on EVERY
// emitted node", and a spot check on one paragraph would stay green while an
// entire record kind went unstamped. The three controls below are what keep the
// 100% assertion from being satisfiable by a crawl that emitted almost nothing,
// or by a run in which the anchor arm was entirely dead.
//
// It deliberately does not enumerate the expected node types: later phases add
// node kinds to this same slice, and every one of them must carry a uri too.
func TestCollect_URICensus_EveryNodeCarriesURI(t *testing.T) {
	body, err := os.ReadFile("testdata/uri_census.html")
	require.NoError(t, err, "the census fixture must be readable")

	_, batch := serveCrawl(t, "uri-census", string(body))
	nodes := batch.Nodes

	// CONTROL 1 — non-vacuity. Without it, a crawl that emitted two nodes of
	// one type would satisfy the 100% assertion below.
	require.GreaterOrEqual(t, len(nodes), 6,
		"the census needs a real node population; %d nodes makes the assertions below near-vacuous", len(nodes))
	types := map[string]int{}
	for _, n := range nodes {
		types[n.Type]++
	}
	require.GreaterOrEqual(t, len(types), 3,
		"the census needs several record kinds represented, got %v", types)

	// THE CENSUS — every node, reported per-node so a regression names the
	// record kind that lost its stamp.
	pageURI := ""
	for _, n := range nodes {
		if n.Type == "page" {
			pageURI = n.Metadata["uri"]
		}
	}
	require.NotEmpty(t, pageURI, "the page node must carry a uri to anchor the prefix assertion")

	missing := 0
	withFragment, withoutFragment := 0, 0
	for _, n := range nodes {
		got := n.Metadata["uri"]
		if got == "" {
			t.Errorf("node id=%s type=%s carries no uri (metadata=%v)", n.Id, n.Type, n.Metadata)
			missing++
			continue
		}
		// Every uri is addressed from THIS page, never some other page's.
		assert.True(t, got == pageURI || strings.HasPrefix(got, pageURI+"#"),
			"node id=%s type=%s uri=%q is not addressed from the page's final URL %q", n.Id, n.Type, got, pageURI)
		if strings.Contains(got, "#") {
			withFragment++
		} else {
			withoutFragment++
		}
	}
	assert.Equal(t, 0, missing, "%d of %d emitted nodes carry no uri", missing, len(nodes))

	// CONTROL 2 — the fragment arm is live. A run where NO uri carried a
	// fragment would satisfy the census with the anchored-section code dead.
	assert.Positive(t, withFragment,
		"no emitted node carried a '#' fragment — the anchored-section arm never ran")
	// CONTROL 3 — and the bare arm is live too, so a stamp that appended a
	// fragment unconditionally would not pass as inheritance.
	assert.Positive(t, withoutFragment,
		"every emitted node carried a '#' fragment — the inheritance arm never ran")
}
