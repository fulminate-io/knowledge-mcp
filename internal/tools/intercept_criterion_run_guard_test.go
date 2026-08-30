// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_criterion_run_guard_test.go drives the FOUR criterion write paths
// with a command that selects tests by name and asserts nothing about whether
// the selector matched anything. `go test <pkg> -run NameThatMatchesNothing`
// exits 0 printing "ok <pkg> [no tests to run]", so a criterion whose pass
// condition is the exit code cannot tell "verified" from "verified nothing".
//
// Each test names the field path the rejection must carry, not merely that some
// error occurred: the indexed paths are what make a create_plan or
// create_test_plan rejection actionable, and the add_criterion path's
// criterion.command is what proves the path-specific guard fired rather than a
// generic payload check that ran earlier and reported a coarser field.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// bareRunCommand selects by name and asserts nothing about the selection. Short
// enough to survive the error's bounded quote unelided, so a test can assert the
// message quotes it in full.
const bareRunCommand = "go test ./internal/recipe/ -run TestSomething"

// TestInterceptAddCriterion_BareRunCommandRejected — mutate(create,
// type=criterion) carrying the bare command in the `command` PARAMETER.
func TestInterceptAddCriterion_BareRunCommandRejected(t *testing.T) {
	gc := seededStepGc()
	args := mustMarshal(t, map[string]any{
		"operation": "create", "type": "criterion", "step_id": testStepID,
		"description": "the recipe suite is green",
		"summary":     "the recipe suite is green",
		"command":     bareRunCommand,
	})
	handled, res := InterceptAddCriterion(opCtx(), &logE2EDeps{gc: gc}, kgtools.CallToolParams{
		Name: "mutate", Arguments: args,
	})
	require.True(t, handled, "a rejected create must be claimed, not fall through")
	require.True(t, res.IsError, "a selector with no assertion on the selection must reject")
	msg := extractText(res)
	assert.Contains(t, msg, "criterion.command", "the rejection must name the field path")
	assert.Contains(t, msg, bareRunCommand, "the rejection must quote the offending command")
	require.Len(t, gc.calls, 1, "only the step lookup fired — no upsert, no links")
	assert.Equal(t, "query", gc.calls[0].tool)
}

// TestInterceptAddCriterion_BareRunMetadataKeyRejected is the METADATA-FORM
// catcher: the same intercept, with the bare command arriving as
// metadata:{"command": ...} and the `command` parameter left EMPTY. The
// intercept copies caller metadata FIRST and only overwrites from the parameter
// when it is set, so this payload reaches the graph unguarded against a fix that
// lints the parameter rather than the value about to be stored.
//
// Naming criterion.command is what makes this catch a second failure shape: a
// generic payload check running earlier on this arm would also reject, but under
// a coarser field path, and the caller would be told to look at the wrong thing.
func TestInterceptAddCriterion_BareRunMetadataKeyRejected(t *testing.T) {
	gc := seededStepGc()
	args := mustMarshal(t, map[string]any{
		"operation": "create", "type": "criterion", "step_id": testStepID,
		"description": "the recipe suite is green",
		"summary":     "the recipe suite is green",
		"metadata":    map[string]string{"command": bareRunCommand},
	})
	handled, res := InterceptAddCriterion(opCtx(), &logE2EDeps{gc: gc}, kgtools.CallToolParams{
		Name: "mutate", Arguments: args,
	})
	require.True(t, handled)
	require.True(t, res.IsError, "the metadata form stores the same value and must reject the same way")
	msg := extractText(res)
	assert.Contains(t, msg, "criterion.command",
		"the rejection must name the criterion field path, not a generic payload path")
	assert.Contains(t, msg, bareRunCommand)
}

