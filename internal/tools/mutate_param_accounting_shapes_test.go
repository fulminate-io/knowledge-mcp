// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// researchNodeResult seeds a research node carrying a symbol_name, which the
// answer arm reads to build its updated summary.
func researchNodeResult(t *testing.T, id, symbolName string) kgtools.ToolResult {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": id, "type": "research", "symbol_name": symbolName, "metadata": map[string]string{},
	})
	require.NoError(t, err)
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(body)}}}
}

// hasMutationOfKind reports whether the fake captured a mutation of the given kind.
func hasMutationOfKind(fc *fakeGraphCaller, kind knowledgev1.MutationPlan_MutationKind) bool {
	for _, m := range fc.execMutations {
		if m.GetKind() == kind {
			return true
		}
	}
	return false
}

// TestInterceptAddCriterion_GraphParam_IsRejectedNotDropped pins the one arm
// where `graph` is rejected rather than consumed. Every other arm reaches its
// handler THROUGH the knowledge-graph guard, which reads a.Graph to decide
// reachability — a discriminant, and so consumed. The criterion-create arm runs
// AHEAD of that guard and never reads graph at all, so a criterion create naming
// a foreign graph is written to the knowledge graph regardless of what the
// caller asked for. That is a real latent drop, and this is the rejection that
// closes it.
//
// Documented collateral: the gate keys on PRESENCE, not value, so an explicit
// graph:"knowledge" — a benign no-op today — rejects here too. The gate cannot
// be narrower without reading values, and the error names the param so the
// caller knows to drop it.
func TestInterceptAddCriterion_GraphParam_IsRejectedNotDropped(t *testing.T) {
	t.Run("foreign graph rejects rather than silently landing in knowledge", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptAddCriterion(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create","type":"criterion","step_id":"step-1",` +
				`"description":"the suite is green","graph":"practice"}`),
		})
		require.True(t, handled, "the criterion arm claims this shape")
		require.True(t, res.IsError, "a criterion create naming a foreign graph must reject: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "graph", "the rejection must NAME the param")
		assert.Empty(t, fc.execMutations, "a pre-write rejection issues ZERO mutations")
	})

	t.Run("explicit knowledge graph also rejects — presence-based gate, documented collateral", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		_, res := InterceptAddCriterion(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create","type":"criterion","step_id":"step-1",` +
				`"description":"the suite is green","graph":"knowledge"}`),
		})
		require.True(t, res.IsError,
			"the gate keys on presence, not value — an explicit knowledge graph rejects too: %s", toolResultText(res))
		assert.Empty(t, fc.execMutations, "a pre-write rejection issues ZERO mutations")
	})

	t.Run("names the knowledge-graph-only contract", func(t *testing.T) {
		// A bare refusal invites re-litigation ("then which call DOES route it?").
		// The contract is settled: no such call will exist, because criteria attach
		// to the plan/step verifies structure, which no other graph family carries.
		// The rejection has to SAY so.
		fc := &fakeGraphCaller{}
		handled, res := InterceptAddCriterion(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create","type":"criterion","step_id":"step-1",` +
				`"description":"the suite is green","graph":"practice"}`),
		})
		require.True(t, handled, "the criterion arm claims this shape")
		require.True(t, res.IsError, "a criterion create naming a foreign graph must reject: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "knowledge-graph-only",
			"the rejection must state the permanent contract, not merely refuse the param")
		assert.Empty(t, fc.execMutations, "a pre-write rejection issues ZERO mutations")
	})

	t.Run("omitting graph is unaffected", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		_, res := InterceptAddCriterion(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create","type":"criterion","step_id":"step-1",` +
				`"description":"the suite is green"}`),
		})
		assert.NotContains(t, toolResultText(res), "graph is not applied",
			"a criterion create with no graph must not trip the accounting gate")
	})
}

