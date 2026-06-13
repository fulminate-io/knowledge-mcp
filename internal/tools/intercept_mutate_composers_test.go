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

// practiceNodeResult seeds a single practice-graph node body keyed by id for the
// fake's Execute ByID probe.
func practiceNodeResult(t *testing.T, id, typ string) kgtools.ToolResult {
	t.Helper()
	payload := map[string]any{"id": id, "type": typ, "symbol_name": id}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(b)}}}
}

// ---------------------------------------------------------------------------
// Sub-item 1: cross-graph-link intra-practice branch.
// ---------------------------------------------------------------------------

// TestMutateComposers_CrossGraphLink_IntraPractice asserts that a
// mutate(link, graph:practice) where BOTH endpoints resolve in the same
// practice/<language> graph is claimed client-side and lowered to a LINK
// MutationPlan targeting that practice graph (the engine routes cross-graph).
func TestMutateComposers_CrossGraphLink_IntraPractice(t *testing.T) {
	fc := &fakeGraphCaller{
		// Both endpoints live in practice/design-patterns, NEITHER in knowledge —
		// so the FROM-first probe confirms cross-graph (foreign FROM) and the
		// intra-practice fast path then resolves both in practice/<language>.
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "practice", Name: "design-patterns"}: {
				"pat-1": practiceNodeResult(t, "pat-1", "pattern"),
				"uc-1":  practiceNodeResult(t, "uc-1", "use_case"),
			},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"practice", "design-patterns"}),
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","graph":"practice","language":"design-patterns","from":"pat-1","to":"uc-1","relationship":"Contains"}`),
	})
	require.True(t, handled, "intra-practice link must be claimed client-side")
	require.False(t, res.IsError, "intra-practice link: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "Linked in practice/design-patterns: pat-1 -[contains]-> uc-1")

	// Exactly ONE LINK MutationPlan, targeting the practice graph, lowercased rel.
	require.Len(t, fc.execMutations, 1)
	plan := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_LINK, plan.GetKind())
	require.NotNil(t, plan.GetEdgeSpec())
	assert.Equal(t, "contains", plan.GetEdgeSpec().GetRelationship(), "practice edges are lowercase")
	assert.Equal(t, "uc-1", plan.GetEdgeSpec().GetToId())
	require.NotNil(t, plan.GetSelection())
	assert.Equal(t, []string{"pat-1"}, plan.GetSelection().GetIds())

	// The Execute request envelope targets practice/<language>.
	require.NotEmpty(t, fc.execRequests)
	last := fc.execRequests[len(fc.execRequests)-1]
	require.NotNil(t, last.GetTarget())
	assert.Equal(t, "practice", last.GetTarget().GetGraph())
	assert.Equal(t, "design-patterns", last.GetTarget().GetLanguage())
}

// TestMutateComposers_CrossGraphLink_ProxyFallsThrough asserts that when an
// endpoint is NOT in the practice graph (the cross-graph proxy case), the
// composer returns (false, _) so the call falls through to the live legacy
// server handleCrossGraphLink. No client LINK MutationPlan is issued.
func TestMutateComposers_CrossGraphLink_ProxyFallsThrough(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			// Only `from` exists in the practice graph; `to` does not.
			"pat-1": practiceNodeResult(t, "pat-1", "pattern"),
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, _ := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","graph":"practice","language":"design-patterns","from":"pat-1","to":"missing-node","relationship":"relates-to"}`),
	})
	assert.False(t, handled, "proxy case must fall through to the legacy server handler")
	assert.Empty(t, fc.execMutations, "no client LINK MutationPlan issued on the proxy fall-through")
}

