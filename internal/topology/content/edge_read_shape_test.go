// SPDX-License-Identifier: Apache-2.0

package content

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// edge_read_shape_test.go — pins WHICH bulk edge read each content analyzer
// issues. The content analyzers index every node of the graph, so their edge
// read is the match-all form (no pivot); the one analyzer that can narrow its
// node set (degree-histogram, via Request.Subset) keeps the pivot form for that
// case so it does not pull edges it will discard.

// recordingCaller wraps fakeCaller and captures every RETURN_MODE_EDGES plan it
// is asked to serve, so a test can assert the read SHAPE rather than only the
// analyzer's output.
type recordingCaller struct {
	*fakeCaller
	edgePlans []*knowledgev1.QueryPlan
}

func (r *recordingCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if q := req.GetQuery(); q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		r.edgePlans = append(r.edgePlans, q)
	}
	return r.fakeCaller.Execute(ctx, req)
}

// hubFixture is the degree-histogram fixture: one hub with 50 outgoing
// reference edges to 50 leaves.
func hubFixture() *fakeCaller {
	nodes := []*knowledgev1.Node{mkNode("hub", "page", "hub")}
	var edges []*knowledgev1.Edge
	for i := range 50 {
		leafID := fmt.Sprintf("leaf-%d", i)
		nodes = append(nodes, mkNode(leafID, "page", leafID))
		edges = append(edges, refEdge("hub", leafID))
	}
	return &fakeCaller{nodes: nodes, edges: edges}
}

// TestDegreeHistogram_EdgeReadShape asserts the conditional split: no Subset →
// match-all plan (no pivot ids); Subset set → pivot plan carrying exactly the
// surviving ids.
func TestDegreeHistogram_EdgeReadShape(t *testing.T) {
	t.Run("no subset sends the match-all plan", func(t *testing.T) {
		rc := &recordingCaller{fakeCaller: hubFixture()}
		r := req(rc.fakeCaller, nil)
		r.Caller = rc
		_, err := DegreeHistogramAnalyzer{}.Run(context.Background(), r)
		require.NoError(t, err)
		require.Len(t, rc.edgePlans, 1, "one bulk edge read")
		assert.Empty(t, rc.edgePlans[0].GetIds(), "no-subset build must send no pivot ids")
		assert.Empty(t, rc.edgePlans[0].GetById())
		assert.Empty(t, rc.edgePlans[0].GetSelection().GetFromId())
	})

	t.Run("subset keeps the pivot plan", func(t *testing.T) {
		rc := &recordingCaller{fakeCaller: hubFixture()}
		r := req(rc.fakeCaller, nil)
		r.Caller = rc
		r.Subset = func(n *knowledgev1.Node) bool { return n.GetId() == "hub" || n.GetId() == "leaf-0" }
		_, err := DegreeHistogramAnalyzer{}.Run(context.Background(), r)
		require.NoError(t, err)
		require.Len(t, rc.edgePlans, 1)
		assert.ElementsMatch(t, []string{"hub", "leaf-0"}, rc.edgePlans[0].GetIds(),
			"a subset build pivots on exactly the surviving ids")
	})
}

// TestDegreeHistogram_SubsetRowsUnchangedByReadShape is the equivalence check
// behind the split: the histogram computed over a subset is the same whichever
// read served it, because the tally ignores an edge whose source is not a
// materialized row. Running the same subset against both fixtures — one where
// the fake answers the pivot plan, one where a match-all plan would hand over
// every edge — must yield identical metrics.
func TestDegreeHistogram_SubsetRowsUnchangedByReadShape(t *testing.T) {
	subset := func(n *knowledgev1.Node) bool { return n.GetId() == "hub" || n.GetId() == "leaf-0" }

	pivotReq := req(hubFixture(), nil)
	pivotReq.Subset = subset
	pivotFindings, err := DegreeHistogramAnalyzer{}.Run(context.Background(), pivotReq)
	require.NoError(t, err)
	require.Len(t, pivotFindings, 1)

	// A caller that serves EVERY edge regardless of the plan's pivots stands in
	// for the match-all read; the analyzer must reach the same numbers.
	allEdges := &allEdgesCaller{fakeCaller: hubFixture()}
	allReq := req(allEdges.fakeCaller, nil)
	allReq.Caller = allEdges
	allReq.Subset = subset
	allFindings, err := DegreeHistogramAnalyzer{}.Run(context.Background(), allReq)
	require.NoError(t, err)
	require.Len(t, allFindings, 1)

	assert.Equal(t, pivotFindings[0].Metrics, allFindings[0].Metrics,
		"the tally ignores edges whose source is not materialized, so the read shape cannot change the histogram")
}

// allEdgesCaller answers every RETURN_MODE_EDGES plan with the WHOLE edge set,
// ignoring any pivot — the broadest thing a match-all read can hand back.
type allEdgesCaller struct{ *fakeCaller }

func (a *allEdgesCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if req.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: a.edges}, nil
	}
	return a.fakeCaller.Execute(ctx, req)
}

// TestStructuralMotif_EdgeReadShape asserts the motif analyzer sends the
// match-all plan (it always indexes every node) while keeping its edge-type
// filter — Subset narrows which ROOTS participate, never which edges are read.
func TestStructuralMotif_EdgeReadShape(t *testing.T) {
	rc := &recordingCaller{fakeCaller: buildMotifFixture()}
	r := req(rc.fakeCaller, map[string]string{"root_types": "section"})
	r.Caller = rc
	_, err := StructuralMotifAnalyzer{}.Run(context.Background(), r)
	require.NoError(t, err)
	require.Len(t, rc.edgePlans, 1, "one bulk edge read")
	assert.Empty(t, rc.edgePlans[0].GetIds(), "motif build must send no pivot ids")
	assert.Equal(t, []string{string(kgtypes.EdgeContains)}, rc.edgePlans[0].GetSelection().GetEdgeTypes(),
		"the contains-only edge-type filter still rides the match-all plan")
}
