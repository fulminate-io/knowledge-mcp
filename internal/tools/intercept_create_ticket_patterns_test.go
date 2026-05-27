// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// intercept_create_ticket_patterns_test.go ports the legacy
// projects/ticket_pattern_validation_test.go coverage onto the LIVE wire path:
// InterceptCreateTicket (local-only parent branch) driven through
// *fakeGraphCaller. resolveTicketPatterns runs the full validate→resolve tail
// (FUL-286 Phase 2) BEFORE any backend side-effect, so these tests assert
// against the create_batch MutationPlan the fake captures.

const localParentID = "proj-local"

// runCreateTicketLocal invokes InterceptCreateTicket with a local-only parent
// against fc. patternFragment is the JSON for the pattern fields (e.g.
// `"no_patterns_reason":"x"` or `"pattern_ids":["a"]`).
func runCreateTicketLocal(t *testing.T, fc *fakeGraphCaller, patternFragment string) kgtools.ToolResult {
	t.Helper()
	deps := interceptTestDeps{gc: fc}
	args := `{"name":"t","project_id":"` + localParentID + `","description":"d","summary":"s"`
	if patternFragment != "" {
		args += "," + patternFragment
	}
	args += "}"
	handled, res := InterceptCreateTicket(deps, kgtools.CallToolParams{
		Name:      "create_ticket",
		Arguments: json.RawMessage(args),
	})
	require.True(t, handled, "create_ticket must be claimed")
	return res
}

// seededTicketFake returns a fakeGraphCaller wired to (a) resolve the local-only
// parent project (no backend metadata) and (b) answer the create_batch Mutation.
func seededTicketFake() *fakeGraphCaller {
	return &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			localParentID: nodeResultJSONForTicketParent(),
		},
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["ticket-1"]}`}},
		},
	}
}

func nodeResultJSONForTicketParent() kgtools.ToolResult {
	// Local-only project: no backend metadata so InterceptCreateTicket takes
	// the createTicketLocalOnly branch.
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{"id":"` + localParentID + `","type":"project","metadata":{}}`}}}
}

// TestInterceptCreateTicket_TristateRejection runs every hard-validation reject
// shape and asserts the interceptor returns an IsError result with the
// load-bearing substring BEFORE any persist (no create_batch Mutation issued).
func TestInterceptCreateTicket_TristateRejection(t *testing.T) {
	cases := []struct {
		name     string
		fragment string
		wantErr  string
	}{
		{"none of three set", "", "exactly one of"},
		{"all three set", `"pattern_ids":["x"],"no_patterns_reason":"x","proposed_patterns":[{"name":"X"}]`, "exactly one of"},
		{"pattern_ids + no_patterns_reason", `"pattern_ids":["x"],"no_patterns_reason":"x"`, "exactly one of"},
		{"pattern_ids + proposed_patterns", `"pattern_ids":["x"],"proposed_patterns":[{"name":"X"}]`, "exactly one of"},
		{"no_patterns_reason + proposed_patterns", `"no_patterns_reason":"x","proposed_patterns":[{"name":"X"}]`, "exactly one of"},
		{"empty pattern_ids slice + no other signal", `"pattern_ids":[]`, "exactly one of"},
		{"pattern_ids contains empty string", `"pattern_ids":["  "]`, "pattern_ids[0] is empty"},
		{"proposed_pattern with empty Name", `"proposed_patterns":[{"name":"","sketch":"x"}]`, "proposed_patterns[0].name is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := seededTicketFake()
			res := runCreateTicketLocal(t, fc, tc.fragment)
			require.True(t, res.IsError, "case %q must reject", tc.name)
			assert.Contains(t, toolResultText(res), tc.wantErr)
			assert.Empty(t, fc.execMutations, "rejection must happen BEFORE any persist")
		})
	}
}

