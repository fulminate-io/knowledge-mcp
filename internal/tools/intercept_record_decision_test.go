// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

func TestInterceptRecordDecision_WrongTool_FallsThrough(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, _ := InterceptRecordDecision(deps, kgtools.CallToolParams{Name: "query"})
	assert.False(t, handled)
}

func TestInterceptRecordDecision_EmptyChoice_Errors(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, res := InterceptRecordDecision(deps, kgtools.CallToolParams{
		Name:      "record_decision",
		Arguments: json.RawMessage(`{"name":"d","choice":"","rationale":"r"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Equal(t,
		"record_decision: choice is required and must be non-empty (what was decided)",
		toolResultText(res))
}

func TestInterceptRecordDecision_HappyPath_TextFormat(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["dec-1"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptRecordDecision(deps, kgtools.CallToolParams{
		Name:      "record_decision",
		Arguments: json.RawMessage(`{"name":"fixture-decision","choice":"do X","rationale":"because"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "happy path should not error: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "Decision recorded: fixture-decision")
	assert.Contains(t, body, "[graph: knowledge/default]")
}

func TestInterceptRecordDecision_JSONFormat(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["dec-1"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptRecordDecision(deps, kgtools.CallToolParams{
		Name:      "record_decision",
		Arguments: json.RawMessage(`{"name":"fixture-decision","choice":"do X","rationale":"because","format":"json"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError)
	var parsed struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &parsed))
	assert.Equal(t, "dec-1", parsed.ID)
	assert.Equal(t, "fixture-decision", parsed.Name)
	assert.Nil(t, parsed.Warnings)
}
