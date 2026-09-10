// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestToCollectResult_RoundTrip drives the full envelope path: a JSON result
// round-trips through encoding/json into a *Result, then ToCollectResult yields
// a *collectorwire.CollectResult whose node/edge fields carry the provider's
// values and whose GraphType/GraphName carry the CLIENT-supplied family and
// instance.
func TestToCollectResult_RoundTrip(t *testing.T) {
	raw := `{
		"walk_complete": true,
		"nodes": [
			{
				"id": "ISSUE-1",
				"type": "issue",
				"symbol_name": "Login broken",
				"start_line": 10,
				"end_line": 20,
				"is_exported": true,
				"metadata": {"priority": "high"}
			}
		],
		"edges": [
			{"from_id": "ISSUE-1", "to_id": "ISSUE-2", "type": "blocks", "weight": 1.5, "confidence": 0.9, "method": "linker"}
		]
	}`

	var r Result
	require.NoError(t, json.Unmarshal([]byte(raw), &r))

	cr, err := r.ToCollectResult("jira", "acme-board")
	require.NoError(t, err)
	require.NotNil(t, cr)

	assert.Equal(t, kgtypes.GraphType("jira"), cr.GraphType)
	assert.Equal(t, "acme-board", cr.GraphName)
	assert.True(t, cr.WalkComplete, "the provider's completeness assertion must ride through to the wire's walk_complete")

	require.Len(t, cr.Nodes, 1)
	n := cr.Nodes[0]
	assert.Equal(t, "ISSUE-1", n.GetId())
	assert.Equal(t, "issue", n.GetType())
	assert.Equal(t, "Login broken", n.GetSymbolName())
	assert.Equal(t, int32(10), n.GetStartLine())
	assert.Equal(t, int32(20), n.GetEndLine())
	assert.True(t, n.GetIsExported())
	assert.Equal(t, "high", n.GetMetadata()["priority"])

	require.Len(t, cr.Edges, 1)
	e := cr.Edges[0]
	assert.Equal(t, -1, e.FromIdx)
	assert.Equal(t, -1, e.ToIdx)
	assert.Equal(t, "ISSUE-1", e.FromID)
	assert.Equal(t, "ISSUE-2", e.ToID)
	assert.Equal(t, kgtypes.EdgeType("blocks"), e.Type)
	assert.InEpsilon(t, 1.5, e.Weight, 1e-9)
	assert.InEpsilon(t, 0.9, e.Confidence, 1e-9)
	assert.Equal(t, "linker", e.Method)
}

// TestToCollectResult_FailsLoud confirms a malformed conversion returns a
// non-nil error and a nil result: an empty family, an empty instance, or a node
// with an empty type each fail loud rather than shipping a degenerate
// CollectResult.
func TestToCollectResult_FailsLoud(t *testing.T) {
	cases := map[string]struct {
		graphType string
		graphName string
		result    Result
	}{
		"empty graph family": {
			graphType: "",
			graphName: "n",
			result:    Result{Nodes: []Node{{ID: "a", Type: "issue"}}},
		},
		"empty graph instance": {
			graphType: "jira",
			graphName: "",
			result:    Result{Nodes: []Node{{ID: "a", Type: "issue"}}},
		},
		"empty node type": {
			graphType: "jira",
			graphName: "n",
			result:    Result{Nodes: []Node{{ID: "a", Type: ""}}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := tc.result
			cr, err := r.ToCollectResult(tc.graphType, tc.graphName)
			require.Error(t, err)
			assert.Nil(t, cr)
		})
	}
}

// TestToCollectResult_IncompleteWalkRidesThrough pins the OTHER value of the
// completeness assertion: false is carried, not smoothed to true. It is the
// signal that disables the deletion phase, so a conversion that dropped it would
// silently enable deletion on an unverified walk.
func TestToCollectResult_IncompleteWalkRidesThrough(t *testing.T) {
	r := Result{Nodes: []Node{{ID: "a", Type: "issue"}}, WalkComplete: false}
	cr, err := r.ToCollectResult("jira", "board")
	require.NoError(t, err)
	assert.False(t, cr.WalkComplete)
}
