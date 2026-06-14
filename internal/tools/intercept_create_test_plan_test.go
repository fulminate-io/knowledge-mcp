// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
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
	handled, res := InterceptCreateTestPlan(deps, kgtools.CallToolParams{
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
	handled, res := InterceptCreateTestPlan(deps, kgtools.CallToolParams{
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
