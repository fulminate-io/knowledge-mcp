// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// countingExec records every request it receives so a test can assert the exact
// read COST of an enrichment, not merely its result. Modeled on the
// fakeTraverseEdgesCaller idiom in package tools rather than inventing a third
// fake shape.
type countingExec struct {
	calls       int
	edgeReqs    []*knowledgev1.QueryPlan
	hydrateReqs []*knowledgev1.QueryPlan
	siblings    []knowledgev1.Edge
	nodes       []*knowledgev1.Node
}

func (c *countingExec) fn() ExecuteFn {
	return func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		c.calls++
		plan := req.GetQuery()
		if plan.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
			c.edgeReqs = append(c.edgeReqs, plan)
			return &knowledgev1.ExecuteResponse{Edges: edgesToProtoForTest(c.siblings)}, nil
		}
		c.hydrateReqs = append(c.hydrateReqs, plan)
		return &knowledgev1.ExecuteResponse{Nodes: c.nodes}, nil
	}
}

func TestEnrichCandidateGroups(t *testing.T) {
	const src = "a/x.go:Caller"
	const key = "a/x.go:1042:CALLS:Run"
	const otherKey = "a/x.go:2000:CALLS:Run"

	// Distinct concrete content per candidate, so a fixture that collapsed them
	// into one is distinguishable from a correct multi-member group.
	memberEdge := func(from, to, evidence string, conf float64) knowledgev1.Edge {
		return knowledgev1.Edge{
			FromId: from, ToId: to, Type: "CALLS",
			Method: kgtypes.EdgeMethodAmbiguousName, Evidence: evidence, Confidence: conf,
		}
	}
	node := func(id, file, sig string, line int32) *knowledgev1.Node {
		return &knowledgev1.Node{Id: id, SymbolName: "Run", Type: "function", FilePath: file, StartLine: line, Signature: sig}
	}

	t.Run("complete_group_issues_zero_executes", func(t *testing.T) {
		// THE COST CATCHER: an implementation that enriches unconditionally would
		// add two Executes to EVERY forward traversal in the product.
		groups := []CandidateGroup{{
			FromID: src, EdgeType: "CALLS", Method: kgtypes.EdgeMethodAmbiguousName, Key: key, Declared: 2,
			Members: []knowledgev1.Edge{
				memberEdge(src, "p/a.go:Run", key, 0.5),
				memberEdge(src, "p/b.go:Run", key, 0.5),
			},
		}}
		require.True(t, groups[0].Complete())

		fake := &countingExec{}
		out, nodes, err := EnrichCandidateGroups(context.Background(), fake.fn(), groups, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, fake.calls, "a complete group must issue ZERO reads")
		assert.Nil(t, nodes)
		require.Len(t, out, 1)
		assert.Len(t, out[0].Members, 2)
	})

	t.Run("incomplete_group_adopts_siblings_by_evidence", func(t *testing.T) {
		// THE EXACTNESS CATCHER: a same-source filter that ignored Evidence would
		// merge two DISTINCT references into one group, destroying the very
		// distinction the reconstruction's negative control protects.
		groups := []CandidateGroup{{
			FromID: src, EdgeType: "CALLS", Method: kgtypes.EdgeMethodAmbiguousName, Key: key, Declared: 3,
			Members: []knowledgev1.Edge{memberEdge(src, "p/a.go:Run", key, 1.0/3.0)},
		}}
		require.False(t, groups[0].Complete())

		fake := &countingExec{siblings: []knowledgev1.Edge{
			memberEdge(src, "p/b.go:Run", key, 1.0/3.0),  // same reference
			memberEdge(src, "p/c.go:Run", key, 1.0/3.0),  // same reference
			memberEdge(src, "q/d.go:Run", otherKey, 0.5), // DIFFERENT reference
			memberEdge(src, "q/e.go:Run", otherKey, 0.5), // DIFFERENT reference
		}}
		out, _, err := EnrichCandidateGroups(context.Background(), fake.fn(), groups, nil)
		require.NoError(t, err)
		require.Len(t, out, 1)

		got := map[string]bool{}
		for mi := range out[0].Members {
			m := &out[0].Members[mi]
			got[m.ToId] = true
		}
		assert.Len(t, out[0].Members, 3, "exactly the three members of THIS reference")
		assert.True(t, got["p/a.go:Run"] && got["p/b.go:Run"] && got["p/c.go:Run"])
		assert.False(t, got["q/d.go:Run"], "a foreign group key must never be adopted")
		assert.False(t, got["q/e.go:Run"], "a foreign group key must never be adopted")
		assert.True(t, out[0].Complete())
	})

	t.Run("hydrate_is_one_bulk_call", func(t *testing.T) {
		// THE N+1 CATCHER.
		groups := []CandidateGroup{
			{
				FromID: src, EdgeType: "CALLS", Method: kgtypes.EdgeMethodAmbiguousName, Key: key, Declared: 3,
				Members: []knowledgev1.Edge{
					memberEdge(src, "p/a.go:Run", key, 1.0/3.0),
					memberEdge(src, "p/b.go:Run", key, 1.0/3.0),
				},
			},
			{
				FromID: "a/y.go:Other", EdgeType: "CALLS", Method: kgtypes.EdgeMethodAmbiguousName, Key: otherKey, Declared: 3,
				Members: []knowledgev1.Edge{
					memberEdge("a/y.go:Other", "p/c.go:Run", otherKey, 1.0/3.0),
					memberEdge("a/y.go:Other", "p/d.go:Run", otherKey, 1.0/3.0),
					memberEdge("a/y.go:Other", "p/e.go:Run", otherKey, 1.0/3.0),
				},
			},
		}
		fake := &countingExec{nodes: []*knowledgev1.Node{
			node("p/a.go:Run", "p/a.go", "func Run(ctx context.Context) error", 10),
			node("p/b.go:Run", "p/b.go", "func Run(n int) string", 20),
			node("p/c.go:Run", "p/c.go", "func Run()", 30),
			node("p/d.go:Run", "p/d.go", "func Run(a, b int) bool", 40),
			node("p/e.go:Run", "p/e.go", "func Run(s string)", 50),
		}}
		_, nodes, err := EnrichCandidateGroups(context.Background(), fake.fn(), groups, nil)
		require.NoError(t, err)

		require.Len(t, fake.hydrateReqs, 1, "exactly ONE bulk hydrate, never one per candidate")
		ids := map[string]bool{}
		for _, id := range fake.hydrateReqs[0].GetIds() {
			ids[id] = true
		}
		for _, want := range []string{"p/a.go:Run", "p/b.go:Run", "p/c.go:Run", "p/d.go:Run", "p/e.go:Run"} {
			assert.True(t, ids[want], "candidate %s must ride the single hydrate", want)
		}
		assert.Len(t, nodes, 5)
	})

	t.Run("short_read_leaves_group_incomplete_and_fabricates_nothing", func(t *testing.T) {
		// THE NO-PADDING CATCHER: an implementation padding to Declared would make
		// the render look whole while naming candidates that do not exist.
		groups := []CandidateGroup{{
			FromID: src, EdgeType: "CALLS", Method: kgtypes.EdgeMethodAmbiguousName, Key: key, Declared: 4,
			Members: []knowledgev1.Edge{memberEdge(src, "p/a.go:Run", key, 0.25)},
		}}
		fake := &countingExec{siblings: []knowledgev1.Edge{
			memberEdge(src, "p/b.go:Run", key, 0.25),
		}}
		out, _, err := EnrichCandidateGroups(context.Background(), fake.fn(), groups, nil)
		require.NoError(t, err)
		require.Len(t, out, 1)

		assert.Len(t, out[0].Members, 2, "only what was actually read")
		assert.False(t, out[0].Complete(), "2 of 4 stays incomplete")
		assert.Equal(t, 4, out[0].Declared, "the DECLARED count is preserved, never shrunk to the observed one")
		for mi := range out[0].Members {
			m := &out[0].Members[mi]
			assert.NotEmpty(t, m.ToId, "no synthesized member with an empty ToId")
		}
	})
}
