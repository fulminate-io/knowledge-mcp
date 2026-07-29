// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// scriptedEdgesFakeCaller answers the match-all edges read over the Execute
// carrier seam: a RETURN_MODE_EDGES plan with no pivot discriminant, answered
// from the edges_json carrier. Records every Execute request so tests can verify
// the bounded 1-Execute contract (no node enumeration, no per-node N+1).
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

// TestFetchAllLogEdges_OneExecAllMetadata asserts the bounded 1-Execute contract
// (a single match-all RETURN_MODE_EDGES read, no node enumeration) and the
// end-to-end metadata round-trip through the edges_json carrier.
func TestFetchAllLogEdges_OneExecAllMetadata(t *testing.T) {
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
	assert.Len(t, gc.execs, 1, "exactly one Execute call (the match-all edge read), no node enumeration, no N+1")
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

	// The one Execute is the match-all RETURN_MODE_EDGES read: the edge-type
	// filter rides, and NO node ids are sent — the empty pivot discriminant is
	// what makes the plan mean "every edge of the graph".
	require.Len(t, gc.execs, 1)
	edgesQ := gc.execs[0].GetQuery()
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_EDGES, edgesQ.GetReturnMode())
	assert.Empty(t, edgesQ.GetSelection().GetIds(), "match-all plan must carry no pivot ids")
	assert.Empty(t, edgesQ.GetIds(), "match-all plan must carry no pivot ids")
	assert.Empty(t, edgesQ.GetById(), "match-all plan must carry no by-id pivot")
	assert.Empty(t, edgesQ.GetSelection().GetFromId(), "match-all plan must carry no from_id pivot")
	assert.ElementsMatch(t, []string{"CORRELATES_WITH", "CONTAINS"}, edgesQ.GetSelection().GetEdgeTypes())
}

// TestFetchAllLogEdges_EmptyResponse asserts an empty log graph → no panic,
// empty slice, still exactly one Execute (the match-all read answers "no edges"
// itself — there is no node enumeration left to short-circuit on).
func TestFetchAllLogEdges_EmptyResponse(t *testing.T) {
	gc := &scriptedEdgesFakeCaller{}
	out, err := fetchAllLogEdges(context.Background(), gc, "q-empty", nil)
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Len(t, gc.execs, 1, "one match-all edge read, nothing else")
}
