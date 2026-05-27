// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

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
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
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

func TestInterceptMutate_CreateResearch_HappyPath(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["res-1"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
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
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
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
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
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
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"answer","id":"missing","conclusion":"x"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "research not found")
}
