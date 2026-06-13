// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// dependsOnCaller answers a node-SET RETURN_MODE_EDGES Execute over a seeded edge
// list, honoring Forward like the real server: Forward=&true returns only edges
// whose FromId is one of the pivot ids (outgoing-only); unset Forward returns any
// edge touching a pivot (both directions). It records the last plan + call count.
type dependsOnCaller struct {
	edges     []knowledgev1.Edge
	execCalls int
	lastPlan  *knowledgev1.QueryPlan
}

func (d *dependsOnCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.TextResult(""), nil
}

func (d *dependsOnCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	d.execCalls++
	q := req.GetQuery()
	d.lastPlan = q
	pivots := map[string]bool{}
	for _, id := range q.GetIds() {
		pivots[id] = true
	}
	outgoingOnly := q.GetForward() // Forward=&true → true
	var out []*knowledgev1.Edge
	for i := range d.edges {
		e := &d.edges[i]
		if outgoingOnly {
			if pivots[e.FromId] {
				out = append(out, &knowledgev1.Edge{FromId: e.FromId, ToId: e.ToId, Type: e.Type})
			}
			continue
		}
		if pivots[e.FromId] || pivots[e.ToId] {
			out = append(out, &knowledgev1.Edge{FromId: e.FromId, ToId: e.ToId, Type: e.Type})
		}
	}
	return &knowledgev1.ExecuteResponse{Edges: out}, nil
}

// TestFetchDependsOnEdges asserts a single Execute carrying the node-SET ids,
// Forward=&true, RETURN_MODE_EDGES, IncludeTombstones, and the depends-on
// edge-type filter; the returned map keys each dependent to its first depends-on
// target; multiple outgoing edges keep the first; and an incoming edge to a pivot
// is NOT reflected (proving outgoing-only scoping).
func TestFetchDependsOnEdges(t *testing.T) {
	dep := func(from, to string) knowledgev1.Edge {
		return knowledgev1.Edge{FromId: from, ToId: to, Type: string(kgtypes.EdgeDependsOn)}
	}
	fc := &dependsOnCaller{
		edges: []knowledgev1.Edge{
			dep("a", "b"), // a's first outgoing depends-on
			dep("a", "c"), // a's second — must be ignored (first wins)
			dep("x", "a"), // incoming to a — must NOT become map[a] under outgoing-only
		},
	}
	ids := []string{"a", "b", "c", "x"}

	got, err := FetchDependsOnEdges(context.Background(), fc, ids)
	require.NoError(t, err)

	assert.Equal(t, 1, fc.execCalls, "exactly one Execute")
	plan := fc.lastPlan
	require.NotNil(t, plan)
	assert.Equal(t, ids, plan.GetIds(), "Ids must be the full node-id set")
	require.NotNil(t, plan.Forward)
	assert.True(t, plan.GetForward(), "Forward must be true (outgoing-only)")
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_EDGES, plan.GetReturnMode())
	assert.True(t, plan.GetIncludeTombstones(), "IncludeTombstones must be set")
	assert.Equal(t, []string{string(kgtypes.EdgeDependsOn)}, plan.GetSelection().GetEdgeTypes())

	assert.Equal(t, "b", got["a"], "map[a] is a's FIRST outgoing depends-on target")
	assert.Equal(t, "a", got["x"], "x's own outgoing depends-on is x→a")
	// The incoming x→a edge must not have set map[a] to anything via the incoming arm.
	assert.Equal(t, "b", got["a"], "incoming edge must not override a's outgoing target")
}
