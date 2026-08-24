// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// corrFake counts node-set fetches and edge fetches and records every compiled
// plan, so both the call-shape invariant and the PLAN-shape invariants (no
// unbounded whole-graph scan; a positive Limit on the match-all edges read) can
// be asserted. The edges arm honors a positive plan Limit, mirroring the server
// contract in collectEdgesForReturnMode (limit <= 0 means unlimited), so the
// capped-scan path the composer relies on is exercised rather than faked.
type corrFake struct {
	nodeFetches int
	edgeFetches int
	plans       []*knowledgev1.QueryPlan
	nodes       []knowledgev1.Node
	edges       []*knowledgev1.Edge
}

func (f *corrFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	f.plans = append(f.plans, q)
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		f.edgeFetches++
		edges := f.edges
		if lim := int(q.GetLimit()); lim > 0 && len(edges) > lim {
			edges = edges[:lim]
		}
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(edges, q)}, nil
	}
	f.nodeFetches++
	ptrs := make([]*knowledgev1.Node, len(f.nodes))
	for i := range f.nodes {
		ptrs[i] = &f.nodes[i]
	}
	return enginetest.ResponseWithNodes(ptrs...), nil
}

// Execute lets corrFake double as a GraphCaller for the pivot/timeline composers
// that now take a ClientDeps (the non-text-seed paths only touch Execute).
func (f *corrFake) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return f.exec(ctx, req)
}

// corrFakeDeps adapts a corrFake into a ClientDeps for composePivot/composeTimeline.
// The text-seed search path (which needs a SegmentManager) is exercised separately
// with a fakeSegmentSearcher; the type/default fetch paths only use GraphCaller.
type corrFakeDeps struct {
	*interceptDeps
	f *corrFake
}

func newCorrFakeDeps(f *corrFake) *corrFakeDeps {
	return &corrFakeDeps{interceptDeps: &interceptDeps{}, f: f}
}

func (d *corrFakeDeps) GraphCaller() GraphCaller { return d.f }

// TestComposeCorrelations_OneBulkEdgeFetch is the CALL-SHAPE guard: exactly ONE
// match-all RETURN_MODE_EDGES fetch + exactly ONE bulk endpoint hydrate, never a
// per-node edge fetch, regardless of node count. Call shape alone is not the
// bound — the PAYLOAD bound is asserted by
// TestComposeCorrelations_NoWholeGraphHydrate and _RenderCapAtScale.
func TestComposeCorrelations_OneBulkEdgeFetch(t *testing.T) {
	// 50 nodes — a per-node edge fetch would be 50 edge Executes.
	nodes := make([]knowledgev1.Node, 50)
	for i := range nodes {
		nodes[i] = knowledgev1.Node{Id: string(rune('a'+i%26)) + string(rune('0'+i/26)), SymbolName: "n", Type: "finding"}
	}
	f := &corrFake{
		nodes: nodes,
		edges: []*knowledgev1.Edge{
			{FromId: nodes[0].Id, ToId: nodes[1].Id, Type: "correlates-with", Confidence: 0.9, Method: "stat"},
			{FromId: nodes[2].Id, ToId: nodes[3].Id, Type: "correlates-with", Confidence: 0.5},
		},
	}
	res := composeCorrelations(context.Background(), f.exec, queryArgs{Graph: "knowledge", Mode: "correlations"})
	require.False(t, res.IsError, textBodyTools(res))

	assert.Equal(t, 1, f.nodeFetches, "exactly one bulk endpoint hydrate (never a whole-graph node scan)")
	assert.Equal(t, 1, f.edgeFetches, "exactly one bulk RETURN_MODE_EDGES fetch regardless of node count")

	body := textBodyTools(res)
	assert.Contains(t, body, "## Correlations — knowledge")
	assert.Contains(t, body, "2 edge(s), sorted by confidence desc.")
	// 0.90 row sorts before 0.50.
	assert.Less(t, indexOf(body, "0.90"), indexOf(body, "0.50"))
}

// TestComposeCorrelations_EdgeTypeFilter asserts the client-side edge_type
// filter (mirroring the server typeSet) drops non-matching edges.
func TestComposeCorrelations_EdgeTypeFilter(t *testing.T) {
	f := &corrFake{
		nodes: []knowledgev1.Node{
			{Id: "a", SymbolName: "A"},
			{Id: "b", SymbolName: "B"},
		},
		edges: []*knowledgev1.Edge{
			{FromId: "a", ToId: "b", Type: "correlates-with", Confidence: 0.9},
			{FromId: "a", ToId: "b", Type: "relates-to", Confidence: 0.8},
		},
	}
	res := composeCorrelations(context.Background(), f.exec, queryArgs{Mode: "correlations", EdgeType: []string{"correlates-with"}})
	body := textBodyTools(res)
	assert.Contains(t, body, "1 edge(s)")
	assert.Contains(t, body, "correlates-with")
	assert.NotContains(t, body, "relates-to")
}

