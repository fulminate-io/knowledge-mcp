// SPDX-License-Identifier: Apache-2.0

package tools

// style_rule_check_scope_test.go is requirement 2's CHECK-SIDE half: the same
// two scope keys a style rule carries must survive onto a CHECK node, so a scan
// can narrow by them.
//
// IT IS A VENUE MATRIX RATHER THAN ONE ASSERTION, because the two admission
// entry points differ in exactly this respect and a single-venue test would pass
// on whichever one the author happened to pick. A mutate write into the checks
// graph leaves every non-contract metadata key untouched; manage_checks(create)
// builds a check's persisted metadata as a CLOSED LITERAL with no caller
// pass-through, so it cannot carry them at all.
//
// THE NEGATIVE CONTROL IS ASSERTED ON THE WRITTEN NODE, NOT ON AN ERROR. The
// manage_checks schema declares no metadata param, so a payload naming a scope
// key is refused as an undeclared param — a refusal that would be satisfied by a
// schema typo just as well. Reading the metadata the venue actually persisted is
// what shows the key could not have ridden it.
//
// NOTHING HERE CREATES A CHECK IN ANY REAL CORPUS: both venues run against test
// fakes, and this ticket's import creates no check at all.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// styleCheckScopePaths is the JSON-array value both venues are probed with. It
// carries a COMMA INSIDE ONE PATH, so a value that survived as two paths, or as
// a comma join, is distinguishable from one that survived whole.
const styleCheckScopePaths = `["cmd/knowledge","a,b/with-comma.go"]`

// TestCheckScopeKeys_SurviveAMutateWriteIntoTheChecksGraph is the seam S2's
// corpus scan consumes: a check written through mutate(create, graph:"checks")
// carrying the two style scope keys reads back carrying both, byte-identical.
func TestCheckScopeKeys_SurviveAMutateWriteIntoTheChecksGraph(t *testing.T) {
	fc := fixturedCaller(t, nil)
	md := checkMeta()
	md[kgtypes.MetaKeyStyleScopeRepo] = "knowledge"
	md[kgtypes.MetaKeyStyleScopePaths] = styleCheckScopePaths
	md[kgtypes.MetaKeySisterPractice] = "practice-rule-id"

	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create", "graph": "checks", "language": "go", "type": "finding",
			"name": "no-bare-http-client", "summary": "s", "metadata": md,
		}),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "the check must be admitted: %s", toolResultText(res))
	require.Len(t, fc.execMutations, 1, "the admitted check reaches the write path once")

	bodies := fc.execMutations[0].GetNodeBodies()
	require.Len(t, bodies, 1)
	written := bodies[0].GetMetadata()

	assert.Equal(t, "knowledge", written[kgtypes.MetaKeyStyleScopeRepo],
		"the repo scope survives the corpus-check gate onto the check node")
	assert.Equal(t, styleCheckScopePaths, written[kgtypes.MetaKeyStyleScopePaths],
		"the path scope survives BYTE-IDENTICAL, comma and all — the gate rewrites nothing")
	assert.Equal(t, "practice-rule-id", written[kgtypes.MetaKeySisterPractice],
		"and so does the cross-link back to the practice node")

	// The keys are read back through the SAME vocabulary the practice side uses,
	// so the two homes cannot drift into two encodings of one fact.
	sc, err := styleScopeFromMetadata(written)
	require.NoError(t, err)
	assert.True(t, sc.RepoSet)
	assert.Equal(t, []string{"cmd/knowledge", "a,b/with-comma.go"}, sc.Paths,
		"a two-path scope decodes as two paths — the comma stayed inside one of them")

	// The contract keys are untouched beside them: carrying scope metadata does
	// not disturb what makes the node a check.
	assert.Equal(t, "ast_pattern", written[corpus.MetaCheckType])
	assert.Equal(t, "warning", written[corpus.MetaSeverity])
}

