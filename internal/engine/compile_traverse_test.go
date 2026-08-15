// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func TestCompileTraverse_DirectionOut(t *testing.T) {
	req, ok := compileTraverse(json.RawMessage(`{"start":"n1","direction":"out"}`))
	require.True(t, ok)
	q := req.GetQuery()
	require.NotNil(t, q.Forward)
	assert.True(t, q.GetForward(), "out → Forward=true")
	assert.Equal(t, []string{"n1"}, q.GetSelection().GetFromId())
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL, q.GetReturnMode())
}

func TestCompileTraverse_DirectionIn(t *testing.T) {
	req, ok := compileTraverse(json.RawMessage(`{"start":"n1","direction":"in"}`))
	require.True(t, ok)
	q := req.GetQuery()
	require.NotNil(t, q.Forward)
	assert.False(t, q.GetForward(), "in → Forward=false")
}

func TestCompileTraverse_DirectionBothNilForward(t *testing.T) {
	req, ok := compileTraverse(json.RawMessage(`{"start":"n1","direction":"both","depth":3}`))
	require.True(t, ok)
	q := req.GetQuery()
	assert.Nil(t, q.Forward, "both → Forward=nil (engine owns the union)")
	assert.Equal(t, int32(3), q.GetMaxHops())
}

func TestCompileTraverse_DefaultDirectionIsOut(t *testing.T) {
	req, ok := compileTraverse(json.RawMessage(`{"start":"n1"}`))
	require.True(t, ok)
	q := req.GetQuery()
	require.NotNil(t, q.Forward)
	assert.True(t, q.GetForward(), "empty direction defaults to out (validateDirection)")
}

// TestCompileTraverse_EdgeTypesCanonicalized pins the client-side
// per-graph edge-casing: the engine now uses edge_types AS-GIVEN, so the client
// canonicalizes them before they ride the wire. An absent graph defaults to
// knowledge → lowercase, so both tokens lower-case.
func TestCompileTraverse_EdgeTypesCanonicalized(t *testing.T) {
	req, ok := compileTraverse(json.RawMessage(`{"start":"n1","edge_types":["CALLS","contains"]}`))
	require.True(t, ok)
	// knowledge (default) → lowercase both tokens (client canonicalizes).
	assert.Equal(t, []string{"calls", "contains"}, req.GetQuery().GetSelection().GetEdgeTypes())
}

// TestCompileTraverse_EdgeTypesCanonicalizedCodeGraph pins the uppercase arm: a
// code-graph traverse uppercases the edge tokens client-side (CALLS), so a
// lowercase "calls" input matches the stored "CALLS" edge type.
func TestCompileTraverse_EdgeTypesCanonicalizedCodeGraph(t *testing.T) {
	req, ok := compileTraverse(json.RawMessage(`{"start":"sym","graph":"code","repo":"knowledge","edge_types":["calls"]}`))
	require.True(t, ok)
	assert.Equal(t, []string{"CALLS"}, req.GetQuery().GetSelection().GetEdgeTypes(),
		"code graph uppercases edge types client-side")
}

func TestCompileTraverse_CrossGraphTarget(t *testing.T) {
	req, ok := compileTraverse(json.RawMessage(`{"start":"sym","graph":"code","repo":"knowledge","edge_types":["CALLS"]}`))
	require.True(t, ok)
	assert.Equal(t, "code", req.GetTarget().GetGraph())
	assert.Equal(t, "knowledge", req.GetTarget().GetRepo())
}

// TestCompileTraverse_CustomGraph pins traverse selector threading for a
// registered custom graph type: graph + name reach the server-side resolver
// (compileTraverse gates only on graph=="logs"; a custom type passes). Guards a
// future closed-allowlist regression on the client read path.
func TestCompileTraverse_CustomGraph(t *testing.T) {
	req, ok := compileTraverse(json.RawMessage(`{"start":"n1","graph":"hellograph","name":"demo"}`))
	require.True(t, ok, "custom-graph traverse is reducible (no client allowlist)")
	assert.Equal(t, "hellograph", req.GetTarget().GetGraph())
	assert.Equal(t, "demo", req.GetTarget().GetName())
}

func TestCompileTraverse_DepthNotInjectedWhenAbsent(t *testing.T) {
	req, ok := compileTraverse(json.RawMessage(`{"start":"n1"}`))
	require.True(t, ok)
	assert.Equal(t, int32(0), req.GetQuery().GetMaxHops(), "no depth → MaxHops 0 (engine defaults to 1)")
}

func TestCompileTraverse_DenyCases(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"logs graph", `{"start":"n1","graph":"logs","name":"q1"}`},
		{"no start (graph-wide-edges)", `{"graph":"logs","name":"q1"}`},
		{"invalid direction", `{"start":"n1","direction":"sideways"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, ok := compileTraverse(json.RawMessage(tc.args))
			assert.False(t, ok, "%s must fall through to legacy", tc.name)
			assert.Nil(t, req)
		})
	}
}

// TestCompileTraverse_IncludeEdgeMetadata asserts an include_edge_metadata
// traverse now COMPILES (T2.4c): the carrier flag is set, and the engine
// re-walks the traversed edges to return the per-edge metadata.
func TestCompileTraverse_IncludeEdgeMetadata(t *testing.T) {
	req, ok := compileTraverse(json.RawMessage(`{"start":"n1","include_edge_metadata":true}`))
	require.True(t, ok, "include_edge_metadata traverse is reducible (T2.4c)")
	assert.True(t, req.GetQuery().GetIncludeEdgeMetadata(), "the include_edge_metadata carrier is set")
}

// TestCompileTraverse_CodeGraphAlwaysRequestsEdges asserts a code-graph
// traversal requests the edge-metadata carrier whether or not the caller asked,
// because only the per-edge Method tells a multi-candidate group from N bound
// edges — and asserts the rule stops at the code graph.
func TestCompileTraverse_CodeGraphAlwaysRequestsEdges(t *testing.T) {
	cases := []struct {
		name string
		args string
		want bool
	}{
		{
			name: "code_graph_without_the_parameter_still_requests_edges",
			args: `{"start":"n1","graph":"code","repo":"knowledge"}`,
			want: true,
		},
		{
			name: "code_graph_with_the_parameter_stays_true",
			args: `{"start":"n1","graph":"code","repo":"knowledge","include_edge_metadata":true}`,
			want: true,
		},
		{
			// THE CATCHER. Without this leg an implementation that sets the
			// carrier unconditionally passes both legs above while silently
			// taxing every non-code traversal in the product with a server-side
			// edge re-walk for a group that cannot exist there.
			name: "knowledge_graph_without_the_parameter_stays_false",
			args: `{"start":"n1","graph":"knowledge"}`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, ok := compileTraverse(json.RawMessage(tc.args))
			require.True(t, ok, "traverse must compile")
			assert.Equal(t, tc.want, req.GetQuery().GetIncludeEdgeMetadata())
		})
	}
}

// TestCompileTraverse_OnePlanPerCall asserts a single ExecuteRequest carrying
// one QueryPlan — no N-fanout for the both case (the engine issues the two
// directed sub-queries server-side; the client emits ONE plan).
func TestCompileTraverse_OnePlanPerCall(t *testing.T) {
	req, ok := compileTraverse(json.RawMessage(`{"start":"n1","direction":"both","depth":5}`))
	require.True(t, ok)
	require.NotNil(t, req.GetQuery())
	assert.Nil(t, req.GetMutation(), "traverse is a read plan, not a mutation")
}