// TestInterceptCreateTicket_NoPatternsReason_Accepted: the no_patterns_reason
// shape is accepted and stamps the metadata on the ticket node.
func TestInterceptCreateTicket_NoPatternsReason_Accepted(t *testing.T) {
	fc := seededTicketFake()
	res := runCreateTicketLocal(t, fc, `"no_patterns_reason":"trivial doc edit"`)
	require.False(t, res.IsError, "no_patterns_reason should be accepted: %s", toolResultText(res))
	m := firstCreateBatch(t, fc)
	assert.Equal(t, "trivial doc edit", m.GetNodeBodies()[0].GetMetadata()["no_patterns_reason"])
}

// TestInterceptCreateTicket_ProposedPatterns_Accepted: a proposed_patterns
// shape eagerly creates an emerging NodePattern + a ticket→pattern EdgeUses
// edge in the create_batch MutationPlan.
func TestInterceptCreateTicket_ProposedPatterns_Accepted(t *testing.T) {
	fc := seededTicketFake()
	res := runCreateTicketLocal(t, fc, `"proposed_patterns":[{"name":"NewThing","sketch":"interface X { Y() }"}]`)
	require.False(t, res.IsError, "proposed_patterns should be accepted: %s", toolResultText(res))
	m := firstCreateBatch(t, fc)

	patIdx := -1
	for i, nb := range m.GetNodeBodies() {
		if nb.GetType() == string(kgtypes.NodePattern) && nb.GetName() == "NewThing" {
			patIdx = i
			assert.Equal(t, "emerging", nb.GetStatus())
			assert.Equal(t, "interface X { Y() }", nb.GetMetadata()["shape"])
		}
	}
	require.GreaterOrEqual(t, patIdx, 0, "expected an emerging NodePattern named NewThing in the batch")

	var sawUsesToPattern bool
	for _, e := range m.GetEdges() {
		if e.GetType() == string(kgtypes.EdgeUses) && int(e.GetToIdx()) == patIdx {
			sawUsesToPattern = true
		}
	}
	assert.True(t, sawUsesToPattern, "expected ticket→pattern EdgeUses edge to the emerging pattern node")
}