// TestComposeCorrelations_Empty asserts the no-edges branch.
func TestComposeCorrelations_Empty(t *testing.T) {
	f := &corrFake{nodes: []knowledgev1.Node{
		{Id: "a"},
	}, edges: nil}
	res := composeCorrelations(context.Background(), f.exec, queryArgs{Mode: "correlations", EdgeType: []string{"x"}})
	assert.Contains(t, textBodyTools(res), "_No edges found for filter: x._")
}

// TestComposeCorrelations_NoWholeGraphHydrate asserts the BOUND on payload
// rather than on call count: no compiled plan may be an unbounded whole-graph
// node scan, and the match-all edge read must carry a positive Limit. The
// violation shape mirrors the census classifier's own rules
// (cmd/knowledge/internal/bootstrap/bounded_reads_census_test.go:265-291): a
// plan that is not RETURN_MODE_EDGES, carries no ById and no Ids, and has
// Limit == 0.
func TestComposeCorrelations_NoWholeGraphHydrate(t *testing.T) {
	nodes := make([]knowledgev1.Node, 50)
	for i := range nodes {
		nodes[i] = knowledgev1.Node{Id: string(rune('a'+i%26)) + string(rune('0'+i/26)), SymbolName: "n", Type: "finding"}
	}
	f := &corrFake{
		nodes: nodes,
		edges: []*knowledgev1.Edge{
			{FromId: nodes[0].Id, ToId: nodes[1].Id, Type: "correlates-with", Confidence: 0.9, Method: "stat"},
			{FromId: nodes[2].Id, ToId: nodes[3].Id, Type: "correlates-with", Confidence: 0.5},
		},
	}
	res := composeCorrelations(context.Background(), f.exec, queryArgs{Graph: "knowledge", Mode: "correlations"})
	require.False(t, res.IsError, textBodyTools(res))

	require.NotEmpty(t, f.plans, "the composer recorded no plans — the probe is broken")
	for i, p := range f.plans {
		unboundedScan := p.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_EDGES &&
			p.GetById() == "" && len(p.GetIds()) == 0 && p.GetLimit() == 0
		assert.False(t, unboundedScan,
			"plan[%d] is an unbounded whole-graph node scan (no ById, no Ids, no Limit, not an edges read)", i)
	}

	cappedMatchAllEdges := false
	for _, p := range f.plans {
		if p.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES &&
			len(p.GetIds()) == 0 && p.GetLimit() > 0 {
			cappedMatchAllEdges = true
		}
	}
	assert.True(t, cappedMatchAllEdges,
		"no capped match-all edge read: expected a RETURN_MODE_EDGES plan with no Ids and a positive Limit")
}

// TestComposeCorrelations_RenderCapAtScale drives the composer at production
// edge cardinality and asserts the rendered body is capped, reports the
// truncation, and flags the capped scan as a SAMPLE (the server's match-all walk
// over an unordered map yields an arbitrary subset, not a prefix).
func TestComposeCorrelations_RenderCapAtScale(t *testing.T) {
	const edgeCount = 216109
	edges := make([]*knowledgev1.Edge, edgeCount)
	for i := range edges {
		edges[i] = &knowledgev1.Edge{
			FromId:     fmt.Sprintf("%032x", i),
			ToId:       fmt.Sprintf("%032x", i+1),
			Type:       "correlates-with",
			Confidence: 0.5,
		}
	}
	f := &corrFake{
		nodes: []knowledgev1.Node{{Id: "a", SymbolName: "A"}, {Id: "b", SymbolName: "B"}},
		edges: edges,
	}
	res := composeCorrelations(context.Background(), f.exec, queryArgs{Graph: "knowledge", Mode: "correlations"})
	require.False(t, res.IsError, textBodyTools(res))

	body := textBodyTools(res)
	rowCount := 0
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "| `") {
			rowCount++
		}
	}
	assert.Equal(t, engine.CorrelationsRowCapDefault, rowCount, "rendered data rows are capped at the default row cap")
	assert.Contains(t, body, "more edge(s) below the top", "truncation notice")
	assert.Equal(t, engine.CorrelationsEdgeScanCap, f.edgesReturned(), "the composer asks the server for at most the edge scan cap")
	assert.Contains(t, body, "Scan capped at", "sampled-ranking notice when the scan is capped")
}

// edgesReturned reports how many edges the fake actually handed back on the last
// edges Execute, after applying the plan's Limit.
func (f *corrFake) edgesReturned() int {
	for _, p := range slices.Backward(f.plans) {
		if p.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_EDGES {
			continue
		}
		if lim := int(p.GetLimit()); lim > 0 && len(f.edges) > lim {
			return lim
		}
		return len(f.edges)
	}
	return 0
}

