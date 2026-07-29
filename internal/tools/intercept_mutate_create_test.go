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
)

func TestInterceptMutate_CreateFinding_HappyPath(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["fnd-1"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"finding","name":"fixture-finding","summary":"summary","description":"desc"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "happy path: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "Finding recorded: fixture-finding")
	assert.Contains(t, body, "(0 references)")
	assert.Contains(t, body, "[graph: knowledge/default]")
}

// TestInterceptMutate_CreateFinding_SummaryClampsAndWarns proves the
// mutate(create, type=finding) handler clamps an over-cap author summary (with a
// warning in the result body) rather than hard-rejecting, AND persists the
// clamped summary. Fails-when-absent: an over-cap summary would error, the
// persisted node body summary would exceed 500 runes, or the warning would be
// missing from the existing warnings channel.
func TestInterceptMutate_CreateFinding_SummaryClampsAndWarns(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["fnd-1"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	over := strings.Repeat("a", 600)
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"finding","name":"fixture-finding","summary":"` + over + `","description":"desc"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "over-cap finding summary must clamp + create, not error: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "summary")
	assert.Contains(t, body, "clamped")
	assert.NotContains(t, body, "exceeds 500 characters", "over-cap author summary must clamp, not hard-reject")
	// The persisted finding node body must carry the clamped summary.
	require.Len(t, fc.execMutations, 1)
	require.Len(t, fc.execMutations[0].GetNodeBodies(), 1)
	assert.LessOrEqual(t, utf8.RuneCountInString(fc.execMutations[0].GetNodeBodies()[0].GetSummary()), 500,
		"persisted finding summary must be clamped to <=500 runes")
}

func TestInterceptMutate_CreateResearch_HappyPath(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["res-1"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"research","name":"fixture-question?","summary":"summary","content":"context"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "happy path: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "Research question recorded: fixture-question?")
	assert.Contains(t, body, "[graph: knowledge/default]")
}

func TestInterceptMutate_CreateRule_HappyPath(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["rule-1"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"rule","name":"fixture-rule","summary":"summary","description":"desc","scope":"*.go"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "happy path: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "Rule added: fixture-rule")
	assert.Contains(t, body, "[graph: knowledge/default]")
}

func TestInterceptMutate_Answer_HappyPath(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"q-1": nodeResultJSON(t, "q-1", "research", map[string]string{}),
		},
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: "updated"}},
		},
	}
	// Need to patch the nodeResultJSON to set symbol_name.
	payload := struct {
		ID         string            `json:"id"`
		Type       string            `json:"type"`
		SymbolName string            `json:"symbol_name"`
		Metadata   map[string]string `json:"metadata"`
	}{ID: "q-1", Type: "research", SymbolName: "fixture-question?", Metadata: map[string]string{}}
	rawNode, merr := json.Marshal(payload)
	require.NoError(t, merr)
	fc.queryResponses["q-1"] = kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: string(rawNode)}},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"answer","id":"q-1","conclusion":"the answer"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "happy path: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "Research answered: fixture-question?")
	assert.Contains(t, body, "[graph: knowledge/default]")
}

func TestInterceptMutate_Answer_MissingNode_Errors(t *testing.T) {
	fc := &fakeGraphCaller{} // queries return not-found
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"answer","id":"missing","conclusion":"x"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "research not found")
}
