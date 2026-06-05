// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// etFake routes Execute by plan shape and counts node-set fetches (the timeline
// one-fetch invariant).
type etFake struct {
	nodeFetches int
	edgeFetches int
	edges       []*knowledgev1.Edge
	idNodes     []knowledgev1.Node // returned for an Ids[] hydrate
	matchNodes  []knowledgev1.Node // returned for a Match (node-set) fetch
}

func (f *etFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		f.edgeFetches++
		return &knowledgev1.ExecuteResponse{Edges: f.edges}, nil
	case len(q.GetIds()) > 0:
		resp := enginetest.ResponseWithNodes(nodePtrs(f.idNodes)...)
		return resp, nil
	default:
		f.nodeFetches++
		resp := enginetest.ResponseWithNodes(nodePtrs(f.matchNodes)...)
		return resp, nil
	}
}

// Execute lets etFake double as a GraphCaller for composeTimeline (which now takes
// a ClientDeps; the type/default fetch paths only touch Execute).
func (f *etFake) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return f.exec(ctx, req)
}

// etFakeDeps adapts an etFake into a ClientDeps for composeTimeline.
type etFakeDeps struct {
	*interceptDeps
	f *etFake
}

func newETFakeDeps(f *etFake) *etFakeDeps {
	return &etFakeDeps{interceptDeps: &interceptDeps{}, f: f}
}

func (d *etFakeDeps) GraphCaller() GraphCaller { return d.f }

// TestInterceptQueryExplainTimeline is the automated criterion gate. Covers the
// explain single-node + pair forms and the timeline one-fetch ascending sort.
func TestInterceptQueryExplainTimeline(t *testing.T) {
	t.Run("gate: claims explain+timeline, falls through otherwise", func(t *testing.T) {
		handled, _ := InterceptQueryExplainTimeline(nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"stats"}`)})
		assert.False(t, handled, "non explain/timeline mode not claimed")
		handled, _ = InterceptQueryExplainTimeline(nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"logs","mode":"explain"}`)})
		assert.False(t, handled, "logs explain owned by InterceptLogsQuery")
	})

	t.Run("explain single-node renders edges with resolved names", func(t *testing.T) {
		f := &etFake{
			edges:   []*knowledgev1.Edge{{FromId: "n1", ToId: "n2", Type: "informed-by", Confidence: 0.8, Method: "manual"}},
			idNodes: []knowledgev1.Node{{Id: "n1", SymbolName: "Source"}, {Id: "n2", SymbolName: "Target"}},
		}
		res := composeExplain(context.Background(), f.exec, queryArgs{Mode: "explain", ID: "n1"})
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Contains(t, body, "## Explain — knowledge")
		assert.Contains(t, body, "### Edge #1 — Source -> Target")
		assert.Contains(t, body, "- Type: informed-by")
		assert.Contains(t, body, "- Score: 0.80")
		assert.Equal(t, 1, f.edgeFetches, "one RETURN_MODE_EDGES fetch")
	})

	t.Run("explain pair filters to b-peer over single fetch", func(t *testing.T) {
		f := &etFake{
			edges: []*knowledgev1.Edge{
				{FromId: "a", ToId: "b", Type: "relates-to"},
				{FromId: "a", ToId: "c", Type: "relates-to"},
			},
			idNodes: []knowledgev1.Node{{Id: "a", SymbolName: "A"}, {Id: "b", SymbolName: "B"}},
		}
		res := composeExplain(context.Background(), f.exec, queryArgs{Mode: "explain", Extra: map[string]string{"a": "a", "b": "b"}})
		body := textBodyTools(res)
		assert.Contains(t, body, "1 edge(s).")
		assert.Equal(t, 1, f.edgeFetches, "pair form uses ONE edge fetch (filters b client-side)")
	})

	t.Run("explain no-edges empty branch", func(t *testing.T) {
		f := &etFake{edges: nil}
		res := composeExplain(context.Background(), f.exec, queryArgs{Mode: "explain", ID: "lonely"})
		assert.Contains(t, textBodyTools(res), "_No edges touching lonely for filter:")
	})

	t.Run("timeline one fetch + ascending sort", func(t *testing.T) {
		f := &etFake{matchNodes: []knowledgev1.Node{
			{Id: "late", SymbolName: "Late", Metadata: map[string]string{"ts": "2026-03-03T00:00:00Z"}},
			{Id: "early", SymbolName: "Early", Metadata: map[string]string{"ts": "2026-01-01T00:00:00Z"}},
			{Id: "mid", SymbolName: "Mid", Metadata: map[string]string{"ts": "2026-02-02T00:00:00Z"}},
		}}
		res := composeTimeline(context.Background(), newETFakeDeps(f), queryArgs{Mode: "timeline", TimeField: "ts"})
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Equal(t, 1, f.nodeFetches, "timeline issues exactly one node-set fetch")
		assert.Contains(t, body, "## Timeline — knowledge")
		// Early sorts before Mid before Late.
		assert.Less(t, indexOf(body, "Early"), indexOf(body, "Mid"))
		assert.Less(t, indexOf(body, "Mid"), indexOf(body, "Late"))
	})

	t.Run("timeline requires time_field", func(t *testing.T) {
		f := &etFake{}
		res := composeTimeline(context.Background(), newETFakeDeps(f), queryArgs{Mode: "timeline"})
		assert.Contains(t, textBodyTools(res), "timeline requires time_field")
	})

	t.Run("timeline bucketed", func(t *testing.T) {
		f := &etFake{matchNodes: []knowledgev1.Node{
			{Id: "a", SymbolName: "A", Metadata: map[string]string{"ts": "2026-01-01T00:00:00Z"}},
			{Id: "b", SymbolName: "B", Metadata: map[string]string{"ts": "2026-01-01T00:00:05Z"}},
		}}
		res := composeTimeline(context.Background(), newETFakeDeps(f), queryArgs{Mode: "timeline", TimeField: "ts", Extra: map[string]string{"bucket": "10s"}})
		require.False(t, res.IsError, textBodyTools(res))
		assert.Contains(t, textBodyTools(res), "## Timeline (bucketed) — knowledge")
	})
}

// TestRenderExplainTimeline_Golden are the engine renderer goldens.
func TestRenderExplainTimeline_Golden(t *testing.T) {
	t.Run("explain", func(t *testing.T) {
		edges := []knowledgev1.Edge{{FromId: "x", ToId: "y", Type: "depends-on", Confidence: 0.5}}
		names := map[string]*knowledgev1.Node{"x": {Id: "x", SymbolName: "X"}, "y": {Id: "y", SymbolName: "Y"}}
		got := engine.RenderExplainEdges("knowledge", edges, names)
		assert.Contains(t, got, "### Edge #1 — X -> Y")
	})
	t.Run("timeline flat", func(t *testing.T) {
		entries := engine.CollectTimelineEntries([]*knowledgev1.Node{
			{Id: "a", SymbolName: "A", Type: "finding", Status: "open", Metadata: map[string]string{"ts": "2026-01-01T00:00:00Z"}},
		}, "ts")
		engine.SortTimelineEntries(entries)
		got := engine.RenderTimelineFlat("knowledge", "ts", entries)
		assert.Contains(t, got, "**field:** `ts`")
		assert.Contains(t, got, "| T+0s | `A` | finding | open |")
	})
}
