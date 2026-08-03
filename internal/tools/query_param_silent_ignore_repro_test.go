// SPDX-License-Identifier: Apache-2.0

package tools

// query_param_silent_ignore_repro_test.go covers four named (param, arm) pairs
// where the query surface used to accept a DECLARED param, return success, and
// drop the param with no signal to the caller.
//
// THESE SUBTESTS CHANGED MEANING at the route-or-reject wiring step, and the
// change is the point of the ticket. They began life as CHARACTERIZATION GUARDS
// — green before the gate and green after, because nothing about the old
// behavior errored; the whole defect was that it did not. Three of them now
// assert the LOUD REJECTION the gate produces instead: IsError true, the message
// naming the dropped param, and ZERO reads issued.
//
// THE RULES SUBTEST WENT ONE STEP FURTHER, and its name says which. A rejection
// is the honest answer only while the arm genuinely cannot route the param; once
// the rule browse learned to page, limit and offset became ROUTABLE, and keeping
// them rejected would have frozen the defect behind a well-worded error. That
// subtest therefore asserts BOTH halves of the arm's current contract: the two
// paging params are served, and `graph` — the one param no rule browse can ever
// mean — is the rejection.
//
// THE PRE-GATE MEASUREMENTS SURVIVE as the named constants below, so a later
// reader can tell a real regression from this expected transition. They are what
// the arms actually sent before the gate existed.
//
// EACH ZERO CARRIES A KNOWN POSITIVE. "No Execute was issued" is also satisfied
// by a probe pointed at nothing, so every zero-read assertion here is paired
// with a payload that drives the SAME arm to a non-zero read count. Without the
// pair, a gate that rejected everything — or a harness that wired nothing —
// would look identical to a correct rejection.

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

// rulesArmObservedLimit / rulesArmObservedOffset are the outgoing plan values
// the rules arm sent BEFORE the gate when the caller supplied limit:1, offset:2.
//
// TEN, not one and not zero: the arm marshaled a fixed rule-browse payload of
// its own carrying no row count, and the engine's Compile then applied
// browseDefaultLimit=10. Both numbers are now history in two steps — the gate
// made supplying them a caller error, and the paging rewrite made them routed —
// which is exactly why they are retained: a future reader who sees a plan
// carrying 10 is looking at a regression to this shape, not at a design.
const (
	rulesArmObservedLimit  = 10
	rulesArmObservedOffset = 0
)

// knowledgeArmProbeGraphName is the `name` value the selector-level subtest
// sends. It is deliberately not a real graph: the point is that the client used
// to route it onto the Target for a graph family whose server-side resolver
// never reads it.
const knowledgeArmProbeGraphName = "probe-graph-name"

// statsProbeFake is a minimal GraphCaller that ALSO satisfies statsRPC,
// recording both the Stats request and every Execute request. It is the
// two-method double for the subtest below, which asserts precisely that pair.
//
// The shared fakeGraphCaller now carries Stats too (the Phase-5 parity harness
// needs it to drive the stats-bearing arms at all), so this recorder is a
// narrowness choice rather than a necessity. The header used to warn that giving
// the shared fake a Stats method "would silently change which arms claim calls in
// every other test in this package" — MEASURED AND FALSE: in every arm that
// type-asserts gc.(statsRPC) the assert runs AFTER the claim (InterceptQueryStats
// returns a claimed errorResult on failure; statsSeamFor does the same for the
// practice/linkage surface), so the seam changes the RESULT BODY and never the
// claim, and the whole package stayed green when it was added.
type statsProbeFake struct {
	stats     *knowledgev1.GraphStats
	statsReqs []*knowledgev1.StatsRequest
	execs     []*knowledgev1.ExecuteRequest
}

func (f *statsProbeFake) Stats(_ context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	f.statsReqs = append(f.statsReqs, req)
	return &knowledgev1.StatsResponse{GraphStats: f.stats}, nil
}

