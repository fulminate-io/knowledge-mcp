// SPDX-License-Identifier: Apache-2.0

package tools

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

// rollupTicketCloseFake seeds a ticket whose contains tree mixes one container
// descendant with the evidence-bearing types a close must never move. The ids
// are deliberately non-overlapping as substrings, so every Contains and
// NotContains assertion below means exactly what it says.
func rollupTicketCloseFake() *fakeRollupGraphCaller {
	return &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "ticket-1", Type: string(kgtypes.NodeTicket)},
		descendants: []knowledgev1.Node{
			{Id: "plan-9", Type: string(kgtypes.NodePlan), Status: "pending"},
			// Findings are created with no status at all, so blank is the
			// common live shape rather than an edge case.
			{Id: "f-blank", Type: string(kgtypes.NodeFinding), Status: ""},
			{Id: "th-hyp", Type: string(kgtypes.NodeThought), Status: "hypothesized"},
			{Id: "dec-x", Type: string(kgtypes.NodeDecision), Status: ""},
			{Id: "res-y", Type: string(kgtypes.NodeResearch), Status: "open"},
			{Id: "f-done", Type: string(kgtypes.NodeFinding), Status: "completed"},
		},
	}
}

// TestInterceptMutate_StatusRollup_TicketClose_LeavesEvidenceNodesUntouched
// pins the allowlist: a close writes status down the container chain and to
// nothing else. A finding, a thought, a decision and a research node all record
// evidence rather than task progress, so a container closing above them is
// evidence of none of it.
func TestInterceptMutate_StatusRollup_TicketClose_LeavesEvidenceNodesUntouched(t *testing.T) {
	fc := rollupTicketCloseFake()
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"ticket-1","status":"completed"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "rollup should succeed: %s", toolResultText(res))
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	// The whole Selection, never Contains: a Contains-only assertion stays green
	// while the evidence nodes still ride along.
	assert.ElementsMatch(t, []string{"ticket-1", "plan-9"}, fc.lastUpdate.GetSelection().GetIds(),
		"only the root and its container descendant take the status")
}

// TestInterceptMutate_StatusRollup_ContainerChain_CascadesEveryContainerType is
// a CHARACTERIZATION GUARD: green both before and after the allowlist. It is red
// only against an allowlist narrowed to the sub-task types, which would drop the
// ticket and the plan out of the Selection and silently break container-chain
// closure. Every other rollup test roots at a plan with phase and step
// descendants, so none of them covers project-to-ticket or ticket-to-plan.
//
// The fixture is declared inline rather than extracted so the root type and each
// descendant type stay visible in this function's own body.
func TestInterceptMutate_StatusRollup_ContainerChain_CascadesEveryContainerType(t *testing.T) {
	fc := &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "project-1", Type: string(kgtypes.NodeProject)},
		descendants: []knowledgev1.Node{
			{Id: "ticket-1", Type: string(kgtypes.NodeTicket), Status: "pending"},
			{Id: "plan-1", Type: string(kgtypes.NodePlan), Status: "pending"},
			{Id: "phase-1", Type: string(kgtypes.NodePhase), Status: "pending"},
			{Id: "step-1", Type: string(kgtypes.NodeStep), Status: "pending"},
		},
	}
	// Fixture integrity, not behavior: the coverage claim this test makes is
	// about the root's TYPE, so a later edit must not keep the id while changing
	// the type out from under the test's name.
	require.Equal(t, string(kgtypes.NodeProject), fc.rootNode.Type)

	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"project-1","status":"completed"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "rollup should succeed: %s", toolResultText(res))
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	ids := fc.lastUpdate.GetSelection().GetIds()
	assert.ElementsMatch(t, []string{"project-1", "ticket-1", "plan-1", "phase-1", "step-1"}, ids,
		"the cascade reaches every container type down the chain")
}

// TestInterceptMutate_StatusRollup_HeldEvidenceNodesNamedInResponse asserts the
// held nodes are named with their types, and named in the held half of the
// message rather than the cascaded half. Splitting the body at the segment
// boundary is what makes the assertions honest: a whole-body Contains would be
// satisfied by an id that was actually cascaded.
func TestInterceptMutate_StatusRollup_HeldEvidenceNodesNamedInResponse(t *testing.T) {
	fc := rollupTicketCloseFake()
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"ticket-1","status":"completed"}`),
	})
	require.False(t, res.IsError, "rollup should succeed: %s", toolResultText(res))
	body := toolResultText(res)
	i := strings.Index(body, "left unchanged")
	require.GreaterOrEqual(t, i, 0, "the held-node segment must exist: %s", body)
	held, cascaded := body[i:], body[:i]

	for _, want := range []string{"f-blank (finding)", "th-hyp (thought)", "dec-x (decision)", "res-y (research)"} {
		assert.Contains(t, held, want, "every held node is named with its type")
	}
	for _, id := range []string{"f-blank", "th-hyp", "dec-x", "res-y"} {
		assert.NotContains(t, cascaded, id, "a held node must not appear among the cascaded ids")
	}
	assert.NotContains(t, body, "f-done",
		"an already-terminal node was held back by nothing, so it is not news")
}
