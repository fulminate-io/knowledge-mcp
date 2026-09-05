// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// fanOutEngineHandler serves the two wire shapes the practice fan-out drives:
//   - RETURN_MODE_GRAPH_NAMES  → the set of practice graph names (graphNames)
//   - ids[] hydrate read       → the seeded nodes whose ids match the request
//
// It keys the hydrate reply by node id (each per-graph hit has a unique id), so a
// per-graph hydrate (Target.Language=<graph>) returns exactly that graph's node.
type fanOutEngineHandler struct {
	graphNames []string
	nodesByID  map[string]*knowledgev1.Node
	// stats, when set, is what Stats answers. Nil (the zero value) keeps the
	// Unimplemented reply every pre-existing fixture relies on — the segment-gap
	// tests are the only ones that need real node/vector counts behind the seam.
	stats *knowledgev1.GraphStats

	mu   sync.Mutex
	reqs []*knowledgev1.ExecuteRequest
}

func (h *fanOutEngineHandler) Check(
	_ context.Context, _ *connect.Request[knowledgev1.CheckRequest],
) (*connect.Response[knowledgev1.CheckResponse], error) {
	return connect.NewResponse(&knowledgev1.CheckResponse{}), nil
}

func (h *fanOutEngineHandler) Status(
	_ context.Context, _ *connect.Request[knowledgev1.StatusRequest],
) (*connect.Response[knowledgev1.StatusResponse], error) {
	return connect.NewResponse(&knowledgev1.StatusResponse{}), nil
}

func (h *fanOutEngineHandler) Execute(
	_ context.Context, req *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	h.mu.Lock()
	h.reqs = append(h.reqs, req.Msg)
	h.mu.Unlock()
	q := req.Msg.GetQuery()
	if q != nil && q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		infos := make([]*knowledgev1.GraphInfo, 0, len(h.graphNames))
		for _, n := range h.graphNames {
			infos = append(infos, &knowledgev1.GraphInfo{Name: n})
		}
		return connect.NewResponse(&knowledgev1.ExecuteResponse{GraphNames: infos}), nil
	}

	// ids[] hydrate read: return the seeded nodes whose ids were requested.
	var nodes []*knowledgev1.Node
	if q != nil {
		for _, id := range q.GetIds() {
			if n, ok := h.nodesByID[id]; ok {
				nodes = append(nodes, n)
			}
		}
	}
	return connect.NewResponse(&knowledgev1.ExecuteResponse{Nodes: nodes}), nil
}

func (h *fanOutEngineHandler) Stats(
	context.Context, *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	if h.stats == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, nil)
	}
	return connect.NewResponse(&knowledgev1.StatsResponse{GraphStats: h.stats}), nil
}

