// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestHandleThinkClient_BasicForward covers the composer happy path: a think
// with content + branches_from + links lowers to a CREATE MutationPlan (thought
// NodeBody + EdgeBranchesFrom + EdgeRelatesTo edges) over the Execute carrier.
// No session here, so no session lineage edges.
func TestHandleThinkClient_BasicForward(t *testing.T) {
	fc := &fakeGraphCaller{
		// branches_from + links resolve in knowledge → raw ids (no proxy).
		queryResponses: map[string]kgtools.ToolResult{
			"parent-id-7": nodeResultJSON(t, "parent-id-7", "thought", nil),
			"link-a":      nodeResultJSON(t, "link-a", "finding", nil),
			"link-b":      nodeResultJSON(t, "link-b", "finding", nil),
		},
		mutateIDs: []string{"abc123"},
	}
	deps := interceptTestDeps{gc: fc}

	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name: "thoughts",
		Arguments: json.RawMessage(`{
			"operation": "think",
			"content": "hypothesis under test",
			"summary": "searchable gist of the hypothesis under test",
			"branches_from": "parent-id-7",
			"links": ["link-a", "link-b"]
		}`),
	})
	require.False(t, res.IsError, "think client must succeed: %s", toolResultText(res))

	// Exactly one CREATE MutationPlan: thought NodeBody + 3 edges (branches_from + 2 links).
	require.Len(t, fc.execMutations, 1, "expected a single create MutationPlan")
	m := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, m.GetKind())
	require.Len(t, m.GetNodeBodies(), 1)
	body := m.GetNodeBodies()[0]
	assert.Equal(t, "thought", body.GetType())
	assert.Equal(t, "hypothesis under test", body.GetName(), "SymbolName=content (under maxLen)")
	assert.Equal(t, "searchable gist of the hypothesis under test", body.GetSummary(), "author summary set on the thought node")
	assert.Equal(t, "hypothesized", body.GetStatus())

	edges := m.GetEdges()
	require.Len(t, edges, 3)
	assert.Equal(t, string(kgtypes.EdgeBranchesFrom), edges[0].GetType())
	assert.Equal(t, "parent-id-7", edges[0].GetToId())
	assert.Equal(t, string(kgtypes.EdgeRelatesTo), edges[1].GetType())
	assert.Equal(t, "link-a", edges[1].GetToId())
	assert.Equal(t, "link-b", edges[2].GetToId())

	body2 := toolResultText(res)
	assert.Contains(t, body2, "Thought recorded → ID: abc123")
	assert.Contains(t, body2, "Branches from: parent-id-7")
}

// TestHandleThinkClient_TicketContains asserts a think() with a resolvable
// ticket_id emits a ticket--contains-->thought edge on the SAME create
// MutationPlan as the thought node + branches_from/links edges. The
// ticket is the existing-node FROM endpoint; the new thought is slot 0.
func TestHandleThinkClient_TicketContains(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"tkt-7": nodeResultJSON(t, "tkt-7", "ticket", nil),
		},
		mutateIDs: []string{"th-ticketed"},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name: "thoughts",
		Arguments: json.RawMessage(`{
			"operation": "think",
			"content": "a ticketed thought",
			"summary": "a ticketed thought, searchable gist",
			"ticket_id": "tkt-7"
		}`),
	})
	require.False(t, res.IsError, "ticketed think must succeed: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 1, "single CREATE plan (no session)")
	m := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, m.GetKind())
	var sawTicketContains bool
	for _, e := range m.GetEdges() {
		if e.GetType() == string(kgtypes.EdgeKGContains) && e.GetFromId() == "tkt-7" && e.GetToIdx() == 0 {
			sawTicketContains = true
		}
	}
	assert.True(t, sawTicketContains, "ticket--contains-->thought must ride the create_batch")
}

