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

// scriptedEdgesFakeCaller answers the two reads the bounded log edge fetch
// issues: a RETURN_MODE_IDS keyset browse over the seeded node ids, then a
// RETURN_MODE_EDGES pivot page answered from the edges_json carrier. Records
// every Execute request so tests can verify the read shape (no per-node N+1).
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
	switch req.GetQuery().GetReturnMode() {
	case knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		return &knowledgev1.ExecuteResponse{Edges: f.edges}, nil
	case knowledgev1.ReturnMode_RETURN_MODE_IDS:
		// A single page: the fixtures are far smaller than the drain's page
		// size, so the first short page ends the drain.
		if req.GetQuery().GetAfterId() != "" {
			return &knowledgev1.ExecuteResponse{}, nil
		}
		ids := make([]string, 0, len(f.nodes))
		for i := range f.nodes {
			ids = append(ids, f.nodes[i].Id)
		}
		return &knowledgev1.ExecuteResponse{Ids: ids}, nil
	}
	resp := enginetest.ResponseWithNodes(nodePtrs(f.nodes)...)
	return resp, nil
}

// TestFetchAllLogEdges_OneExecAllMetadata asserts the BOUNDED read shape — an id
// keyset browse followed by pivot-paged edge reads, each carrying an explicit
// positive limit — and the end-to-end metadata round-trip through the edges_json
// carrier. The single unbounded match-all read it used to issue is retired.
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
	// One id page (short, so the drain stops) plus one edge page for the three
	// seeded ids — bounded, and still nothing per-node.
	assert.Len(t, gc.execs, 2, "one id keyset page + one pivot edge page, no per-node N+1")
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

	// Read 1 is the id keyset browse; read 2 is the edge page, which names the
	// drained ids as its pivots and carries an explicit positive limit. Both
	// bounds are the point: an unlimited plan with no pivot is what was retired.
	require.Len(t, gc.execs, 2)
	idsQ := gc.execs[0].GetQuery()
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_IDS, idsQ.GetReturnMode())
	assert.Positive(t, idsQ.GetLimit(), "the id page must carry an explicit positive limit")

	edgesQ := gc.execs[1].GetQuery()
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_EDGES, edgesQ.GetReturnMode())
	assert.ElementsMatch(t, []string{"tpl-a", "tpl-b", "chunk-1"}, edgesQ.GetIds(),
		"the edge page pivots on the drained node ids")
	assert.Positive(t, edgesQ.GetLimit(), "the edge page must carry an explicit positive limit")
	assert.ElementsMatch(t, []string{"CORRELATES_WITH", "CONTAINS"}, edgesQ.GetSelection().GetEdgeTypes())
}

// TestFetchAllLogEdges_EmptyResponse asserts an empty log graph → no panic,
// empty slice, and exactly ONE Execute: the id browse comes back empty, so the
// edge drain short-circuits without asking for a single page.
func TestFetchAllLogEdges_EmptyResponse(t *testing.T) {
	gc := &scriptedEdgesFakeCaller{}
	out, err := fetchAllLogEdges(context.Background(), gc, "q-empty", nil)
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Len(t, gc.execs, 1, "the empty id browse alone — no edge page is requested")
}