func (h *fanOutEngineHandler) MetadataStats(
	context.Context, *connect.Request[knowledgev1.MetadataStatsRequest],
) (*connect.Response[knowledgev1.MetadataStatsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *fanOutEngineHandler) Index(
	context.Context, *connect.Request[knowledgev1.IndexRequest],
) (*connect.Response[knowledgev1.IndexResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *fanOutEngineHandler) PipelineScan(
	context.Context, *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *fanOutEngineHandler) PipelineGenPoll(
	context.Context, *connect.Request[knowledgev1.PipelineGenPollRequest],
) (*connect.Response[knowledgev1.PipelineGenPollResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *fanOutEngineHandler) CorpusDelta(
	context.Context, *connect.Request[knowledgev1.CorpusDeltaRequest],
) (*connect.Response[knowledgev1.CorpusDeltaResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *fanOutEngineHandler) ExportGraph(
	context.Context, *connect.Request[knowledgev1.ExportGraphRequest],
) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *fanOutEngineHandler) OverwriteGraph(
	context.Context, *connect.Request[knowledgev1.OverwriteGraphRequest],
) (*connect.Response[knowledgev1.OverwriteGraphResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

// recordedReqs returns a copy of every ExecuteRequest the fan-out handler
// captured, taken under the lock so callers race neither with the per-graph
// fan-out goroutines nor the append.
func (h *fanOutEngineHandler) recordedReqs() []*knowledgev1.ExecuteRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*knowledgev1.ExecuteRequest, len(h.reqs))
	copy(out, h.reqs)
	return out
}

// newFanOutHarness wires a GraphClient at a fan-out handler seeded with the given
// practice graph names + hydrate nodes (keyed by id).
func newFanOutHarness(t *testing.T, graphNames []string, nodes ...*knowledgev1.Node) *graphclient.GraphClient {
	t.Helper()
	gc, _ := newFanOutHarnessWithHandler(t, graphNames, nodes...)
	return gc
}

// newFanOutHarnessWithHandler is newFanOutHarness but also returns the handler so
// callers can inspect the captured requests (recordedReqs) — e.g. to assert no
// server RETURN_MODE_SEARCH was dispatched by the client-served fan-out.
func newFanOutHarnessWithHandler(t *testing.T, graphNames []string, nodes ...*knowledgev1.Node) (*graphclient.GraphClient, *fanOutEngineHandler) {
	t.Helper()
	byID := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		byID[n.GetId()] = n
	}
	h := &fanOutEngineHandler{graphNames: graphNames, nodesByID: byID}

	mux := http.NewServeMux()
	hp, hh := knowledgev1connect.NewHealthServiceHandler(h)
	mux.Handle(hp, hh)
	ep, eh := knowledgev1connect.NewEngineServiceHandler(h)
	mux.Handle(ep, eh)

	h2s := &http2.Server{}
	srv := httptest.NewServer(h2c.NewHandler(mux, h2s))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	gc := graphclient.NewGraphClientForURL(srv.URL)
	t.Cleanup(gc.Close)
	return gc, h
}

// practiceNode builds a practice hit node with the importance/category metadata.
func practiceNode(id, name, content string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id:         id,
		SymbolName: name,
		Status:     "active",
		Content:    content,
		Metadata:   map[string]string{"importance": "high", "category": "concurrency"},
	}
}

// TestPracticeFanOut_MergesAttributedAcrossGraphs is the PRIMARY acceptance
// criterion: BOTH entry points — the SEARCH tool and the QUERY tool
// (Language:"all") — search every enumerated practice graph, and the rendered
// output interleaves hits from >=2 graphs, each annotated with its source graph
// and sorted by score.
//
// The SEARCH arm passes NO language. The search tool has no per-language scope
// and now REFUSES the param rather than accepting and ignoring it; fanning
// across every graph is its only behavior, so omitting it is the honest way to
// exercise the fan-out here. TestPracticeFanOut_SearchRefusesLanguage owns the
// refusal itself.
func TestPracticeFanOut_MergesAttributedAcrossGraphs(t *testing.T) {
	seed := func() (*graphclient.GraphClient, *fanOutSegmentSearcher) {
		gc := newFanOutHarness(t, []string{"go", "python"},
			practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
			practiceNode("p:py", "PyThreadPool", "thread pool executor"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go":     {{ID: "p:go", Score: 0.90}},
			"python": {{ID: "p:py", Score: 0.70}},
		})
		return gc, mgr
	}

	assertMerged := func(t *testing.T, body string, mgr *fanOutSegmentSearcher) {
		t.Helper()
		// Every enumerated graph was searched exactly once.
		assert.Equal(t, []string{"go", "python"}, mgr.searchedNames())
		assert.Equal(t, 1, mgr.callCount("go"))
		assert.Equal(t, 1, mgr.callCount("python"))
		// Header names both graphs.
		assert.Contains(t, body, "Searched 2 practice graphs (go, python)")
		// Both hits rendered, each tagged with its source graph.
		assert.Contains(t, body, "### 1. GoWorkerPool [high] (concurrency) — go")
		assert.Contains(t, body, "### 2. PyThreadPool [high] (concurrency) — python")
		// Score-desc order: the 0.90 go hit precedes the 0.70 python hit.
		assert.Less(t, strings.Index(body, "GoWorkerPool"), strings.Index(body, "PyThreadPool"))
	}

	t.Run("SEARCH tool", func(t *testing.T) {
		gc, mgr := seed()
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"graph": "practice", "query": "pool"}))
		require.True(t, handled)
		require.False(t, out.IsError, "result is not an error: %s", textBodyTools(out))
		assertMerged(t, textBodyTools(out), mgr)
	})

	t.Run("QUERY tool Language:all", func(t *testing.T) {
		gc, mgr := seed()
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Language: "all", Text: "pool"})
		assertMerged(t, textBodyTools(res), mgr)
	})
}

