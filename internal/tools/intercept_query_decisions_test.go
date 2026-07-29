// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
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

// decisionsFakeGc scripts both read shapes the decisions intercept fires over
// the Execute carrier seam: a query(type:"decision") listing
// (nodes_json carrier) and a search(graph:"knowledge", types:["decision"])
// topic-search (search_results_json carrier). Records every Execute request for
// compiled-plan shape assertions.
type decisionsFakeGc struct {
	listing []*knowledgev1.Node // returned by the listing query Execute
	search  []*knowledgev1.Node // returned by the search Execute (no Score)
	execs   []*knowledgev1.ExecuteRequest
}

// Call satisfies the interface; the decisions intercept routes through Execute.
func (g *decisionsFakeGc) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (g *decisionsFakeGc) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	g.execs = append(g.execs, req)
	q := req.GetQuery()
	if len(q.GetQueries()) > 0 {
		// Search path → search_results carrier ([]engine.SearchResult).
		hits := make([]engine.SearchResult, len(g.search))
		for i, n := range g.search {
			hits[i] = engine.SearchResult{Node: n}
		}
		return &knowledgev1.ExecuteResponse{SearchResults: searchResultsToProtoForTest(hits)}, nil
	}
	// Listing path → nodes_json carrier + total.
	resp := enginetest.ResponseWithNodes(g.listing...)
	resp.Total = int64(len(g.listing))
	return resp, nil
}

func seedDecisionsFixture() *decisionsFakeGc {
	mkDecision := func(id, name string, withAlts bool) *knowledgev1.Node {
		n := &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeDecision), SymbolName: name,
			Source: "test", Status: "active",
			Description: "decision " + name, Summary: "summary " + name,
		}
		kgtypes.SetValue(n, "choice", "choice "+name)
		kgtypes.SetValue(n, "rationale", "rationale "+name)
		if withAlts {
			kgtypes.SetValue(n, "alternatives", "alt-1, alt-2")
		}
		return n
	}
	dec1 := mkDecision("00000000000000000000000000000aa1", "cap-dec-alpha", true)
	dec2 := mkDecision("00000000000000000000000000000aa2", "cap-dec-beta", false)
	// The pre-relocation capture's listing returned beta-then-alpha
	// ("most recent first" per the formatDecisions header). We match
	// that ordering here so the byte-parity assertion holds — the
	// in-graph store iterates by insertion order reversed; the wire
	// fake mirrors the server-side ordering directly.
	return &decisionsFakeGc{
		listing: []*knowledgev1.Node{dec2, dec1},
		search:  []*knowledgev1.Node{dec1, dec2},
	}
}

func TestInterceptQueryDecisions_Listing_ByteIdentical(t *testing.T) {
	gc := seedDecisionsFixture()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "decision", "limit": 10})

	handled, res := InterceptQueryDecisions(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept error: %v", res.Content)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "decisions")
	assert.Equal(t, want, got)
}

func TestInterceptQueryDecisions_TopicSearch_ByteIdentical(t *testing.T) {
	gc := &decisionsFakeGc{
		// Empty search to mirror the goldengen capture's "No
		// decisions found." baseline — the capture seeded decisions
		// but BM25 wasn't indexed against the fixture, so the
		// search returned 0 results. Mirror that baseline.
	}
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "decision", "text": "cap-dec", "limit": 10})

	handled, res := InterceptQueryDecisions(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "decisions_search")
	assert.Equal(t, want, got)
}

