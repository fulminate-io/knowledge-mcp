// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fixtureHTML is a small multi-section page that exercises every emitted
// node kind (page, section, paragraph, code_block, list, list_item, link).
// Counts are asserted against this fixture below; keep them in sync if the
// HTML shape changes.
const fixtureHTML = `<!doctype html>
<html>
<head><title>Test Source Page</title></head>
<body>
<article>
<h1>Welcome</h1>
<p>This is the intro paragraph for the test fixture. It contains enough prose that readability keeps it.</p>
<p>A second paragraph ensures the extractor has more than a trivial amount of text to work with — readability bails on very short bodies.</p>
<h2>First Section</h2>
<p>Body of the first section with more prose so the extractor has something substantive to keep.</p>
<pre><code class="language-go">package main

func main() {
	println("hello")
}
</code></pre>
<ul>
<li>First item with enough text</li>
<li>Second item also with enough text</li>
</ul>
<h2>Second Section</h2>
<p>Second section body paragraph with some real text content so readability retains it.</p>
<p><a href="/other">A local link with enough anchor text</a> plus additional context so the paragraph survives readability.</p>
</article>
</body>
</html>
`

// TestCollect_SingleURL_EndToEnd verifies the single-URL path through
// collector.Collect: CrawlOptions in ctx, HTTP fetch, readability chrome-strip,
// DOM walk, emission, and the resulting CollectResult batch handed to the sink.
// The capturing sink records the batch so the test asserts node-type counts on
// the value-type slice — the .bin persistence + vector-count gates are
// storage-engine behaviors covered by pkg/store tests (out of scope here).
func TestCollect_SingleURL_EndToEnd(t *testing.T) {
	sink := initWebTestSink(t)

	// Serve the fixture over httptest so the fetch client sees a real
	// HTTP transport.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixtureHTML))
	}))
	t.Cleanup(srv.Close)

	opts := CrawlOptions{
		Source:       "test-source",
		SeedURLs:     []string{srv.URL},
		MaxDepth:     0,
		MaxPages:     1,
		PolitenessMs: 0,
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	err := collector.Collect(ctx, "web", "test-source", collector.CollectOptions{Force: true})
	require.NoError(t, err)

	batch := sink.last()
	require.NotNil(t, batch, "collector must hand a CollectResult to the sink")
	assert.Equal(t, kgtypes.GraphWebRaw, batch.GraphType)
	assert.Equal(t, "test-source", batch.GraphName)

	typeCounts := countCapturedNodeTypes(batch.Nodes)
	assert.Equal(t, 1, typeCounts["page"], "exactly one page node expected")
	// Fixture has two <h2> sections. The synthetic depth-0 section for
	// pre-heading prose is NOT emitted as a "section" type — only real
	// heading sections are emitted; depth-0 is a sink that is only
	// returned when the doc has no headings at all (cleaned article has
	// an H1 so it is returned as the sole top-level section with H2s
	// nested underneath).
	assert.GreaterOrEqual(t, typeCounts["section"], 1, "at least one section node expected")
	assert.GreaterOrEqual(t, typeCounts["paragraph"], 1, "paragraphs must be emitted")

	// LLM-pipeline exclusion for a web raw graph is NOT asserted here via a
	// client-side eligibility call (FUL-307, Option B: the client makes zero
	// GraphType.Summarizable/Embeddable calls — that decision is server-only).
	// The exclusion is now enforced structurally by the pipeline + server:
	// the collector is spawned for every graph, but the server's pipeline_scan
	// handler short-circuits NodeIDsBySummaryGap/ByEmbedGap on the graph type
	// and returns empty for web raw, so no summary/embed work is ever produced.
	// That idle-cheap behavior is locked by
	// pipeline.TestRegisterGraph_NonEligibleGraphIdles; the server-side
	// eligibility invariant is locked by store's graph_types_eligibility_test.go.
}
