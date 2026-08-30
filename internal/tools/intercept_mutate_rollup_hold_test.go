// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_rollup_hold_test.go covers the hold rule: the completed-status
// cascade must never write status to a container descendant that still carries an
// unevaluated criterion, because a criterion records whether its check was RUN and
// a container closing above it is evidence of none of it.
//
// THREE OF THESE START RED against the unfixed tree, and they fail on assertions
// rather than on a build break — every symbol they touch already exists:
//   HeldWhenCriterionUnevaluated   the held step is still in the cascade Selection
//   TruncatedTraverseRefusesWrite  a clamped walk is ignored and the write happens
//   ResponseNamesHeldNodes         no held-nodes sentence exists to find
//
// TWO ARE CHARACTERIZATION GUARDS, green before and after, and they are labeled
// as such rather than claimed as red-first:
//   CascadesWhenCriteriaTerminal   the pair to the first test, differing in ONE
//                                  field — the criterion's status — so together
//                                  they show the rule discriminates on the
//                                  property rather than on a criterion merely
//                                  being present. It is what stops the rule being
//                                  satisfiable by holding everything.
//   RootNeverHeld                  pins the exemption the fix must not break: the
//                                  caller named the root, and holding it would
//                                  make a criteria-bearing step impossible to
//                                  complete at all.
//
// Every case asserts on the WHOLE Selection slice with Equal or ElementsMatch,
// never Contains — a Contains assertion stays green while the wrong id still
// rides along, which is the exact defect this file exists to catch.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// heldNodesLiteral is the locked sentence the success line uses to introduce the
// held bucket. It is spelled once here and asserted by name, so a reworded message
// fails loudly instead of silently dropping the audit surface the caller reads.
const heldNodesLiteral = "nodes held (criteria not yet evaluated; run and mark each criterion, then complete the node explicitly): "

// rollupHoldFake seeds plan-1 -> phase-1 -> step-1 -> crit-pending with REAL
// contains edges, so the partition can attribute the criterion to the step that
// owns it. critStatus is the one field the held and cascaded cases differ in.
func rollupHoldFake(critStatus string) *fakeRollupGraphCaller {
	return &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "plan-1", Type: string(kgtypes.NodePlan)},
		descendants: []knowledgev1.Node{
			{Id: "phase-1", Type: string(kgtypes.NodePhase), Status: "pending"},
			{Id: "step-1", Type: string(kgtypes.NodeStep), Status: "pending"},
			{Id: "crit-pending", Type: string(kgtypes.NodeCriterion), Status: critStatus},
		},
		structureEdges: []knowledgev1.Edge{
			{FromId: "plan-1", ToId: "phase-1", Type: string(kgtypes.EdgeKGContains)},
			{FromId: "phase-1", ToId: "step-1", Type: string(kgtypes.EdgeKGContains)},
			{FromId: "step-1", ToId: "crit-pending", Type: string(kgtypes.EdgeKGContains)},
		},
	}
}

