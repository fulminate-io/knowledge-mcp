// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// etFake routes Execute by plan shape, counts node-set fetches, and records every
// compiled plan so the page-plan shape can be asserted. Its node-set arm is
// PAGE-AWARE (see pageOfMatchNodes) — a fake that returned the whole matchNodes
// slice for every call would make a real keyset drain loop forever.
type etFake struct {
	nodeFetches int
	edgeFetches int
	plans       []*knowledgev1.QueryPlan
	edges       []*knowledgev1.Edge
	idNodes     []knowledgev1.Node // returned for an Ids[] hydrate
	matchNodes  []knowledgev1.Node // returned for a Match (node-set) fetch
}

func (f *etFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	f.plans = append(f.plans, q)
	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		f.edgeFetches++
		return &knowledgev1.ExecuteResponse{Edges: f.edges}, nil
	case len(q.GetIds()) > 0:
		resp := enginetest.ResponseWithNodes(nodePtrs(f.idNodes)...)
		return resp, nil
	default:
		f.nodeFetches++
		resp := enginetest.ResponseWithNodes(nodePtrs(f.pageOfMatchNodes(q))...)
		return resp, nil
	}
}

// pageOfMatchNodes serves matchNodes as a keyset page: everything after the
// plan's AfterId cursor, truncated to a positive plan Limit. An unset cursor and
// an unset limit both mean "from the start, all of it" — which is exactly the
// unbounded shape the bound tests exist to catch.
func (f *etFake) pageOfMatchNodes(q *knowledgev1.QueryPlan) []knowledgev1.Node {
	nodes := f.matchNodes
	if after := q.GetAfterId(); after != "" {
		idx := -1
		for i := range nodes {
			if nodes[i].Id == after {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil
		}
		nodes = nodes[idx+1:]
	}
	if lim := int(q.GetLimit()); lim > 0 && len(nodes) > lim {
		nodes = nodes[:lim]
	}
	return nodes
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
		handled, _ := InterceptQueryExplainTimeline(opCtx(), nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"stats"}`)})
		assert.False(t, handled, "non explain/timeline mode not claimed")
		handled, _ = InterceptQueryExplainTimeline(opCtx(), nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"logs","mode":"explain"}`)})
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
		assert.Equal(t, 1, f.nodeFetches, "a corpus below one page drains in a single keyset page")
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

// TestComposeTimeline_KeysetPagedAndCapped asserts the timeline fetch pages with
// a bounded keyset browse AND that the flat render is capped by default, not
// only when the caller supplies a limit. 600 nodes with distinct parseable
// timestamps: an uncapped render emits all 600.
func TestComposeTimeline_KeysetPagedAndCapped(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	nodes := make([]knowledgev1.Node, 600)
	for i := range nodes {
		nodes[i] = knowledgev1.Node{
			Id:         fmt.Sprintf("n%04d", i),
			SymbolName: fmt.Sprintf("N%04d", i),
			Type:       "finding",
			Status:     "open",
			Metadata:   map[string]string{"ts": base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)},
		}
	}
	f := &etFake{matchNodes: nodes}
	res := composeTimeline(context.Background(), newETFakeDeps(f), queryArgs{Mode: "timeline", TimeField: "ts"})
	require.False(t, res.IsError, textBodyTools(res))

	nodePlans := 0
	for i, p := range f.plans {
		if p.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
			continue
		}
		nodePlans++
		assert.Equal(t, int32(engine.BrowsePageSize), p.GetLimit(), "plan[%d] carries the page limit", i)
		assert.NotNil(t, p.AfterId, "plan[%d] SETS the keyset cursor (presence selects the keyset browse)", i)
		assert.True(t, p.GetSkipTotal(), "plan[%d] skips Total", i)
	}
	assert.Positive(t, nodePlans, "no node plans recorded — the probe is broken")

	body := textBodyTools(res)
	rows := 0
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "| T+") {
			rows++
		}
	}
	assert.Equal(t, engine.TimelineRowCapDefault, rows, "the flat render is capped at the default timeline row cap")
	assert.Contains(t, body, "more node(s) below the earliest", "truncation notice")
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
		got := engine.RenderTimelineFlat("knowledge", "ts", entries, 1)
		assert.Contains(t, got, "**field:** `ts`")
		assert.Contains(t, got, "| T+0s | `A` | finding | open |")
	})
}
