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

// intercept_create_plan_patterns_test.go ports the legacy
// projects/plan_pattern_validation_test.go coverage onto the LIVE wire path:
// InterceptCreatePlan driven through *fakeGraphCaller. The interceptor now runs
// the full validate→resolve tail, so these tests assert the
// behavior against the create_batch MutationPlan the fake captures in
// execMutations rather than re-querying a real store.

// minimalPlanPhases is the one-phase/one-step JSON fragment every plan case
// needs (BuildPlanGraph requires at least one phase + a step with a description
// long enough to pass validate.StepDescription).
const minimalPlanPhases = `"phases":[{"name":"ph","overview":"o","summary":"s","steps":[{"name":"st","description":"step 1 description body","summary":"s"}]}]`

// runCreatePlan invokes InterceptCreatePlan with the given args JSON against fc.
func runCreatePlan(t *testing.T, fc *fakeGraphCaller, argsJSON string) kgtools.ToolResult {
	t.Helper()
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreatePlan(deps, kgtools.CallToolParams{
		Name:      "create_plan",
		Arguments: json.RawMessage(argsJSON),
	})
	require.True(t, handled, "create_plan must be claimed")
	return res
}

// seededPlanID is the fixed plan node ID the create_batch fake returns.
const seededPlanID = "plan-1"

