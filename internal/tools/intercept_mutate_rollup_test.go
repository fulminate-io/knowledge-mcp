// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestInterceptMutate_StatusRollup_OneTraverse_OneUpdate asserts that completing
// a container fires exactly the bounded carrier sequence: one ByID lookup
// Execute, one traversal Execute (descendants), one UPDATE Mutation Execute —
// regardless of descendant count (Phase 6 carrier path).
func TestInterceptMutate_StatusRollup_OneTraverse_OneUpdate(t *testing.T) {
	fc := &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "plan-1", Type: string(kgtypes.NodePlan)},
		descendants: []knowledgev1.Node{
			{Id: "phase-1", Type: string(kgtypes.NodePhase), Status: "pending"},
			{Id: "step-1", Type: string(kgtypes.NodeStep), Status: "pending"},
			{Id: "step-2", Type: string(kgtypes.NodeStep), Status: "pending"},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"plan-1","status":"completed"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "rollup should succeed: %s", toolResultText(res))
	assert.Equal(t, 1, fc.traversalExecutes, "exactly 1 traversal Execute to collect descendants")
	assert.Equal(t, 1, fc.updateExecutes, "exactly 1 UPDATE Mutation Execute regardless of descendant count")
}

// TestInterceptMutate_StatusRollup_TerminalDescendants_Skipped asserts the
// UPDATE Mutation's Selection.Ids excludes failed + completed descendants.
func TestInterceptMutate_StatusRollup_TerminalDescendants_Skipped(t *testing.T) {
	fc := &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "plan-1", Type: string(kgtypes.NodePlan)},
		descendants: []knowledgev1.Node{
			{Id: "step-1", Type: string(kgtypes.NodeStep), Status: "pending"},
			{Id: "step-2", Type: string(kgtypes.NodeStep), Status: "failed"},
			{Id: "step-3", Type: string(kgtypes.NodeStep), Status: "completed"},
		},
	}
	deps := interceptTestDeps{gc: fc}
	_, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"plan-1","status":"completed"}`),
	})
	require.False(t, res.IsError)
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, fc.lastUpdate.GetKind())
	ids := fc.lastUpdate.GetSelection().GetIds()
	// Root + step-1 only; step-2 (failed) + step-3 (completed) skipped.
	require.Len(t, ids, 2)
	assert.Contains(t, ids, "plan-1")
	assert.Contains(t, ids, "step-1")
}

// TestInterceptMutate_StatusRollup_ExpandFalse_NoCascade asserts that
// completing a container with expand_to_descendants:false suppresses the
// cascade: no traversal, no cascade UPDATE through the rollup arm, and the
// descendant lands in no update Selection. The intercept declines (handled
// false) so the named container's real status=completed single-node update
// routes through the engine fall-through, not the rollup path. This test is
// RED on pre-gate HEAD: before the cascadeToDescendants() gate the rollup
// fired regardless of the flag, so traversalExecutes would be 1.
func TestInterceptMutate_StatusRollup_ExpandFalse_NoCascade(t *testing.T) {
	fc := &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "plan-1", Type: string(kgtypes.NodePlan)},
		descendants: []knowledgev1.Node{
			{Id: "phase-1", Type: string(kgtypes.NodePhase), Status: "pending"},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, _ := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"plan-1","status":"completed","expand_to_descendants":false}`),
	})
	// The rollup arm declines on explicit-false; the named container's
	// single-node completed update routes through the engine fall-through
	// (handled==false here), which is NOT the rollup path under test.
	assert.False(t, handled, "explicit-false must not be claimed by the rollup arm")
	assert.Equal(t, 0, fc.traversalExecutes, "no descendant traversal when expand_to_descendants:false")
	assert.Equal(t, 0, fc.updateExecutes, "no cascade UPDATE through the rollup arm when expand_to_descendants:false")
	assert.Nil(t, fc.lastUpdate, "no cascade Selection — the descendant is in no update Selection")
}

// TestInterceptMutate_StatusRollup_ExpandAbsent_StillCascades is the critical
// default guard: omitting expand_to_descendants must still cascade (the
// long-standing behavior). A plain-bool wiring with the wrong default would
// regress this to no-cascade for every caller that omits the flag.
func TestInterceptMutate_StatusRollup_ExpandAbsent_StillCascades(t *testing.T) {
	fc := &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "plan-1", Type: string(kgtypes.NodePlan)},
		descendants: []knowledgev1.Node{
			{Id: "phase-1", Type: string(kgtypes.NodePhase), Status: "pending"},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"plan-1","status":"completed"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "rollup should succeed: %s", toolResultText(res))
	assert.Equal(t, 1, fc.traversalExecutes, "absent flag still cascades (default-true)")
	require.NotNil(t, fc.lastUpdate, "a cascade UPDATE Mutation must have fired")
	ids := fc.lastUpdate.GetSelection().GetIds()
	assert.Contains(t, ids, "plan-1", "cascade Selection carries the root")
	assert.Contains(t, ids, "phase-1", "cascade Selection carries the descendant")
}