// TestInterceptQueryDecisions_TopicSearch_ClientEngine is the census-gap
// fix: the decisions topic-search is a KNOWLEDGE-graph search
// that now runs against the CLIENT segment engine (mgr.Search → hydrate → keep
// decisions) and dispatches NO server RETURN_MODE_SEARCH. The only Execute is the
// ids[] hydrate read, which is not a server search plan.
func TestInterceptQueryDecisions_TopicSearch_ClientEngine(t *testing.T) {
	var execHits atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "d1", Type: string(kgtypes.NodeDecision), SymbolName: "cap-dec-alpha",
			Source: "test", Status: "active"},
	))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "d1", Score: 0.9}}}
	deps := &interceptDeps{gc: gc, segMgr: mgr}
	args := mustMarshal(t, map[string]any{"type": "decision", "text": "foo", "limit": 5})

	handled, res := InterceptQueryDecisions(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "%v", engine.FirstTextContent(res))

	require.Equal(t, int64(1), mgr.calls.Load(), "decisions search drove the CLIENT knowledge engine")
	require.Equal(t, kgtypes.GraphKnowledge, mgr.lastGT)
	assert.Equal(t, "foo decision", mgr.lastText, "the topic+' decision' query reached the engine")
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
		"decisions topic-search must NOT dispatch a server search")
	assert.Contains(t, engine.FirstTextContent(res), "cap-dec-alpha")
}

// browseJSONEnvelope is the {graph, type, results, total} shape the agent
// graph-explorer BrowseResponse consumes — the contract the format:"json"
// intercept branch must emit (handleBrowseJSON parity).
type browseJSONEnvelope struct {
	Graph   string `json:"graph"`
	Type    string `json:"type"`
	Total   int    `json:"total"`
	Results []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Type   string `json:"type"`
		Status string `json:"status"`
	} `json:"results"`
}

func TestInterceptQueryDecisions_Listing_JSON(t *testing.T) {
	gc := seedDecisionsFixture()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "decision", "limit": 10, "format": "json"})

	handled, res := InterceptQueryDecisions(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept error: %v", res.Content)

	var env browseJSONEnvelope
	require.NoError(t, json.Unmarshal([]byte(extractText(res)), &env),
		"format:json must return parseable browse JSON, got: %s", extractText(res))
	assert.Equal(t, "knowledge", env.Graph)
	assert.Equal(t, "decision", env.Type)
	assert.Equal(t, 2, env.Total)
	require.Len(t, env.Results, 2)
	// Listing fixture order: dec2 (beta) then dec1 (alpha).
	assert.Equal(t, "00000000000000000000000000000aa2", env.Results[0].ID)
	assert.Equal(t, "cap-dec-beta", env.Results[0].Name)
	assert.Equal(t, "decision", env.Results[0].Type)
	assert.Equal(t, "active", env.Results[0].Status)
	assert.Equal(t, "cap-dec-alpha", env.Results[1].Name)

	// The default (no-format) caller MUST still receive the human markdown — the
	// intercept only diverges on format:"json".
	mdArgs := mustMarshal(t, map[string]any{"type": "decision", "limit": 10})
	handledMD, resMD := InterceptQueryDecisions(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: mdArgs})
	require.True(t, handledMD)
	require.False(t, resMD.IsError)
	assert.Contains(t, extractText(resMD), "Decisions (")
}

func TestInterceptQueryDecisions_Listing_WireShape_NoIncludeTombstones(t *testing.T) {
	gc := seedDecisionsFixture()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "decision", "limit": 10})

	handled, _ := InterceptQueryDecisions(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.Len(t, gc.execs, 1)
	q := gc.execs[0].GetQuery()
	require.NotNil(t, q, "listing compiles a type-browse QueryPlan")
	assert.Equal(t, "decision", q.GetSelection().GetNodeType())
	// The listing MUST NOT request tombstones — the compiled plan leaves
	// include_tombstones false (executor-level default-hidden behavior is
	// symmetric across handleDecisions + handleBrowseJSON).
	assert.False(t, q.GetIncludeTombstones(), "include_tombstones must not be set on the compiled listing plan")
}

func TestInterceptQueryDecisions_WrongType_FallsThrough(t *testing.T) {
	gc := seedDecisionsFixture()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "finding"})

	handled, _ := InterceptQueryDecisions(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled)
}

func TestInterceptQueryDecisions_WrongTool_FallsThrough(t *testing.T) {
	gc := seedDecisionsFixture()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "decision"})

	handled, _ := InterceptQueryDecisions(opCtx(), deps, kgtools.CallToolParams{Name: "search", Arguments: args})
	assert.False(t, handled)
}
