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

// TestBuildContextLinks_TicketResolvable asserts a resolvable ticket_id lowers
// to a ticket--contains-->node batch edge (FromID existing-node shape, ToIdx the
// node slot), with no warning.
func TestBuildContextLinks_TicketResolvable(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"tkt-1": nodeResultJSON(t, "tkt-1", "ticket", nil),
	}}
	cl := buildContextLinks(context.Background(), fc, "tkt-1", "", nil)

	require.Empty(t, cl.warnings, "a resolvable ticket emits no warning")
	require.Len(t, cl.batchEdges, 1)
	e := cl.batchEdges[0]
	assert.Equal(t, kgtypes.EdgeKGContains, e.Type)
	assert.Equal(t, "tkt-1", e.FromID, "ticket is the existing-node FROM endpoint")
	assert.Equal(t, -1, e.FromIdx, "existing-node FROM uses Idx==-1")
	assert.Equal(t, 0, e.ToIdx, "node slot is the contains TO endpoint")
	assert.Empty(t, cl.codeLinks)
}

// TestBuildContextLinks_TicketAbsent_DropsAndWarns is the fails-when-absent
// guard for the never-blocks contract: an unresolvable ticket_id emits NO
// batch edge (no Idx==-1 endpoint that would abort applyCreate) and a warning.
func TestBuildContextLinks_TicketAbsent_DropsAndWarns(t *testing.T) {
	fc := &fakeGraphCaller{} // ticket id resolves nowhere.
	cl := buildContextLinks(context.Background(), fc, "ghost-ticket", "", nil)

	assert.Empty(t, cl.batchEdges, "an absent ticket must NOT ride the batch")
	require.Len(t, cl.warnings, 1)
	assert.Contains(t, cl.warnings[0], "ghost-ticket")
	assert.Contains(t, cl.warnings[0], "dropped")
}

// TestBuildContextLinks_SessionGetOrCreate asserts the session edge reuses
// getOrCreateThoughtSessionClient: an existing session (found by the
// NodeThoughtSession Match) yields a session--contains-->node batch edge.
func TestBuildContextLinks_SessionGetOrCreate(t *testing.T) {
	fc := &fakeGraphCaller{
		// The NodeThoughtSession Match (knowledge type-browse) returns the
		// existing session by name — the same seeding shape the think lineage
		// test uses.
		nodeMatchResults: map[graphKey][]*knowledgev1.Node{
			{Type: "knowledge"}: {
				{Id: "sess-9", Type: string(kgtypes.NodeThoughtSession), SymbolName: "design"},
			},
		},
	}
	cl := buildContextLinks(context.Background(), fc, "", "design", nil)

	require.Empty(t, cl.warnings)
	require.Len(t, cl.batchEdges, 1)
	e := cl.batchEdges[0]
	assert.Equal(t, kgtypes.EdgeKGContains, e.Type)
	assert.Equal(t, "sess-9", e.FromID, "resolved session is the existing-node FROM endpoint")
	assert.Equal(t, 0, e.ToIdx)
}

// writePathInvoker invokes one of the five context-linking write paths with the given
// ticket/session/links context, returning the tool result text + IsError and the
// emitted CREATE MutationPlan. Centralizes the per-path call shape so the
// link-class matrix table runs identically across all five.
type writePathInvoker func(t *testing.T, fc *fakeGraphCaller, ticketID, session string, links []string) (text string, isErr bool, create *knowledgev1.MutationPlan)

func findingInvoker(t *testing.T, fc *fakeGraphCaller, ticketID, session string, links []string) (string, bool, *knowledgev1.MutationPlan) {
	res := handleClientMutateCreateFinding(context.Background(), interceptTestDeps{gc: fc}, mutateArgs{
		Operation: "create", Type: "finding", Name: "f", Summary: "a finding summary searchable",
		TicketID: ticketID, Session: session, Links: links,
	})
	return toolResultText(res), res.IsError, firstCreatePlan(t, fc)
}

func researchInvoker(t *testing.T, fc *fakeGraphCaller, ticketID, session string, links []string) (string, bool, *knowledgev1.MutationPlan) {
	res := handleClientMutateCreateResearch(context.Background(), interceptTestDeps{gc: fc}, mutateArgs{
		Operation: "create", Type: "research", Name: "r", Summary: "a research summary searchable",
		TicketID: ticketID, Session: session, Links: links,
	})
	return toolResultText(res), res.IsError, firstCreatePlan(t, fc)
}

