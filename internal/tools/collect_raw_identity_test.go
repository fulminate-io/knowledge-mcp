// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector" // registers the "pdf" collector
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/web"              // registers the "web" collector
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// collect_raw_identity_test.go drives InterceptCollect ITSELF rather than the
// helpers underneath it, so every assertion here is on the rendered answer a
// caller receives or on the CollectResult the REAL collector handed its sink.
// A derivation that is wrong but compiles, a refusal that never fires, and a
// refusal so broad it blocks a legitimate re-collect are each visible only at
// this level: the helpers agree with themselves by construction.

// rawIdentityDeps is detachFullDeps with a CAPTURING sink, so a test can read
// the collector's own GraphName and node metadata off the result the collect
// shipped. Everything else — the standing runtime, PipelineReady, the
// GraphCaller the catalog and root reads go through — is reused as-is.
type rawIdentityDeps struct {
	*detachFullDeps
	sink *capturingSink
}

func (d *rawIdentityDeps) Sink() collector.Sink { return d.sink }

// newRawIdentityDeps builds the dependency set one collect runs against, with
// the supplied catalog fake answering both wire reads the precheck issues.
func newRawIdentityDeps(fake *rawModulesFake) *rawIdentityDeps {
	return &rawIdentityDeps{
		detachFullDeps: &detachFullDeps{rt: NewCollectRuntime(), gc: fake},
		sink:           &capturingSink{},
	}
}

// emptyRawCatalog is a catalog holding no graphs of the type: the first-ever
// collect of a document.
func emptyRawCatalog() *rawModulesFake {
	return &rawModulesFake{roots: map[string]*knowledgev1.Node{}, bulk: map[string][]*knowledgev1.Node{}}
}

// oneGraphRawCatalog is a catalog holding exactly one graph, whose root carries
// the given metadata key and value. An EMPTY key seeds a root carrying no source
// key at all — the shape of a graph collected before its family recorded one.
func oneGraphRawCatalog(name, rootType, key, value string) *rawModulesFake {
	md := map[string]string{}
	if key != "" {
		md[key] = value
	}
	return &rawModulesFake{
		catalog: []*knowledgev1.GraphInfo{{Name: name, Loaded: true}},
		roots: map[string]*knowledgev1.Node{
			name: {Id: "root-" + name, Type: rootType, Metadata: md},
		},
		bulk: map[string][]*knowledgev1.Node{},
	}
}

// rawIdentityDocName is the basename every copy of the fixture lands under.
// ONE name across every call is the point: each call gets its OWN t.TempDir(),
// so two calls produce two DIFFERENT documents that share a basename — which is
// exactly the input the collision rule exists to act on.
const rawIdentityDocName = "designing-systems"

// copyFixturePDF copies the package's small PDF fixture into a fresh directory
// under rawIdentityDocName and returns the absolute path it wrote.
func copyFixturePDF(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs("../collector/pdf/testdata/t4_paragraph_simple.pdf")
	require.NoError(t, err)
	body, err := os.ReadFile(src) //nolint:gosec // G703: the package's own checked-in fixture, resolved from a literal
	require.NoError(t, err)
	dst := filepath.Join(t.TempDir(), rawIdentityDocName+".pdf")
	require.NoError(t, os.WriteFile(dst, body, 0o600)) //nolint:gosec // G703: fixture path under t.TempDir()
	return dst
}

// runCollect drives the real InterceptCollect over a JSON payload.
func runCollect(t *testing.T, deps ClientDeps, payload map[string]any) kgtools.ToolResult {
	t.Helper()
	args, err := json.Marshal(payload)
	require.NoError(t, err)
	handled, res := InterceptCollect(opCtx(), deps,
		kgtools.CallToolParams{Name: "collect", Arguments: json.RawMessage(args)})
	require.True(t, handled, "InterceptCollect must claim a collect call")
	return res
}