// TestHandleThinkClient_AbsentTicket_ThoughtStillCreated is the never-blocks
// guard for think's ticket arm: an unresolvable ticket_id is dropped (no
// contains edge) and the thought is still created (non-error, ID returned).
func TestHandleThinkClient_AbsentTicket_ThoughtStillCreated(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"th-noticket"}} // ticket resolves nowhere.
	deps := interceptTestDeps{gc: fc}
	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name: "thoughts",
		Arguments: json.RawMessage(`{
			"operation": "think",
			"content": "an orphan-ticket thought",
			"summary": "orphan-ticket thought gist",
			"ticket_id": "ghost-ticket"
		}`),
	})
	require.False(t, res.IsError, "an absent ticket must NOT fail the think")
	assert.Contains(t, toolResultText(res), "th-noticket", "thought ID still returned")

	require.Len(t, fc.execMutations, 1)
	for _, e := range fc.execMutations[0].GetEdges() {
		assert.NotEqual(t, "ghost-ticket", e.GetFromId(),
			"no ticket--contains edge may ride the batch for an unresolvable ticket")
	}
}

func TestHandleThinkClient_EmptyContent_Errors(t *testing.T) {
	fc := &fakeGraphCaller{}
	deps := interceptTestDeps{gc: fc}
	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"think","content":"   "}`),
	})
	assert.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "content is required")
	assert.Empty(t, fc.execMutations, "no create should fire on empty content")
}

// TestHandleThinkClient_MissingSummary_Errors asserts the CEO-locked contract:
// summary is REQUIRED for think and a missing/empty/whitespace summary is
// rejected CLIENT-SIDE before the wire — no create fires. Mirrors the
// empty-content gate. (Content is present so the content gate does not preempt
// the summary gate.)
func TestHandleThinkClient_MissingSummary_Errors(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"absent", `{"operation":"think","content":"a thought"}`},
		{"empty", `{"operation":"think","content":"a thought","summary":""}`},
		{"whitespace", `{"operation":"think","content":"a thought","summary":"   "}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeGraphCaller{}
			deps := interceptTestDeps{gc: fc}
			res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
				Name:      "thoughts",
				Arguments: json.RawMessage(tc.args),
			})
			assert.True(t, res.IsError, "missing summary must be rejected: %s", toolResultText(res))
			assert.Contains(t, toolResultText(res), "summary is required", "client-side summary-required message")
			assert.Empty(t, fc.execMutations, "no create should fire when summary is missing — rejected before the wire")
		})
	}
}