// seededPlanFake returns a fakeGraphCaller wired to answer the create_batch
// Mutation with the created IDs and the post-create FetchNode walk (the plan
// node must resolve so the text-format renderer reaches the
// writeClientWarningsSection tail instead of the walk-failed one-liner).
func seededPlanFake() *fakeGraphCaller {
	return &fakeGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["` + seededPlanID + `","phase-1","step-1"]}`}},
		},
		queryResponses: map[string]kgtools.ToolResult{
			seededPlanID: {Content: []kgtools.ContentBlock{{Type: "text", Text: `{"id":"` + seededPlanID + `","type":"plan","metadata":{}}`}}},
		},
	}
}

// firstCreateBatch returns the CREATE MutationPlan the fake captured. Proxy
// UPSERTs (cross-graph resolution) precede the create_batch in execMutations, so
// scan for the CREATE kind rather than assuming index 0.
func firstCreateBatch(t *testing.T, fc *fakeGraphCaller) *knowledgev1.MutationPlan {
	t.Helper()
	require.NotEmpty(t, fc.execMutations, "expected a create_batch Mutation Execute")
	for _, m := range fc.execMutations {
		if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			return m
		}
	}
	t.Fatalf("no CREATE MutationPlan among %d captured mutations", len(fc.execMutations))
	return nil
}

// usesEdgeTargets returns every EdgeUses ToId on the MutationPlan.
func usesEdgeTargets(m *knowledgev1.MutationPlan) []string {
	var out []string
	for _, e := range m.GetEdges() {
		if e.GetType() == string(kgtypes.EdgeUses) {
			out = append(out, e.GetToId())
		}
	}
	return out
}

// TestInterceptCreatePlan_TristateRejection runs every hard-validation reject
// shape and asserts the interceptor returns an IsError result with the
// load-bearing substring BEFORE any persist (no create_batch Mutation issued).
func TestInterceptCreatePlan_TristateRejection(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		wantErr string
	}{
		{
			name:    "none of three set",
			args:    `{"name":"p","goal":"g","summary":"s",` + minimalPlanPhases + `}`,
			wantErr: "exactly one of",
		},
		{
			name:    "all three set",
			args:    `{"name":"p","goal":"g","summary":"s","pattern_ids":["x"],"no_patterns_reason":"x","proposed_patterns":[{"name":"X"}],` + minimalPlanPhases + `}`,
			wantErr: "exactly one of",
		},
		{
			name:    "pattern_ids + no_patterns_reason",
			args:    `{"name":"p","goal":"g","summary":"s","pattern_ids":["x"],"no_patterns_reason":"x",` + minimalPlanPhases + `}`,
			wantErr: "exactly one of",
		},
		{
			name:    "pattern_ids + proposed_patterns",
			args:    `{"name":"p","goal":"g","summary":"s","pattern_ids":["x"],"proposed_patterns":[{"name":"X"}],` + minimalPlanPhases + `}`,
			wantErr: "exactly one of",
		},
		{
			name:    "no_patterns_reason + proposed_patterns",
			args:    `{"name":"p","goal":"g","summary":"s","no_patterns_reason":"x","proposed_patterns":[{"name":"X"}],` + minimalPlanPhases + `}`,
			wantErr: "exactly one of",
		},
		{
			name:    "empty pattern_ids slice + no other signal",
			args:    `{"name":"p","goal":"g","summary":"s","pattern_ids":[],` + minimalPlanPhases + `}`,
			wantErr: "exactly one of",
		},
		{
			name:    "pattern_ids contains empty string",
			args:    `{"name":"p","goal":"g","summary":"s","pattern_ids":["  "],` + minimalPlanPhases + `}`,
			wantErr: "pattern_ids[0] is empty",
		},
		{
			name:    "proposed_pattern with empty Name",
			args:    `{"name":"p","goal":"g","summary":"s","proposed_patterns":[{"name":"","sketch":"x"}],` + minimalPlanPhases + `}`,
			wantErr: "proposed_patterns[0].name is empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := seededPlanFake()
			res := runCreatePlan(t, fc, tc.args)
			require.True(t, res.IsError, "case %q must reject", tc.name)
			assert.Contains(t, toolResultText(res), tc.wantErr)
			assert.Empty(t, fc.execMutations, "rejection must happen BEFORE any persist")
		})
	}
}

// TestInterceptCreatePlan_NoPatternsReason_Accepted: the no_patterns_reason
// shape is accepted and stamps the metadata on the plan node.
func TestInterceptCreatePlan_NoPatternsReason_Accepted(t *testing.T) {
	fc := seededPlanFake()
	res := runCreatePlan(t, fc, `{"name":"p","goal":"trivial doc edit","summary":"s","no_patterns_reason":"trivial doc edit",`+minimalPlanPhases+`}`)
	require.False(t, res.IsError, "no_patterns_reason should be accepted: %s", toolResultText(res))
	m := firstCreateBatch(t, fc)
	assert.Equal(t, "trivial doc edit", m.GetNodeBodies()[0].GetMetadata()["no_patterns_reason"])
}

// TestInterceptCreatePlan_ProposedPatterns_Accepted: a proposed_patterns shape
// eagerly creates an emerging NodePattern + a plan→pattern EdgeUses edge in the
// create_batch MutationPlan.
func TestInterceptCreatePlan_ProposedPatterns_Accepted(t *testing.T) {
	fc := seededPlanFake()
	res := runCreatePlan(t, fc, `{"name":"p","goal":"introduce a new pattern","summary":"s","proposed_patterns":[{"name":"NewThing","sketch":"interface X { Y() }"}],`+minimalPlanPhases+`}`)
	require.False(t, res.IsError, "proposed_patterns should be accepted: %s", toolResultText(res))
	m := firstCreateBatch(t, fc)

	// An emerging NodePattern with the proposed name + shape must be in the batch.
	var found bool
	patIdx := -1
	for i, nb := range m.GetNodeBodies() {
		if nb.GetType() == string(kgtypes.NodePattern) && nb.GetName() == "NewThing" {
			found = true
			patIdx = i
			assert.Equal(t, "emerging", nb.GetStatus())
			assert.Equal(t, "interface X { Y() }", nb.GetMetadata()["shape"])
			assert.NotEmpty(t, nb.GetSummary(), "pattern node must carry a non-empty Summary — an empty summary fails create-time validation and rolls back the whole batch")
		}
	}
	require.True(t, found, "expected an emerging NodePattern named NewThing in the batch")

	// A plan→pattern EdgeUses (by ToIdx to the pattern node) must exist.
	var sawUsesToPattern bool
	for _, e := range m.GetEdges() {
		if e.GetType() == string(kgtypes.EdgeUses) && int(e.GetToIdx()) == patIdx {
			sawUsesToPattern = true
		}
	}
	assert.True(t, sawUsesToPattern, "expected plan→pattern EdgeUses edge to the emerging pattern node")
}

// TestInterceptCreatePlan_UnresolvedPatternID_WarnsAndStampsMetadata: a
// pattern_ids entry that resolves nowhere is accepted with a ## Warnings
// section and lands as unresolved_pattern_ids metadata on the plan node.
func TestInterceptCreatePlan_UnresolvedPatternID_WarnsAndStampsMetadata(t *testing.T) {
	fc := seededPlanFake()
	res := runCreatePlan(t, fc, `{"name":"p","goal":"g","summary":"s","pattern_ids":["does-not-exist-pattern"],`+minimalPlanPhases+`}`)
	require.False(t, res.IsError, "unresolved pattern id is a soft warning, not an error: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "## Warnings")
	assert.Contains(t, body, "does-not-exist-pattern")

	m := firstCreateBatch(t, fc)
	assert.Equal(t, "does-not-exist-pattern", m.GetNodeBodies()[0].GetMetadata()["unresolved_pattern_ids"],
		"plan node should record the unresolved pattern id as metadata")
	// The EdgeUses target for an unresolved id passes through AS-IS.
	assert.Equal(t, []string{"does-not-exist-pattern"}, usesEdgeTargets(m))
}

// TestInterceptCreatePlan_ResolvedKnowledgePatternID_NoWarnings: a pattern_id
// resolving to a knowledge-graph NodePattern produces no warnings and the
// EdgeUses target is the raw knowledge id (no proxy created).
func TestInterceptCreatePlan_ResolvedKnowledgePatternID_NoWarnings(t *testing.T) {
	fc := seededPlanFake()
	fc.queryResponses["kg-pat"] = nodeResultJSON(t, "kg-pat", "pattern", map[string]string{})
	res := runCreatePlan(t, fc, `{"name":"p","goal":"g","summary":"s","pattern_ids":["kg-pat"],`+minimalPlanPhases+`}`)
	require.False(t, res.IsError, "resolved knowledge pattern should be accepted: %s", toolResultText(res))
	assert.NotContains(t, toolResultText(res), "## Warnings", "resolved pattern should produce no warnings")

	m := firstCreateBatch(t, fc)
	assert.Empty(t, m.GetNodeBodies()[0].GetMetadata()["unresolved_pattern_ids"])
	assert.Equal(t, []string{"kg-pat"}, usesEdgeTargets(m), "knowledge pattern passes through unchanged as the edge target")
}

// TestInterceptCreatePlan_LanguagePatternsIndependent: language_patterns is
// independent of the architectural-pattern tristate — a plan can carry an
// unresolved language_pattern alongside no_patterns_reason; the language
// warning surfaces and unresolved_language_patterns metadata is stamped.
func TestInterceptCreatePlan_LanguagePatternsIndependent(t *testing.T) {
	fc := seededPlanFake()
	res := runCreatePlan(t, fc, `{"name":"p","goal":"g","summary":"s","no_patterns_reason":"no architectural pattern needed","language_patterns":["missing-lang-pattern"],`+minimalPlanPhases+`}`)
	require.False(t, res.IsError, "language_patterns + no_patterns_reason must coexist: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "## Warnings")
	assert.Contains(t, body, "missing-lang-pattern")

	m := firstCreateBatch(t, fc)
	md := m.GetNodeBodies()[0].GetMetadata()
	assert.Equal(t, "missing-lang-pattern", md["unresolved_language_patterns"])
	assert.Equal(t, "no architectural pattern needed", md["no_patterns_reason"])
}

// TestInterceptCreatePlan_PracticePatternResolvesToProxyTarget is the cross-graph
// PROOF the ticket demands: a pattern_id seeded ONLY in a practice graph resolves
// — over the live wire path through the fake — to its DETERMINISTIC knowledge
// proxy ID (store.BuildCrossGraphProxy) as the create_batch EdgeUses target, with
// no false "not found" warning. This proves cross-practice pattern resolution
// works on the rewritten wire path.
func TestInterceptCreatePlan_PracticePatternResolvesToProxyTarget(t *testing.T) {
	const practiceGraph = "knowledge-architecture"
	const practicePatID = "practice-pat-1"

	// The practice-graph pattern node (the source the proxy is built from), in
	// the wire shape the fake response carrier returns.
	practiceNode := knowledgev1.Node{
		Type:       string(kgtypes.NodePattern),
		SymbolName: "init-time-registry",
		Source:     "test",
		Status:     "active",
	}
	practiceNode.Id = practicePatID

	// Expected deterministic proxy ID — byte-identical to what the wire path
	// (crossgraph.UpsertForeignProxy → crossgraph.BuildCrossGraphProxy) produces.
	// The builder now takes the *knowledgev1.Node wire node + the proto
	// *knowledgev1.ProxyTarget directly; &practiceNode is the source (a pointer,
	// not a value copy, so the embedded proto MessageState is not copylocked).
	expectedProxy, err := crossgraph.BuildCrossGraphProxy(&knowledgev1.ProxyTarget{
		GraphType: string(kgtypes.GraphPractice),
		Name:      practiceGraph,
		NodeId:    practicePatID,
	}, &practiceNode)
	require.NoError(t, err)

	fc := seededPlanFake()
	// Knowledge by-id MISS for practicePatID (the queryResponsesByGraph map IS
	// configured for "knowledge" but lacks the id → not found in knowledge).
	fc.queryResponsesByGraph = map[string]map[string]kgtools.ToolResult{
		"knowledge": {},
	}
	// Practice graph enumeration: listForeignGraphs reads this to discover the
	// loaded practice graph.
	fc.listGraphsResult = &kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"graphs":[{"graph_type":"practice","graph_name":"` + practiceGraph + `"}]}`}},
	}
	// Practice by-id HIT keyed by (practice, knowledge-architecture).
	fc.queryResponsesByGraphName = map[graphKey]map[string]kgtools.ToolResult{
		{Type: "practice", Name: practiceGraph}: {
			practicePatID: nodeResultForNode(t, &practiceNode),
		},
	}

	res := runCreatePlan(t, fc, `{"name":"p","goal":"g","summary":"s","pattern_ids":["`+practicePatID+`"],`+minimalPlanPhases+`}`)
	require.False(t, res.IsError, "practice-graph pattern should resolve cleanly: %s", toolResultText(res))
	assert.NotContains(t, toolResultText(res), "## Warnings", "practice-graph pattern must NOT produce a false not-found warning")

	m := firstCreateBatch(t, fc)
	targets := usesEdgeTargets(m)
	require.Len(t, targets, 1, "exactly one plan→pattern EdgeUses edge")
	assert.Equal(t, expectedProxy.Id, targets[0],
		"EdgeUses target must be the deterministic knowledge proxy ID, not the raw practice id")
	assert.NotEqual(t, practicePatID, targets[0], "edge target must NOT be the raw practice id")
	assert.True(t, strings.HasPrefix(expectedProxy.Id, "proxy:practice:"+practiceGraph+":"),
		"sanity: deterministic practice proxy ID shape")

	// The proxy itself was UPSERTed over the wire as a separate Mutation.
	var sawProxyUpsert bool
	for _, mp := range fc.execMutations {
		if mp.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_UPSERT {
			sawProxyUpsert = true
		}
	}
	assert.True(t, sawProxyUpsert, "expected the practice proxy to be UPSERTed over the wire")
}

// nodeResultForNode marshals a full knowledgev1.Node into the single-node ToolResult
// body shape the fake's Execute path decodes (encodeNodeResult → nodes_json).
func nodeResultForNode(t *testing.T, n *knowledgev1.Node) kgtools.ToolResult {
	t.Helper()
	b, err := json.Marshal(n)
	require.NoError(t, err)
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(b)}}}
}
