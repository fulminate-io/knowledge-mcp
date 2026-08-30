// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestInterceptCreateTestPlan_TopLevelSummaryClampsAndWarns proves the warnings
// channel ADDED to create_test_plan (which previously had none) surfaces a clamp
// warning end-to-end: an over-cap top-level author summary is clamped at a word
// boundary and the create SUCCEEDS, with a warning naming the field in the
// result body. Fails-when-absent: if the over-cap summary still hard-rejected,
// res.IsError would be true; if the added channel did not render, the body would
// carry no "clamped" warning.
func TestInterceptCreateTestPlan_TopLevelSummaryClampsAndWarns(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"tp-1", "step-1"}}
	deps := interceptTestDeps{gc: fc}
	longSummary := strings.Repeat("a", 600)
	handled, res := InterceptCreateTestPlan(opCtx(), deps, kgtools.CallToolParams{
		Name: "create_test_plan",
		Arguments: json.RawMessage(`{
			"name":"tp","goal":"g","summary":"` + longSummary + `","format":"json",
			"steps":[{"name":"s1","description":"do a thing","summary":"a concise step summary"}]
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "over-cap top-level summary must clamp + create, not error: %s", toolResultText(res))
	msg := toolResultText(res)
	assert.Contains(t, msg, "summary")
	assert.Contains(t, msg, "clamped")
	assert.NotContains(t, msg, "exceeds 500 characters", "over-cap author summary must clamp, not hard-reject")
}

// TestInterceptCreateTestPlan_StepSummaryClampsAndWarns proves the nested
// steps[i].summary clamp + slice-index assign-back works: an over-cap step
// summary is clamped and the create succeeds with a warning naming the indexed
// step field. Fails-when-absent: a range-value (rather than slice-index) assign
// back would clamp a copy, leaving the persisted summary over-cap; a missing
// channel would drop the warning.
func TestInterceptCreateTestPlan_StepSummaryClampsAndWarns(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"tp-1", "step-1"}}
	deps := interceptTestDeps{gc: fc}
	longStepSummary := strings.Repeat("b", 600)
	handled, res := InterceptCreateTestPlan(opCtx(), deps, kgtools.CallToolParams{
		Name: "create_test_plan",
		Arguments: json.RawMessage(`{
			"name":"tp","goal":"g","summary":"s","format":"json",
			"steps":[{"name":"s1","description":"do a thing","summary":"` + longStepSummary + `"}]
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "over-cap step summary must clamp + create, not error: %s", toolResultText(res))
	msg := toolResultText(res)
	assert.Contains(t, msg, "steps[0].summary")
	assert.Contains(t, msg, "clamped")
}

// TestInterceptCreateTestPlan_CriterionSummary is the gate proving the FOURTH
// criterion-creating path carried the author-supplied summary. create_test_plan's
// criteria reach the graph through projects.CriterionArgs and BuildCriterionNode,
// so a change that covered only create_plan would emit empty-summary criterion
// nodes here and the server would refuse the whole batch.
func TestInterceptCreateTestPlan_CriterionSummary(t *testing.T) {
	testPlanArgs := func(critFields string) json.RawMessage {
		return json.RawMessage(`{
			"name":"tp","goal":"g","summary":"a concise plan summary","format":"json",
			"steps":[{"name":"s1","description":"do a thing","summary":"a concise step summary",
			          "criteria":[{"description":"short criterion",` + critFields + `}]}]
		}`)
	}

	t.Run("empty criterion summary is refused at the indexed path", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateIDs: []string{"tp-1", "step-1"}}
		handled, res := InterceptCreateTestPlan(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "create_test_plan", Arguments: testPlanArgs(`"summary":""`),
		})
		require.True(t, handled)
		require.True(t, res.IsError, "a criterion with no summary must be refused, never derived")
		assert.Contains(t, toolResultText(res), "steps[0].criteria[0].summary")
		assert.Empty(t, fc.execMutations, "the reject must precede the persist — no partial test plan")
	})

	t.Run("over-cap summary clamps in the persisted body", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateIDs: []string{"tp-1", "step-1", "crit-1"}}
		handled, res := InterceptCreateTestPlan(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "create_test_plan", Arguments: testPlanArgs(`"summary":"` + strings.Repeat("word ", 120) + `"`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an over-cap criterion summary CLAMPS — never a hard reject: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "clamped")

		require.Len(t, fc.execMutations, 1)
		var found *knowledgev1.NodeBody
		for _, b := range fc.execMutations[0].GetNodeBodies() {
			if b.GetType() == string(kgtypes.NodeCriterion) {
				found = b
			}
		}
		require.NotNil(t, found, "no criterion body reached the persisted batch")
		assert.LessOrEqual(t, utf8.RuneCountInString(found.GetSummary()), 500)
	})
}
