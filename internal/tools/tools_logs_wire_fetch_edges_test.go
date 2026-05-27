// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// scriptedEdgesFakeCaller answers the graph-wide-edges composition over the
// Execute carrier seam (T-GTB6): (1) a Match-all RETURN_MODE_NODES enumeration
// (nodes_json carrier), then (2) a RETURN_MODE_EDGES ids[]→union read
// (edges_json carrier). Records every Execute request so tests can verify the
// bounded 2-Execute contract (no per-node N+1).
type scriptedEdgesFakeCaller struct {
	nodes []knowledgev1.Node
	edges []*knowledgev1.Edge
	execs []*knowledgev1.ExecuteRequest
}

// Call satisfies the interface; fetchAllLogEdges routes through Execute.
func (f *scriptedEdgesFakeCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (f *scriptedEdgesFakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.execs = append(f.execs, req)
	if req.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: f.edges}, nil
	}
	resp := enginetest.ResponseWithNodes(nodePtrs(f.nodes)...)
	return resp, nil
}

// TestFetchAllLogEdges_TwoExecAllMetadata asserts the bounded 2-Execute contract
// (Match-all nodes, then RETURN_MODE_EDGES union) and the end-to-end metadata
// round-trip through the edges_json carrier.
func TestFetchAllLogEdges_TwoExecAllMetadata(t *testing.T) {
	gc := &scriptedEdgesFakeCaller{
		nodes: []knowledgev1.Node{{Id: "tpl-a"}, {Id: "tpl-b"}, {Id: "chunk-1"}},
		edges: []*knowledgev1.Edge{
			{
				FromId: "tpl-a", ToId: "tpl-b", Type: string(kgtypes.EdgeType("CORRELATES_WITH")),
				Confidence: 0.92, Method: "co-occurrence", Weight: 3.5, Evidence: "joint-hits=12",
				LastValidated: time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC).UnixNano(),
			},
			{FromId: "tpl-a", ToId: "chunk-1", Type: string(kgtypes.EdgeType("CONTAINS"))},
		},
	}

	out, err := fetchAllLogEdges(context.Background(), gc, "q-fixture",
		[]kgtypes.EdgeType{"CORRELATES_WITH", "CONTAINS"})
	require.NoError(t, err)
	assert.Len(t, gc.execs, 2, "exactly two Execute calls (node enumeration + edge union), no N+1")
	require.Len(t, out, 2)

	corr := &out[0]
	assert.Equal(t, "tpl-a", corr.FromId)
	assert.Equal(t, "tpl-b", corr.ToId)
	assert.Equal(t, string(kgtypes.EdgeType("CORRELATES_WITH")), corr.Type)
	assert.InDelta(t, 0.92, corr.Confidence, 0.001)
	assert.Equal(t, "co-occurrence", corr.Method)
	assert.InDelta(t, 3.5, corr.Weight, 0.001)
	assert.Equal(t, "joint-hits=12", corr.Evidence)
	assert.NotZero(t, corr.LastValidated, "LastValidated must thread through")

	contains := &out[1]
	assert.Equal(t, string(kgtypes.EdgeType("CONTAINS")), contains.Type)
	assert.InDelta(t, 0.0, contains.Confidence, 1e-9, "no edge_metadata → zero defaults")

	// Verify the second Execute is the RETURN_MODE_EDGES union carrying the
	// edge-type filter + every node id.
	require.GreaterOrEqual(t, len(gc.execs), 2)
	edgesQ := gc.execs[1].GetQuery()
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_EDGES, edgesQ.GetReturnMode())
	assert.ElementsMatch(t, []string{"tpl-a", "tpl-b", "chunk-1"}, edgesQ.GetSelection().GetIds())
	assert.ElementsMatch(t, []string{"CORRELATES_WITH", "CONTAINS"}, edgesQ.GetSelection().GetEdgeTypes())
}

// TestFetchAllLogEdges_EmptyResponse asserts no nodes → no panic, empty slice
// (and the edge-union Execute is skipped when there are zero nodes).
func TestFetchAllLogEdges_EmptyResponse(t *testing.T) {
	gc := &scriptedEdgesFakeCaller{}
	out, err := fetchAllLogEdges(context.Background(), gc, "q-empty", nil)
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Len(t, gc.execs, 1, "empty node enumeration short-circuits before the edge union")
}
