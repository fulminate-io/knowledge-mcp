// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// fakeLogSearchStore is a minimal in-memory log-graph search backend over the
// Execute carrier seam. Tests register a per-graph slice of
// "templates" (and optional non-template hits) keyed by query_id. The
// type-browse Execute returns ALL nodes of the requested type via the
// nodes_json carrier (the CLIENT does the substring filter now —
// filterLogHitsBySubstring); an unknown graph returns a CodeNotFound error
// mimicking the server's "not found" response.
type fakeLogSearchStore struct {
	graphs map[string][]map[string]any
}

func newFakeLogSearchStore() *fakeLogSearchStore {
	return &fakeLogSearchStore{graphs: map[string][]map[string]any{}}
}

// Call satisfies the interface; the search reads route through Execute.
func (f *fakeLogSearchStore) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (f *fakeLogSearchStore) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	sel := req.GetTarget()
	if sel.GetGraph() != "logs" {
		return enginetest.ResponseWithNodes(), nil
	}
	nodeMaps, ok := f.graphs[sel.GetName()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("graph %q not found", sel.GetName()))
	}
	wantType := q.GetSelection().GetNodeType()
	var nodes []*knowledgev1.Node
	for _, nm := range nodeMaps {
		n := searchNodeMapToStore(nm)
		if wantType != "" && n.Type != wantType {
			continue
		}
		nodes = append(nodes, n)
	}
	return enginetest.ResponseWithNodes(nodes...), nil
}

// searchNodeMapToStore converts a seeded node-map into a knowledgev1.Node (id,
// symbol_name/name → SymbolName, type, metadata).
func searchNodeMapToStore(nm map[string]any) *knowledgev1.Node {
	n := &knowledgev1.Node{}
	if v, ok := nm["id"].(string); ok {
		n.Id = v
	}
	if v, ok := nm["name"].(string); ok {
		n.SymbolName = v
	}
	if v, ok := nm["symbol_name"].(string); ok && n.SymbolName == "" {
		n.SymbolName = v
	}
	if v, ok := nm["type"].(string); ok {
		n.Type = v
	}
	if md, ok := nm["metadata"].(map[string]any); ok {
		n.Metadata = make(map[string]string, len(md))
		for k, v := range md {
			if s, ok := v.(string); ok {
				n.Metadata[k] = s
			}
		}
	}
	return n
}

// seedSearchGraph adds a handful of realistic templates (plus a non-
// template node mixed in) to a fake store under queryID.
func seedSearchGraph(f *fakeLogSearchStore, queryID string) {
	templates := []map[string]any{
		{
			"id":   "log-template:connection-timeout",
			"name": "connection timeout while reaching <*>",
			"type": "log-template",
			"metadata": map[string]any{
				"pattern":    "connection timeout while reaching <*>",
				"severity":   "ERROR",
				"count":      "42",
				"first_seen": "2026-04-13T12:00:00.000000000Z",
				"last_seen":  "2026-04-13T12:05:00.000000000Z",
			},
			"score": 0.95,
		},
		{
			"id":   "log-template:permission-denied",
			"name": "permission denied on resource <*>",
			"type": "log-template",
			"metadata": map[string]any{
				"pattern":  "permission denied on resource <*>",
				"severity": "WARN",
				"count":    "7",
			},
			"score": 0.7,
		},
		{
			"id":   "log-template:out-of-memory",
			"name": "out of memory killing process <*>",
			"type": "log-template",
			"metadata": map[string]any{
				"pattern":  "out of memory killing process <*>",
				"severity": "FATAL",
				"count":    "3",
			},
			"score": 0.5,
		},
		{
			"id":       "log-template:hello",
			"name":     "hello world from <*>",
			"type":     "log-template",
			"metadata": map[string]any{"pattern": "hello world from <*>", "severity": "INFO", "count": "1000"},
			"score":    0.3,
		},
	}
	nonTemplates := []map[string]any{
		{
			"id":       "log-chunk:timeout-chunk-1",
			"name":     "connection timeout reaching api.example.com",
			"type":     "log-chunk",
			"metadata": map[string]any{"stream_id": "s1", "template_id": "log-template:connection-timeout"},
			"score":    0.8,
		},
		{
			"id":       "log-stream:api",
			"name":     "stream-api",
			"type":     "log-stream",
			"metadata": map[string]any{"label:service": "api"},
			"score":    0.6,
		},
	}
	f.graphs[queryID] = append(templates, nonTemplates...)
}

// testSearchQueryID is the fixed queryID every search test seeds. Tests
// share the constant rather than passing it through newSearchHandler so
// the (currently single-graph) fake stays free of unparam noise.
const testSearchQueryID = "q-test-search"

// newSearchHandler returns a Handler wired to a fresh fake search store
// with one pre-seeded graph under testSearchQueryID. The store is
// owned by the handler and inspecting captured calls is currently
// unused — tests that need the fake should build it inline.
func newSearchHandler(t *testing.T) *Handler {
	t.Helper()
	store := newFakeLogSearchStore()
	seedSearchGraph(store, testSearchQueryID)
	return &Handler{graphCallerOverride: store}
}

