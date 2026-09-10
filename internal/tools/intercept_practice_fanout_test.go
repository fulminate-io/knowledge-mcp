// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestPracticeSearch_NoSelectorSearchesTheSingleton is requirement 6's first
// cell and the one a sentinel-only retirement fails.
//
// BOTH the empty language and the literal "all" used to fall through to the
// scatter-gather, so a change that retired only the sentinel would leave the
// DEFAULT call still fanning out across the pre-singleton graphs — green on the
// refusal test and wrong on every real call. This asserts on WHICH POOL WAS
// SEARCHED rather than on the rendered rows, because a fan-out that happened to
// include the combined graph would satisfy a result-only assertion.
func TestPracticeSearch_NoSelectorSearchesTheSingleton(t *testing.T) {
	seed := func() (*graphclient.GraphClient, *fanOutSegmentSearcher) {
		// The legacy graphs are enumerable and MUST NOT be searched: they are the
		// discriminating half of this fixture.
		gc := newFanOutHarness(t, []string{"default", "go", "python"},
			practiceNode("p:combined", "CombinedPattern", "lives in the one graph"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"default": {{ID: "p:combined", Score: 0.90}},
			"go":      {{ID: "p:go", Score: 0.99}},
			"python":  {{ID: "p:py", Score: 0.98}},
		})
		return gc, mgr
	}

	t.Run("SEARCH tool", func(t *testing.T) {
		gc, mgr := seed()
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"graph": "practice", "query": "pool"}))
		require.True(t, handled)
		require.False(t, out.IsError, "result is not an error: %s", textBodyTools(out))
		assert.Equal(t, []string{"default"}, mgr.searchedNames(),
			"an unselected practice search reads the ONE combined graph and no other")
		assert.Contains(t, textBodyTools(out), "CombinedPattern")
	})

	t.Run("QUERY tool", func(t *testing.T) {
		gc, mgr := seed()
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Text: "pool"})
		require.False(t, res.IsError, "%s", textBodyTools(res))
		assert.Equal(t, []string{"default"}, mgr.searchedNames(),
			"the two tools must agree about what an unselected practice search reads")
	})
}

// TestPracticeSearch_AllSentinelRefused pins requirement 6's second cell on BOTH
// tools.
//
// IT ASSERTS ON THE MESSAGE, not merely on IsError. The sentinel asked for a
// scatter-gather that no longer exists, and the useful answer names the call
// that does — a bare refusal leaves the caller guessing whether practice search
// broke.
func TestPracticeSearch_AllSentinelRefused(t *testing.T) {
	seed := func() (*graphclient.GraphClient, *fanOutSegmentSearcher) {
		gc := newFanOutHarness(t, []string{"default", "go"})
		return gc, newFanOutSegmentSearcher(nil)
	}

	t.Run("SEARCH tool", func(t *testing.T) {
		gc, mgr := seed()
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "practice", "language": "all", "query": "pool",
		}))
		require.True(t, handled)
		require.True(t, out.IsError, "the retired sentinel must be refused, not served")
		body := textBodyTools(out)
		assert.Contains(t, body, "retired")
		assert.Contains(t, body, "Omit the selector", "the refusal names the call that works")
		assert.Empty(t, mgr.searchedNames(), "the refusal costs no read")
	})

	t.Run("QUERY tool", func(t *testing.T) {
		gc, mgr := seed()
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Language: "all", Text: "pool"})
		require.True(t, res.IsError)
		assert.Contains(t, textBodyTools(res), "retired")
		assert.Empty(t, mgr.searchedNames(), "the refusal costs no read")
	})
}

// TestPracticeSearch_LegacyLanguageStillScopes is requirement 5 on the search
// arms: the eight pre-singleton graphs stay readable through `language`, and a
// named one is searched INSTEAD OF the combined graph rather than beside it.
func TestPracticeSearch_LegacyLanguageStillScopes(t *testing.T) {
	gc := newFanOutHarness(t, []string{"default", "go", "python"},
		practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
	)
	mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
		"default": {{ID: "p:combined", Score: 0.99}},
		"go":      {{ID: "p:go", Score: 0.90}},
		"python":  {{ID: "p:py", Score: 0.80}},
	})
	deps := &interceptDeps{gc: gc, segMgr: mgr}
	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "practice", "language": "go", "query": "pool",
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "the legacy read selector is accepted: %s", textBodyTools(out))
	assert.Equal(t, []string{"go"}, mgr.searchedNames(),
		"a named language reads THAT pre-singleton graph and no other, including not the combined one")
	assert.Contains(t, textBodyTools(out), "GoWorkerPool")
}

// TestPracticeSearch_JSONAndText covers the json contract for the one practice
// composer that survives. The fan-out half of this test went with the fan-out.
func TestPracticeSearch_JSONAndText(t *testing.T) {
	seed := func() (*graphclient.GraphClient, *fanOutSegmentSearcher) {
		gc := newFanOutHarness(t, []string{"default"},
			practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"default": {{ID: "p:go", Score: 0.90}},
		})
		return gc, mgr
	}

	gc, mgr := seed()
	deps := &interceptDeps{gc: gc, segMgr: mgr}
	jsonRes := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Text: "pool", Format: "json"})
	var env engine.SearchJSONResponse
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(jsonRes)), &env), "json branch must parse")
	require.Equal(t, 1, env.Total)
	require.Len(t, env.Results, 1)
	assert.Equal(t, "p:go", env.Results[0].ID)
	assert.Equal(t, "GoWorkerPool", env.Results[0].SymbolName)

	gc2, mgr2 := seed()
	deps2 := &interceptDeps{gc: gc2, segMgr: mgr2}
	textRes := gatedRoutePractice(opCtx(), deps2, gc2, queryArgs{Graph: "practice", Text: "pool"})
	body := textBodyTools(textRes)
	assert.Contains(t, body, "GoWorkerPool", "text path renders RenderPracticeResults markdown")
	var env2 engine.SearchJSONResponse
	assert.Error(t, json.Unmarshal([]byte(body), &env2), "text path must not emit JSON")
}