// TestCollectPDF_NamedAfterItsFile_AndRefusesASecondSource is the pdf half of
// the ticket, observed end to end: the name a document lands under, and the
// refusal that is now the only thing keeping two same-basename documents apart.
func TestCollectPDF_NamedAfterItsFile_AndRefusesASecondSource(t *testing.T) {
	t.Run("first_collect_lands_under_the_plain_basename", func(t *testing.T) {
		path := copyFixturePDF(t)
		deps := newRawIdentityDeps(emptyRawCatalog())

		res := runCollect(t, deps, map[string]any{"type": "pdf", "id": path})
		body := resultText(res)
		require.False(t, res.IsError, "the first collect of a document must be admitted: %s", body)

		got := deps.sink.last()
		require.NotNil(t, got, "the collect must have reached the sink")
		assert.Equal(t, rawIdentityDocName, got.GraphName,
			"a pdf graph is named after its file: the plain sanitized basename, no hash suffix")
		assert.Contains(t, body, rawIdentityDocName, "the answer must name the graph the collect landed in")
		assert.Contains(t, body, "drop_graph", "the raw-collect answer must carry its drop call")

		// The document root records WHERE IT CAME FROM — the value the refusal
		// below compares an incoming collect against.
		require.NotEmpty(t, got.Nodes)
		assert.Equal(t, path, got.Nodes[0].GetMetadata()["path"],
			"the document root must record the absolute path it was collected from")
	})

	t.Run("a_different_file_under_the_same_name_is_refused_naming_both", func(t *testing.T) {
		occupant := copyFixturePDF(t) // a DIFFERENT directory, same basename
		incoming := copyFixturePDF(t)
		require.NotEqual(t, occupant, incoming, "the two documents must be distinct files")

		deps := newRawIdentityDeps(oneGraphRawCatalog(rawIdentityDocName, "document", "path", occupant))
		res := runCollect(t, deps, map[string]any{"type": "pdf", "id": incoming})
		body := resultText(res)

		assert.True(t, res.IsError, "a second source under an occupied name must be REFUSED: %s", body)
		assert.Contains(t, body, occupant, "the refusal must name the source already recorded")
		assert.Contains(t, body, incoming, "the refusal must name the source being collected")
		assert.Contains(t, body, rawIdentityDocName, "the refusal must name the graph both want")
		assert.Nil(t, deps.sink.last(),
			"nothing may be written: the refusal runs BEFORE the walk, so the document was never parsed")
	})

	t.Run("the_same_file_re_collects", func(t *testing.T) {
		path := copyFixturePDF(t)
		deps := newRawIdentityDeps(oneGraphRawCatalog(rawIdentityDocName, "document", "path", path))

		res := runCollect(t, deps, map[string]any{"type": "pdf", "id": path})
		require.False(t, res.IsError,
			"re-collecting the SAME document must be admitted; a refusal here would make every "+
				"raw graph collect-once: %s", resultText(res))
		require.NotNil(t, deps.sink.last(), "the re-collect must reach the sink")
		assert.Equal(t, rawIdentityDocName, deps.sink.last().GraphName)
	})

	t.Run("an_unrecorded_source_is_not_a_different_source", func(t *testing.T) {
		path := copyFixturePDF(t)
		// A root carrying NO path key: a graph collected before its family
		// recorded a source. Absence means nothing is known, never that it differs.
		deps := newRawIdentityDeps(oneGraphRawCatalog(rawIdentityDocName, "document", "", ""))

		res := runCollect(t, deps, map[string]any{"type": "pdf", "id": path})
		require.False(t, res.IsError,
			"a graph whose source was never recorded must still re-collect; refusing there would "+
				"make every legacy graph permanently un-re-collectable: %s", resultText(res))
		require.NotNil(t, deps.sink.last())
	})

	t.Run("the_older_suffixed_graph_is_reported_never_dropped", func(t *testing.T) {
		path := copyFixturePDF(t)
		const legacy = rawIdentityDocName + "-3f7a1b2c"
		fake := emptyRawCatalog()
		fake.catalog = []*knowledgev1.GraphInfo{{Name: legacy, Loaded: true}}
		deps := newRawIdentityDeps(fake)

		res := runCollect(t, deps, map[string]any{"type": "pdf", "id": path})
		body := resultText(res)
		require.False(t, res.IsError, "an older suffixed graph must not block the collect: %s", body)

		assert.Contains(t, body, legacy, "the answer must name the older suffixed graph")
		assert.Contains(t, body, fmt.Sprintf("%q,\"name\":%q", "pdf", legacy),
			"the answer must carry the drop call for the older graph")
		assert.Contains(t, body, "nothing was dropped",
			"the notice must state that the older graph was left alone")
		require.NotNil(t, deps.sink.last(), "the new graph is still written")
		assert.Equal(t, rawIdentityDocName, deps.sink.last().GraphName,
			"the collect lands under the NEW name; the old graph is reported, never renamed")
	})

	t.Run("an_unreadable_catalog_refuses_rather_than_admitting", func(t *testing.T) {
		path := copyFixturePDF(t)
		fake := emptyRawCatalog()
		fake.catalogErr = fmt.Errorf("catalog unreachable")
		deps := newRawIdentityDeps(fake)

		res := runCollect(t, deps, map[string]any{"type": "pdf", "id": path})
		body := resultText(res)
		assert.True(t, res.IsError,
			"a FAILED catalog read must refuse, never read as an all-clear: %s", body)
		assert.Contains(t, body, "catalog unreachable", "the refusal must surface the read failure")
		assert.Nil(t, deps.sink.last(), "nothing may be written when the target could not be verified")
	})
}