// TestSearchLogs_Basic verifies BM25 finds the most relevant template
// for a plain-English query and surfaces its metadata.
func TestSearchLogs_Basic(t *testing.T) {
	queryID := testSearchQueryID
	h := newSearchHandler(t)

	result := h.searchLogs(context.Background(), searchArgs{
		Graph: "logs", Name: queryID, Query: "connection timeout", Limit: 10,
	})
	require.False(t, result.IsError, "expected success, got: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "connection timeout", "top result should be the timeout template")
	assert.Contains(t, text, "ERROR", "severity metadata should render")
	assert.Contains(t, text, "Count: 42", "count metadata should render")
	assert.Contains(t, text, queryID, "output header should mention the queryID")
}

// TestSearchLogs_JSONAndText covers the JSON contract for the logs path: searchLogs with
// Format:"json" parses to the SearchJSONResponse envelope carrying the template
// SymbolName + severity metadata; the no-format run stays on the markdown
// log-templates header path.
func TestSearchLogs_JSONAndText(t *testing.T) {
	queryID := testSearchQueryID

	jsonRes := newSearchHandler(t).searchLogs(context.Background(), searchArgs{
		Graph: "logs", Name: queryID, Query: "connection timeout", Limit: 10, Format: "json",
	})
	require.False(t, jsonRes.IsError, resultText(jsonRes))
	var env engine.SearchJSONResponse
	require.NoError(t, json.Unmarshal([]byte(resultText(jsonRes)), &env), "json branch must parse to SearchJSONResponse")
	require.GreaterOrEqual(t, env.Total, 1)
	require.NotEmpty(t, env.Results)
	hit := env.Results[0]
	assert.Equal(t, "log-template:connection-timeout", hit.ID)
	assert.Equal(t, "connection timeout while reaching <*>", hit.SymbolName, "template pattern rides through SymbolName")
	assert.Equal(t, "ERROR", hit.Metadata["severity"], "severity rides through metadata")
	assert.Equal(t, "42", hit.Metadata["count"])
	assert.Equal(t, "2026-04-13T12:00:00.000000000Z", hit.Metadata["first_seen"])

	textRes := newSearchHandler(t).searchLogs(context.Background(), searchArgs{
		Graph: "logs", Name: queryID, Query: "connection timeout", Limit: 10,
	})
	require.False(t, textRes.IsError, resultText(textRes))
	body := resultText(textRes)
	assert.Contains(t, body, "Log templates in", "text path renders the markdown header")
	assert.Contains(t, body, "Count: 42")
	var env2 engine.SearchJSONResponse
	assert.Error(t, json.Unmarshal([]byte(body), &env2), "text path must not emit JSON")
}

// TestSearchLogs_TemplateOnlyFilter confirms chunks/streams never appear
// even when their text matches the query. The test query deliberately
// mentions a chunk's name — without filtering, the fake would return it.
func TestSearchLogs_TemplateOnlyFilter(t *testing.T) {
	queryID := testSearchQueryID
	h := newSearchHandler(t)

	result := h.searchLogs(context.Background(), searchArgs{
		Graph: "logs", Name: queryID, Query: "api example timeout", Limit: 10,
	})
	require.False(t, result.IsError, "expected success: %s", resultText(result))
	text := resultText(result)

	// Chunk and stream node IDs must never leak into template-only output.
	assert.NotContains(t, text, "log-chunk:timeout-chunk-1",
		"chunk hits must be filtered out of template search")
	assert.NotContains(t, text, "log-stream:api",
		"stream hits must be filtered out of template search")
}

// TestSearchLogs_NoMatch returns a clear message (not an error) when the
// query has no template hits. Callers should be able to distinguish
// "zero results" from "graph missing".
func TestSearchLogs_NoMatch(t *testing.T) {
	queryID := testSearchQueryID
	h := newSearchHandler(t)

	result := h.searchLogs(context.Background(), searchArgs{
		Graph: "logs", Name: queryID, Query: "quantum foam crystallography", Limit: 10,
	})
	require.False(t, result.IsError)
	text := resultText(result)
	assert.Contains(t, text, "No log template matches",
		"should advertise zero matches, got: %s", text)
}

// TestSearchLogs_UnknownGraph returns an error when the caller names a
// queryID that was never collected. Create-on-missing is intentionally
// absent so typos are caught up front.
func TestSearchLogs_UnknownGraph(t *testing.T) {
	store := newFakeLogSearchStore()
	h := &Handler{graphCallerOverride: store}

	result := h.searchLogs(context.Background(), searchArgs{
		Graph: "logs", Name: "q-does-not-exist", Query: "whatever", Limit: 10,
	})
	require.True(t, result.IsError, "unknown graph should be an error")
	assert.Contains(t, resultText(result), "not found")
}

// TestSearchLogs_RequiresName enforces the contract: logs search needs a
// queryID because there is no sensible "default" log graph.
func TestSearchLogs_RequiresName(t *testing.T) {
	store := newFakeLogSearchStore()
	h := &Handler{graphCallerOverride: store}

	result := h.searchLogs(context.Background(), searchArgs{
		Graph: "logs", Query: "anything",
	})
	require.True(t, result.IsError)
	assert.Contains(t, resultText(result), "name is required")
}

// TestSearchLogs_RequiresQuery enforces that a search without a query
// string errors rather than returning every template.
func TestSearchLogs_RequiresQuery(t *testing.T) {
	queryID := testSearchQueryID
	h := newSearchHandler(t)

	result := h.searchLogs(context.Background(), searchArgs{
		Graph: "logs", Name: queryID, Limit: 10,
	})
	require.True(t, result.IsError)
	assert.Contains(t, resultText(result), "query")
}