// TestInterceptMutate_StatusRollup_ExpandTrue_StillCascades asserts an explicit
// expand_to_descendants:true cascades, same as the default.
func TestInterceptMutate_StatusRollup_ExpandTrue_StillCascades(t *testing.T) {
	fc := &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "plan-1", Type: string(kgtypes.NodePlan)},
		descendants: []knowledgev1.Node{
			{Id: "phase-1", Type: string(kgtypes.NodePhase), Status: "pending"},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"plan-1","status":"completed","expand_to_descendants":true}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "rollup should succeed: %s", toolResultText(res))
	assert.Equal(t, 1, fc.traversalExecutes, "explicit true cascades")
	require.NotNil(t, fc.lastUpdate, "a cascade UPDATE Mutation must have fired")
	ids := fc.lastUpdate.GetSelection().GetIds()
	assert.Contains(t, ids, "plan-1")
	assert.Contains(t, ids, "phase-1")
}

// rollupPlanCloseFake seeds a plan whose contains tree carries both sub-task
// descendants (which the cascade moves) and evidence-bearing ones (which it must
// leave alone): a pending criterion, an already-completed criterion, and an open
// question. isTerminalForClientRollup treats "open" as non-terminal, so an
// unfiltered cascade writes completed onto the question exactly as it does onto
// the criterion.
func rollupPlanCloseFake() *fakeRollupGraphCaller {
	return &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "plan-1", Type: string(kgtypes.NodePlan)},
		descendants: []knowledgev1.Node{
			{Id: "phase-1", Type: string(kgtypes.NodePhase), Status: "pending"},
			{Id: "step-1", Type: string(kgtypes.NodeStep), Status: "pending"},
			{Id: "crit-1", Type: string(kgtypes.NodeCriterion), Status: "pending"},
			{Id: "crit-done", Type: string(kgtypes.NodeCriterion), Status: "completed"},
			{Id: "q-1", Type: string(kgtypes.NodeQuestion), Status: "open"},
		},
	}
}

// TestInterceptMutate_StatusRollup_StepClose_LeavesCriteriaUntouched pins the
// reported defect at its original level: closing a step must not write status to
// any criterion beneath it. A criterion records whether its check was RUN, so a
// container closing above it is not evidence the check happened — a cascaded
// "completed" makes a criterion nobody executed read green.
//
// The blank-status criterion is deliberate: create_plan writes criteria with no
// status at all, and isTerminalForClientRollup("") is false, so blank is the
// common live cascade target rather than an edge case.
func TestInterceptMutate_StatusRollup_StepClose_LeavesCriteriaUntouched(t *testing.T) {
	fc := &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "step-1", Type: string(kgtypes.NodeStep)},
		descendants: []knowledgev1.Node{
			{Id: "crit-pending", Type: string(kgtypes.NodeCriterion), Status: "pending"},
			{Id: "crit-blank", Type: string(kgtypes.NodeCriterion), Status: ""},
			{Id: "crit-done", Type: string(kgtypes.NodeCriterion), Status: "completed"},
		},
	}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"step-1","status":"completed"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "rollup should succeed: %s", toolResultText(res))
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	// Equal on the whole slice, never Contains: a Contains-only assertion stays
	// green while the criteria still ride along in the same Selection.
	assert.Equal(t, []string{"step-1"}, fc.lastUpdate.GetSelection().GetIds(),
		"a step close writes status to the step alone — no criterion rides the Selection")
}

// TestInterceptMutate_StatusRollup_PlanClose_LeavesCriteriaUntouched covers the
// ancestor level the incident report did not: the same defect fires from a plan
// close, because the exclusion belongs at the descendant-collection point rather
// than at any one container type.
func TestInterceptMutate_StatusRollup_PlanClose_LeavesCriteriaUntouched(t *testing.T) {
	fc := rollupPlanCloseFake()
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"plan-1","status":"completed"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "rollup should succeed: %s", toolResultText(res))
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	ids := fc.lastUpdate.GetSelection().GetIds()
	assert.ElementsMatch(t, []string{"plan-1", "phase-1", "step-1"}, ids,
		"the cascade carries the root and its sub-task descendants only")
	assert.NotContains(t, ids, "crit-1", "a criterion's status is never written by a cascade")
	assert.NotContains(t, ids, "q-1", "an open question records a decision not yet made")
}