// TestInterceptMutate_DeclinedLinkGraphLink_IsRejectedNotDropped pins a silent drop
// the seven canonical shapes do not reach. The cross-graph link composer claims
// the one link_graph value it understands; a link_graph link it DECLINES reaches
// the link-fallthrough arm, and no path downstream reads the param — the engine
// denies the whole shape on a non-empty link_graph, and the dispatch default
// bucket forwards it unaccounted to a reader that does not exist.
//
// So the param must be rejected: waving it through is the exact
// success-while-dropping shape this gate closes, and rejecting a param no path
// routes cannot be a false rejection.
//
// A link_graph value other than the claimed one is the shape that reaches the
// decline most directly (the composer returns not-claimed on it outright).
func TestInterceptMutate_DeclinedLinkGraphLink_IsRejectedNotDropped(t *testing.T) {
	fc := &fakeGraphCaller{}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"link","link_graph":"some-other-graph",` +
			`"from":"a","to":"b","relationship":"relates-to"}`),
	})
	require.True(t, handled, "the rejection is a claim — the call must not fall through silently")
	require.True(t, res.IsError, "an unroutable link_graph must reject, not drop: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "link_graph", "the rejection must NAME the dropped param")
	assert.Empty(t, fc.execMutations, "a pre-write rejection issues ZERO mutations")
}

// TestInterceptMutate_CurrentlyWorkingShapes_NotRejected is the anti-false-
// rejection guard, and it is the only one. The census and partition assertions
// both catch a MISSED param drop; neither can catch the opposite error, because
// a param wrongly declared rejected is simply confirmed by the gate's own
// rejection and a declared-equals-observed harness goes green on it.
//
// Every shape below WORKS on the pre-gate tree and must keep working. Each is
// red-if-broken in the direction that matters: applying a mechanical "reject
// whatever is not demonstrably written to a node body" default turns all seven
// into hard pre-write rejections, and this test is what fires. If one of these
// rejects, the classification is wrong — not the test.
func TestInterceptMutate_CurrentlyWorkingShapes_NotRejected(t *testing.T) {
	t.Run("answer via the question_id alias with no id", func(t *testing.T) {
		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{"q-1": researchNodeResult(t, "q-1", "Why slow?")},
			mutateResult:   kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: "updated"}}},
		}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"answer","question_id":"q-1","conclusion":"the answer"}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "question_id is the documented id alias: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "Research answered: Why slow?")
	})

	t.Run("finding create carrying concludes plus question_id", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateIDs: []string{"fnd-1"}}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create","type":"finding","name":"f",` +
				`"summary":"a searchable finding summary","question_id":"q-1","concludes":true}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "concludes drives the question status update: %s", toolResultText(res))
		assert.True(t, hasMutationOfKind(fc, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE),
			"concludes must fire the status update on the question")
	})

	t.Run("finding create carrying supports", func(t *testing.T) {
		// `supports` is now a DECLARED schema param classified consumed on this
		// arm and rejected on the other twenty, so this case no longer rides on
		// the gate having no cell for it — it asserts the positive routing the
		// declaration commits to: the edge is drawn and the call is not rejected.
		// The sibling case below pins the other half, that a non-finding arm
		// carrying supports now rejects rather than silently ignoring it.
		fc := &fakeGraphCaller{mutateIDs: []string{"fnd-2"}}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create","type":"finding","name":"f",` +
				`"summary":"a searchable finding summary","supports":"th-1"}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an undeclared-but-consumed key must never be rejected: %s", toolResultText(res))
		create := firstCreatePlan(t, fc)
		var found bool
		for _, e := range create.GetEdges() {
			if e.GetToId() == "th-1" {
				found = true
			}
		}
		assert.True(t, found, "supports must still draw its edge")
	})

	t.Run("answer carrying findings", func(t *testing.T) {
		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{"q-2": researchNodeResult(t, "q-2", "Which path?")},
			mutateResult:   kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: "updated"}}},
		}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"answer","id":"q-2","conclusion":"c",` +
				`"findings":"fnd-1, fnd-2"}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "findings is comma-split into answers edges: %s", toolResultText(res))
		assert.True(t, hasMutationOfKind(fc, knowledgev1.MutationPlan_MUTATION_KIND_LINK),
			"findings must still link each id to the question")
	})

	t.Run("update carrying ids with exactly one element", func(t *testing.T) {
		// A one-element ids[] is normalized to a single-id update BEFORE any arm
		// is selected, so it must be accounted as the single-id arm it became,
		// never as a batch. Every pre-existing ids[] test uses two or more
		// elements, so nothing else covers this.
		fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
			"t-1": nodeResultJSON(t, "t-1", "ticket", map[string]string{}),
		}}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"update","ids":["t-1"],"name":"renamed"}`),
		})
		assert.False(t, handled, "a one-element ids[] update declines to the engine single-id arm")
		assert.False(t, res.IsError, "a one-element ids[] update must not be rejected: %s", toolResultText(res))
	})

	t.Run("upsert on a practice graph falls past the passthrough guard", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"upsert","graph":"practice","language":"go",` +
				`"id":"pat-1","type":"pattern"}`),
		})
		assert.False(t, handled, "the client declines a non-knowledge upsert")
		assert.False(t, res.IsError, "a declined non-knowledge shape must not be rejected: %s", toolResultText(res))
		assert.Empty(t, fc.execMutations, "a declined shape issues no client-side write")
	})

	t.Run("unlink on a practice graph reaches the same arm by a different operation", func(t *testing.T) {
		// Paired with the upsert case above: the two together prove the
		// non-knowledge fallthrough was authored as OPERATION-POLYMORPHIC. An
		// arm authored from the upsert shape alone would reject this one, since
		// its body surface (from/to/relationship) shares nothing with an upsert.
		fc := &fakeGraphCaller{}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"unlink","graph":"practice","language":"go",` +
				`"from":"a","to":"b","relationship":"uses"}`),
		})
		assert.False(t, handled, "the client declines a non-knowledge unlink")
		assert.False(t, res.IsError, "a declined non-knowledge shape must not be rejected: %s", toolResultText(res))
		assert.Empty(t, fc.execMutations, "a declined shape issues no client-side write")
	})
}