// TestPracticeSearch_JSONAndText covers the JSON contract for BOTH practice composers:
// composePracticeSearchClient (specific language, QUERY tool) and
// composePracticeSearchFanOut (language:"all"). format:"json" parses to the
// SearchJSONResponse envelope; the no-format run stays on the markdown path.
func TestPracticeSearch_JSONAndText(t *testing.T) {
	t.Run("single-language client json + text", func(t *testing.T) {
		seed := func() (*graphclient.GraphClient, *fanOutSegmentSearcher) {
			gc := newFanOutHarness(t, []string{"go"},
				practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
			)
			mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
				"go": {{ID: "p:go", Score: 0.90}},
			})
			return gc, mgr
		}

		gc, mgr := seed()
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		jsonRes := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Language: "go", Text: "pool", Format: "json"})
		var env engine.SearchJSONResponse
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(jsonRes)), &env), "json branch must parse")
		require.Equal(t, 1, env.Total)
		require.Len(t, env.Results, 1)
		assert.Equal(t, "p:go", env.Results[0].ID)
		assert.Equal(t, "GoWorkerPool", env.Results[0].SymbolName)

		gc2, mgr2 := seed()
		deps2 := &interceptDeps{gc: gc2, segMgr: mgr2}
		textRes := gatedRoutePractice(opCtx(), deps2, gc2, queryArgs{Graph: "practice", Language: "go", Text: "pool"})
		body := textBodyTools(textRes)
		assert.Contains(t, body, "GoWorkerPool", "text path renders RenderPracticeResults markdown")
		var env2 engine.SearchJSONResponse
		assert.Error(t, json.Unmarshal([]byte(body), &env2), "text path must not emit JSON")
	})

	t.Run("fan-out json + text", func(t *testing.T) {
		seed := func() (*graphclient.GraphClient, *fanOutSegmentSearcher) {
			gc := newFanOutHarness(t, []string{"go", "python"},
				practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
				practiceNode("p:py", "PyThreadPool", "thread pool executor"),
			)
			mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
				"go":     {{ID: "p:go", Score: 0.90}},
				"python": {{ID: "p:py", Score: 0.70}},
			})
			return gc, mgr
		}

		gc, mgr := seed()
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		jsonRes := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Language: "all", Text: "pool", Format: "json"})
		var env engine.SearchJSONResponse
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(jsonRes)), &env), "fan-out json branch must parse")
		require.Equal(t, 2, env.Total)
		// Score-desc merge: the 0.90 go hit precedes the 0.70 python hit.
		assert.Equal(t, "p:go", env.Results[0].ID)
		assert.Equal(t, "p:py", env.Results[1].ID)
		// Flat shape drops per-graph attribution (markdown-only) — no "Searched ... graphs" header.
		assert.NotContains(t, textBodyTools(jsonRes), "Searched")

		gc2, mgr2 := seed()
		deps2 := &interceptDeps{gc: gc2, segMgr: mgr2}
		textRes := gatedRoutePractice(opCtx(), deps2, gc2, queryArgs{Graph: "practice", Language: "all", Text: "pool"})
		body := textBodyTools(textRes)
		assert.Contains(t, body, "Searched 2 practice graphs (go, python)", "text path renders the fan-out markdown header")
		var env2 engine.SearchJSONResponse
		assert.Error(t, json.Unmarshal([]byte(body), &env2), "text path must not emit JSON")
	})
}