func (f *statsProbeFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.execs = append(f.execs, req)
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestQueryArms_RejectDeclaredParamsTheyDoNotRoute is the post-gate form of the
// four named (param, arm) pairs.
func TestQueryArms_RejectDeclaredParamsTheyDoNotRoute(t *testing.T) {
	t.Run("rules_arm_routes_paging_and_rejects_graph", func(t *testing.T) {
		t.Logf("pre-gate measurement retained: the arm sent limit=%d offset=%d regardless of the caller",
			rulesArmObservedLimit, rulesArmObservedOffset)
		fc := &fakeGraphCaller{nodeMatchResults: map[graphKey][]*knowledgev1.Node{
			{Type: "knowledge"}: {
				{Id: "rule-1", Type: string(kgtypes.NodeRule), SymbolName: "first", Description: "d1"},
				{Id: "rule-2", Type: string(kgtypes.NodeRule), SymbolName: "second", Description: "d2"},
				{Id: "rule-3", Type: string(kgtypes.NodeRule), SymbolName: "third", Description: "d3"},
			},
		}}
		handled, res := InterceptQueryRules(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "query",
			Arguments: json.RawMessage(`{"type":"rule","limit":1,"offset":2,"format":"json"}`),
		})
		require.True(t, handled, "the rules arm claims query(type:\"rule\")")
		require.False(t, res.IsError, "the paging params are routed now, not refused: %s", toolResultText(res))
		require.NotEmpty(t, fc.execRequests, "and the browse still issues its read")
		assert.NotEqualValues(t, rulesArmObservedLimit, fc.execRequests[0].GetQuery().GetLimit(),
			"the plan must no longer carry the pre-gate browse default — that value IS the defect")
		// The page is taken from the FILTERED set client-side: offset 2 over three
		// rules leaves one, and the total stays three.
		var env browseJSONEnvelope
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &env))
		assert.Equal(t, 3, env.Total, "the total is the whole matching set")
		assert.Len(t, env.Results, 1, "and the page is what offset+limit selected out of it")

		// `graph` is the param this arm genuinely cannot route, and its rejection is
		// pre-read.
		rejected := &fakeGraphCaller{nodeMatchResults: map[graphKey][]*knowledgev1.Node{
			{Type: "knowledge"}: {{Id: "rule-1", Type: string(kgtypes.NodeRule), SymbolName: "first"}},
		}}
		handled, res = InterceptQueryRules(opCtx(), interceptTestDeps{gc: rejected}, kgtools.CallToolParams{
			Name:      "query",
			Arguments: json.RawMessage(`{"type":"rule","graph":"practice","format":"json"}`),
		})
		require.True(t, handled)
		require.True(t, res.IsError, "a graph selector on a rule browse is a loud rejection")
		body := toolResultText(res)
		assert.Contains(t, body, "graph", "the message names the dropped param")
		assert.Contains(t, body, "knowledge-graph nodes", "with the shared contract wording")
		assert.Empty(t, rejected.execRequests, "the rejection is pre-read: no Execute is issued")

		// KNOWN POSITIVE for the zero above: the same arm, same fake, without the
		// rejected param, must still issue its read.
		ok := &fakeGraphCaller{nodeMatchResults: map[graphKey][]*knowledgev1.Node{
			{Type: "knowledge"}: {{Id: "rule-1", Type: string(kgtypes.NodeRule), SymbolName: "first"}},
		}}
		handled, res = InterceptQueryRules(opCtx(), interceptTestDeps{gc: ok}, kgtools.CallToolParams{
			Name:      "query",
			Arguments: json.RawMessage(`{"type":"rule","format":"json"}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "the routable shape still succeeds: %s", toolResultText(res))
		assert.NotEmpty(t, ok.execRequests,
			"the control must drive a real read — otherwise the zero above proves nothing")
	})

	t.Run("reflect_arm_rejects_plural_types", func(t *testing.T) {
		// queryReflectArgs has no Types field at all, so before the gate the plural
		// set was gone by the time the arm decided anything.
		params := kgtools.CallToolParams{
			Name:      "query",
			Arguments: json.RawMessage(`{"mode":"charges","types":["thought"]}`),
		}
		handled, res := InterceptThoughts(opCtx(), interceptTestDeps{gc: &fakeGraphCaller{}}, params)
		require.True(t, handled, "query(mode:charges) is claimed by the reflect arm's recall route")
		require.True(t, res.IsError, "the plural type set is not routed by the recall arm")
		assert.Contains(t, toolResultText(res), "types", "the message names the dropped param")

		// KNOWN POSITIVE: the SINGULAR type spelling IS routed (recallParamsFromQuery
		// derives all_types from it), so the same arm accepts it. Without this the
		// rejection above could come from an arm that rejects everything.
		var a queryReflectArgs
		require.NoError(t, json.Unmarshal([]byte(`{"mode":"charges","type":"all"}`), &a))
		forwarded := recallParamsFromQuery(params, a)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(forwarded.Arguments, &payload))
		assert.Equal(t, true, payload["all_types"],
			"the singular spelling reaches the recall payload, which is why it is consumed and types is not")
	})

	t.Run("stats_arm_rejects_limit_and_offset", func(t *testing.T) {
		bare := &statsProbeFake{stats: &knowledgev1.GraphStats{
			NodeCount: 3, NodesByType: map[string]int64{"finding": 3},
		}}
		handled, res := InterceptQueryStats(opCtx(), interceptTestDeps{gc: bare}, kgtools.CallToolParams{
			Name: "query", Arguments: json.RawMessage(`{"mode":"stats","limit":5,"offset":5}`),
		})
		require.True(t, handled, "the stats arm claims query(mode:stats) on the knowledge graph")
		require.True(t, res.IsError, "a Stats RPC has nowhere to put a limit or an offset")
		assert.Contains(t, toolResultText(res), "limit")
		assert.Empty(t, bare.statsReqs, "the rejection precedes the Stats RPC")
		assert.Empty(t, bare.execs, "and issues no Execute")

		// KNOWN POSITIVE for both zeros: samples:true on the SAME arm, without the
		// two rejected params, drives one Stats RPC and a non-zero Execute count.
		sampled := &statsProbeFake{stats: &knowledgev1.GraphStats{
			NodeCount: 3, NodesByType: map[string]int64{"finding": 3},
		}}
		handled, res = InterceptQueryStats(opCtx(), interceptTestDeps{gc: sampled}, kgtools.CallToolParams{
			Name: "query", Arguments: json.RawMessage(`{"mode":"stats","samples":true}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, toolResultText(res))
		require.Len(t, sampled.statsReqs, 1, "the control drives exactly one Stats RPC")
		require.NotEmpty(t, sampled.execs, "and real sample reads — the control for the zeros above")
		for i, req := range sampled.execs {
			assert.EqualValues(t, 2, req.GetQuery().GetLimit(),
				"Execute %d carries the sample fetch's own limit of 2", i)
		}
	})

	t.Run("knowledge_arms_reject_name", func(t *testing.T) {
		// THE SELECTOR-LEVEL INSTANCE, and the one that was NOT a plain drop.
		// `name` is a declared query param the client faithfully ROUTED onto the
		// Execute Target and the server then DISCARDED, so it looked consumed at
		// every point a client-side test can observe.
		//
		// TRACED, NOT EXECUTED: the discard half is verified by reading, not by
		// this test. ResolveGraphDB's knowledge arm
		// (cmd/knowledge-server/internal/tools/tools_graph_routing.go) returns
		// store.StoreForContext(ctx) and states outright that it never reads
		// sel.Name. This is a client-side test and cannot reach the server resolver.
		cases := []struct {
			name    string
			args    string
			okArgs  string
			run     func(context.Context, ClientDeps, kgtools.CallToolParams) (bool, kgtools.ToolResult)
			control string
		}{
			{
				// time_field is REQUIRED here: without it the arm returns
				// "timeline requires time_field when graph is not logs" and issues
				// ZERO Execute requests, so the control would measure nothing.
				name:    "timeline",
				args:    `{"graph":"knowledge","mode":"timeline","time_field":"CreatedAt","name":"` + knowledgeArmProbeGraphName + `"}`,
				okArgs:  `{"graph":"knowledge","mode":"timeline","time_field":"CreatedAt"}`,
				run:     InterceptQueryExplainTimeline,
				control: "the timeline arm still reads when name is absent",
			},
			{
				name:    "correlations",
				args:    `{"graph":"knowledge","mode":"correlations","name":"` + knowledgeArmProbeGraphName + `"}`,
				okArgs:  `{"graph":"knowledge","mode":"correlations"}`,
				run:     InterceptQueryCorrelationsPivot,
				control: "the correlations arm still reads when name is absent",
			},
			{
				name:    "recent",
				args:    `{"graph":"knowledge","mode":"recent","name":"` + knowledgeArmProbeGraphName + `"}`,
				okArgs:  `{"graph":"knowledge","mode":"recent"}`,
				run:     InterceptQueryKnowledgeSearch,
				control: "the bare-recent browse still reads when name is absent",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fc := &fakeGraphCaller{}
				handled, res := tc.run(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
					Name: "query", Arguments: json.RawMessage(tc.args),
				})
				require.True(t, handled, "the %s arm must claim its own mode", tc.name)
				require.True(t, res.IsError,
					"%s targets a graph family whose resolver discards name, so routing it is a silent drop", tc.name)
				assert.Contains(t, toolResultText(res), "name", "the message names the dropped param")
				assert.Empty(t, fc.execRequests, "the rejection is pre-read: no Execute is issued")

				// KNOWN POSITIVE: the same arm, same fake, without `name`.
				ok := &fakeGraphCaller{}
				handled, res = tc.run(opCtx(), interceptTestDeps{gc: ok}, kgtools.CallToolParams{
					Name: "query", Arguments: json.RawMessage(tc.okArgs),
				})
				require.True(t, handled)
				require.False(t, res.IsError, "%s: %s", tc.control, toolResultText(res))
				assert.NotEmpty(t, ok.execRequests, tc.control)
			})
		}
	})
}
