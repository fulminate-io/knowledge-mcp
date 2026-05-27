// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
)

// corrFake counts node-set fetches and edge fetches so the BOUNDED invariant
// (edge fetches == 1 regardless of node count) can be asserted.
type corrFake struct {
	nodeFetches int
	edgeFetches int
	nodes       []knowledgev1.Node
	edges       []*knowledgev1.Edge
}

func (f *corrFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		f.edgeFetches++
		return &knowledgev1.ExecuteResponse{Edges: f.edges}, nil
	}
	f.nodeFetches++
	ptrs := make([]*knowledgev1.Node, len(f.nodes))
	for i := range f.nodes {
		ptrs[i] = &f.nodes[i]
	}
	return enginetest.ResponseWithNodes(ptrs...), nil
}

// TestComposeCorrelations_OneBulkEdgeFetch is the BOUNDED guard (criterion
// b15042ed): the composer issues exactly ONE node-set fetch + ONE
// RETURN_MODE_EDGES over the collected ids[] — never a per-node edge fetch,
// regardless of node count.
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

	assert.Equal(t, 1, f.nodeFetches, "exactly one node-set fetch")
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

// TestComposePivot_Matrix drives the pivot path over a node-set, asserting the
// matrix render shape.
func TestComposePivot_Matrix(t *testing.T) {
	f := &corrFake{nodes: []knowledgev1.Node{
		{Id: "1", Type: "finding", Status: "open"},
		{Id: "2", Type: "finding", Status: "open"},
		{Id: "3", Type: "decision", Status: "closed"},
	}}
	res := composePivot(context.Background(), f.exec, queryArgs{Mode: "pivot", Rows: "type", Cols: "status"})
	require.False(t, res.IsError, textBodyTools(res))
	body := textBodyTools(res)
	assert.Contains(t, body, "## Pivot — knowledge")
	assert.Contains(t, body, "**rows:** `type` × **cols:** `status`")
	assert.Contains(t, body, "| `finding` |")
}

// TestComposePivot_Validation asserts the rows/cols required + must-differ guards.
func TestComposePivot_Validation(t *testing.T) {
	f := &corrFake{}
	res := composePivot(context.Background(), f.exec, queryArgs{Mode: "pivot", Rows: "type"})
	assert.Contains(t, textBodyTools(res), "pivot requires rows and cols")

	res = composePivot(context.Background(), f.exec, queryArgs{Mode: "pivot", Rows: "type", Cols: "type"})
	assert.Contains(t, textBodyTools(res), "rows and cols must differ")
}

// TestRenderCorrelations_Golden is the engine renderer golden (criterion 30f4abca).
func TestRenderCorrelations_Golden(t *testing.T) {
	rows := []engine.CorrelationEdgeRow{
		{Edge: knowledgev1.Edge{FromId: "a", ToId: "b", Type: "correlates-with", Confidence: 0.91, Method: "cooccur"},
			FromName: "Alpha", ToName: "Bravo", FromType: "finding", ToType: "decision"},
	}
	got := engine.RenderCorrelations("knowledge", rows)
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
