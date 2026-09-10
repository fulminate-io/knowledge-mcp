// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestSearchJSONCarriesSourceGraph_AllFamilies is the source-graph-identity test of record: it
// asserts the UNIVERSAL contract with NO exclusions — every json-emitting search
// family stamps a NON-EMPTY, CORRECT source graph (and the right per-result
// instance) on each SearchJSONResult it renders. The graph-UI uses this stamp to
// traverse each result in ITS OWN graph; before the fix every non-knowledge
// family rendered an absent graph and the UI fell back to the single dropdown
// selector, so practice fan-out (instance varies PER HIT) returned zero traverse
// expansion.
//
// RED on current working-tree HEAD: SearchJSONResult carries no Graph/GraphInstance
// field and no compose path stamps one, so this file fails to COMPILE (the
// env.Results[i].Graph field reference is undefined) — the red of red-green.
//
// Reuses the existing per-family fakes verbatim: knowledge/recent/code-single/
// registered custom → newInterceptHarness + cannedNodesResp + fakeSegmentSearcher;
// code-multi + practice (single + fan-out) → newFanOutHarness +
// newFanOutSegmentSearcher + practiceNode; logs → newSearchHandler +
// testSearchQueryID.
func TestSearchJSONCarriesSourceGraph_AllFamilies(t *testing.T) {
	// parseEnv decodes a rendered json ToolResult into the SearchJSONResponse
	// envelope, failing the test loudly when the body is not the json shape.
	parseEnv := func(t *testing.T, body string) engine.SearchJSONResponse {
		t.Helper()
		var env engine.SearchJSONResponse
		require.NoError(t, json.Unmarshal([]byte(body), &env), "json branch must parse to SearchJSONResponse; body=%s", body)
		return env
	}

	t.Run("knowledge-search", func(t *testing.T) {
		var execHits, embedCalls atomic.Int64
		gc := newInterceptHarness(t, &execHits, cannedNodesResp(
			&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit"},
		))
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "n1", Score: 0.9}}}
		deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"query": "x", "graph": "knowledge", "format": "json",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, engine.FirstTextContent(out))
		env := parseEnv(t, engine.FirstTextContent(out))
		require.Len(t, env.Results, 1)
		assert.Equal(t, "knowledge", env.Results[0].Graph, "knowledge search stamps graph=knowledge")
		assert.Empty(t, env.Results[0].GraphInstance, "knowledge default instance is empty")
	})

	t.Run("knowledge-recent", func(t *testing.T) {
		var execHits atomic.Int64
		// composeRecentBrowse is a pure temporal BROWSE: it issues a RETURN_MODE_NODES
		// Execute (no segment Manager) and reranks. The intercept harness serves the
		// canned nodes for that browse read.
		gc := newInterceptHarness(t, &execHits, cannedNodesResp(
			&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "RecentHit", UpdatedAt: 1},
		))
		mgr := &fakeSegmentSearcher{}
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		// Bare recent (empty text) → composeRecentBrowse. format:json drives the json arm.
		handled, out := InterceptQueryKnowledgeSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "knowledge", "mode": "recent", "format": "json",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, engine.FirstTextContent(out))
		env := parseEnv(t, engine.FirstTextContent(out))
		require.Len(t, env.Results, 1)
		assert.Equal(t, "knowledge", env.Results[0].Graph, "recent browse stamps graph=knowledge")
		assert.Empty(t, env.Results[0].GraphInstance)
	})

	t.Run("code-single-repo", func(t *testing.T) {
		var execHits atomic.Int64
		gc := newInterceptHarness(t, &execHits, cannedNodesResp(
			&knowledgev1.Node{Id: "f.go:Foo", SymbolName: "Foo", Type: "function", FilePath: "f.go", StartLine: 1},
		))
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "f.go:Foo", Score: 0.9}}}
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		handled, res := interceptSearchCode(opCtx(), deps, gc.Execute,
			json.RawMessage(`{"graph":"code","query":"foo","repo":"knowledge","format":"json"}`))
		require.True(t, handled)
		require.False(t, res.IsError, textBodyTools(res))
		env := parseEnv(t, textBodyTools(res))
		require.Len(t, env.Results, 1)
		assert.Equal(t, "code", env.Results[0].Graph, "code search stamps graph=code")
		assert.Equal(t, "knowledge", env.Results[0].GraphInstance,
			"single-repo code stamps the request repo as the instance (the :311 a.Repo fix)")
	})

	t.Run("code-multi-repo", func(t *testing.T) {
		// repo:"all" → composeCodeSearchMultiRepo. The fan-out harness enumerates the
		// code graphs (RETURN_MODE_GRAPH_NAMES) and serves the per-repo ids[] hydrate;
		// the fan-out segment searcher dispatches by name == repo, so each repo
		// surfaces its OWN node. Each result must carry its OWN repo as the instance.
		// Empty temp manifest: the fan-out detects each repo's branch from the
		// machine-local manifest, and this subtest is about per-result graph
		// identity. Pinning both repos to the no-entry state keeps them on the
		// single-pool path regardless of what this developer has collected.
		withTestManifest(t)
		gc := newFanOutHarness(t, []string{"repoA", "repoB"},
			&knowledgev1.Node{Id: "a.go:A", SymbolName: "A", Type: "function", FilePath: "a.go", StartLine: 1},
			&knowledgev1.Node{Id: "b.go:B", SymbolName: "B", Type: "function", FilePath: "b.go", StartLine: 1},
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"repoA": {{ID: "a.go:A", Score: 0.90}},
			"repoB": {{ID: "b.go:B", Score: 0.70}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		handled, res := interceptSearchCode(opCtx(), deps, gc.Execute,
			json.RawMessage(`{"graph":"code","query":"x","repo":"all","format":"json"}`))
		require.True(t, handled)
		require.False(t, res.IsError, textBodyTools(res))
		env := parseEnv(t, textBodyTools(res))
		require.Len(t, env.Results, 2, "both repos' hits flatten into the json envelope")
		byID := map[string]engine.SearchJSONResult{}
		for _, r := range env.Results {
			assert.Equal(t, "code", r.Graph, "multi-repo code stamps graph=code on every result")
			byID[r.ID] = r
		}
		// Per-result instance VARIES: each result carries its own source repo.
		assert.Equal(t, "repoA", byID["a.go:A"].GraphInstance, "repoA hit carries repoA instance")
		assert.Equal(t, "repoB", byID["b.go:B"].GraphInstance, "repoB hit carries repoB instance")
	})

	// THE RESOURCE-GRAPH ROW MOVED TO THE REGISTERED-CUSTOM ARM. It read a cicd
	// graph through the per-account resource composer, and both went with that
	// family; a contrib collector's inventory graph is a registered custom type
	// now, so the identity stamp it must carry is graph=<registration name> with
	// the graph NAME as the instance.
	t.Run("registered-custom", func(t *testing.T) {
		const customFamily = "acme-ci"
		var embedCalls atomic.Int64
		gc := newFanOutHarness(t, []string{"acct"},
			&knowledgev1.Node{
				Id:         "res-1",
				SymbolName: "my-resource",
				Type:       "cicd-resource",
				Metadata:   map[string]string{"resource_type": "workflow"},
			},
		)
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "res-1", Score: 0.8}}}
		deps := &interceptDeps{
			gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr,
			gtCRUD: registeredGraphTypes(customFamily),
		}

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": customFamily, "name": "acct", "text": "bucket", "format": "json",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, engine.FirstTextContent(out))
		env := parseEnv(t, engine.FirstTextContent(out))
		require.Len(t, env.Results, 1)
		assert.Equal(t, customFamily, env.Results[0].Graph, "a registered-custom search stamps its own family")
		assert.Equal(t, "acct", env.Results[0].GraphInstance, "and the graph name as the instance")
	})

	t.Run("practice-single", func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"go"},
			practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go": {{ID: "p:go", Score: 0.90}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Language: "go", Text: "pool", Format: "json"})
		env := parseEnv(t, textBodyTools(res))
		require.Len(t, env.Results, 1)
		assert.Equal(t, "practice", env.Results[0].Graph, "practice single stamps graph=practice")
		assert.Equal(t, "go", env.Results[0].GraphInstance, "practice single stamps the language as instance")
	})

	t.Run("practice-no-selector-stamps-the-family-alone", func(t *testing.T) {
		// The per-hit-varying instance case retired with the fan-out that produced
		// it: results came from N graphs and each had to carry its own. An
		// unselected practice search reads ONE graph, so every result carries the
		// family and no instance — and asserting the EMPTY instance is what would
		// catch a render that started stamping a stale language onto combined-graph
		// rows.
		gc := newFanOutHarness(t, []string{"default"},
			practiceNode("p:a", "GoWorkerPool", "bounded goroutines"),
			practiceNode("p:b", "PyThreadPool", "thread pool executor"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"default": {{ID: "p:a", Score: 0.90}, {ID: "p:b", Score: 0.70}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Text: "pool", Format: "json"})
		env := parseEnv(t, textBodyTools(res))
		require.Len(t, env.Results, 2)
		for _, r := range env.Results {
			assert.Equal(t, "practice", r.Graph, "every result stamps graph=practice")
			assert.Empty(t, r.GraphInstance,
				"an unselected practice read names no instance, because there is one graph")
		}
	})
}