// TestCheckScopeKeys_CannotRideManageChecksCreate is the negative control, on
// the same observable: the metadata manage_checks(create) PERSISTS carries
// neither scope key, because it is built as a closed literal.
func TestCheckScopeKeys_CannotRideManageChecksCreate(t *testing.T) {
	fake := &checksWriteFake{}
	res := runChecksCreate(t, fake, createChecksArgs())
	require.False(t, res.IsError, "the control payload must succeed: %s", res.Content[0].Text)
	require.Len(t, fake.plans, 2, "a successful create writes the fixtures, then the check")

	checkBodies := fake.plans[1].GetNodeBodies()
	require.Len(t, checkBodies, 1)
	written := checkBodies[0].GetMetadata()

	// The control first: this venue DID write a check, and its contract keys are
	// present — so the two absences below are earned rather than vacuous.
	require.Equal(t, "ast_pattern", written[corpus.MetaCheckType])
	require.Equal(t, "warning", written[corpus.MetaSeverity])

	assert.NotContains(t, written, kgtypes.MetaKeyStyleScopeRepo,
		"manage_checks(create) builds check metadata as a closed literal with no caller "+
			"pass-through, so a scope key cannot ride it — the mutate venue is the one that can")
	assert.NotContains(t, written, kgtypes.MetaKeyStyleScopePaths)
	assert.NotContains(t, written, kgtypes.MetaKeySisterPractice)
}

// TestCheckScopeKeys_VenueMatrix states the two rows together, so a reader sees
// the difference as a property of the VENUE rather than of either test.
func TestCheckScopeKeys_VenueMatrix(t *testing.T) {
	for _, row := range []struct {
		venue      string
		carriesKey bool
		metadata   func(t *testing.T) map[string]string
	}{
		{
			venue: "mutate(create, graph:\"checks\")", carriesKey: true,
			metadata: func(t *testing.T) map[string]string {
				t.Helper()
				fc := fixturedCaller(t, nil)
				md := checkMeta()
				md[kgtypes.MetaKeyStyleScopeRepo] = "knowledge"
				md[kgtypes.MetaKeyStyleScopePaths] = styleCheckScopePaths
				_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
					Name: "mutate",
					Arguments: mutateJSON(t, map[string]any{
						"operation": "create", "graph": "checks", "language": "go",
						"type": "finding", "name": "P", "summary": "s", "metadata": md,
					}),
				})
				require.False(t, res.IsError, toolResultText(res))
				require.Len(t, fc.execMutations, 1)
				return fc.execMutations[0].GetNodeBodies()[0].GetMetadata()
			},
		},
		{
			venue: "manage_checks(create)", carriesKey: false,
			metadata: func(t *testing.T) map[string]string {
				t.Helper()
				fake := &checksWriteFake{}
				res := runChecksCreate(t, fake, createChecksArgs())
				require.False(t, res.IsError, res.Content[0].Text)
				require.Len(t, fake.plans, 2)
				return fake.plans[1].GetNodeBodies()[0].GetMetadata()
			},
		},
	} {
		t.Run(row.venue, func(t *testing.T) {
			md := row.metadata(t)
			require.NotEmpty(t, md[corpus.MetaCheckType],
				"control: this venue wrote a check, so an absence below means the venue, not a no-op")
			for _, key := range []string{
				kgtypes.MetaKeyStyleScopeRepo, kgtypes.MetaKeyStyleScopePaths,
			} {
				if row.carriesKey {
					assert.Contains(t, md, key, "%s can carry %s", row.venue, key)
					continue
				}
				assert.NotContains(t, md, key, "%s cannot carry %s", row.venue, key)
			}
		})
	}
}