// TestInterceptCreateTicket_UnresolvedPatternID_WarnsAndStampsMetadata: a
// pattern_ids entry that resolves nowhere is accepted with a ## Warnings
// section and lands as unresolved_pattern_ids metadata on the ticket node.
func TestInterceptCreateTicket_UnresolvedPatternID_WarnsAndStampsMetadata(t *testing.T) {
	fc := seededTicketFake()
	res := runCreateTicketLocal(t, fc, `"pattern_ids":["does-not-exist-pattern"]`)
	require.False(t, res.IsError, "unresolved pattern id is a soft warning, not an error: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "## Warnings")
	assert.Contains(t, body, "does-not-exist-pattern")

	m := firstCreateBatch(t, fc)
	assert.Equal(t, "does-not-exist-pattern", m.GetNodeBodies()[0].GetMetadata()["unresolved_pattern_ids"],
		"ticket node should record the unresolved pattern id as metadata")
	assert.Equal(t, []string{"does-not-exist-pattern"}, usesEdgeTargets(m))
}

// TestInterceptCreateTicket_ResolvedKnowledgePatternID_NoWarnings: a pattern_id
// resolving to a knowledge-graph NodePattern produces no warnings and the
// EdgeUses target is the raw knowledge id (no proxy created).
func TestInterceptCreateTicket_ResolvedKnowledgePatternID_NoWarnings(t *testing.T) {
	fc := seededTicketFake()
	fc.queryResponses["kg-pat"] = nodeResultJSON(t, "kg-pat", "pattern", map[string]string{})
	res := runCreateTicketLocal(t, fc, `"pattern_ids":["kg-pat"]`)
	require.False(t, res.IsError, "resolved knowledge pattern should be accepted: %s", toolResultText(res))
	assert.NotContains(t, toolResultText(res), "## Warnings")

	m := firstCreateBatch(t, fc)
	assert.Empty(t, m.GetNodeBodies()[0].GetMetadata()["unresolved_pattern_ids"])
	assert.Equal(t, []string{"kg-pat"}, usesEdgeTargets(m))
}

// TestInterceptCreateTicket_LanguagePatternsIndependent: language_patterns is
// independent of the architectural-pattern tristate.
func TestInterceptCreateTicket_LanguagePatternsIndependent(t *testing.T) {
	fc := seededTicketFake()
	res := runCreateTicketLocal(t, fc, `"no_patterns_reason":"no architectural pattern needed","language_patterns":["missing-lang-2"]`)
	require.False(t, res.IsError, "language_patterns + no_patterns_reason must coexist: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "## Warnings")
	assert.Contains(t, body, "missing-lang-2")

	m := firstCreateBatch(t, fc)
	md := m.GetNodeBodies()[0].GetMetadata()
	assert.Equal(t, "missing-lang-2", md["unresolved_language_patterns"])
	assert.Equal(t, "no architectural pattern needed", md["no_patterns_reason"])
}

// TestInterceptCreateTicket_PracticePatternResolvesToProxyTarget is the
// cross-graph proof for the ticket path: a pattern_id seeded ONLY in a practice
// graph resolves to its deterministic knowledge proxy ID as the create_batch
// EdgeUses target, with no false "not found" warning.
func TestInterceptCreateTicket_PracticePatternResolvesToProxyTarget(t *testing.T) {
	const practiceGraph = "knowledge-architecture"
	const practicePatID = "practice-pat-tkt"

	practiceNode := knowledgev1.Node{
		Type:       string(kgtypes.NodePattern),
		SymbolName: "init-time-registry",
		Source:     "test",
		Status:     "active",
	}
	practiceNode.Id = practicePatID

	// crossgraph.BuildCrossGraphProxy takes the *knowledgev1.Node wire node + the
	// proto *knowledgev1.ProxyTarget directly; &practiceNode is the source (a
	// pointer, not a value copy, so the embedded proto MessageState is not
	// copylocked).
	expectedProxy, err := crossgraph.BuildCrossGraphProxy(&knowledgev1.ProxyTarget{
		GraphType: string(kgtypes.GraphPractice),
		Name:      practiceGraph,
		NodeId:    practicePatID,
	}, &practiceNode)
	require.NoError(t, err)

	fc := &fakeGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["ticket-1"]}`}},
		},
		// Knowledge-graph reads: the local-only parent project resolves here,
		// and the practice pattern id MISSES here (configured map, absent id).
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {
				localParentID: nodeResultJSONForTicketParent(),
			},
		},
		listGraphsResult: &kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"graphs":[{"graph_type":"practice","graph_name":"` + practiceGraph + `"}]}`}},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "practice", Name: practiceGraph}: {
				practicePatID: nodeResultForNode(t, &practiceNode),
			},
		},
	}

	res := runCreateTicketLocal(t, fc, `"pattern_ids":["`+practicePatID+`"]`)
	require.False(t, res.IsError, "practice-graph pattern should resolve cleanly: %s", toolResultText(res))
	assert.NotContains(t, toolResultText(res), "## Warnings", "practice-graph pattern must NOT produce a false not-found warning")

	m := firstCreateBatch(t, fc)
	targets := usesEdgeTargets(m)
	require.Len(t, targets, 1, "exactly one ticket→pattern EdgeUses edge")
	assert.Equal(t, expectedProxy.Id, targets[0],
		"EdgeUses target must be the deterministic knowledge proxy ID, not the raw practice id")
	assert.NotEqual(t, practicePatID, targets[0])
	assert.True(t, strings.HasPrefix(expectedProxy.Id, "proxy:practice:"+practiceGraph+":"))

	var sawProxyUpsert bool
	for _, mp := range fc.execMutations {
		if mp.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_UPSERT {
			sawProxyUpsert = true
		}
	}
	assert.True(t, sawProxyUpsert, "expected the practice proxy to be UPSERTed over the wire")
}
