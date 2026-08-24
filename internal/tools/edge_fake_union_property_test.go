// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// unionProbeTypes is the seven-type unified pivot set, spelled with kgtypes constants
// because unifiedPivotEdgeTypes is unexported in the thought package and test symbols
// do not cross package boundaries.
var unionProbeTypes = []kgtypes.EdgeType{
	kgtypes.EdgeNext,
	kgtypes.EdgeBranchesFrom,
	kgtypes.EdgeRelatesTo,
	kgtypes.EdgeProduced,
	kgtypes.EdgeBecause,
	kgtypes.EdgeKGContains,
	kgtypes.EdgeChargedBy,
}

// unionProbeSeed is one edge of EACH of the seven unified types plus one edge of a
// type OUTSIDE that set. Both halves are load-bearing: the in-set edges catch a fake
// that under-serves, and the out-of-set edge catches a fake that "passes" by returning
// its whole seed unconditionally.
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
		// OUTSIDE the unified set — must NOT come back.
		e(kgtypes.EdgeSupports, "x1", "x2"),
	}
}

// unionProbeRequest is a RETURN_MODE_EDGES plan asking for the full seven-type set,
// pivoted on every endpoint the seed touches so node-set incidence keeps all of them.
//
// THE IDS ARE REQUIRED, not incidental: ctxCaller returns an empty response for any
// plan carrying no ids and no ById, before its edges arm is ever reached, so an
// id-less probe would measure that early return rather than the union contract.
func unionProbeRequest() *knowledgev1.ExecuteRequest {
	ets := make([]string, 0, len(unionProbeTypes))
	for _, t := range unionProbeTypes {
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

// assertUnionServed is the shared both-halves assertion.
func assertUnionServed(t *testing.T, got []*knowledgev1.Edge) {
	t.Helper()
	byType := map[string]bool{}
	for _, e := range got {
		byType[e.GetType()] = true
	}
	for _, want := range unionProbeTypes {
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

	t.Run("ctxCaller", func(t *testing.T) {
		// The live hazard: its arm returned ONLY chargeEdges for any request mentioning
		// charged-by, which a seven-type request does.
		c := &ctxCaller{unionSeed: unionProbeSeed()}
		resp, err := c.Execute(context.Background(), req)
		require.NoError(t, err)
		assertUnionServed(t, resp.GetEdges())
	})

	t.Run("chargeHydrateRecorder", func(t *testing.T) {
		// Its arm returned an EMPTY response for any set that was not exactly
		// {charged-by} — a multi-type request got nothing at all.
		r := &chargeHydrateRecorder{unionSeed: unionProbeSeed()}
		resp, err := r.Execute(context.Background(), req)
		require.NoError(t, err)
		assertUnionServed(t, resp.GetEdges())
	})
}
