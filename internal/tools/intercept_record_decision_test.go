// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// TestBuildDecisionNode_LongRationale_BoundedSummary proves the Summary fix:
// buildDecisionNode no longer composes choice+rationale+alternatives into
// Summary (which could blow past the 500-rune cap), but sets Summary to the
// bounded choice. A long rationale/alternatives is preserved in Description
// and metadata for retrieval, never leaked into Summary.
func TestBuildDecisionNode_LongRationale_BoundedSummary(t *testing.T) {
	choice := "Adopt the composite-DB store engine"
	rationale := strings.Repeat("x", 2000)
	alternatives := strings.Repeat("y", 2000)
	node := buildDecisionNode(recordDecisionArgs{
		Name:         "store-engine-decision",
		Choice:       choice,
		Rationale:    rationale,
		Alternatives: alternatives,
	})

	// (1) Summary stays within the rune cap — the long rationale never leaks in.
	assert.LessOrEqual(t, utf8.RuneCountInString(node.Summary), validate.SummaryMaxLen,
		"Summary must stay within the rune cap regardless of rationale length")

	// (2) Summary is the bounded choice, not the composed prose.
	assert.Equal(t, truncateAtWordCreate(choice, validate.SummaryMaxLen), node.Summary,
		"Summary must be the bounded choice, not choice+rationale+alternatives prose")

	// (3) Description and metadata carry the full content for retrieval.
	assert.Equal(t, choice, node.Description, "Description must preserve the full choice")
	assert.Equal(t, choice, kgtypes.Value(node, "choice"), "choice metadata preserved")
	assert.Equal(t, rationale, kgtypes.Value(node, "rationale"), "rationale metadata preserved in full")
	assert.Equal(t, alternatives, kgtypes.Value(node, "alternatives"), "alternatives metadata preserved in full")
}

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

func TestInterceptRecordDecision_EmptyRationale_Errors(t *testing.T) {
	const wantErr = "record_decision: rationale is required and must be non-empty (why this was decided)"

	// Empty rationale (valid choice) is rejected.
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, res := InterceptRecordDecision(deps, kgtools.CallToolParams{
		Name:      "record_decision",
		Arguments: json.RawMessage(`{"name":"d","choice":"do X","rationale":""}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Equal(t, wantErr, toolResultText(res))

	// Rationale key entirely absent is rejected the same way.
	handled, res = InterceptRecordDecision(deps, kgtools.CallToolParams{
		Name:      "record_decision",
		Arguments: json.RawMessage(`{"name":"d","choice":"do X"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Equal(t, wantErr, toolResultText(res))
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