// TestInterceptMutate_Upsert_RejectsUnroutableParams pins the upsert arm's
// rejections. compileMutateUpsert builds ONE NodeBody, and that body has no
// Keywords field (it mirrors the wire NodeBody) and no per-type param fields, so
// keywords / command / criterion_type / scope / enforcement / evidence have
// nowhere to land. Rejecting is the honest classification: the alternative is a
// call that reports success with the value nowhere on the node.
//
// Widening the NodeBody to carry them would be a WIRE change, which is out of
// scope here — if upsert ever needs to route keywords, that is a scope decision
// to raise, not a local fix.
//
// The last case is the guard against over-rejection: an upsert carrying only
// routable body fields must still fall through unclaimed to the engine.
func TestInterceptMutate_Upsert_RejectsUnroutableParams(t *testing.T) {
	for _, param := range []string{
		"keywords", "command", "criterion_type", "scope", "enforcement", "evidence",
	} {
		t.Run(param+" rejects with zero mutations", func(t *testing.T) {
			fc := &fakeGraphCaller{}
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate",
				Arguments: json.RawMessage(
					`{"operation":"upsert","id":"n-1","type":"worker","name":"w",` +
						`"` + param + `":"x"}`),
			})
			require.True(t, handled, "a rejected param must be claimed, not fall through")
			require.True(t, res.IsError)
			assert.Contains(t, toolResultText(res), param, "the rejection must name the offending param")
			assert.Empty(t, fc.execMutations, "zero writes on a rejected upsert")
		})
	}

	t.Run("a routable-body-only upsert still falls through unclaimed", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(
				`{"operation":"upsert","id":"n-1","type":"worker","name":"w",` +
					`"description":"d","summary":"s","content":"c","status":"active",` +
					`"source":"llm:claude","metadata":{"k":"v"}}`),
		})
		assert.False(t, handled, "the upsert arm declines to the engine — it must not be claimed")
		assert.False(t, res.IsError, "a fully-routable upsert must not be rejected: %s", toolResultText(res))
	})
}

// TestInterceptMutate_BareLinkWithName_RoutesNotRejected is the over-rejection
// guard for the link arms. `link` is the one operation dispatched AHEAD of the
// non-knowledge guard — the cross-graph composer must be able to see foreign
// endpoints — so armLinkFallthrough is the only arm that can run on a
// name-addressed graph, and it CONSUMES `name` as the Execute Target instance for
// those families. armLinkCrossGraph rejects the same param.
//
// The hazard this pins is GATE-BEFORE-CLAIM-DECISION: accounting the cross-graph
// arm before the composer has decided whether it claims the call would apply that
// arm's stricter surface to a call the arm never handles, hard-rejecting a schema
// shape that works. A bare knowledge↔knowledge link is declined by the composer,
// so only the fallthrough arm's surface may gate it.
//
// The probe rides the KNOWLEDGE graph deliberately: knowledge is name-blind, so
// the engine drops the name rather than routing it (mutateTargetName), and the
// call must still SUCCEED. Rejection here would mean the wrong arm's gate ran.
func TestInterceptMutate_BareLinkWithName_RoutesNotRejected(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"k-from": nodeResultJSON(t, "k-from", "finding", map[string]string{}),
		"k-to":   nodeResultJSON(t, "k-to", "finding", map[string]string{}),
	}}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"link","from":"k-from","to":"k-to",` +
			`"relationship":"relates-to","name":"a node name"}`),
	})
	assert.False(t, res.IsError,
		"a bare link carrying name must NOT be rejected — the engine LINK arm owns it: %s",
		toolResultText(res))
	assert.False(t, handled, "the declined bare link belongs to the engine dispatch")
}

// TestInterceptMutate_SupportsRejectedOnNonFindingArm is the half that declaring
// `supports` in the schema newly buys. While it was undeclared it had no cell in
// any arm's sets, so a rule create carrying it was SILENTLY DROPPED: the rule
// arm never reads supports, and the gate cannot reject a key it has no cell for.
// Declaring the param forced a classification on all 21 arms — consumed on
// finding-create, rejected everywhere else — which converts that silent drop
// into a loud error naming the field.
//
// Deliberately its own test rather than a case in the currently-working-shapes
// table: that table asserts calls are NOT rejected, and its subtest count is
// pinned, so a rejection case does not belong in it.
func TestInterceptMutate_SupportsRejectedOnNonFindingArm(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"rule-2"}}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"rule","name":"r",` +
			`"summary":"a searchable rule summary","supports":"th-1"}`),
	})
	require.True(t, handled, "a rejected param must be claimed, not fall through")
	require.True(t, res.IsError, "the rule arm does not read supports, so it must reject it")
	assert.Contains(t, toolResultText(res), "supports", "the rejection must name the offending param")
	assert.Empty(t, fc.execMutations, "the reject must precede any write")
}
