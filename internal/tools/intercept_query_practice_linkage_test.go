// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// fanOutSegmentSearcher is a fan-out-aware SegmentSearcher: it returns a DISTINCT
// hits slice per graph name (so a merged render can be checked for per-graph
// attribution) and records the set of names it was asked to search plus per-name
// call counts for assertions. The existing fakeSegmentSearcher only records the
// LAST name, which cannot prove a scatter-gather hit every enumerated graph.
type fanOutSegmentSearcher struct {
	mu       sync.Mutex
	hitsByGr map[string][]searchengine.Hit
	calls    map[string]int
}

func newFanOutSegmentSearcher(hitsByGraph map[string][]searchengine.Hit) *fanOutSegmentSearcher {
	return &fanOutSegmentSearcher{hitsByGr: hitsByGraph, calls: map[string]int{}}
}

func (f *fanOutSegmentSearcher) Search(
	_ context.Context, _ kgtypes.GraphType, name, _ string, _ []byte, _ int,
) ([]searchengine.Hit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
	return f.hitsByGr[name], nil
}

// searchedNames returns the sorted set of graph names Search was invoked on.
func (f *fanOutSegmentSearcher) searchedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for n := range f.calls {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// callCount returns how many times Search was invoked for a given graph name.
func (f *fanOutSegmentSearcher) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

// plFake routes Execute by plan shape (search vs by-id vs Match) and Stats by
// canned graph stats, for the practice/linkage composer tests.
type plFake struct {
	searchResults []engine.SearchResult
	byIDNode      *knowledgev1.Node
	matchNodes    []knowledgev1.Node
	stats         *knowledgev1.GraphStats
}

func (f *plFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	switch {
	case len(q.GetQueries()) > 0:
		return &knowledgev1.ExecuteResponse{SearchResults: searchResultsToProtoForTest(f.searchResults)}, nil
	case q.GetById() != "":
		var nodes []*knowledgev1.Node
		if f.byIDNode != nil {
			nodes = []*knowledgev1.Node{f.byIDNode}
		}
		resp := enginetest.ResponseWithNodes(nodes...)
		return resp, nil
	default:
		resp := enginetest.ResponseWithNodes(nodePtrs(f.matchNodes)...)
		return resp, nil
	}
}

func (f *plFake) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{GraphStats: f.stats}, nil
}

// TestPracticeRoute_StatsAndSearch covers practice mode=stats and search.
func TestPracticeRoute_StatsAndSearch(t *testing.T) {
	t.Run("stats", func(t *testing.T) {
		f := &plFake{stats: &knowledgev1.GraphStats{NodeCount: 9, EdgeCount: 1, NodesByType: map[string]int64{"pattern": 9}}}
		res := gatedRoutePractice(opCtx(), nil, f, queryArgs{Graph: "practice", Language: "go", Mode: "stats"})
		body := textBodyTools(res)
		assert.Contains(t, body, "## Practice Graph: go")
		assert.Contains(t, body, "Nodes: 9")
	})

	t.Run("search renders Best Practices shape", func(t *testing.T) {
		// practice search is served UNCONDITIONALLY by the CLIENT engine
		// (Manager.Search → RRF → hydrate → RenderPracticeResults). The hydrate
		// ids[] read is served by the harness GraphClient; the ranked node is the
		// canned RETURN_MODE_NODES reply.
		var execHits atomic.Int64
		gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
			&knowledgev1.Node{Id: "p1", SymbolName: "Use errgroup", Content: "do x", Status: "active",
				Metadata: map[string]string{"category": "concurrency", "importance": "high"}},
		))
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "p1", Score: 0.88}}}
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Language: "go", Text: "errgroup"})
		body := textBodyTools(res)
		assert.Equal(t, int64(1), mgr.calls.Load(), "practice search drove the CLIENT engine")
		assert.Equal(t, kgtypes.GraphPractice, mgr.lastGT)
		assert.Equal(t, "go", mgr.lastName, "practice engine keyed on language")
		assert.False(t, dispatchedAServerSearch(handler.recordedReqs()), "practice search must NOT dispatch a server search")
		assert.Contains(t, body, "## Go Best Practices — 1 results for \"errgroup\"")
		assert.Contains(t, body, "### 1. Use errgroup [high] (concurrency)")
		assert.Contains(t, body, "ID: p1 | Status: active")
	})
}