func ruleInvoker(t *testing.T, fc *fakeGraphCaller, ticketID, session string, links []string) (string, bool, *knowledgev1.MutationPlan) {
	res := handleClientMutateCreateRule(context.Background(), interceptTestDeps{gc: fc}, mutateArgs{
		Operation: "create", Type: "rule", Name: "ru", Summary: "a rule summary searchable",
		TicketID: ticketID, Session: session, Links: links,
	})
	return toolResultText(res), res.IsError, firstCreatePlan(t, fc)
}

func decisionInvoker(t *testing.T, fc *fakeGraphCaller, ticketID, session string, links []string) (string, bool, *knowledgev1.MutationPlan) {
	a := struct {
		Operation string   `json:"operation"`
		Name      string   `json:"name"`
		Choice    string   `json:"choice"`
		Rationale string   `json:"rationale"`
		TicketID  string   `json:"ticket_id,omitempty"`
		Session   string   `json:"session,omitempty"`
		Links     []string `json:"links,omitempty"`
	}{Operation: "record_decision", Name: "d", Choice: "X", Rationale: "because", TicketID: ticketID, Session: session, Links: links}
	args, err := json.Marshal(a)
	require.NoError(t, err)
	_, res := InterceptRecordDecision(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{Name: "record_decision", Arguments: args})
	return toolResultText(res), res.IsError, firstCreatePlan(t, fc)
}