// TestInterceptMutate_StatusRollup_ResponseEnumeratesCascadedIDs asserts the
// success line names every id whose status the write moved. A bare count tells
// the caller something else changed without telling them what, which is how an
// unnoticed cascade survives.
func TestInterceptMutate_StatusRollup_ResponseEnumeratesCascadedIDs(t *testing.T) {
	fc := rollupPlanCloseFake()
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"plan-1","status":"completed"}`),
	})
	require.False(t, res.IsError, "rollup should succeed: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "plan-1", "the named node must appear in the success line")
	assert.Contains(t, body, "phase-1", "every cascaded id must be named")
	assert.Contains(t, body, "step-1", "every cascaded id must be named")
}

// TestInterceptMutate_StatusRollup_ResponseNamesHeldCriteria asserts the success
// line also names what the cascade deliberately left unmarked, so the caller
// knows those nodes still need attention. An already-terminal criterion is not
// news and must not be listed.
func TestInterceptMutate_StatusRollup_ResponseNamesHeldCriteria(t *testing.T) {
	fc := rollupPlanCloseFake()
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"plan-1","status":"completed"}`),
	})
	require.False(t, res.IsError, "rollup should succeed: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "criteria left unmarked")
	assert.Contains(t, body, "crit-1")
	assert.Contains(t, body, "questions left open")
	assert.Contains(t, body, "q-1")
	assert.NotContains(t, body, "crit-done",
		"an already-terminal criterion was not held back by the cascade, so it is not news")
}

// rollupCombinedFake seeds the container node plus one non-terminal descendant —
// the shape every combined-rollup case below drives.
func rollupCombinedFake(t *testing.T) *fakeGraphCaller {
	t.Helper()
	return &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"plan-1": nodeResultJSON(t, "plan-1", string(kgtypes.NodePlan), map[string]string{}),
		},
		traversalByRoot: map[string][]*knowledgev1.Node{
			"plan-1": {{Id: "step-1", Type: string(kgtypes.NodeStep), Status: "pending"}},
		},
	}
}

// rollupCombinedArgs carries EVERY body field the rollup arm declares consumed,
// not a representative pair. Each one is a distinct routing decision in
// rollupNamedNodeFields, so a fixture covering only two would leave the other
// five asserted nowhere outside the (flip-tautological) parity harness.
const rollupCombinedArgs = `{"operation":"update","id":"plan-1","status":"completed",` +
	`"metadata":{"owner":"me"},"description":"a new description","name":"a new name",` +
	`"summary":"a new summary","content":"new content","keywords":"alpha beta",` +
	`"source":"llm:claude"}`

// TestInterceptMutate_RollupCombinedShape_AppliesFieldsToNamedNode pins the
// combined shape: a completed-status container update carrying body fields now
// applies BOTH halves rather than dropping the fields or rejecting the call.
//
// This SUPERSEDES the earlier assertion that the same shape rejected pre-write.
// That reject was the deliberately-temporary first step — it converted a silent
// drop into a loud error; this step converts the loud error into the write the
// caller asked for. On a container update carrying status plus body fields the
// intent is unambiguous, so the combined shape is legal and must land.
//
// The two halves are distinct writes on purpose: status cascades down the
// contains tree, the body fields apply to the NAMED node only.
func TestInterceptMutate_RollupCombinedShape_AppliesFieldsToNamedNode(t *testing.T) {
	fc := rollupCombinedFake(t)
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate", Arguments: json.RawMessage(rollupCombinedArgs),
	})
	require.True(t, handled, "the rollup arm claims a completed-status container update")
	require.False(t, res.IsError, "the combined shape must apply, not reject: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 2, "both halves must be written: the field update and the status rollup")

	// First write: the named node's fields, carrying NO status (the rollup owns it).
	fieldWrite := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, fieldWrite.GetKind())
	assert.Equal(t, []string{"plan-1"}, fieldWrite.GetSelection().GetIds(),
		"body fields apply to the NAMED node only, never the descendants")
	// Every consumed body field must land, each asserted by name: this is the
	// ticket's In-Scope item 2 set, and a partial fixture would let any single
	// one of them regress to a silent drop unnoticed.
	for field, want := range map[string]string{
		"description": "a new description",
		"name":        "a new name",
		"summary":     "a new summary",
		"content":     "new content",
		"keywords":    "alpha beta",
		"source":      "llm:claude",
	} {
		assert.Equalf(t, want, fieldWrite.GetSetFields()[field],
			"%q must reach the named node's field write", field)
	}
	assert.Equal(t, "me", fieldWrite.GetSetMetadata()["owner"])
	assert.NotContains(t, fieldWrite.GetSetFields(), "status",
		"status must not ride the field write — it would double-write through two plans")

	// Second write: the status rollup over root + non-terminal descendants.
	statusWrite := fc.execMutations[1]
	assert.Equal(t, "completed", statusWrite.GetSetFields()["status"])
	assert.ElementsMatch(t, []string{"plan-1", "step-1"}, statusWrite.GetSelection().GetIds())

	// Success must name the second write; silent success about it is the same
	// defect in miniature.
	body := toolResultText(res)
	assert.Contains(t, body, "description")
	assert.Contains(t, body, "metadata")
}

