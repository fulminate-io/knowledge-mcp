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
	res := handleClientMutateCreateFinding(context.Background(), deps, withRawArgs(mutateArgs{
		Operation: "create", Type: "finding",
		Name: "a finding", Summary: "a searchable finding summary",
		TicketID: "tkt-1",
	}, `{"operation":"create","type":"finding","name":"a finding","summary":"a searchable finding summary","ticket_id":"tkt-1"}`))
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
			"summary": "X is chosen over Y for its simplicity",
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
	res := handleClientMutateCreateFinding(context.Background(), deps, withRawArgs(mutateArgs{
		Operation: "create", Type: "finding",
		Name: "a finding", Summary: "a searchable finding summary",
		TicketID: "ghost-ticket",
	}, `{"operation":"create","type":"finding","name":"a finding","summary":"a searchable finding summary","ticket_id":"ghost-ticket"}`))
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
	res := handleClientMutateCreateFinding(context.Background(), deps, withRawArgs(mutateArgs{
		Operation: "create", Type: "finding",
		Name: "a finding", Summary: "a searchable finding summary",
		Links: []string{"ghost-link"},
	}, `{"operation":"create","type":"finding","name":"a finding","summary":"a searchable finding summary","links":["ghost-link"]}`))
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
	res := handleClientMutateCreateFinding(context.Background(), deps, withRawArgs(mutateArgs{
		Operation: "create", Type: "finding",
		Name: "a finding", Summary: "a searchable finding summary",
		Links: []string{"code-sym-1"},
	}, `{"operation":"create","type":"finding","name":"a finding","summary":"a searchable finding summary","links":["code-sym-1"]}`))
	require.False(t, res.IsError, "a failing code-link must NOT fail the create: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "finding-4", "node ID is still returned despite the link failure")

	// A CREATE landed; the failing LINK is the post-create relates-to.
	m := firstCreatePlan(t, fc)
	require.NotNil(t, m)
}

// TestMutateCreateDocument_TicketContainsAndLinks (FAILS-WHEN-ABSENT) asserts the
// document create born-links exactly as the finding path does. It MIRRORS the
// TestMutateCreateFinding_TicketContains / _AbsentTicket_NodeStillCreated pair
// above rather than inventing a fixture shape, because the contract it gates is
// the same one — buildContextLinks, reused unchanged.
//
// It drives the TYPE-BLIND handler rather than a document-specific one: document
// has no handler of its own, and the born-linking it gets is the born-linking
// every type gets. The test keeps document as its subject anyway, because what
// it fences is that this named type still born-links — a property whose loss
// would be invisible in a test that only ever probed a generic type.
//
// Leg 3 is both-directions cover for legs 1 and 2: without it they are satisfiable
// by a handler that refuses any create whose links do not all resolve, which is
// precisely the fail-tolerance contract this handler must NOT redesign.
func TestMutateCreateDocument_TicketContainsAndLinks(t *testing.T) {
	t.Run("a resolvable ticket_id produces ticket--contains-->document", func(t *testing.T) {
		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"tkt-doc": nodeResultJSON(t, "tkt-doc", "ticket", nil),
			},
			mutateIDs: []string{"doc-1"},
		}
		res := handleClientMutateCreateContextLinked(context.Background(), interceptTestDeps{gc: fc}, withRawArgs(mutateArgs{
			Operation: "create", Type: "document",
			Name: "a retro guide", Summary: "a searchable document summary",
			TicketID: "tkt-doc",
		}, `{"operation":"create","type":"document","name":"a retro guide","summary":"a searchable document summary","ticket_id":"tkt-doc"}`))
		require.False(t, res.IsError, "create must succeed: %s", toolResultText(res))

		m := firstCreatePlan(t, fc)
		assert.True(t, findContainsToNewNode(m, "tkt-doc"),
			"document create MutationPlan must carry ticket--contains-->document")
	})

	t.Run("a resolvable links target produces document--relates-to-->target", func(t *testing.T) {
		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"rel-1": nodeResultJSON(t, "rel-1", "finding", nil),
			},
			mutateIDs: []string{"doc-2"},
		}
		res := handleClientMutateCreateContextLinked(context.Background(), interceptTestDeps{gc: fc}, withRawArgs(mutateArgs{
			Operation: "create", Type: "document",
			Name: "a retro guide", Summary: "a searchable document summary",
			Links: []string{"rel-1"},
		}, `{"operation":"create","type":"document","name":"a retro guide","summary":"a searchable document summary","links":["rel-1"]}`))
		require.False(t, res.IsError, "create must succeed: %s", toolResultText(res))

		m := firstCreatePlan(t, fc)
		found := false
		for _, e := range m.GetEdges() {
			if e.GetToId() == "rel-1" && e.GetType() == string(kgtypes.EdgeRelatesTo) {
				found = true
			}
		}
		assert.True(t, found, "document create MutationPlan must carry document--relates-to-->rel-1; edges: %v", m.GetEdges())
	})

	t.Run("an absent ticket_id drops the link and still creates the document", func(t *testing.T) {
		// THE FAIL-TOLERANCE CONTRACT, inherited from buildContextLinks and asserted
		// here because a handler that added its own error return would break it for
		// this type alone.
		fc := &fakeGraphCaller{mutateIDs: []string{"doc-3"}} // ticket resolves nowhere.
		res := handleClientMutateCreateContextLinked(context.Background(), interceptTestDeps{gc: fc}, withRawArgs(mutateArgs{
			Operation: "create", Type: "document",
			Name: "a retro guide", Summary: "a searchable document summary",
			TicketID: "ghost-ticket",
		}, `{"operation":"create","type":"document","name":"a retro guide","summary":"a searchable document summary","ticket_id":"ghost-ticket"}`))
		require.False(t, res.IsError, "an absent ticket must NOT fail the create")
		assert.Contains(t, toolResultText(res), "doc-3", "node ID is still returned")
		assert.Contains(t, toolResultText(res), "ghost-ticket", "a drop warning is rendered")

		m := firstCreatePlan(t, fc)
		for _, e := range m.GetEdges() {
			assert.NotEqual(t, "ghost-ticket", e.GetFromId(),
				"no ticket--contains edge may ride the batch for an unresolvable ticket")
		}
	})

	t.Run("the dispatcher routes type document to this handler", func(t *testing.T) {
		// WITHOUT THIS LEG the handler could be correct and unreachable: the three
		// legs above call it directly, so none of them would notice a missing
		// claim in the dispatcher, and the fallthrough arm would keep rejecting the
		// three context params exactly as before. The claim it exercises is the
		// trio predicate — document reaches the handler because the payload carries
		// a ticket_id, not because the dispatcher names the type.
		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"tkt-doc": nodeResultJSON(t, "tkt-doc", "ticket", nil),
			},
			mutateIDs: []string{"doc-4"},
		}
		handled, res := dispatchClientMutateCreate(context.Background(), interceptTestDeps{gc: fc}, withRawArgs(mutateArgs{
			Operation: "create", Type: "document",
			Name: "a retro guide", Summary: "a searchable document summary",
			TicketID: "tkt-doc",
		}, `{"operation":"create","type":"document","name":"a retro guide","summary":"a searchable document summary","ticket_id":"tkt-doc"}`))
		require.True(t, handled, "the create dispatcher CLAIMS type:document rather than declining to the engine arm")
		require.False(t, res.IsError, "and serves it: %s", toolResultText(res))
		assert.True(t, findContainsToNewNode(firstCreatePlan(t, fc), "tkt-doc"))
	})
}
