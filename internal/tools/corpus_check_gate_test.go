// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestCorpusCheckGate_RefusesCheckWithoutFixtures drives the real mutate
// dispatch head with a checks-graph finding that declares check_type and a
// pattern body but binds NO fixtures.
//
// It is the reproduction of the vacuous admission: before the gate exists this
// payload is admitted and reaches Execute, so a check nothing has ever run is
// written into the corpus and every later reader treats it as validated.
func TestCorpusCheckGate_RefusesCheckWithoutFixtures(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"new-check"}}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"create","graph":"checks","language":"go","type":"finding",` +
			`"name":"P","summary":"s","metadata":{"check_type":"ast_pattern","severity":"warning",` +
			`"language":"go","dsl_pattern":"defer $X.Close()"}}`),
	})
	require.True(t, handled, "a checks create is claimed client-side")
	require.True(t, res.IsError, "a check with no fixtures must be refused, got: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "check_fixture_bad",
		"the refusal must name the missing binding")
	assert.Empty(t, fc.execMutations, "a refused check must never reach the write path")
}

// The two fixture bodies the gate resolves. They differ only in whether the
// defer-Close the check looks for is present, so an admission below is
// attributable to the check firing and being silent in the right places.
const (
	gateBadFixture  = "package p\n\ntype c struct{}\n\nfunc (c) Close() error { return nil }\n\nfunc f() {\n\tdb := c{}\n\tdefer db.Close()\n}\n"
	gateGoodFixture = "package p\n\nfunc g() {}\n"
)

// checkMeta is a well-formed ast_pattern check bound to the two fixtures below.
func checkMeta() map[string]string {
	return map[string]string{
		"check_type":         "ast_pattern",
		"severity":           "warning",
		"language":           "go",
		"dsl_pattern":        "defer $X.Close()",
		"check_fixture_bad":  "fx-bad",
		"check_fixture_good": "fx-good",
	}
}

// exampleNodeResult seeds one checks-graph example node carrying Content,
// which is what the gate materializes and walks.
func exampleNodeResult(t *testing.T, id, content string) kgtools.ToolResult {
	t.Helper()
	payload := map[string]any{"id": id, "type": string(kgtypes.NodeExample), "content": content,
		"metadata": map[string]string{"language": "go"}}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(b)}}}
}

// fixturedCaller is a fake whose checks graph resolves both fixture nodes. The
// key carries an EMPTY name because checks is a singleton — the same shape the
// server's selector policy admits.
func fixturedCaller(t *testing.T, extra map[string]kgtools.ToolResult) *fakeGraphCaller {
	t.Helper()
	byID := map[string]kgtools.ToolResult{
		"fx-bad":  exampleNodeResult(t, "fx-bad", gateBadFixture),
		"fx-good": exampleNodeResult(t, "fx-good", gateGoodFixture),
	}
	maps.Copy(byID, extra)
	return &fakeGraphCaller{
		mutateIDs:                 []string{"new-check"},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{{Type: "checks"}: byID},
	}
}

func mutateJSON(t *testing.T, payload map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return b
}

// TestCorpusCheckGate_AdmitsValidatedCheck is the known-positive control for
// every refusal in this file: a check whose bad fixture matches and whose good
// fixture does not is WRITTEN. Without it a gate that refused every check would
// satisfy all the refusal assertions.
func TestCorpusCheckGate_AdmitsValidatedCheck(t *testing.T) {
	fc := fixturedCaller(t, nil)
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create", "graph": "checks", "language": "go", "type": "finding",
			"name": "P", "summary": "s", "metadata": checkMeta(),
		}),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "a validated check must be admitted: %s", toolResultText(res))
	require.Len(t, fc.execMutations, 1, "the admitted check reaches the write path exactly once")
}

// TestCorpusCheckGate_DoesNotGatePracticeWrites is the NEGATIVE leg of the
// retarget at the WRITE side, and it is what makes the admission tests above
// discriminating: a gate that fired on BOTH graphs would satisfy every one of
// them identically.
//
// The SAME fixture-less check payload that is refused with graph:"checks" is NOT
// refused by this gate with graph:"practice" — the gate does not consult the old
// location at all. The known-positive control runs first, so a non-refusal here
// cannot be explained by a gate that has stopped firing everywhere.
func TestCorpusCheckGate_DoesNotGatePracticeWrites(t *testing.T) {
	md := checkMeta()
	delete(md, "check_fixture_bad")
	delete(md, "check_fixture_good")

	// KNOWN-POSITIVE CONTROL: the identical payload IS refused in the new location.
	control := fixturedCaller(t, nil)
	_, controlRes := InterceptMutate(opCtx(), interceptTestDeps{gc: control}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create", "graph": "checks", "language": "go", "type": "finding",
			"name": "P", "summary": "s", "metadata": md,
		}),
	})
	require.True(t, controlRes.IsError,
		"control: a fixture-less check in the checks graph MUST be refused, or the assertion below is vacuous")

	// THE ACTUAL ASSERTION: the same payload aimed at the OLD location is not
	// this gate's business and reaches the write path.
	fc := fixturedCaller(t, nil)
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create", "graph": "practice", "language": "go", "type": "finding",
			"name": "P", "summary": "s", "metadata": md,
		}),
	})
	require.False(t, res.IsError,
		"the check gate must not fire on a practice write — it is scoped to the checks graph: %s", toolResultText(res))
	assert.Len(t, fc.execMutations, 1,
		"a practice write is not gated here and reaches the write path")
}

// TestCorpusCheckGate_RefusesOnUpsertAndUpdate covers the two shapes that never
// reach the graph passthrough arm as a create would: upsert declines past it
// entirely, and an update merges its metadata with the node's own.
func TestCorpusCheckGate_RefusesOnUpsertAndUpdate(t *testing.T) {
	t.Run("upsert", func(t *testing.T) {
		fc := fixturedCaller(t, nil)
		md := checkMeta()
		delete(md, "check_fixture_bad")
		delete(md, "check_fixture_good")
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: mutateJSON(t, map[string]any{
				"operation": "upsert", "graph": "checks", "language": "go", "type": "finding",
				"id": "chk-1", "name": "P", "summary": "s", "metadata": md,
			}),
		})
		require.True(t, res.IsError, "a fixture-less check must be refused on upsert: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "check_fixture_bad")
		assert.Empty(t, fc.execMutations)
	})

	// The merge case: the PAYLOAD carries only dsl_pattern, so a gate validating
	// the payload alone would see no check_type and wave it through. What the
	// write actually produces is the payload merged over the stored node.
	t.Run("update merges with the stored node", func(t *testing.T) {
		stored := nodeResultJSON(t, "chk-1", "finding", map[string]string{
			"check_type": "ast_pattern", "severity": "warning", "language": "go",
		})
		fc := fixturedCaller(t, map[string]kgtools.ToolResult{"chk-1": stored})
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: mutateJSON(t, map[string]any{
				"operation": "update", "graph": "checks", "language": "go", "id": "chk-1",
				"metadata": map[string]string{"dsl_pattern": "defer $X.Close()"},
			}),
		})
		require.True(t, res.IsError, "the merged node is a fixture-less check and must be refused: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "check_fixture_bad")
		assert.Empty(t, fc.execMutations)
	})
}

// TestCorpusCheckGate_RefusesCheckInKnowledgeGraph pins the placement rule: a
// check lives in a checks graph with the fixtures that validate it, so the
// knowledge-graph finding create refuses one rather than admitting it
// unvalidated — there are no fixtures to resolve on that path.
func TestCorpusCheckGate_RefusesCheckInKnowledgeGraph(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"kn-1"}}
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create", "type": "finding", "name": "P", "summary": "s",
			"metadata": checkMeta(),
		}),
	})
	require.True(t, res.IsError, "a knowledge-graph check must be refused: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "checks",
		"the refusal must name the graph the author should have used")
	assert.Empty(t, fc.execMutations)
}

// TestCorpusCheckGate_AllFourCarriersGated drives the same fixture-less check
// through each of the four metadata carriers a mutate payload can use. A decoder
// that declares four carriers and consults one passes a struct-tag grep; it does
// not pass this.
func TestCorpusCheckGate_AllFourCarriersGated(t *testing.T) {
	md := checkMeta()
	delete(md, "check_fixture_bad")
	delete(md, "check_fixture_good")

	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"metadata", map[string]any{
			"operation": "create", "graph": "checks", "language": "go", "type": "finding",
			"name": "P", "summary": "s", "metadata": md,
		}},
		{"nodes", map[string]any{
			"operation": "create_batch", "graph": "checks", "language": "go",
			"nodes": []map[string]any{{"type": "finding", "name": "P", "summary": "s", "metadata": md}},
		}},
		{"items", map[string]any{
			"operation": "update_batch", "graph": "checks", "language": "go",
			"items": []map[string]any{{"id": "chk-1", "metadata": md}},
		}},
		{"updates", map[string]any{
			"operation": "bulk_update_metadata", "graph": "checks", "language": "go",
			"updates": []map[string]any{{"id": "chk-1", "metadata": md}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := fixturedCaller(t, nil)
			_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name:      "mutate",
				Arguments: mutateJSON(t, tc.payload),
			})
			require.True(t, res.IsError, "carrier %s admitted a fixture-less check: %s", tc.name, toolResultText(res))
			assert.Contains(t, toolResultText(res), "check_fixture_bad")
			assert.Empty(t, fc.execMutations, "carrier %s reached the write path", tc.name)
		})
	}
}

// TestPayloadCommands_PathNamesCarrierAndIndex pins the diagnostic the shared
// carrier walk exists to preserve. Nothing else asserts the path text, so a
// helper that returned bare maps would leave the tools package green while the
// batch diagnostic quietly stopped naming which entry was at fault.
func TestPayloadCommands_PathNamesCarrierAndIndex(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"n1", "n2"}}
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create_batch",
			"nodes": []map[string]any{
				{"type": "finding", "name": "A", "summary": "a"},
				{"type": "criterion", "name": "B", "summary": "b", "metadata": map[string]string{
					"command": "go test ./cmd/knowledge/... -run '^TestSomething$'",
				}},
			},
		}),
	})
	require.True(t, res.IsError, "a vacuous selector command must be refused: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "nodes[1].metadata.command",
		"the refusal must name the carrier AND the index")
}

// The two GRAPH fixture bodies. They differ only in whether one function calls
// another, so a graph_assertion of "no function may call another function"
// FIRES on the first and is SILENT on the second.
//
// THE CALLEE IS DEFINED IN THE SNIPPET DELIBERATELY: an unresolved external
// symbol is dropped by reference resolution and emits no CALLS edge, so a
// fixture calling into the standard library would produce zero edges and the
// check would look clean for the wrong reason.
const (
	gateGraphBadFixture  = "package p\n\nfunc helper() int { return 1 }\n\nfunc caller() int { return helper() }\n\nfunc lonely() int { return 2 }\n"
	gateGraphGoodFixture = "package p\n\nfunc alpha() int { return 1 }\n\nfunc beta() int { return 2 }\n"
)

// graphCheckMeta is a well-formed graph_assertion check bound to the two graph
// fixtures above.
func graphCheckMeta() map[string]string {
	return map[string]string{
		"check_type":         "graph_assertion",
		"severity":           "warning",
		"language":           "go",
		"dsl_pattern":        `{"node_type":"function_declaration","edge_type":"CALLS","direction":"out","require":"absent"}`,
		"check_fixture_bad":  "gfx-bad",
		"check_fixture_good": "gfx-good",
	}
}

// TestCorpusCheckGate_AdmitsValidatedGraphAssertion proves the dispatch reaches
// the graph validator: a properly fixtured graph_assertion check is ADMITTED.
//
// THE ASSERTION IS ADMISSION, NOT REFUSAL, AND THAT IS THE POINT. Before the
// dispatch arm exists this check falls to the contract validator, which refuses
// every non-ast type with ErrNoExecutor — so a refusal-only assertion could not
// tell the wired state from the unwired one. Only an admission can.
func TestCorpusCheckGate_AdmitsValidatedGraphAssertion(t *testing.T) {
	fc := fixturedCaller(t, map[string]kgtools.ToolResult{
		"gfx-bad":  exampleNodeResult(t, "gfx-bad", gateGraphBadFixture),
		"gfx-good": exampleNodeResult(t, "gfx-good", gateGraphGoodFixture),
	})
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create", "graph": "checks", "language": "go", "type": "finding",
			"name": "no function calls another", "summary": "s", "metadata": graphCheckMeta(),
		}),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "a validated graph_assertion must be ADMITTED: %s", toolResultText(res))
	require.Len(t, fc.execMutations, 1, "the admitted check reaches the write path exactly once")
}

// TestCorpusCheckGate_RefusesUnvalidatedGraphAssertion is the discriminating
// half of the test above: the SAME dispatch arm must still refuse a graph check
// whose fixtures do not separate. Without it, an arm that admitted every
// graph_assertion unconditionally would pass the admission test.
func TestCorpusCheckGate_RefusesUnvalidatedGraphAssertion(t *testing.T) {
	fc := fixturedCaller(t, map[string]kgtools.ToolResult{
		// Both fixtures carry a call, so the check FIRES on the good example.
		"gfx-bad":  exampleNodeResult(t, "gfx-bad", gateGraphBadFixture),
		"gfx-good": exampleNodeResult(t, "gfx-good", gateGraphBadFixture),
	})
	md := graphCheckMeta()
	md["check_fixture_good"] = "gfx-good"
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create", "graph": "checks", "language": "go", "type": "finding",
			"name": "no function calls another", "summary": "s", "metadata": md,
		}),
	})
	require.True(t, handled)
	require.True(t, res.IsError, "a graph check that fires on its good example must be refused")
	assert.Empty(t, fc.execMutations, "a refused check must never reach the write path")
}

// TestCorpusCheckGate_RefusesCrossLanguageFixture pins a hazard that DID NOT
// EXIST before checks collapsed into one graph.
//
// Previously a Go check could not name a Python fixture: they lived in different
// per-language graphs, so the id simply did not resolve and the binding failed
// for free. Now every fixture in every language is one id lookup away, so a
// copy-pasted or mistyped binding resolves happily and validates a Go check
// against Python source. The structural guarantee became a metadata check, and
// this is the test that says so.
func TestCorpusCheckGate_RefusesCrossLanguageFixture(t *testing.T) {
	// The bad fixture is labeled python while the check declares go.
	pyBad := kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text",
		Text: string(mutateJSON(t, map[string]any{
			"id": "fx-bad", "type": string(kgtypes.NodeExample), "content": gateBadFixture,
			"metadata": map[string]string{"language": "python"},
		}))}}}
	fc := fixturedCaller(t, map[string]kgtools.ToolResult{"fx-bad": pyBad})

	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create", "graph": "checks", "language": "go", "type": "finding",
			"name": "P", "summary": "s", "metadata": checkMeta(),
		}),
	})
	require.True(t, res.IsError,
		"a go check bound to a python fixture must be refused: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "language",
		"the refusal must name the mismatched key so the author can see which side is wrong")
	assert.Empty(t, fc.execMutations, "a refused check must never reach the write path")
}

// TestCorpusCheckGate_RequiresLanguageQualifiedID pins the collision guard on
// CALLER-SUPPLIED ids, and its control pins the exemption that makes the rule
// workable.
//
// One graph means one id namespace across every language, so an author who names
// a node "no-naked-defer" for Go and again for Python silently overwrites the
// first. The rule applies ONLY where an author can name a node: a create carries
// no id and the store generates a unique one, so requiring a prefix there would
// reject every ordinary authoring call for no safety gain.
func TestCorpusCheckGate_RequiresLanguageQualifiedID(t *testing.T) {
	// CONTROL: a CREATE (no caller id) is admitted — generated ids cannot collide,
	// so the rule must not fire here or normal authoring breaks.
	control := fixturedCaller(t, nil)
	_, controlRes := InterceptMutate(opCtx(), interceptTestDeps{gc: control}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "create", "graph": "checks", "language": "go", "type": "finding",
			"name": "P", "summary": "s", "metadata": checkMeta(),
		}),
	})
	require.False(t, controlRes.IsError,
		"control: an id-less create must still be admitted: %s", toolResultText(controlRes))

	// THE ASSERTION: an upsert naming an UNQUALIFIED id is refused.
	fc := fixturedCaller(t, nil)
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "upsert", "graph": "checks", "language": "go", "type": "finding",
			"id": "no-naked-defer", "name": "P", "summary": "s", "metadata": checkMeta(),
		}),
	})
	require.True(t, res.IsError,
		"an unqualified caller-supplied check id must be refused: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "go:",
		"the refusal must show the required namespace so the fix is obvious")

	// AND THE QUALIFIED FORM IS ADMITTED, so the rule is a namespace requirement
	// rather than a blanket ban on naming your own nodes.
	okCaller := fixturedCaller(t, nil)
	_, okRes := InterceptMutate(opCtx(), interceptTestDeps{gc: okCaller}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "upsert", "graph": "checks", "language": "go", "type": "finding",
			"id": "go:no-naked-defer", "name": "P", "summary": "s", "metadata": checkMeta(),
		}),
	})
	require.False(t, okRes.IsError,
		"a language-qualified caller id must be admitted: %s", toolResultText(okRes))
}
