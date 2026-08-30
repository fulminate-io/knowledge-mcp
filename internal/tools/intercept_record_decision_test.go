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

// TestBuildDecisionNode_LongRationale_AuthorSummaryOnly proves the Summary rule:
// buildDecisionNode reads the AUTHOR'S summary and composes nothing: neither
// choice, nor rationale, nor alternatives reaches Summary. A long
// rationale/alternatives is preserved in Description and metadata for
// retrieval, never leaked into Summary.
func TestBuildDecisionNode_LongRationale_AuthorSummaryOnly(t *testing.T) {
	choice := "Adopt the composite-DB store engine"
	const authored = "the store engine is the composite DB, not the per-graph blob"
	rationale := strings.Repeat("x", 2000)
	alternatives := strings.Repeat("y", 2000)
	node := buildDecisionNode(recordDecisionArgs{
		Name:         "store-engine-decision",
		Summary:      authored,
		Choice:       choice,
		Rationale:    rationale,
		Alternatives: alternatives,
	})

	// (1) Summary is the author's text, byte-for-byte.
	assert.Equal(t, authored, node.Summary,
		"Summary must be the author's, not composed from choice/rationale/alternatives")

	// (2) None of the composition sources leaked in. Asserted separately from
	// the equality because a future composer that APPENDED to the author's text
	// would still start with it.
	assert.NotContains(t, node.Summary, choice)
	assert.NotContains(t, node.Summary, "x")
	assert.LessOrEqual(t, utf8.RuneCountInString(node.Summary), validate.SummaryMaxLen)

	// (3) Description and metadata carry the full content for retrieval.
	assert.Equal(t, choice, node.Description, "Description must preserve the full choice")
	assert.Equal(t, choice, kgtypes.Value(node, "choice"), "choice metadata preserved")
	assert.Equal(t, rationale, kgtypes.Value(node, "rationale"), "rationale metadata preserved in full")
	assert.Equal(t, alternatives, kgtypes.Value(node, "alternatives"), "alternatives metadata preserved in full")
}

func TestInterceptRecordDecision_WrongTool_FallsThrough(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, _ := InterceptRecordDecision(opCtx(), deps, kgtools.CallToolParams{Name: "query"})
	assert.False(t, handled)
}

func TestInterceptRecordDecision_EmptyChoice_Errors(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, res := InterceptRecordDecision(opCtx(), deps, kgtools.CallToolParams{
		Name:      "record_decision",
		Arguments: json.RawMessage(`{"name":"d","summary":"a decision summary","choice":"","rationale":"r"}`),
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
	handled, res := InterceptRecordDecision(opCtx(), deps, kgtools.CallToolParams{
		Name:      "record_decision",
		Arguments: json.RawMessage(`{"name":"d","summary":"a decision summary","choice":"do X","rationale":""}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Equal(t, wantErr, toolResultText(res))

	// Rationale key entirely absent is rejected the same way.
	handled, res = InterceptRecordDecision(opCtx(), deps, kgtools.CallToolParams{
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
	handled, res := InterceptRecordDecision(opCtx(), deps, kgtools.CallToolParams{
		Name:      "record_decision",
		Arguments: json.RawMessage(`{"name":"fixture-decision","summary":"do X, because","choice":"do X","rationale":"because"}`),
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
	handled, res := InterceptRecordDecision(opCtx(), deps, kgtools.CallToolParams{
		Name:      "record_decision",
		Arguments: json.RawMessage(`{"name":"fixture-decision","summary":"do X, because","choice":"do X","rationale":"because","format":"json"}`),
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

// TestRecordDecision_SummaryRequired pins the author-supplied summary on
// record_decision in both directions: a call carrying name/choice/rationale and
// no summary is refused, and a call supplying one stores that text rather than
// the choice.
//
// The second arm is the control that stops the first from being satisfied by a
// handler that refuses everything, and asserting the stored value is NOT the
// choice is what separates an author summary from the retired composition.
func TestRecordDecision_SummaryRequired(t *testing.T) {
	t.Run("no summary is refused", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptRecordDecision(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "record_decision",
			Arguments: json.RawMessage(`{"name":"d","choice":"adopt the composite store","rationale":"because"}`),
		})
		require.True(t, handled)
		require.True(t, res.IsError, "a decision with no summary must be refused, never derived from the choice")
		assert.Contains(t, toolResultText(res), "record_decision: summary is required and must be non-empty")
		assert.Empty(t, fc.execMutations, "the refusal must precede any write")
	})

	t.Run("a supplied summary is stored verbatim, not the choice", func(t *testing.T) {
		const authored = "the store engine is the composite DB, not the per-graph blob"
		const choice = "adopt the composite store"
		fc := &fakeGraphCaller{mutateIDs: []string{"dec-1"}}
		handled, res := InterceptRecordDecision(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "record_decision",
			Arguments: json.RawMessage(`{"name":"d","summary":"` + authored + `",
				"choice":"` + choice + `","rationale":"because"}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an authored decision summary must be accepted: %s", toolResultText(res))

		require.Len(t, fc.execMutations, 1)
		bodies := fc.execMutations[0].GetNodeBodies()
		require.Len(t, bodies, 1)
		assert.Equal(t, authored, bodies[0].GetSummary(), "the author's summary must reach the node untouched")
		assert.NotEqual(t, choice, bodies[0].GetSummary(), "the choice was the retired composition's source")
	})
}