// TestStyleRuleShape_IsNeverCompiled pins the other half of requirement 2: a
// shaped rule's check fields ride the PRACTICE node and nothing executes them.
//
// TWO MECHANISMS HOLD IT, AND THEY ARE DIFFERENT ONES. A style rule carries no
// check_type, so nothing in the tree reads it as a check WHEREVER it lives —
// that is the contract's own boundary rule. Independently, the corpus-check gate
// returns before doing anything unless the write targets graph "checks", so even
// a practice node that DID carry check_type would never be compiled. The first
// two subtests observe the first mechanism; the third observes the second, and
// without it the graph filter would be unobserved — a fact this test learned by
// deleting the filter and staying green.
func TestStyleRuleShape_IsNeverCompiled(t *testing.T) {
	shape := map[string]any{
		kgtypes.MetaKeyPracticeKind: kgtypes.PracticeKindStyleRule,
		"severity":                  "warning",
		"dsl_pattern":               "defer $X.Close()",
		"check_where":               "{}",
	}

	// The hub the payload names has to BE one: a `source_hub` that resolves to no
	// node is refused on every practice write arm, so a fake that seeded no hub
	// would report that refusal instead of the admission this row measures.
	practiceFake := &fakeGraphCaller{
		mutateIDs:      []string{"new-style-rule"},
		queryResponses: map[string]kgtools.ToolResult{"hub-go": styleRuleHubNode("hub-go")},
	}
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: practiceFake}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create", "graph": "practice", "type": "pattern",
			"name": "defer-close", "summary": "close what you open",
			"source_hub": "hub-go", "metadata": shape,
		}),
	})
	require.False(t, res.IsError,
		"a practice rule carrying a pattern is admitted with NO fixtures — nothing compiles it: %s",
		toolResultText(res))
	require.Len(t, practiceFake.execMutations, 1, "the rule was written")
	assert.Equal(t, "defer $X.Close()",
		practiceFake.execMutations[0].GetNodeBodies()[0].GetMetadata()["dsl_pattern"],
		"the pattern is CARRIED onto the practice node — present, and never executed")

	// THE CONTROL, on the same instrument in the same run: the identical shape
	// written into the CHECKS graph IS compiled, and is refused for having no
	// fixtures. Without this row, the acceptance above would be equally
	// satisfied by a gate that never fires anywhere.
	checksFake := &fakeGraphCaller{mutateIDs: []string{"new-check"}}
	_, controlRes := InterceptMutate(opCtx(), interceptTestDeps{gc: checksFake}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create", "graph": "checks", "language": "go", "type": "finding",
			"name": "defer-close", "summary": "close what you open",
			"metadata": map[string]any{
				"check_type": "ast_pattern", "severity": "warning", "language": "go",
				"dsl_pattern": "defer $X.Close()",
			},
		}),
	})
	require.True(t, controlRes.IsError,
		"control: the same shape in the CHECKS graph is compiled and refused for having no fixtures")
	assert.Contains(t, toolResultText(controlRes), "check_fixture_bad")
	assert.Empty(t, checksFake.execMutations)

	// THE GATE'S GRAPH FILTER, OBSERVED DIRECTLY. The two writes above differ in
	// their metadata as well as their graph, so neither of them can tell the
	// filter from the missing check_type. This one carries the FULL check body —
	// check_type and a pattern, with no fixtures — into the PRACTICE graph. The
	// identical payload is refused in the checks graph, so admitting it here is
	// the filter and nothing else.
	practiceWithCheckType := &fakeGraphCaller{
		mutateIDs:      []string{"new-style-rule-2"},
		queryResponses: map[string]kgtools.ToolResult{"hub-go": styleRuleHubNode("hub-go")},
	}
	_, gateRes := InterceptMutate(opCtx(), interceptTestDeps{gc: practiceWithCheckType},
		kgtools.CallToolParams{
			Name: "mutate",
			Arguments: mutateJSON(t, map[string]any{
				"operation": "create", "graph": "practice", "type": "pattern",
				"name": "defer-close", "summary": "close what you open",
				"source_hub": "hub-go",
				"metadata": map[string]any{
					"check_type": "ast_pattern", "severity": "warning", "language": "go",
					"dsl_pattern": "defer $X.Close()",
				},
			}),
		})
	require.False(t, gateRes.IsError,
		"the corpus-check gate is graph-scoped: a practice write is not its business, "+
			"however check-shaped its metadata looks: %s", toolResultText(gateRes))
	require.Len(t, practiceWithCheckType.execMutations, 1,
		"and the node is written rather than refused")
}

// styleRuleHubNode renders one practice HUB node (type source) for the fakes in
// this file, which name a hub in their payloads and must therefore seed one.
func styleRuleHubNode(id string) kgtools.ToolResult {
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text",
		Text: `{"id":"` + id + `","type":"source"}`}}}
}