// TestCollectWeb_NamedAfterItsSeedHost is the web half: the name an unnamed
// crawl lands under, the explicit name that survives it, and the same refusal
// keyed on the crawl's seed host instead of a file path.
func TestCollectWeb_NamedAfterItsSeedHost(t *testing.T) {
	newSite := func(t *testing.T) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><head><title>Seed</title></head><body>" +
				"<h1>Seed</h1><p>One paragraph of prose for the emitter to keep.</p></body></html>"))
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	t.Run("an_unnamed_collect_is_named_after_the_seed_host", func(t *testing.T) {
		srv := newSite(t)
		deps := newRawIdentityDeps(emptyRawCatalog())

		// NO id at all — the payload carries only seed_urls.
		res := runCollect(t, deps, map[string]any{
			"type": "web", "seed_urls": []string{srv.URL}, "max_pages": 1,
		})
		require.False(t, res.IsError, "a web collect with seeds and no id must be accepted: %s", resultText(res))

		got := deps.sink.last()
		require.NotNil(t, got, "the crawl must have reached the sink")
		assert.Equal(t, "127-0-0-1", got.GraphName,
			"an unnamed web collect is named after its seed host, dots mapped to hyphens")

		require.NotEmpty(t, got.Nodes)
		md := got.Nodes[0].GetMetadata()
		assert.Equal(t, "127.0.0.1", md["seed_host"],
			"the page root must record the crawl's seed host — the value the refusal compares")
		assert.Equal(t, "127-0-0-1", md["source_name"],
			"the page root must record the graph name the crawl landed under")
	})

	t.Run("an_explicit_name_wins", func(t *testing.T) {
		srv := newSite(t)
		deps := newRawIdentityDeps(emptyRawCatalog())

		res := runCollect(t, deps, map[string]any{
			"type": "web", "id": "hohpe-eip", "seed_urls": []string{srv.URL}, "max_pages": 1,
		})
		require.False(t, res.IsError, "an explicitly named web collect must be accepted: %s", resultText(res))

		got := deps.sink.last()
		require.NotNil(t, got)
		assert.Equal(t, "hohpe-eip", got.GraphName,
			"an explicit name is used VERBATIM: never sanitized, never replaced by the derived one")
		require.NotEmpty(t, got.Nodes)
		assert.Equal(t, "hohpe-eip", got.Nodes[0].GetMetadata()["source_name"])
	})

	t.Run("a_different_site_under_the_same_name_is_refused", func(t *testing.T) {
		srv := newSite(t)
		// The occupying graph records a DIFFERENT seed host under the name this
		// local crawl derives.
		deps := newRawIdentityDeps(oneGraphRawCatalog("127-0-0-1", "page", "seed_host", "example.com"))

		res := runCollect(t, deps, map[string]any{
			"type": "web", "seed_urls": []string{srv.URL}, "max_pages": 1,
		})
		body := resultText(res)
		assert.True(t, res.IsError, "a different site under an occupied name must be REFUSED: %s", body)
		assert.Contains(t, body, "example.com", "the refusal must name the host already recorded")
		assert.Contains(t, body, "127.0.0.1", "the refusal must name the host being collected")
		assert.Nil(t, deps.sink.last(), "nothing may be crawled: the refusal runs before the walk")
	})

	t.Run("no_name_and_no_seed_is_bad_input", func(t *testing.T) {
		deps := newRawIdentityDeps(emptyRawCatalog())
		res := runCollect(t, deps, map[string]any{"type": "web"})
		body := resultText(res)
		assert.True(t, res.IsError,
			"a web collect with neither an id nor a seed URL names nothing and must error: %s", body)
		assert.Nil(t, deps.sink.last())
	})
}

// TestPrecheckRawCollect_CostsOneCatalogReadAndAtMostOneRootRead is the perf
// gate on the pre-walk check. It exists because the two expensive
// implementations are BEHAVIORALLY IDENTICAL to the cheap one: draining the
// target graph to find its root, or re-enumerating the catalog per candidate,
// both produce exactly the right answers. Only the read shape separates them,
// and the fake counts drain-shaped reads structurally rather than by total.
func TestPrecheckRawCollect_CostsOneCatalogReadAndAtMostOneRootRead(t *testing.T) {
	t.Run("first_collect_reads_the_catalog_and_no_graph", func(t *testing.T) {
		fake := emptyRawCatalog()
		deps := newRawIdentityDeps(fake)

		legacy, err := precheckRawCollect(context.Background(), deps, "pdf", rawIdentityDocName, "/docs/"+rawIdentityDocName+".pdf")
		require.NoError(t, err)
		assert.Empty(t, legacy)

		assert.Equal(t, 1, fake.catalogReads, "exactly one catalog enumeration")
		assert.Equal(t, 0, fake.rootReads, "a name nothing occupies costs no root read")
		assert.Equal(t, 0, fake.drainPages, "the target graph is never drained")
		assert.Equal(t, 1, fake.totalExecs,
			"the total must equal the sum of the named reads, so no other wire read hides inside the check")
	})

	t.Run("re_collect_adds_exactly_one_root_read", func(t *testing.T) {
		path := "/docs/" + rawIdentityDocName + ".pdf"
		fake := oneGraphRawCatalog(rawIdentityDocName, "document", "path", path)
		deps := newRawIdentityDeps(fake)

		legacy, err := precheckRawCollect(context.Background(), deps, "pdf", rawIdentityDocName, path)
		require.NoError(t, err)
		assert.Empty(t, legacy)

		assert.Equal(t, 1, fake.catalogReads, "still exactly one catalog enumeration")
		assert.Equal(t, 1, fake.rootReads, "an occupied name costs exactly one Limit-1 root read")
		assert.Equal(t, 0, fake.drainPages, "the target graph is never drained")
		assert.Equal(t, 2, fake.totalExecs,
			"the total must equal the sum of the named reads")
	})
}