func thinkInvoker(t *testing.T, fc *fakeGraphCaller, ticketID, session string, links []string) (string, bool, *knowledgev1.MutationPlan) {
	a := struct {
		Operation string   `json:"operation"`
		Content   string   `json:"content"`
		Summary   string   `json:"summary"`
		TicketID  string   `json:"ticket_id,omitempty"`
		Session   string   `json:"session,omitempty"`
		Links     []string `json:"links,omitempty"`
	}{Operation: "think", Content: "a thought", Summary: "a thought summary searchable", TicketID: ticketID, Session: session, Links: links}
	args, err := json.Marshal(a)
	require.NoError(t, err)
	res := handleThinkClient(context.Background(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{Name: "thoughts", Arguments: args})
	return toolResultText(res), res.IsError, firstCreatePlan(t, fc)
}

var allWritePaths = map[string]writePathInvoker{
	"finding":  findingInvoker,
	"research": researchInvoker,
	"rule":     ruleInvoker,
	"decision": decisionInvoker,
	"think":    thinkInvoker,
}

// hasContainsFrom reports whether the plan carries a contains-->slot0 edge from
// fromID (used for both ticket and session contains assertions).
func hasContainsFrom(m *knowledgev1.MutationPlan, fromID string) bool {
	for _, e := range m.GetEdges() {
		if e.GetType() == string(kgtypes.EdgeKGContains) && e.GetFromId() == fromID && e.GetToIdx() == 0 {
			return true
		}
	}
	return false
}

// hasRelatesTo reports whether the plan carries a slot0--relates-to-->toID edge.
func hasRelatesTo(m *knowledgev1.MutationPlan, toID string) bool {
	for _, e := range m.GetEdges() {
		if e.GetType() == string(kgtypes.EdgeRelatesTo) && e.GetFromIdx() == 0 && e.GetToId() == toID {
			return true
		}
	}
	return false
}

// sessionContainsPresent reports whether the session--contains-->node edge is
// present across ANY mutation the path issued. The four mutate-create/decision
// paths emit it as a batch edge ON the create plan (via buildContextLinks); think
// now ALSO rides it as a batch edge on the create_batch (the atomic think-path
// fix — composeThoughtCreate appends session--contains-->thought to the CREATE
// plan, the hasContainsFrom shape). The LINK-plan shape is retained below for any
// legacy path that still emits a separate post-create contains LINK. Both shapes
// count as "present".
func sessionContainsPresent(plans []*knowledgev1.MutationPlan, sessionID string) bool {
	for _, m := range plans {
		// (a) batch-edge shape on a CREATE plan.
		if hasContainsFrom(m, sessionID) {
			return true
		}
		// (b) LINK-plan shape (think's linkSessionLineage): Selection.Ids[0]=session,
		// EdgeSpec.Relationship=contains.
		if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_LINK &&
			m.GetEdgeSpec().GetRelationship() == string(kgtypes.EdgeKGContains) {
			if ids := m.GetSelection().GetIds(); len(ids) > 0 && ids[0] == sessionID {
				return true
			}
		}
	}
	return false
}

// TestWritePaths_AllLinkClassesPresentWhenResolvable is the matrix proof:
// across ALL FIVE write paths (finding/research/rule/
// decision/think), a resolvable ticket_id + session + knowledge-link each ride
// the emitted create MutationPlan as ticket--contains, session--contains, and
// relates-to respectively. Session is present on the happy path because
// getOrCreateThoughtSessionClient creates the session when absent.
func TestWritePaths_AllLinkClassesPresentWhenResolvable(t *testing.T) {
	for name, invoke := range allWritePaths {
		t.Run(name, func(t *testing.T) {
			fc := &fakeGraphCaller{
				queryResponses: map[string]kgtools.ToolResult{
					"tkt":  nodeResultJSON(t, "tkt", "ticket", nil),
					"link": nodeResultJSON(t, "link", "finding", nil),
				},
				// An EXISTING session by name → getOrCreateThoughtSessionClient
				// returns "sess-x"; the session--contains edge uses that FROM id.
				nodeMatchResults: map[graphKey][]*knowledgev1.Node{
					{Type: "knowledge"}: {{Id: "sess-x", Type: string(kgtypes.NodeThoughtSession), SymbolName: "sx"}},
				},
				mutateIDs: []string{"new-node"},
			}
			text, isErr, m := invoke(t, fc, "tkt", "sx", []string{"link"})
			require.False(t, isErr, "%s create must succeed: %s", name, text)
			assert.True(t, hasContainsFrom(m, "tkt"), "%s: ticket--contains must ride the create batch", name)
			assert.True(t, hasRelatesTo(m, "link"), "%s: knowledge link--relates-to must ride the create batch", name)
			// Session-contains is present across the path's mutations: a batch edge
			// on the create plan (finding/research/rule/decision) or a post-create
			// LINK plan (think's pre-existing linkSessionLineage).
			assert.True(t, sessionContainsPresent(fc.execMutations, "sess-x"),
				"%s: session--contains must be present (resolvable session)", name)
		})
	}
}

// TestWritePaths_SessionResolveError_DropsAndWarns is degradation case (c):
// a getOrCreateThoughtSessionClient resolve error (the
// session browse Match errors) drops the session edge + warns, and the node is
// still created (non-error, ID returned). Asserted on the create paths whose
// session arm is delegated to buildContextLinks (finding/research/rule/decision;
// think keeps its own session handling and is out of this case's scope).
func TestWritePaths_SessionResolveError_DropsAndWarns(t *testing.T) {
	helperSessionPaths := map[string]writePathInvoker{
		"finding": findingInvoker, "research": researchInvoker,
		"rule": ruleInvoker, "decision": decisionInvoker,
	}
	for name, invoke := range helperSessionPaths {
		t.Run(name, func(t *testing.T) {
			fc := &fakeGraphCaller{
				mutateIDs: []string{"new-node"},
				// The NodeThoughtSession browse Match ERRORS → resolve/create fails.
				nodeMatchErr: map[string]error{string(kgtypes.NodeThoughtSession): errors.New("session backend down")},
			}
			text, isErr, m := invoke(t, fc, "", "doomed-session", nil)
			require.False(t, isErr, "%s: a session resolve error must NOT fail the create: %s", name, text)
			assert.Contains(t, text, "new-node", "%s: node ID still returned", name)
			assert.Contains(t, text, "doomed-session", "%s: a session-drop warning is rendered", name)
			for _, e := range m.GetEdges() {
				assert.NotEqual(t, string(kgtypes.EdgeKGContains), e.GetType(),
					"%s: no session--contains edge may ride the batch on a resolve error", name)
			}
		})
	}
}

// TestBuildContextLinks_LinkClassification covers the single-probe a/b/c split:
// (a) a knowledge-resolvable id rides the batch as relates-to; (c) an
// unresolvable id drops+warns.
func TestBuildContextLinks_LinkClassification(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"finding-1": nodeResultJSON(t, "finding-1", "finding", nil),
	}}
	cl := buildContextLinks(context.Background(), fc, "", "", []string{"finding-1", "ghost-link"})

	// Knowledge-resolvable link rode the batch.
	var sawRelates bool
	for _, e := range cl.batchEdges {
		if e.Type == kgtypes.EdgeRelatesTo && e.ToID == "finding-1" {
			sawRelates = true
			assert.Equal(t, 0, e.FromIdx, "relates-to FROM is the new node slot")
		}
	}
	assert.True(t, sawRelates, "a knowledge-resolvable link must ride the batch as relates-to")
	// Unresolvable link dropped+warned.
	require.Len(t, cl.warnings, 1)
	assert.Contains(t, cl.warnings[0], "ghost-link")
	assert.Empty(t, cl.codeLinks, "no foreign graphs seeded → no code links")
}
