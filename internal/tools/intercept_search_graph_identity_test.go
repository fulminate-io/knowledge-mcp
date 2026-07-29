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
// cloud/cicd → newInterceptHarness + cannedNodesResp + fakeSegmentSearcher;
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

	for _, tc := range []struct {
		graph string
	}{
		{"cloud"},
		{"cicd"},
	} {
		t.Run(tc.graph, func(t *testing.T) {
			var execHits, embedCalls atomic.Int64
			gc := newInterceptHarness(t, &execHits, cannedNodesResp(
				&knowledgev1.Node{
					Id:         "res-1",
					SymbolName: "my-resource",
					Type:       "cloud_resource",
					Metadata:   map[string]string{"resource_type": "s3:bucket"},
				},
			))
			mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "res-1", Score: 0.8}}}
			deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

			handled, out := InterceptQueryCloudCICD(opCtx(), deps, queryParams(t, map[string]any{
				"graph": tc.graph, "account": "acct", "text": "bucket", "format": "json",
			}))
			require.True(t, handled)
			require.False(t, out.IsError, engine.FirstTextContent(out))
			env := parseEnv(t, engine.FirstTextContent(out))
			require.Len(t, env.Results, 1)
			assert.Equal(t, tc.graph, env.Results[0].Graph, "%s search stamps graph=%s", tc.graph, tc.graph)
			assert.Equal(t, "acct", env.Results[0].GraphInstance, "%s stamps the account as instance", tc.graph)
		})
	}

	t.Run("practice-single", func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"go"},
			practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go": {{ID: "p:go", Score: 0.90}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		res := routePracticeClient(opCtx(), deps, gc,
			queryArgs{Graph: "practice", Language: "go", Text: "pool", Format: "json"})
		env := parseEnv(t, textBodyTools(res))
		require.Len(t, env.Results, 1)
		assert.Equal(t, "practice", env.Results[0].Graph, "practice single stamps graph=practice")
		assert.Equal(t, "go", env.Results[0].GraphInstance, "practice single stamps the language as instance")
	})

	t.Run("practice-fanout-per-hit-varying", func(t *testing.T) {
		// The case that was actually broken: instance VARIES per hit. Each merged
		// fan-out result must carry its OWN source language, not a single selector.
		gc := newFanOutHarness(t, []string{"go", "python"},
			practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
			practiceNode("p:py", "PyThreadPool", "thread pool executor"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go":     {{ID: "p:go", Score: 0.90}},
			"python": {{ID: "p:py", Score: 0.70}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		res := routePracticeClient(opCtx(), deps, gc,
			queryArgs{Graph: "practice", Language: "all", Text: "pool", Format: "json"})
		env := parseEnv(t, textBodyTools(res))
		require.Len(t, env.Results, 2)
		byID := map[string]engine.SearchJSONResult{}
		for _, r := range env.Results {
			assert.Equal(t, "practice", r.Graph, "every fan-out result stamps graph=practice")
			byID[r.ID] = r
		}
		assert.Equal(t, "go", byID["p:go"].GraphInstance, "go hit carries its OWN language instance")
		assert.Equal(t, "python", byID["p:py"].GraphInstance, "python hit carries its OWN language instance")
	})

	t.Run("logs", func(t *testing.T) {
		res := newSearchHandler(t).searchLogs(opCtx(), searchArgs{
			Graph: "logs", Name: testSearchQueryID, Query: "connection timeout", Limit: 10, Format: "json",
		})
		require.False(t, res.IsError, resultText(res))
		env := parseEnv(t, resultText(res))
		require.NotEmpty(t, env.Results)
		assert.Equal(t, "logs", env.Results[0].Graph, "logs search stamps graph=logs")
		assert.Equal(t, testSearchQueryID, env.Results[0].GraphInstance, "logs stamps the queryID name as instance")
	})
}