// TestMutateComposers_CrossGraphLink_LinkageHandledClientSide covers the case:
// mutate(link, link_graph:"linkage") is HANDLED client-side
// via crossgraph.ResolveAndLink — the proxies + the metadata-carrying edge land in
// the LINKAGE graph over the Execute seam, and the server's legacy ResolveOrProxy
// is NOT reached (no mutate gc.Call). With neither endpoint a node, both resolve
// best-effort to their raw ids and the LINK targets linkage with UPPERCASE casing.
func TestMutateComposers_CrossGraphLink_LinkageHandledClientSide(t *testing.T) {
	fc := &fakeGraphCaller{}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","link_graph":"linkage","from":"a","to":"b","relationship":"relates-to","method":"linker:test","confidence":0.7}`),
	})
	require.True(t, handled, "link_graph:linkage is handled client-side (no server ResolveOrProxy)")
	require.False(t, res.IsError, "linkage link: %s", toolResultText(res))

	// Exactly one LINK targeting linkage, carrying the edge metadata. No mutate
	// gc.Call (the composer rides Execute).
	var link *knowledgev1.MutationPlan
	var linkReq *knowledgev1.ExecuteRequest
	for _, r := range fc.execRequests {
		if m := r.GetMutation(); m != nil && m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_LINK {
			link, linkReq = m, r
		}
	}
	require.NotNil(t, link, "a linkage LINK plan was composed")
	assert.Equal(t, "linkage", linkReq.GetTarget().GetGraph(), "LINK targets the linkage graph")
	assert.Equal(t, []string{"a"}, link.GetSelection().GetIds(), "best-effort raw FROM")
	assert.Equal(t, "b", link.GetEdgeSpec().GetToId(), "best-effort raw TO")
	assert.Equal(t, "RELATES-TO", link.GetEdgeSpec().GetRelationship(), "linkage edge type UPPERCASE")
	assert.Equal(t, "linker:test", link.GetEdgeSpec().GetMethod())
	assert.InDelta(t, 0.7, link.GetEdgeSpec().GetConfidence(), 1e-9)
}

// The clear_llm_failures composer tests live in
// intercept_clear_llm_failures_test.go (file-length cap; same package).

// ---------------------------------------------------------------------------
// Sub-item 2: create-time validation gate (the intercept-composer path). The
// finding create composer rejects an embed-only type with a missing summary
// BEFORE any write, surfacing the legacy-equivalent error. This composer-level
// validation is a SEPARATE path from the type-aware engine create-body
// validation, which was relocated server-side — this composer gate stays
// client-side and is unaffected.
// ---------------------------------------------------------------------------

// TestMutateComposers_CreateValidationGateFires asserts the client-side
// create-validation gate fires for an embed-only type missing its summary. The
// finding create handler runs validate.Summary and returns an error result with
// no PersistBatch Execute issued.
func TestMutateComposers_CreateValidationGateFires(t *testing.T) {
	fc := &fakeGraphCaller{}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"finding","name":"f","description":"d"}`),
	})
	require.True(t, handled, "create=finding is claimed client-side")
	assert.True(t, res.IsError, "missing summary on an embed-only finding must be rejected")
	assert.Contains(t, toolResultText(res), "summary")
	assert.Empty(t, fc.execMutations, "no write issued when validation fails")
}

// (The type-aware create-body validation — system-managed reject / step
// description / embed-only summary+name — is enforced SERVER-SIDE in the engine
// decodeCreate. The client no longer prechecks it; instead a CREATE
// flows to Execute and the server's invalidMutation error is relayed verbatim.
// That end-to-end relay is verified in the engine package by the dispatch test
// that drives a non-summarizable create through Dispatch and asserts exec is
// called once and the server's "summary is required" message surfaces.)

// ---------------------------------------------------------------------------
// Sub-item 3: Source=llm:claude default on client create.
// ---------------------------------------------------------------------------

// TestMutateComposers_CreateStampsLLMClaudeSource drives a mutate(create,
// type=finding) through tools.InterceptMutate → handleClientMutateCreateFinding →
// buildFindingNode (stamps Source='llm:claude') → PersistBatch (engine.Compile +
// Execute), and asserts the captured CREATE NodeBody carries Source='llm:claude'.
// The captured plan IS the proto that crosses the wire, so this single assertion
// proves BOTH (a) the client default-stamp landed (buildFindingNode) AND (b) the
// NodeBody.source wire carriage (Phase 1 proto field-9 + Phase 3 nodeBodyToProto +
// wire_persist Source map) — i.e. Source is no longer lossy on the batch wire.
func TestMutateComposers_CreateStampsLLMClaudeSource(t *testing.T) {
	// Cheap in-memory unit check: the builder stamps the default.
	node := buildFindingNode(mutateArgs{Name: "f", Summary: "s", Description: "d"})
	assert.Equal(t, "llm:claude", node.Source, "client create stamps the llm:claude default")

	// End-to-end: the default survives onto the CREATE NodeBody crossing the wire.
	fc := &fakeGraphCaller{mutateIDs: []string{"finding-1"}}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"finding","name":"f","summary":"s"}`),
	})
	require.True(t, handled, "mutate(create, type=finding) is claimed client-side")
	require.False(t, res.IsError, "create finding: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 1, "exactly one CREATE Execute (PersistBatch)")
	plan := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, plan.GetKind())
	require.NotEmpty(t, plan.GetNodeBodies())
	assert.Equal(t, "llm:claude", plan.GetNodeBodies()[0].GetSource(),
		"the client llm:claude default survives onto the CREATE NodeBody.source on the wire")
}