// TestLinkageRoute_AllShapes covers linkage stats, id getNode, and search (proxy
// annotation reuse).
func TestLinkageRoute_AllShapes(t *testing.T) {
	t.Run("stats with proxy breakdown", func(t *testing.T) {
		f := &plFake{
			stats: &knowledgev1.GraphStats{NodeCount: 4, EdgeCount: 2, NodesByType: map[string]int64{"proxy": 4}},
			matchNodes: []knowledgev1.Node{
				{Id: "x1", Type: string(kgtypes.NodeProxy), Metadata: map[string]string{"foreign_graph": "code"}},
				{Id: "x2", Type: string(kgtypes.NodeProxy), Metadata: map[string]string{"foreign_graph": "cloud"}},
			},
		}
		res := gatedRouteLinkage(opCtx(), f, queryArgs{Graph: "linkage", Mode: "stats"})
		body := textBodyTools(res)
		assert.Contains(t, body, "## Linkage Graph")
		assert.Contains(t, body, "### Proxy Breakdown")
		assert.Contains(t, body, "- cloud: 1 proxies")
		assert.Contains(t, body, "- code: 1 proxies")
	})

	t.Run("id getNode", func(t *testing.T) {
		n := knowledgev1.Node{Id: "proxy:code:foo", SymbolName: "Foo", Type: string(kgtypes.NodeProxy)}
		f := &plFake{byIDNode: &n}
		res := gatedRouteLinkage(opCtx(), f, queryArgs{Graph: "linkage", ID: "proxy:code:foo"})
		assert.Contains(t, textBodyTools(res), "## linkage node")
	})

	t.Run("ranked text search RETIRED", func(t *testing.T) {
		// a text-only linkage query returns the ranked-search-retired
		// result and dispatches NO server search (the index-free ops still work).
		f := &plFake{}
		res := gatedRouteLinkage(opCtx(), f, queryArgs{Graph: "linkage", Text: "bar"})
		body := textBodyTools(res)
		assert.Contains(t, body, "retired", "linkage ranked search is retired")
		assert.Contains(t, body, "linkage", "the retired message names the graph")
		assert.NotContains(t, body, "results for", "no ranked result list is rendered")
	})
}

// TestPracticeStats_JSON asserts the practice mode=stats format:"json" arm
// returns the structured shape with graph=practice + language + counts + maps,
// driven through routePracticeClient. Text path stays covered by
// TestPracticeRoute_StatsAndSearch.
func TestPracticeStats_JSON(t *testing.T) {
	f := &plFake{stats: &knowledgev1.GraphStats{
		NodeCount: 9, EdgeCount: 1, BinaryVectorCount: 3,
		NodesByType: map[string]int64{"pattern": 9},
		EdgesByType: map[string]int64{"relates-to": 1},
	}}
	res := gatedRoutePractice(opCtx(), nil, f, queryArgs{Graph: "practice", Language: "go", Mode: "stats", Format: "json"})
	require.False(t, res.IsError, textBodyTools(res))

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(res)), &payload), "body must be valid JSON")
	assert.Equal(t, "practice", payload["graph"])
	assert.Equal(t, "go", payload["language"])
	assert.EqualValues(t, 9, payload["node_count"])
	assert.EqualValues(t, 1, payload["edge_count"])
	assert.EqualValues(t, 3, payload["binary_vector_count"])
	nbt, ok := payload["nodes_by_type"].(map[string]any)
	require.True(t, ok, "nodes_by_type is an object")
	assert.EqualValues(t, 9, nbt["pattern"])
	ebt, ok := payload["edges_by_type"].(map[string]any)
	require.True(t, ok, "edges_by_type is an object")
	assert.EqualValues(t, 1, ebt["relates-to"])
}