// TestPracticeFanOut_NoSilentZeroWhenMatchesExist asserts that with >=1 practice
// graph returning hits, a SEARCH does not return a false zero — including when
// some enumerated graphs match nothing.
func TestPracticeFanOut_NoSilentZeroWhenMatchesExist(t *testing.T) {
	t.Run("SEARCH renders seeded hits when one graph is empty", func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"go", "rust"},
			practiceNode("p:go", "GoPattern", "go content"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go":   {{ID: "p:go", Score: 0.81}},
			"rust": {}, // rust has no match — go still surfaces (no silent zero).
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"graph": "practice", "query": "x"}))
		require.True(t, handled)
		body := textBodyTools(out)
		assert.Contains(t, body, "Searched 2 practice graphs (go, rust)")
		assert.Contains(t, body, "### 1. GoPattern [high] (concurrency) — go")
	})

	t.Run("SEARCH omitted language fans out", func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"go"},
			practiceNode("p:go", "GoPattern", "go content"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go": {{ID: "p:go", Score: 0.81}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		// No "language" key → omitted-language SEARCH must fan out (not silent-0).
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"graph": "practice", "query": "x"}))
		require.True(t, handled)
		body := textBodyTools(out)
		assert.Equal(t, []string{"go"}, mgr.searchedNames())
		assert.Contains(t, body, "Searched 1 practice graphs (go)")
		assert.Contains(t, body, "GoPattern")
	})
}

// TestPracticeFanOut_SearchLanguageScopes pins the SEARCH tool's contract for
// `language`, which this file has now stated three different ways as the surface
// changed. The history is worth keeping because each version was correct at the
// time and the last one is the reason this test exists at all:
//
//	(1) ACCEPTED AND DROPPED — a caller asking for one language got results
//	    silently spanning all of them, with no signal the scoping had not applied.
//	(2) REFUSED as an unknown parameter — honest, because the tool declared no
//	    `language` and had no single-graph branch to route it to. That refusal
//	    surface is GONE: the schema now declares the param
//	    (firstclass_schema.go, SearchToolDef) and the practice arm now branches on
//	    it (intercept_search_reducible_graph.go, `case "practice"`).
//	(3) SCOPED — what this test now asserts.
//
// The subtest below is the SAME fixture as before, re-pointed at the new
// behaviour: two seeded graphs, so "scoped to one" is observable as "the other
// was never searched" rather than merely "no error".
//
// The QUERY tool's empty-language browse is unaffected and stays below.
func TestPracticeFanOut_SearchLanguageScopes(t *testing.T) {
	t.Run("SEARCH tool language:go searches ONLY go", func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"go", "python"},
			practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
			practiceNode("p:py", "PyThreadPool", "thread pool executor"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go":     {{ID: "p:go", Score: 0.90}},
			"python": {{ID: "p:py", Score: 0.70}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"graph": "practice", "language": "go", "query": "pool"}))
		require.True(t, handled)
		require.False(t, out.IsError, "language is declared and routed, no longer refused: %s", textBodyTools(out))
		// THE DISCRIMINATING ASSERTION, and the reason the fixture seeds two
		// graphs: a schema-only change would accept the param and still fan out,
		// turning the old loud refusal into a SILENT DROP — strictly worse. The
		// searched-name list is what tells those two apart.
		assert.Equal(t, []string{"go"}, mgr.searchedNames(),
			"a named language searches THAT graph and no other")
		assert.NotContains(t, textBodyTools(out), "PyThreadPool", "and returns no hit from the graph it did not search")
	})

	t.Run("QUERY tool empty language stays browse", func(t *testing.T) {
		// Empty language on the QUERY tool is the list-graphs BROWSE — it must NOT
		// fan out into a search. listPracticeGraphs enumerates + renders the browse.
		gc := newFanOutHarness(t, []string{"go", "python"})
		deps := &interceptDeps{gc: gc, segMgr: newFanOutSegmentSearcher(nil)}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice"})
		body := textBodyTools(res)
		assert.Contains(t, body, "Practice graphs (2)")
		assert.NotContains(t, body, "Searched", "empty-language QUERY is browse, not a fan-out search")
	})
}

