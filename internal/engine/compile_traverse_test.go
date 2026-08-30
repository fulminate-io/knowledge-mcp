// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"errors"
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
	assert.True(t, q.GetForward(), "empty direction defaults to out (the default ValidateTraverseDirection admits)")
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

// TestCompileTraverse_DenyCases pins the shapes compileTraverse does not
// reduce. ok=false is a DENY, not a fall-through: there is no legacy dispatch
// path behind it. Two of these are claimed earlier (the logs intercept, and
// dispatchGraphWideEdges for a start-less traverse); the invalid-direction leg
// is the compile-side default-deny that guards the programmatic Compile callers
// — Dispatch refuses that value ahead of Compile via precheckTraverse.
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
			assert.False(t, ok, "%s must be denied by the compiler", tc.name)
			assert.Nil(t, req)
		})
	}
}

// TestValidateTraverseDirection_Vocabulary pins the accepted set and the
// normalization. The normalization legs are not decoration: compileTraverse's
// switch lowercases and trims the same value, so a spelling this validator
// accepts but that switch would not recognize (or the reverse) is a drift bug —
// these legs are what make the two agree by test rather than by hope.
func TestValidateTraverseDirection_Vocabulary(t *testing.T) {
	for _, ok := range []string{"", "out", "in", "both", "OUT", " both ", "In"} {
		require.NoError(t, ValidateTraverseDirection(ok), "%q is in the published vocabulary", ok)
		if ok == "" {
			continue
		}
		_, compiled := compileTraverse(json.RawMessage(`{"start":"n1","direction":"` + ok + `"}`))
		assert.True(t, compiled, "%q is accepted by the validator, so the compile switch must recognize it too", ok)
	}
	for _, bad := range []string{"sideways", "down", "up", "outward", "forward"} {
		err := ValidateTraverseDirection(bad)
		require.Error(t, err, "%q is outside the published vocabulary", bad)
		assert.Contains(t, err.Error(), bad, "the refusal quotes the offending value")
		assert.Contains(t, err.Error(), "out, in, both", "the refusal names the accepted vocabulary")
	}
}

// TestDispatch_PrecheckTraverseUnknownDirection is the regression guard for the
// bad-input rule on traverse's direction. An unknown direction used to reach the
// GENERIC post-cutover deny ("tool traverse is not a recognized engine-reducible
// shape"), which named neither the offending value nor the Enum{out,in,both} the
// tool schema publishes. precheckTraverse now refuses it BEFORE Compile with a
// message carrying both — exec NEVER runs (bounded-constant: 0).
//
// Driven END-TO-END through Dispatch, not precheckTraverse in isolation, so the
// suite catches a future regression where the seam stops being invoked. The
// start-less leg matters independently: it proves the precheck runs AHEAD of
// dispatchGraphWideEdges, which would otherwise have served a graph-wide
// enumeration while echoing the invalid direction back in its own JSON payload.
func TestDispatch_PrecheckTraverseUnknownDirection(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"from_id walk", `{"start":"n1","direction":"sideways"}`},
		{"start-less graph-wide", `{"direction":"sideways"}`},
		{"logs graph", `{"start":"n1","graph":"logs","name":"q1","direction":"sideways"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &dispatchCounters{}
			out, err := Dispatch(context.Background(),
				d.exec(nil, errors.New("exec must not run — precheck rejects the direction")),
				"traverse", json.RawMessage(tc.args))
			require.NoError(t, err, "the validation error is rendered, not returned")
			assert.True(t, out.IsError, "an unknown direction is a validation failure (IsError)")
			assert.Contains(t, out.Content[0].Text, `"sideways"`,
				"the refusal quotes the offending value back")
			assert.Contains(t, out.Content[0].Text, "out, in, both",
				"the refusal names the published vocabulary")
			assert.NotContains(t, out.Content[0].Text, "not a recognized engine-reducible shape",
				"the named refusal replaces the generic deny")
			assert.Equal(t, 0, d.execCalls, "a precheck failure issues NO Execute RPC")
		})
	}
}

// TestDispatch_TraverseAcceptedDirectionsStillWalk is the known-positive control
// for the refusal above: every published direction, plus the omitted-direction
// default, still compiles and issues EXACTLY ONE Execute. Without it a
// precheckTraverse that refused everything — or one wired to the wrong
// normalization — would pass the refusal test while breaking the tool.
func TestDispatch_TraverseAcceptedDirectionsStillWalk(t *testing.T) {
	for _, dir := range []string{"", "out", "in", "both", "BOTH"} {
		t.Run("direction="+dir, func(t *testing.T) {
			d := &dispatchCounters{}
			args := `{"start":"n1"}`
			if dir != "" {
				args = `{"start":"n1","direction":"` + dir + `"}`
			}
			out, err := Dispatch(context.Background(),
				d.exec(&knowledgev1.ExecuteResponse{}, nil),
				"traverse", json.RawMessage(args))
			require.NoError(t, err)
			assert.False(t, out.IsError, "%q is accepted and walks", dir)
			assert.Equal(t, 1, d.execCalls, "an accepted direction issues exactly one Execute")
		})
	}
}

// TestDispatch_StartlessLogsTraverse_DeniedWhileOtherGraphsEnumerate pins the
// disposition the logs intercept's fall-through comment now describes: a
// start-less logs traverse is claimed by nobody — dispatchGraphWideEdges
// declines graph=="logs" and compileTraverse declines it too — so it reaches the
// Compile-miss deny with NO Execute RPC.
//
// The knowledge leg is the known-positive: the SAME start-less shape on another
// graph is SERVED by the graph-wide arm and issues an Execute. Without it, a
// wiring change that broke every start-less traverse would leave the logs leg
// passing for the wrong reason.
//
// This pins the CURRENT disposition. Whether a logs graph-wide enumeration
// should be served, and whether the deny should name the missing start instead
// of the generic unrecognized-shape text, are open questions — if either is
// answered, this test changes with the answer.
func TestDispatch_StartlessLogsTraverse_DeniedWhileOtherGraphsEnumerate(t *testing.T) {
	t.Run("logs is denied", func(t *testing.T) {
		d := &dispatchCounters{}
		out, err := Dispatch(context.Background(),
			d.exec(nil, errors.New("exec must not run — a start-less logs traverse is denied")),
			"traverse", json.RawMessage(`{"graph":"logs","name":"q1"}`))
		require.NoError(t, err, "the deny is rendered, not returned as a Go error")
		assert.True(t, out.IsError, "a start-less logs traverse is denied")
		assert.Equal(t, 0, d.execCalls, "a denied shape issues NO Execute RPC")
	})
	t.Run("knowledge enumerates", func(t *testing.T) {
		d := &dispatchCounters{}
		out, err := Dispatch(context.Background(),
			d.exec(&knowledgev1.ExecuteResponse{}, nil),
			"traverse", json.RawMessage(`{"graph":"knowledge"}`))
		require.NoError(t, err)
		assert.False(t, out.IsError, "the same shape on knowledge is the graph-wide enumeration")
		assert.Positive(t, d.execCalls, "the served shape reaches the wire")
	})
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
