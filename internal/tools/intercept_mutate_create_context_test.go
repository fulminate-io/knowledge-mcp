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
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// findContainsToNewNode reports whether the CREATE MutationPlan carries a
// ticket/session--contains-->newNode(slot 0) edge whose FROM is fromID.
func findContainsToNewNode(m *knowledgev1.MutationPlan, fromID string) bool {
	for _, e := range m.GetEdges() {
		if e.GetType() == string(kgtypes.EdgeKGContains) && e.GetFromId() == fromID && e.GetToIdx() == 0 {
			return true
		}
	}
	return false
}

// firstCreatePlan returns the first CREATE MutationPlan the carrier issued.
func firstCreatePlan(t *testing.T, fc *fakeGraphCaller) *knowledgev1.MutationPlan {
	t.Helper()
	for _, m := range fc.execMutations {
		if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			return m
		}
	}
	t.Fatal("no CREATE MutationPlan was issued")
	return nil
}

// TestMutateCreateFinding_TicketContains asserts a finding created with a
// resolvable ticket_id carries a ticket--contains-->finding edge on the emitted
// create MutationPlan.
func TestMutateCreateFinding_TicketContains(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"tkt-1": nodeResultJSON(t, "tkt-1", "ticket", nil),
		},
		mutateIDs: []string{"finding-1"},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleClientMutateCreateFinding(context.Background(), deps, mutateArgs{
		Operation: "create", Type: "finding",
		Name: "a finding", Summary: "a searchable finding summary",
		TicketID: "tkt-1",
	})
	require.False(t, res.IsError, "create must succeed: %s", toolResultText(res))

	m := firstCreatePlan(t, fc)
	assert.True(t, findContainsToNewNode(m, "tkt-1"),
		"finding create MutationPlan must carry ticket--contains-->finding")
}

// TestRecordDecision_TicketContains asserts a decision recorded with a
// resolvable ticket_id carries a ticket--contains-->decision edge.
func TestRecordDecision_TicketContains(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"tkt-2": nodeResultJSON(t, "tkt-2", "ticket", nil),
		},
		mutateIDs: []string{"decision-1"},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptRecordDecision(opCtx(), deps, kgtools.CallToolParams{
		Name: "record_decision",
		Arguments: json.RawMessage(`{
			"name": "use X over Y",
			"choice": "X",
			"rationale": "X is simpler",
			"ticket_id": "tkt-2"
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "record_decision must succeed: %s", toolResultText(res))

	m := firstCreatePlan(t, fc)
	assert.True(t, findContainsToNewNode(m, "tkt-2"),
		"decision create MutationPlan must carry ticket--contains-->decision")
}

// TestMutateCreateFinding_AbsentTicket_NodeStillCreated is the fails-when-absent
// guard for the never-blocks contract: a finding whose ticket_id resolves
// NOWHERE is still created (non-error, node ID returned), a warning is rendered,
// and the create MutationPlan carries NO ticket--contains edge (no Idx==-1
// endpoint that would abort applyCreate).
func TestMutateCreateFinding_AbsentTicket_NodeStillCreated(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"finding-2"}} // ticket resolves nowhere.
	deps := interceptTestDeps{gc: fc}
	res := handleClientMutateCreateFinding(context.Background(), deps, mutateArgs{
		Operation: "create", Type: "finding",
		Name: "a finding", Summary: "a searchable finding summary",
		TicketID: "ghost-ticket",
	})
	require.False(t, res.IsError, "an absent ticket must NOT fail the create")
	assert.Contains(t, toolResultText(res), "finding-2", "node ID is still returned")
	assert.Contains(t, toolResultText(res), "ghost-ticket", "a drop warning is rendered")

	m := firstCreatePlan(t, fc)
	for _, e := range m.GetEdges() {
		assert.NotEqual(t, "ghost-ticket", e.GetFromId(),
			"no ticket--contains edge may ride the batch for an unresolvable ticket")
	}
}

// TestMutateCreateFinding_AbsentKnowledgeLink_NodeStillCreated asserts an
// unresolvable knowledge-target link drops+warns without blocking the create and
// without riding the batch as a relates-to edge.
func TestMutateCreateFinding_AbsentKnowledgeLink_NodeStillCreated(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"finding-3"}} // link resolves nowhere.
	deps := interceptTestDeps{gc: fc}
	res := handleClientMutateCreateFinding(context.Background(), deps, mutateArgs{
		Operation: "create", Type: "finding",
		Name: "a finding", Summary: "a searchable finding summary",
		Links: []string{"ghost-link"},
	})
	require.False(t, res.IsError, "an absent link must NOT fail the create")
	assert.Contains(t, toolResultText(res), "finding-3", "node ID is still returned")
	assert.Contains(t, toolResultText(res), "ghost-link", "a drop warning is rendered")

	m := firstCreatePlan(t, fc)
	for _, e := range m.GetEdges() {
		assert.NotEqual(t, "ghost-link", e.GetToId(),
			"no relates-to edge may ride the batch for an unresolvable link")
	}
}

// TestMutateCreateFinding_CodeLinkFailure_CreateStillSucceeds is the
// loud-degrade-no-block proof for code links: a links id that resolves in a
// foreign (code) graph is proxied + linked POST-create via LinkOne; when that
// LinkOne errors, the create still succeeds (non-error, node ID returned) and
// the failure is surfaced as a warning. The per-MutationKind error map fails
// ONLY the LINK plan, so the CREATE and the proxy UPSERT both land.
func TestMutateCreateFinding_CodeLinkFailure_CreateStillSucceeds(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateIDs: []string{"finding-4"},
		// code/myrepo is the loaded foreign graph; code-sym-1 resolves there but
		// NOT in knowledge → outcome (b): a code target → post-create LinkOne.
		listGraphsResult: listGraphsResultFor(t, [2]string{"code", "myrepo"}),
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "code", Name: "myrepo"}: {
				"code-sym-1": graphNodeResult(t, "code-sym-1", "function", "DoThing", "does a thing"),
			},
		},
		// Fail ONLY the post-create relates-to LINK; the CREATE + proxy UPSERT succeed.
		mutateErrByKind: map[knowledgev1.MutationPlan_MutationKind]error{
			knowledgev1.MutationPlan_MUTATION_KIND_LINK: errors.New("link backend down"),
		},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleClientMutateCreateFinding(context.Background(), deps, mutateArgs{
		Operation: "create", Type: "finding",
		Name: "a finding", Summary: "a searchable finding summary",
		Links: []string{"code-sym-1"},
	})
	require.False(t, res.IsError, "a failing code-link must NOT fail the create: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "finding-4", "node ID is still returned despite the link failure")

	// A CREATE landed; the failing LINK is the post-create relates-to.
	m := firstCreatePlan(t, fc)
	require.NotNil(t, m)
}