// TestInterceptMutate_RollupCombinedShape_PartialFailureNamesWhatPersisted
// covers all THREE failure paths. The field write runs first precisely so its
// own failure is a clean zero-write reject; once it has landed, neither
// remaining failure may report a bare "traverse failed" — that would itself be
// a silent-partial report about a node the caller cannot see was already
// changed.
func TestInterceptMutate_RollupCombinedShape_PartialFailureNamesWhatPersisted(t *testing.T) {
	t.Run("field write fails: zero writes, no partial language", func(t *testing.T) {
		fc := rollupCombinedFake(t)
		fc.mutateErrOnNth = map[int]error{1: errors.New("field write down")}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: json.RawMessage(rollupCombinedArgs),
		})
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, "field write down", "the underlying cause must surface")
		assert.NotContains(t, body, "applied to plan-1",
			"nothing persisted, so the message must carry no partial-failure language")
		// The fake records a mutation before applying the ordinal error, so the
		// attempted-and-failed field write is the ONLY plan; a second entry would
		// mean the status rollup ran after the field write failed.
		assert.Len(t, fc.execMutations, 1, "the status rollup must not run after a failed field write")
	})

	t.Run("traverse fails after the fields landed: names what persisted", func(t *testing.T) {
		fc := rollupCombinedFake(t)
		fc.traversalErr = errors.New("traverse down")
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: json.RawMessage(rollupCombinedArgs),
		})
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, "plan-1", "the message must name the id")
		assert.Contains(t, body, "description", "the message must name the fields that persisted")
		assert.Contains(t, body, "metadata")
		assert.Contains(t, body, "traverse down", "the underlying cause must surface")
		assert.Contains(t, body, "descendants",
			"the message must say status reached neither the node nor its descendants")
	})

	t.Run("status batch fails after the fields landed: names what persisted", func(t *testing.T) {
		fc := rollupCombinedFake(t)
		// Both writes are MUTATION_KIND_UPDATE against the same Target, so only an
		// ordinal knob can fail the second while letting the first land.
		fc.mutateErrOnNth = map[int]error{2: errors.New("status batch down")}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: json.RawMessage(rollupCombinedArgs),
		})
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, "plan-1", "the message must name the id")
		assert.Contains(t, body, "description", "the message must name the fields that persisted")
		assert.Contains(t, body, "metadata")
		assert.Contains(t, body, "status batch down", "the underlying cause must surface")
		assert.Contains(t, body, "descendants",
			"the message must say status reached neither the node nor its descendants")
	})
}

// fakeRollupGraphCaller answers the carrier sequence the rollup drives: a ByID
// lookup (root), a RETURN_MODE_TRAVERSAL (descendants), and an UPDATE Mutation.
type fakeRollupGraphCaller struct {
	rootNode    knowledgev1.Node
	descendants []knowledgev1.Node

	traversalExecutes int
	updateExecutes    int
	lastUpdate        *knowledgev1.MutationPlan
}

// Call satisfies the interface; the rollup flow's reads/writes all ride the
// Execute carrier seam now (lookupNodeBackend → render.FetchNode, the
// descendants traversal, and the UPDATE mutation).
func (f *fakeRollupGraphCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (f *fakeRollupGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		f.updateExecutes++
		f.lastUpdate = m
		return &knowledgev1.ExecuteResponse{AffectedCount: int64(len(m.GetSelection().GetIds()))}, nil
	}
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
		f.traversalExecutes++
		results := make([]engine.TraversalResult, len(f.descendants))
		for i := range f.descendants {
			results[i] = engine.TraversalResult{Distance: 1, Node: &f.descendants[i]}
		}
		return &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForTest(results)}, nil
	}
	// ByID root lookup (lookupNodeBackend → render.FetchNode): answer the seeded
	// root node via the nodes_json carrier.
	if q.GetById() == f.rootNode.Id {
		resp := enginetest.ResponseWithNodes([]*knowledgev1.Node{&f.rootNode}...)
		return resp, nil
	}
	// Any other Execute → empty (not-found).
	return &knowledgev1.ExecuteResponse{}, nil
}