// ---------------------------------------------------------------------------
// CLASS-A: practice/transformers create/update/delete via Execute seam.
// ---------------------------------------------------------------------------

// TestMutateComposers_PracticeCreate_TargetRouted asserts a practice create
// (no link_graph) is claimed client-side and lowered to a CREATE MutationPlan
// whose Execute envelope targets practice/<language>.
func TestMutateComposers_PracticeCreate_TargetRouted(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"new-pat"}}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"create","graph":"practice","language":"go","type":"finding","name":"P","summary":"s"}`),
	})
	require.True(t, handled, "practice create (no link_graph) must be claimed client-side")
	require.False(t, res.IsError, "practice create: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 1)
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, fc.execMutations[0].GetKind())
	require.NotEmpty(t, fc.execRequests)
	target := fc.execRequests[len(fc.execRequests)-1].GetTarget()
	require.NotNil(t, target)
	assert.Equal(t, "practice", target.GetGraph())
	assert.Equal(t, "go", target.GetLanguage())
}

// TestMutateComposers_TransformersCreate_TargetRouted asserts a transformers
// create is claimed and targets the transformers graph.
func TestMutateComposers_TransformersCreate_TargetRouted(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"r1"}}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"create","graph":"transformers","type":"recipe","name":"r","summary":"s"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "transformers create: %s", toolResultText(res))
	require.NotEmpty(t, fc.execRequests)
	assert.Equal(t, "transformers", fc.execRequests[len(fc.execRequests)-1].GetTarget().GetGraph())
}

// TestMutateComposers_PracticeUpdate_TargetRouted asserts a practice by-id
// update is claimed and lowered to an UPDATE MutationPlan targeting practice.
func TestMutateComposers_PracticeUpdate_TargetRouted(t *testing.T) {
	fc := &fakeGraphCaller{mutateAffected: 1}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","graph":"practice","language":"go","id":"pat-1","status":"active"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "practice update: %s", toolResultText(res))
	require.Len(t, fc.execMutations, 1)
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, fc.execMutations[0].GetKind())
	assert.Equal(t, []string{"pat-1"}, fc.execMutations[0].GetSelection().GetIds())
}

// TestMutateComposers_PracticeDelete_TargetRouted asserts a practice
// delete-by-ids is claimed and lowered to a DELETE MutationPlan.
func TestMutateComposers_PracticeDelete_TargetRouted(t *testing.T) {
	fc := &fakeGraphCaller{mutateAffected: 2}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"delete","graph":"practice","language":"go","ids":["a","b"]}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "practice delete: %s", toolResultText(res))
	require.Len(t, fc.execMutations, 1)
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, fc.execMutations[0].GetKind())
	assert.Equal(t, []string{"a", "b"}, fc.execMutations[0].GetSelection().GetIds())
}