// TestInterceptCreatePlan_BareRunCriterionRejected — the create_plan per-criterion
// loop, whose rejection carries the INDEXED path so the author can find the
// offending criterion in a tree of dozens.
func TestInterceptCreatePlan_BareRunCriterionRejected(t *testing.T) {
	fc := &fakePlanGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["plan-1","phase-1","step-1"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreatePlan(opCtx(), deps, kgtools.CallToolParams{
		Name: "create_plan",
		Arguments: json.RawMessage(`{
			"name":"fixture-plan",
			"goal":"fixture plan goal",
			"summary":"fixture summary",
			"no_patterns_reason":"trivial",
			"phases":[{"name":"phase-1","overview":"o","summary":"s","steps":[
				{"name":"step-1","description":"step 1 description body","summary":"s",
				 "criteria":[{"description":"the recipe suite is green","summary":"the recipe suite is green","type":"automated",
				              "command":"` + bareRunCommand + `"}]}
			]}]
		}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError, "a bare selector anywhere in the tree must fail the whole create")
	assert.Contains(t, toolResultText(res), "phases[0].steps[0].criteria[0].command",
		"the rejection must carry the indexed path to the offending criterion")
	assert.Empty(t, fc.calls, "the reject must precede every RPC — no partial plan is persisted")
}

// TestInterceptCreateTestPlan_BareRunCriterionRejected — the fourth write path.
// create_test_plan's criteria feed projects.CriterionArgs and reach the graph
// through the test-plan builder, a route no criterion validation covered.
func TestInterceptCreateTestPlan_BareRunCriterionRejected(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"tp-1", "step-1"}}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreateTestPlan(opCtx(), deps, kgtools.CallToolParams{
		Name: "create_test_plan",
		Arguments: json.RawMessage(`{
			"name":"tp","goal":"g","summary":"a concise plan summary",
			"steps":[{"name":"s1","description":"do a thing","summary":"a concise step summary",
			          "criteria":[{"description":"the recipe suite is green","summary":"the recipe suite is green","type":"automated",
			                       "command":"` + bareRunCommand + `"}]}]
		}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError, "create_test_plan stores the same command shape and must reject it too")
	assert.Contains(t, toolResultText(res), "steps[0].criteria[0].command",
		"the rejection must carry the indexed path to the offending criterion")
	assert.Empty(t, fc.execMutations, "the reject must precede the persist — no partial test plan")
}

// TestInterceptMutateUpdate_BareRunCommandRejected covers the path a stored
// command CHANGES through, and its false-block fence.
//
// The fence is the load-bearing half. Thousands of criteria already carry a bare
// selector; a guard reading the EFFECTIVE post-update command (supplied value
// falling back to the stored one) would reject every ordinary edit to all of
// them — including the repair edits that replace those very commands. The guard
// fires only when the caller is SETTING a command.
func TestInterceptMutateUpdate_BareRunCommandRejected(t *testing.T) {
	t.Run("setting_a_bare_command_is_rejected", func(t *testing.T) {
		node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
			map[string]string{"type": "automated"})
		a := mutateArgs{Operation: "update", ID: "c1", Command: bareRunCommand}
		fc := &fakeGraphCaller{}
		deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}
		handled, res := handleClientMutateUpdateTyped(context.Background(), deps,
			withRawArgs(a, typedUpdateRaw(t, a)), node)

		require.True(t, handled, "the rejection is a claim — it must not fall through silently")
		require.True(t, res.IsError, "setting a bare selector must reject: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "criterion.command",
			"the rejection must name the field path")
		assert.Empty(t, fc.execMutations, "a rejected update issues ZERO forwards")
	})

	t.Run("updating_other_fields_leaves_a_legacy_command_untouched", func(t *testing.T) {
		node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
			map[string]string{"type": "automated", "command": bareRunCommand})
		fc, handled := runTypedUpdate(t, node, mutateArgs{
			Operation:   "update",
			ID:          "c1",
			Description: "a clearer description of the same check",
		})
		require.True(t, handled)
		m := lastUpdatePlan(t, fc)
		assert.Equal(t, "a clearer description of the same check", m.GetSetFields()["description"],
			"an edit that never mentions command must go through untouched")
	})
}