// TestLinkageStats_JSON asserts the linkage mode=stats format:"json" arm returns
// the structured shape with graph=linkage (no instance key) + counts + maps, and
// that the markdown-only proxy-by-foreign_graph breakdown is ABSENT from JSON.
// Driven through routeLinkageClient (the production entry that threads a.Format
// into linkageStatsClient) so the format arg is exercised end-to-end. Text path
// stays covered by TestLinkageRoute_AllShapes.
func TestLinkageStats_JSON(t *testing.T) {
	f := &plFake{
		stats: &knowledgev1.GraphStats{
			NodeCount: 4, EdgeCount: 2, BinaryVectorCount: 0,
			NodesByType: map[string]int64{"proxy": 4},
			EdgesByType: map[string]int64{"links-to": 2},
		},
		matchNodes: []knowledgev1.Node{
			{Id: "x1", Type: string(kgtypes.NodeProxy), Metadata: map[string]string{"foreign_graph": "code"}},
			{Id: "x2", Type: string(kgtypes.NodeProxy), Metadata: map[string]string{"foreign_graph": "cloud"}},
		},
	}
	res := gatedRouteLinkage(opCtx(), f, queryArgs{Graph: "linkage", Mode: "stats", Format: "json"})
	require.False(t, res.IsError, textBodyTools(res))

	body := textBodyTools(res)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &payload), "body must be valid JSON")
	assert.Equal(t, "linkage", payload["graph"])
	assert.NotContains(t, payload, "name", "linkage omits the instance key")
	assert.EqualValues(t, 4, payload["node_count"])
	assert.EqualValues(t, 2, payload["edge_count"])
	nbt, ok := payload["nodes_by_type"].(map[string]any)
	require.True(t, ok, "nodes_by_type is an object")
	assert.EqualValues(t, 4, nbt["proxy"])
	ebt, ok := payload["edges_by_type"].(map[string]any)
	require.True(t, ok, "edges_by_type is an object")
	assert.EqualValues(t, 2, ebt["links-to"])
	// The proxy-by-foreign_graph breakdown is markdown-only — never in JSON.
	assert.NotContains(t, payload, "proxy_breakdown")
	assert.NotContains(t, body, "Proxy Breakdown", "proxy breakdown stays markdown-only")
}

// TestRouteWebPDF_RetiresRankedTextPassesIndexFreeOps is the Phase 3
// web/pdf criterion: a ranked-text query (text or queries[], no id/specialized
// mode) returns the retired result; every index-free op (by-id, type-browse,
// mode=stats, mode=modules) falls through unhandled to the engineDispatch path.
func TestRouteWebPDF_RetiresRankedTextPassesIndexFreeOps(t *testing.T) {
	for _, graph := range []string{"web", "pdf"} {
		t.Run(graph+" ranked text retired", func(t *testing.T) {
			for _, args := range []queryArgs{
				{Graph: graph, Text: "x"},
				{Graph: graph, Queries: []string{"x"}},
				{Graph: graph, Mode: "text", Text: "x"},
			} {
				handled, res := gatedRouteWebPDF(args)
				require.True(t, handled, "%s ranked text is claimed (retired)", graph)
				body := textBodyTools(res)
				assert.Contains(t, body, "retired")
				assert.Contains(t, body, graph)
			}
		})
		t.Run(graph+" index-free ops fall through", func(t *testing.T) {
			for _, args := range []queryArgs{
				{Graph: graph, ID: "n1"},        // by-id getNode
				{Graph: graph, Type: "finding"}, // type-browse
				{Graph: graph, Mode: "stats"},   // stats
				{Graph: graph, Mode: "modules"}, // list-graphs
				{Graph: graph},                  // bare
			} {
				handled, _ := gatedRouteWebPDF(args)
				assert.False(t, handled, "%s index-free op %+v must fall through to engineDispatch", graph, args)
			}
		})
	}
}

// TestInterceptQueryPracticeLinkage_WebPDFClaim asserts the top-level intercept
// routes web/pdf ranked text through routeWebPDFClient (claimed) and lets their
// index-free ops fall through (handled=false).
func TestInterceptQueryPracticeLinkage_WebPDFClaim(t *testing.T) {
	handled, res := InterceptQueryPracticeLinkage(opCtx(), nil, kgtools.CallToolParams{
		Name: "query", Arguments: json.RawMessage(`{"graph":"web","text":"x"}`),
	})
	require.True(t, handled, "web ranked text is claimed + retired")
	assert.Contains(t, textBodyTools(res), "retired")

	handled, _ = InterceptQueryPracticeLinkage(opCtx(), nil, kgtools.CallToolParams{
		Name: "query", Arguments: json.RawMessage(`{"graph":"pdf","id":"n1"}`),
	})
	assert.False(t, handled, "pdf by-id getNode falls through to engineDispatch")
}

// TestInterceptQueryPracticeLinkage_Gate asserts the intercept claims only
// practice/linkage and falls through (false) for other graphs/tools.
func TestInterceptQueryPracticeLinkage_Gate(t *testing.T) {
	handled, _ := InterceptQueryPracticeLinkage(opCtx(), nil, kgtools.CallToolParams{Name: "search", Arguments: json.RawMessage(`{}`)})
	assert.False(t, handled, "non-query tool not claimed")
}