// TestPracticeFanOut_NoLanguageSpansLanguageGraphs is the ticket acceptance
// criterion: a SEARCH with NO language key returns hits spanning >=2 language
// graphs (go AND python), each attributed to its source graph.
func TestPracticeFanOut_NoLanguageSpansLanguageGraphs(t *testing.T) {
	gc := newFanOutHarness(t, []string{"go", "python"},
		practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
		practiceNode("p:py", "PyThreadPool", "thread pool executor"),
	)
	mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
		"go":     {{ID: "p:go", Score: 0.90}},
		"python": {{ID: "p:py", Score: 0.70}},
	})
	deps := &interceptDeps{gc: gc, segMgr: mgr}
	// No "language" key at all — a single search fans across every language graph.
	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"graph": "practice", "query": "pool"}))
	require.True(t, handled)
	require.False(t, out.IsError, "result is not an error: %s", textBodyTools(out))
	body := textBodyTools(out)
	// Both language graphs were searched and both hits surfaced, each tagged.
	assert.Equal(t, []string{"go", "python"}, mgr.searchedNames())
	assert.Contains(t, body, "Searched 2 practice graphs (go, python)")
	assert.Contains(t, body, "### 1. GoWorkerPool [high] (concurrency) — go")
	assert.Contains(t, body, "### 2. PyThreadPool [high] (concurrency) — python")
	assert.Less(t, strings.Index(body, "GoWorkerPool"), strings.Index(body, "PyThreadPool"))
}

// TestSearchPractice_LanguageScopesToOneGraph (FAILS-WHEN-ABSENT) asserts BOTH
// halves of the gap, because either alone is a defect.
//
// A SCHEMA-ONLY change would turn a loud refusal into a SILENT DROP — the caller
// asks for one language, the tool accepts the param and fans out anyway. A
// ROUTING-ONLY change is unreachable: the undeclared-param sweep refuses the call
// before the arm runs. Leg 2 is what catches the first; leg 1 the second.
func TestSearchPractice_LanguageScopesToOneGraph(t *testing.T) {
	newDeps := func(t *testing.T) (*interceptDeps, *fanOutSegmentSearcher) {
		t.Helper()
		gc := newFanOutHarness(t, []string{"go", "python"},
			practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
			practiceNode("p:py", "PyThreadPool", "thread pool executor"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go":     {{ID: "p:go", Score: 0.90}},
			"python": {{ID: "p:py", Score: 0.70}},
		})
		return &interceptDeps{gc: gc, segMgr: mgr}, mgr
	}

	t.Run("SCHEMA: language is no longer refused as unknown", func(t *testing.T) {
		deps, _ := newDeps(t)
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "practice", "language": "go", "query": "pool",
		}))
		require.True(t, handled)
		assert.False(t, out.IsError, "the param is declared on SearchToolDef: %s", textBodyTools(out))
		assert.NotContains(t, textBodyTools(out), `unknown parameter "language"`)
	})

	t.Run("ROUTING: the supplied language scopes the search to that graph", func(t *testing.T) {
		// The leg that catches a schema-only change. Asserted on WHICH GRAPHS WERE
		// SEARCHED, not on the rendered hits: a fan-out would also surface the go
		// hit, so a result-only assertion is green against the silent drop.
		deps, mgr := newDeps(t)
		_, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "practice", "language": "go", "query": "pool",
		}))
		require.False(t, out.IsError, "%s", textBodyTools(out))
		assert.Equal(t, []string{"go"}, mgr.searchedNames(), "exactly the named graph was searched")
	})

	t.Run("DEFAULT PRESERVED: no language still fans across every loaded graph", func(t *testing.T) {
		// Both-directions cover, and it protects the documented silent-zero defense:
		// the fan-out is what stops mgr.Search(GraphPractice,"all",…) returning a
		// confident empty result.
		deps, mgr := newDeps(t)
		_, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "practice", "query": "pool",
		}))
		require.False(t, out.IsError, "%s", textBodyTools(out))
		assert.Equal(t, []string{"go", "python"}, mgr.searchedNames(), "an absent language fans out")
	})

	t.Run("VOCABULARY: language all behaves as the fan-out, matching query", func(t *testing.T) {
		// The two tools must agree on what the word means, or a caller who learned
		// the spelling on one gets a different operation on the other.
		deps, mgr := newDeps(t)
		_, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "practice", "language": "all", "query": "pool",
		}))
		require.False(t, out.IsError, "%s", textBodyTools(out))
		assert.Equal(t, []string{"go", "python"}, mgr.searchedNames(), `"all" IS the fan-out`)
	})
}