// completePlanOne drives the rollup arm the way a caller does: a single-id update
// of plan-1 to completed, with expand_to_descendants left absent (the default).
func completePlanOne(t *testing.T, fc *fakeRollupGraphCaller) kgtools.ToolResult {
	t.Helper()
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"plan-1","status":"completed"}`),
	})
	require.True(t, handled, "the rollup arm claims a completed-status container update")
	return res
}

// TestInterceptMutate_RollupHold_HeldWhenCriterionUnevaluated is the defect
// itself: step-1 owns a criterion nobody has marked, so the cascade must stop
// above it. RED against the unfixed tree, where step-1 rides the Selection.
func TestInterceptMutate_RollupHold_HeldWhenCriterionUnevaluated(t *testing.T) {
	fc := rollupHoldFake("pending")
	res := completePlanOne(t, fc)
	require.False(t, res.IsError, "the rollup should succeed: %s", toolResultText(res))
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	assert.ElementsMatch(t, []string{"plan-1", "phase-1"}, fc.lastUpdate.GetSelection().GetIds(),
		"step-1 owns an unevaluated criterion, so the cascade must not write its status")
}

// TestInterceptMutate_RollupHold_CascadesWhenCriteriaTerminal is a
// CHARACTERIZATION GUARD — green before and after this change. It is the pair to
// the test above and differs from it in exactly one field, the criterion's
// status, so the two together show the hold discriminates on whether the
// criterion was evaluated rather than on whether one exists.
func TestInterceptMutate_RollupHold_CascadesWhenCriteriaTerminal(t *testing.T) {
	fc := rollupHoldFake("completed")
	res := completePlanOne(t, fc)
	require.False(t, res.IsError, "the rollup should succeed: %s", toolResultText(res))
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	assert.ElementsMatch(t, []string{"plan-1", "phase-1", "step-1"}, fc.lastUpdate.GetSelection().GetIds(),
		"a step whose only criterion is already marked is not held — the rule is not satisfiable by holding everything")
}

// TestInterceptMutate_RollupHold_RootNeverHeld is a CHARACTERIZATION GUARD —
// green before and after. The caller named the root, so it is never held: holding
// it would make a step that carries criteria impossible to complete at all.
func TestInterceptMutate_RollupHold_RootNeverHeld(t *testing.T) {
	fc := &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "step-1", Type: string(kgtypes.NodeStep)},
		descendants: []knowledgev1.Node{
			{Id: "crit-pending", Type: string(kgtypes.NodeCriterion), Status: "pending"},
		},
		structureEdges: []knowledgev1.Edge{
			{FromId: "step-1", ToId: "crit-pending", Type: string(kgtypes.EdgeKGContains)},
		},
	}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"step-1","status":"completed"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "the rollup should succeed: %s", toolResultText(res))
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	assert.Equal(t, []string{"step-1"}, fc.lastUpdate.GetSelection().GetIds(),
		"the named root is never held, however many unevaluated criteria hang off it")
}

// TestInterceptMutate_RollupHold_TruncatedTraverseRefusesWrite pins the refusal.
// A clamped walk can drop a criterion out of the fetched set, which makes its step
// look criterion-free and cascades it — reintroducing the phantom through a silent
// partial read. The only correct disposition is to write nothing.
//
// The assertion is on the ABSENCE OF THE WRITE, not on the message: a refusal that
// reported an error after writing would satisfy an IsError-only check. RED against
// the unfixed tree, which ignores truncation entirely.
func TestInterceptMutate_RollupHold_TruncatedTraverseRefusesWrite(t *testing.T) {
	fc := rollupHoldFake("pending")
	fc.truncated = true
	res := completePlanOne(t, fc)
	require.True(t, res.IsError, "a truncated contains walk must refuse the write")
	assert.Equal(t, 0, fc.updateExecutes, "nothing may be written when the walk was clamped")
	assert.Contains(t, toolResultText(res), "plan-1", "the refusal must name the id it did not move")
}

// TestInterceptMutate_RollupHold_PhaseRootHoldsOnlyTheReviewStep drives the
// shape the reported incident actually took: a phase closing over two sibling
// steps, one of which is a review step nobody ran. The rule must be SELECTIVE at
// that level — the work step still completes, only the review step is held — and
// no other case in this file roots at a phase or puts two siblings under one
// parent, so nothing else can see a hold that is either too wide or too narrow
// by one node.
func TestInterceptMutate_RollupHold_PhaseRootHoldsOnlyTheReviewStep(t *testing.T) {
	fc := &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "phase-1", Type: string(kgtypes.NodePhase)},
		descendants: []knowledgev1.Node{
			{Id: "step-work", Type: string(kgtypes.NodeStep), Status: "pending"},
			{Id: "step-review", Type: string(kgtypes.NodeStep), Status: "pending"},
			{Id: "crit-review", Type: string(kgtypes.NodeCriterion), Status: "pending"},
		},
		structureEdges: []knowledgev1.Edge{
			{FromId: "phase-1", ToId: "step-work", Type: string(kgtypes.EdgeKGContains)},
			{FromId: "phase-1", ToId: "step-review", Type: string(kgtypes.EdgeKGContains)},
			{FromId: "step-review", ToId: "crit-review", Type: string(kgtypes.EdgeKGContains)},
		},
	}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"phase-1","status":"completed"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "the rollup should succeed: %s", toolResultText(res))
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	assert.ElementsMatch(t, []string{"phase-1", "step-work"}, fc.lastUpdate.GetSelection().GetIds(),
		"the work step completes with its phase; only the step owning an unevaluated criterion is held")
	body := toolResultText(res)
	heldAt := strings.Index(body, heldNodesLiteral)
	require.GreaterOrEqual(t, heldAt, 0, "the success line must introduce the held bucket: %s", body)
	assert.Contains(t, body[heldAt:], "step-review", "the held review step must be named to the caller")
}

// TestInterceptMutate_RollupHold_ResponseNamesHeldNodes asserts the held bucket
// reaches the caller. A hold nobody is told about is the same invisible-cascade
// defect wearing the opposite sign: the node silently stays open. RED against the
// unfixed tree, where no such sentence exists.
func TestInterceptMutate_RollupHold_ResponseNamesHeldNodes(t *testing.T) {
	fc := rollupHoldFake("pending")
	res := completePlanOne(t, fc)
	require.False(t, res.IsError, "the rollup should succeed: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, heldNodesLiteral, "the success line must introduce the held bucket")
	assert.Contains(t, body, "step-1", "the held node must be named so the caller can act on it")
}
