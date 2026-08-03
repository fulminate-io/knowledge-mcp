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

	mu   sync.Mutex
	reqs []*knowledgev1.ExecuteRequest
}

func (h *fanOutEngineHandler) Check(
	_ context.Context, _ *connect.Request[knowledgev1.HealthCheckRequest],
) (*connect.Response[knowledgev1.HealthCheckResponse], error) {
	return connect.NewResponse(&knowledgev1.HealthCheckResponse{}), nil
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
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
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

func (h *fanOutEngineHandler) Hive(
	context.Context, *connect.Request[knowledgev1.HiveRequest],
) (*connect.Response[knowledgev1.HiveResponse], error) {
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
	t.Cleanup(srv.Close)
	return graphclient.NewGraphClientForURL(srv.URL), h
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

// TestPracticeFanOut_SearchRefusesLanguage pins the SEARCH tool's contract for a
// param it has no scope for.
//
// The search tool ALWAYS fans across every loaded practice graph — there is no
// single-language branch — so `language` selects nothing here. It used to be
// ACCEPTED AND DROPPED: a caller asking for one language got results silently
// spanning all of them, with no signal that the scoping had not applied. It is
// now REFUSED, naming the valid set, so the caller can reach for
// query(graph:"practice", language:...) which does honor it.
//
// The tool declares no `language`, so nothing was under-declared and nothing was
// added: the schema was already honest and the acceptance was the lie. The QUERY
// tool's empty-language browse is unaffected and stays below.
func TestPracticeFanOut_SearchRefusesLanguage(t *testing.T) {
	t.Run("SEARCH tool language:go is refused", func(t *testing.T) {
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
		require.True(t, out.IsError, "an unscopeable param must be refused, not silently dropped")
		body := textBodyTools(out)
		assert.Contains(t, body, `search: unknown parameter "language"`,
			"the refusal names the offending param")
		assert.Contains(t, body, "query", "the refusal enumerates the valid set so the caller can correct the call")
		// AND NO SEARCH RAN. A refusal that still fanned out would have served
		// the caller unscoped results behind an error — the worst of both.
		assert.Empty(t, mgr.searchedNames(), "a refused call must issue no reads")
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