// TestHandleThinkClient_NewSession_BothSummariesSet is the regression for the
// live dissolution bug: thinking into a NEW session used to fail because the
// auto-created thought_session node carried no summary and the server's
// summary-required validation rejected the create. Now: (1) the new session
// node is created with a composer-derived non-empty Summary, and (2) the thought
// node carries the author-supplied Summary. Both CREATE NodeBodies must end up
// with non-empty summaries.
func TestHandleThinkClient_NewSession_BothSummariesSet(t *testing.T) {
	fc := &fakeGraphCaller{
		// No seeded session → the name-match scan finds nothing → a NEW session
		// node is created (this is the path that used to fail).
		mutateIDs: []string{"new-id"},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"think","content":"first thought in a brand new session","summary":"new-session thought, searchable gist","session":"fresh-topic"}`),
	})
	require.False(t, res.IsError, "new-session think with summary must succeed: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "Session: fresh-topic")

	// Walk every CREATE MutationPlan NodeBody; both the thought_session and the
	// thought must carry a non-empty Summary.
	var sawSession, sawThought bool
	for _, m := range fc.execMutations {
		if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			continue
		}
		for _, b := range m.GetNodeBodies() {
			switch b.GetType() {
			case string(kgtypes.NodeThoughtSession):
				sawSession = true
				assert.NotEmpty(t, strings.TrimSpace(b.GetSummary()), "new session node must carry a composer-derived summary")
				assert.Contains(t, b.GetSummary(), "fresh-topic", "session summary derived from the session name")
			case string(kgtypes.NodeThought):
				sawThought = true
				assert.Equal(t, "new-session thought, searchable gist", b.GetSummary(), "thought node carries the author summary")
			}
		}
	}
	assert.True(t, sawSession, "a thought_session CREATE must fire for a new session")
	assert.True(t, sawThought, "a thought CREATE must fire")
}

// TestHandleThinkClient_StatusOverride_FollowsUpWithUpdate asserts a non-default
// status chases up with a by-id UPDATE MutationPlan after the create.
func TestHandleThinkClient_StatusOverride_FollowsUpWithUpdate(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"xyz999"}}
	deps := interceptTestDeps{gc: fc}
	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name: "thoughts",
		Arguments: json.RawMessage(`{
			"operation": "think",
			"content": "validated thought",
			"summary": "a validated thought, searchable",
			"status": "validated"
		}`),
	})
	require.False(t, res.IsError, "%s", toolResultText(res))

	// Two MutationPlans: the create, then the status UPDATE.
	require.Len(t, fc.execMutations, 2)
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, fc.execMutations[0].GetKind())
	upd := fc.execMutations[1]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, upd.GetKind())
	assert.Equal(t, []string{"xyz999"}, upd.GetSelection().GetIds())
	assert.Equal(t, "validated", upd.GetSetFields()["status"])
}

// TestHandleThinkClient_SessionLineage covers the session invariants under the
// ATOMIC think-path fix: an EXISTING NodeThoughtSession (found by name) gets an
// EdgeKGContains session→thought edge that RIDES THE CREATE MutationPlan (batch
// edge, not a separate post-create LINK), and when the session already holds a
// prior thought an EdgeNext prev→thought lineage edge is added as a separate
// LINK. The containment assertion is the fails-when-absent regression guard:
// reverting the Phase-1 source edit (which appends the contains batch edge)
// makes the create-batch contains edge absent and turns this test RED. NO
// ThoughtLatestTSKey watermark is written.
func TestHandleThinkClient_SessionLineage(t *testing.T) {
	fc := &fakeGraphCaller{
		// The session Match (knowledge type-browse) returns the existing session.
		nodeMatchResults: map[graphKey][]*knowledgev1.Node{
			{Type: "knowledge"}: {
				{Id: "sess-1", Type: string(kgtypes.NodeThoughtSession), SymbolName: "design"},
			},
		},
		// The session already contains one prior thought (the EdgeNext source).
		traversalByRoot: map[string][]*knowledgev1.Node{
			"sess-1": {{Id: "th-prev", Type: string(kgtypes.NodeThought)}},
		},
		mutateIDs: []string{"th-new"},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"think","content":"a session thought","summary":"session thought gist","session":"design"}`),
	})
	require.False(t, res.IsError, "session think should succeed: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "Session: design")

	// Mutations: [0] the thought CREATE (carrying the session contains batch edge),
	// then only the EdgeNext lineage LINK.
	require.GreaterOrEqual(t, len(fc.execMutations), 2, "create (with contains batch edge) + EdgeNext link")
	createPlan := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, createPlan.GetKind())

	// FAILS-WHEN-ABSENT: the session→thought containment edge must ride the CREATE
	// batch (the atomic shape). hasContainsFrom checks the contains-->slot0 batch
	// edge from sess-1; reverting the Phase-1 append makes this false → RED.
	assert.True(t, hasContainsFrom(createPlan, "sess-1"),
		"EdgeKGContains session→thought must ride the CREATE batch (atomic), not a post-create LINK")

	// The contains edge must NOT also be emitted as a standalone post-create LINK
	// plan (the redundant LinkOne was removed from linkSessionLineage).
	sawContainsLink, sawNext := false, false
	for _, m := range fc.execMutations[1:] {
		if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_LINK {
			continue
		}
		from := ""
		if ids := m.GetSelection().GetIds(); len(ids) > 0 {
			from = ids[0]
		}
		rel := m.GetEdgeSpec().GetRelationship()
		to := m.GetEdgeSpec().GetToId()
		switch rel {
		case string(kgtypes.EdgeKGContains):
			sawContainsLink = true
		case string(kgtypes.EdgeNext):
			assert.Equal(t, "th-prev", from, "EdgeNext from prior thought")
			assert.Equal(t, "th-new", to)
			sawNext = true
		}
	}
	assert.False(t, sawContainsLink, "no standalone EdgeKGContains LINK plan — contains rides the create batch")
	assert.True(t, sawNext, "EdgeNext prev→thought must be linked post-create")
}
