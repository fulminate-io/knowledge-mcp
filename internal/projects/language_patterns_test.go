// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// language_patterns_test.go keeps the PURE-BUILDER language-pattern tests:
// they drive BuildTicketNode / BuildPlanGraph directly with no store and no
// wire. The store-using validation + creation coverage (the
// TestValidateLanguagePatterns_* family and the *_LanguagePatternsIndependent
// cases) moved to package tools, exercised against fakeGraphCaller on the live
// interceptor path.

// singlePhaseSingleStep returns a minimal phase tree so BuildPlanGraph can run
// the full path without bloating the table. Lives here (the only remaining
// no-store consumer) after the store-using plan tests were migrated to tools.
func singlePhaseSingleStep() []PhaseArgs {
	return []PhaseArgs{
		{
			Name:     "phase-1",
			Overview: "only phase",
			Steps: []StepArgs{
				{Name: "step-1", Description: "only step"},
			},
		},
	}
}

// TestBuildTicketNode_EmitsAuditsEdges verifies that BuildTicketNode wires
// EdgeAudits edges from ticket → each LanguagePattern ID supplied.
func TestBuildTicketNode_EmitsAuditsEdges(t *testing.T) {
	args := TicketArgs{
		Name:             "t-with-lang-patterns",
		NoPatternsReason: "test",
		LanguagePatterns: []string{"lp-1", "lp-2"},
	}

	nodes, edges := BuildTicketNode(args, nil, nil, "", backends.RemoteRef{}, backends.Group{}, "")
	require.NotEmpty(t, nodes)

	var auditsTargets []string
	for _, e := range edges {
		if e.Type == kgtypes.EdgeAudits {
			auditsTargets = append(auditsTargets, e.ToID)
		}
	}
	assert.ElementsMatch(t, []string{"lp-1", "lp-2"}, auditsTargets,
		"BuildTicketNode should emit one EdgeAudits edge per LanguagePattern")
}

// TestBuildTicketNode_PersistsUnresolvedLanguagePatternsMetadata: when
// unresolvedLanguagePatternIDs is non-empty, the comma-joined list lands on
// the ticket node as the `unresolved_language_patterns` metadata key.
func TestBuildTicketNode_PersistsUnresolvedLanguagePatternsMetadata(t *testing.T) {
	args := TicketArgs{
		Name:             "t-unresolved-lang",
		NoPatternsReason: "test",
	}

	nodes, _ := BuildTicketNode(args, nil, []string{"unresolved-1", "unresolved-2"}, "", backends.RemoteRef{}, backends.Group{}, "")
	require.NotEmpty(t, nodes)
	ticket := nodes[0]
	assert.Equal(t, "unresolved-1,unresolved-2", kgtypes.Value(ticket, "unresolved_language_patterns"))
}

// TestBuildTicketNode_NoLanguagePatternsMetadataWhenEmpty: when the slice is
// empty/nil, the metadata key must NOT be set (avoid stale "" markers).
func TestBuildTicketNode_NoLanguagePatternsMetadataWhenEmpty(t *testing.T) {
	args := TicketArgs{
		Name:             "t-no-lang",
		NoPatternsReason: "test",
	}
	nodes, _ := BuildTicketNode(args, nil, nil, "", backends.RemoteRef{}, backends.Group{}, "")
	require.NotEmpty(t, nodes)
	assert.Empty(t, kgtypes.Value(nodes[0], "unresolved_language_patterns"),
		"empty unresolvedLanguagePatternIDs must not set metadata")
}

// TestBuildPlanGraph_EmitsAuditsEdges verifies that BuildPlanGraph wires
// EdgeAudits edges from plan → each LanguagePattern ID supplied.
func TestBuildPlanGraph_EmitsAuditsEdges(t *testing.T) {
	plan := PlanArgs{
		Name:             "p-with-lang-patterns",
		Goal:             "test",
		NoPatternsReason: "test",
		LanguagePatterns: []string{"lp-a", "lp-b", "lp-c"},
		Phases:           singlePhaseSingleStep(),
	}

	_, edges, buildErr := BuildPlanGraph(plan, nil, nil)
	require.NoError(t, buildErr)

	var auditsTargets []string
	for _, e := range edges {
		if e.Type == kgtypes.EdgeAudits {
			auditsTargets = append(auditsTargets, e.ToID)
		}
	}
	assert.ElementsMatch(t, []string{"lp-a", "lp-b", "lp-c"}, auditsTargets,
		"BuildPlanGraph should emit one EdgeAudits edge per LanguagePattern")
}

// TestBuildPlanGraph_PersistsUnresolvedLanguagePatternsMetadata: same as the
// ticket path — comma-joined unresolved IDs land on the plan node.
func TestBuildPlanGraph_PersistsUnresolvedLanguagePatternsMetadata(t *testing.T) {
	plan := PlanArgs{
		Name:             "p-unresolved-lang",
		Goal:             "test",
		NoPatternsReason: "test",
		Phases:           singlePhaseSingleStep(),
	}

	nodes, _, buildErr := BuildPlanGraph(plan, nil, []string{"u-1", "u-2"})
	require.NoError(t, buildErr)
	require.NotEmpty(t, nodes)
	planNode := nodes[0]
	assert.Equal(t, "u-1,u-2", kgtypes.Value(planNode, "unresolved_language_patterns"))
}