// TestComposePivot_Matrix drives the pivot path over a node-set, asserting the
// matrix render shape.
func TestComposePivot_Matrix(t *testing.T) {
	f := &corrFake{nodes: []knowledgev1.Node{
		{Id: "1", Type: "finding", Status: "open"},
		{Id: "2", Type: "finding", Status: "open"},
		{Id: "3", Type: "decision", Status: "closed"},
	}}
	res := composePivot(context.Background(), newCorrFakeDeps(f), queryArgs{Mode: "pivot", Rows: "type", Cols: "status"})
	require.False(t, res.IsError, textBodyTools(res))
	body := textBodyTools(res)
	assert.Contains(t, body, "## Pivot — knowledge")
	assert.Contains(t, body, "**rows:** `type` × **cols:** `status`")
	assert.Contains(t, body, "| `finding` |")
}

// TestComposePivot_KeysetPaged asserts BOTH pivot fetch arms (match-all and
// by-type) read in bounded keyset pages rather than one Limit-0 whole-graph
// fetch. AfterId PRESENCE is asserted rather than its value: presence is what
// selects the keyset browse, and an omitted cursor pages in the backend's
// default order, so the cursor taken from page 1 would skip every lower id.
// SkipTotal is load-bearing too — the bounded-reads census classifies a
// Limit-without-SkipTotal page as browse_no_skip_total.
func TestComposePivot_KeysetPaged(t *testing.T) {
	assertPagePlans := func(t *testing.T, plans []*knowledgev1.QueryPlan) {
		t.Helper()
		nodePlans := 0
		for i, p := range plans {
			if p.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
				continue
			}
			nodePlans++
			assert.Equal(t, int32(paging.BrowsePageSize), p.GetLimit(), "plan[%d] carries the page limit", i)
			assert.NotNil(t, p.AfterId, "plan[%d] SETS the keyset cursor (presence selects the keyset browse)", i)
			assert.True(t, p.GetSkipTotal(), "plan[%d] skips Total", i)
		}
		assert.Positive(t, nodePlans, "no node plans recorded — the probe is broken")
	}

	nodes := []knowledgev1.Node{
		{Id: "1", Type: "finding", Status: "open"},
		{Id: "2", Type: "decision", Status: "closed"},
	}

	t.Run("match-all arm", func(t *testing.T) {
		f := &corrFake{nodes: nodes}
		res := composePivot(context.Background(), newCorrFakeDeps(f), queryArgs{Mode: "pivot", Rows: "type", Cols: "status"})
		require.False(t, res.IsError, textBodyTools(res))
		assertPagePlans(t, f.plans)
	})

	t.Run("by-type arm", func(t *testing.T) {
		f := &corrFake{nodes: nodes}
		res := composePivot(context.Background(), newCorrFakeDeps(f), queryArgs{Mode: "pivot", Rows: "type", Cols: "status", Type: "finding"})
		require.False(t, res.IsError, textBodyTools(res))
		assertPagePlans(t, f.plans)
	})
}

// TestComposePivot_Validation asserts the rows/cols required + must-differ guards.
func TestComposePivot_Validation(t *testing.T) {
	f := &corrFake{}
	deps := newCorrFakeDeps(f)
	res := composePivot(context.Background(), deps, queryArgs{Mode: "pivot", Rows: "type"})
	assert.Contains(t, textBodyTools(res), "pivot requires rows and cols")

	res = composePivot(context.Background(), deps, queryArgs{Mode: "pivot", Rows: "type", Cols: "type"})
	assert.Contains(t, textBodyTools(res), "rows and cols must differ")
}

// TestRenderCorrelations_Golden is the engine renderer golden.
func TestRenderCorrelations_Golden(t *testing.T) {
	rows := []engine.CorrelationEdgeRow{
		{Edge: knowledgev1.Edge{FromId: "a", ToId: "b", Type: "correlates-with", Confidence: 0.91, Method: "cooccur"},
			FromName: "Alpha", ToName: "Bravo", FromType: "finding", ToType: "decision"},
	}
	got := engine.RenderCorrelations("knowledge", rows, 1, false, nil)
	assert.Contains(t, got, "## Correlations — knowledge")
	assert.Contains(t, got, "| `Alpha` [finding] | correlates-with | `Bravo` [decision] | 0.91 | cooccur |")
}

// TestRenderPivotMatrix_Golden is the engine pivot renderer golden.
func TestRenderPivotMatrix_Golden(t *testing.T) {
	m := engine.BuildPivotMatrix([]*knowledgev1.Node{
		{Type: "finding", Status: "open"},
		{Type: "finding", Status: "open"},
		{Type: "decision", Status: "closed"},
	}, "type", "status")
	got := engine.RenderPivotMatrix("knowledge", m)
	assert.Contains(t, got, "## Pivot — knowledge")
	assert.Contains(t, got, "| row ↓ / status → |")
	assert.Contains(t, got, "| `finding` |")
	assert.Contains(t, got, "**total**")
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
