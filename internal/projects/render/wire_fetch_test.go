// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// fakeGc is the carrier-backed fake: it implements BOTH Call
// (the legacy GraphCaller surface — unused by the repointed helpers but kept so
// fakeGc still satisfies GraphCaller) AND Execute (the carrier seam the helpers
// type-assert). Tests seed wire-node / knowledgev1.Edge fixtures; Execute emits them
// via the typed Nodes / Edges carriers per request shape.
type fakeGc struct {
	nodes   []*knowledgev1.Node // returned for a ByID (RETURN_MODE_NODES) plan.
	edges   []knowledgev1.Edge  // returned for a RETURN_MODE_EDGES plan.
	execErr error
	calls   int
}

func newFakeGc() *fakeGc { return &fakeGc{} }

func (f *fakeGc) seedNode(n *knowledgev1.Node) *fakeGc {
	f.nodes = []*knowledgev1.Node{
		n,
	}
	return f
}

func (f *fakeGc) seedEdges(edges ...knowledgev1.Edge) *fakeGc {
	f.edges = edges
	return f
}

func (f *fakeGc) seedExecErr(err error) *fakeGc {
	f.execErr = err
	return f
}

// Call satisfies GraphCaller. The repointed wire-fetch helpers use Execute, so
// this is exercised only by other render-package callers (assemble/test_plan).
func (f *fakeGc) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.TextResult(""), nil
}

// Execute satisfies render.Executor — returns the seeded carrier per plan shape.
func (f *fakeGc) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.calls++
	if f.execErr != nil {
		return nil, f.execErr
	}
	if req.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: edgesToProtoForTest(f.edges)}, nil
	}
	return enginetest.ResponseWithNodes(f.nodes...), nil
}

func TestMarshalQueryByID(t *testing.T) {
	raw := MarshalQueryByID("abc123")
	var parsed struct {
		ID                string `json:"id"`
		IncludeTombstones bool   `json:"include_tombstones"`
	}
	require.NoError(t, json.Unmarshal(raw, &parsed))
	assert.Equal(t, "abc123", parsed.ID)
	assert.True(t, parsed.IncludeTombstones, "include_tombstones must default to true")
}

func TestFetchNode_DecodesCarrier(t *testing.T) {
	gc := newFakeGc().seedNode(&knowledgev1.Node{
		Id: "n1", Type: string(kgtypes.NodeType("ticket")), SymbolName: "T-1",
		Description: "desc", Summary: "sum", Status: "open",
		Metadata: map[string]string{"k": "v"},
	})
	node, err := FetchNode(context.Background(), gc, "n1")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, "n1", node.Id)
	assert.Equal(t, string(kgtypes.NodeType("ticket")), node.Type)
	assert.Equal(t, "T-1", node.SymbolName)
	assert.Equal(t, "desc", node.Description)
	assert.Equal(t, "open", node.Status)
	assert.Equal(t, "v", kgtypes.Value(node, "k"))
	assert.Equal(t, 1, gc.calls)
}

func TestFetchNode_NotFoundReturnsZero(t *testing.T) {
	gc := newFakeGc() // no seeded node → empty nodes_json.
	node, err := FetchNode(context.Background(), gc, "missing")
	require.NoError(t, err)
	assert.Nil(t, node)
}

func TestFetchNode_PropagatesTransportError(t *testing.T) {
	gc := newFakeGc().seedExecErr(errors.New("boom"))
	_, err := FetchNode(context.Background(), gc, "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// TestFetchNode_RequiresExecutor + the callOnlyGc fake were removed — the
// GraphCaller interface now REQUIRES Execute (it is identical to Executor), so a
// "Call-only, not an Executor" GraphCaller is no longer constructible. The
// asExecutor upgrade-or-error path survives as a defensive seam but cannot be
// exercised by a non-Executor input anymore.

func TestIterEdges_ReconstructsFromCarrier(t *testing.T) {
	gc := newFakeGc().seedEdges(
		knowledgev1.Edge{FromId: "pivot", ToId: "child1", Type: "contains"},
		knowledgev1.Edge{FromId: "pivot", ToId: "child2", Type: "contains"},
		knowledgev1.Edge{FromId: "parent1", ToId: "pivot", Type: "contained-by"},
	)
	edges, err := IterEdges(context.Background(), gc, "pivot", kgwire.BothEdges)
	require.NoError(t, err)
	require.Len(t, edges, 3)
	assert.Equal(t, "pivot", edges[0].FromId)
	assert.Equal(t, "child1", edges[0].ToId)
	assert.Equal(t, "contains", edges[0].Type)
	assert.Equal(t, "parent1", edges[2].FromId)
	assert.Equal(t, "pivot", edges[2].ToId)
}

func TestIterEdges_DirectionFilterOutgoing(t *testing.T) {
	gc := newFakeGc().seedEdges(
		knowledgev1.Edge{FromId: "pivot", ToId: "c", Type: "contains"},
		knowledgev1.Edge{FromId: "p", ToId: "pivot", Type: "contained-by"},
	)
	edges, err := IterEdges(context.Background(), gc, "pivot", kgwire.OutgoingEdges)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "pivot", edges[0].FromId)
	assert.Equal(t, "c", edges[0].ToId)
}

func TestIterEdges_DirectionFilterIncoming(t *testing.T) {
	gc := newFakeGc().seedEdges(
		knowledgev1.Edge{FromId: "pivot", ToId: "c", Type: "contains"},
		knowledgev1.Edge{FromId: "p", ToId: "pivot", Type: "contained-by"},
	)
	edges, err := IterEdges(context.Background(), gc, "pivot", kgwire.IncomingEdges)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "p", edges[0].FromId)
	assert.Equal(t, "pivot", edges[0].ToId)
}

func TestIterEdges_TypeFilter(t *testing.T) {
	gc := newFakeGc().seedEdges(
		knowledgev1.Edge{FromId: "pivot", ToId: "c1", Type: "contains"},
		knowledgev1.Edge{FromId: "pivot", ToId: "d1", Type: "depends-on"},
		knowledgev1.Edge{FromId: "pivot", ToId: "c2", Type: "contains"},
	)
	edges, err := IterEdges(context.Background(), gc, "pivot", kgwire.BothEdges, kgtypes.EdgeType("contains"))
	require.NoError(t, err)
	require.Len(t, edges, 2)
	for _, e := range edges {
		assert.Equal(t, "contains", e.Type)
	}
}

func TestIterEdges_EmptyResponseReturnsNil(t *testing.T) {
	gc := newFakeGc() // no seeded edges.
	edges, err := IterEdges(context.Background(), gc, "pivot", kgwire.BothEdges)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

func TestIterEdges_NilGcShortCircuits(t *testing.T) {
	edges, err := IterEdges(context.Background(), nil, "pivot", kgwire.BothEdges)
	require.NoError(t, err)
	assert.Nil(t, edges)
}

func TestProxyAnnotation_NonProxyReturnsEmpty(t *testing.T) {
	n := &knowledgev1.Node{Id: "n1", Type: string(kgtypes.NodePlan)}
	assert.Empty(t, proxyAnnotation(n))
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", truncate("short", 10))
	assert.Equal(t, "tru...", truncate("truncated", 3))
}
