// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// unionProbeSeed is one edge of EACH of the seven unifiedPivotEdgeTypes plus one edge
// of a type OUTSIDE that set. Both halves are load-bearing: the in-set edges catch a
// fake that under-serves, and the out-of-set edge catches a fake that "passes" by
// returning its whole seed unconditionally.
func unionProbeSeed() []*knowledgev1.Edge {
	e := func(t kgtypes.EdgeType, from, to string) *knowledgev1.Edge {
		return &knowledgev1.Edge{Type: string(t), FromId: from, ToId: to}
	}
	return []*knowledgev1.Edge{
		e(kgtypes.EdgeNext, "n1", "n2"),
		e(kgtypes.EdgeBranchesFrom, "b1", "b2"),
		e(kgtypes.EdgeRelatesTo, "r1", "r2"),
		e(kgtypes.EdgeProduced, "p1", "p2"),
		e(kgtypes.EdgeBecause, "c1", "c2"),
		e(kgtypes.EdgeKGContains, "s1", "t1"),
		e(kgtypes.EdgeChargedBy, "t1", "ch1"),
		// OUTSIDE unifiedPivotEdgeTypes — must NOT come back.
		e(kgtypes.EdgeSupports, "x1", "x2"),
	}
}

// unionProbeRequest is a RETURN_MODE_EDGES plan asking for the full seven-type set,
// pivoted on every endpoint the seed touches so node-set incidence keeps all of them.
// The ids mirror the tools-side probe, where they are required because one fake
// returns early for an id-less plan before its edges arm is reached.
func unionProbeRequest() *knowledgev1.ExecuteRequest {
	ets := make([]string, 0, len(unifiedPivotEdgeTypes))
	for _, t := range unifiedPivotEdgeTypes {
		ets = append(ets, string(t))
	}
	seen := map[string]bool{}
	var ids []string
	for _, e := range unionProbeSeed() {
		for _, id := range []string{e.GetFromId(), e.GetToId()} {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Ids:        ids,
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
			Selection:  &knowledgev1.Selection{EdgeTypes: ets},
		}},
	}
}

// assertUnionServed is the shared both-halves assertion: every one of the seven in-set
// seeded edges comes back, and the out-of-set edge does not.
func assertUnionServed(t *testing.T, got []*knowledgev1.Edge) {
	t.Helper()
	byType := map[string]bool{}
	for _, e := range got {
		byType[e.GetType()] = true
	}
	for _, want := range unifiedPivotEdgeTypes {
		assert.True(t, byType[string(want)],
			"the fake must serve the seeded %q edge for a seven-type request — a missing "+
				"type is the starvation defect", want)
	}
	assert.False(t, byType[string(kgtypes.EdgeSupports)],
		"the fake must NOT serve the out-of-set %q edge — returning the whole seed "+
			"unconditionally passes the positive half while being wrong the other way",
		kgtypes.EdgeSupports)
}

// TestEdgeFakes_ServeUnionOfRequestedTypes is the PROPERTY gate: it asserts the union
// contract on the FAKES THEMSELVES rather than on any one test's output. That is the
// point — the starvation this guards against is invisible through a test's assertions
// whenever those assertions never reach the dropped content.
//
// One subtest per converted fake, driven explicitly, so a fake that was never routed
// through unionEdgesForRequest fails its own named subtest instead of hiding inside an
// aggregate.
func TestEdgeFakes_ServeUnionOfRequestedTypes(t *testing.T) {
	req := unionProbeRequest()

	t.Run("watermarkLoopFake", func(t *testing.T) {
		f := newWatermarkLoopFake()
		f.unionSeed = unionProbeSeed()
		resp, err := f.Execute(context.Background(), req)
		require.NoError(t, err)
		assertUnionServed(t, resp.GetEdges())
	})
}
