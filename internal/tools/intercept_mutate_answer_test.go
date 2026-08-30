// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// answerResearchGc seeds a research question node the answer arm can resolve,
// carrying a SymbolName so the composed shape this test forbids would be
// constructible if the handler still built one.
func answerResearchGc(t *testing.T) *fakeGraphCaller {
	t.Helper()
	payload := struct {
		ID         string            `json:"id"`
		Type       string            `json:"type"`
		SymbolName string            `json:"symbol_name"`
		Metadata   map[string]string `json:"metadata"`
	}{ID: "q-1", Type: "research", SymbolName: "why is the sweep slow?", Metadata: map[string]string{}}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"q-1": {Content: []kgtools.ContentBlock{{Type: "text", Text: string(raw)}}},
		},
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: "updated"}},
		},
	}
}

// TestMutateAnswer_SummaryRequiredAndNotOverwritten pins three things about
// mutate(answer)'s summary: a call omitting it is refused; a call supplying one
// forwards exactly that text; and the forwarded summary is NOT the composed
// "Research question: ... Conclusion: ..." shape the arm used to write over the
// stored value.
//
// The third assertion is the one that stops the test passing against an
// implementation that accepts the parameter and then composes over it anyway.
func TestMutateAnswer_SummaryRequiredAndNotOverwritten(t *testing.T) {
	t.Run("no summary is refused", func(t *testing.T) {
		fc := answerResearchGc(t)
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"answer","id":"q-1","conclusion":"the sweep re-reads the manifest"}`),
		})
		require.True(t, handled)
		require.True(t, res.IsError, "an answer with no summary must be refused, never composed")
		assert.Contains(t, toolResultText(res), "mutate(answer): summary is required and must be non-empty")
		assert.Empty(t, fc.execMutations, "the refusal must precede any write")
	})

	t.Run("a supplied summary is forwarded verbatim and nothing is composed", func(t *testing.T) {
		const authored = "the sweep is slow because it re-reads the manifest per request"
		fc := answerResearchGc(t)
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"answer","id":"q-1","summary":"` + authored + `",` +
				`"conclusion":"the sweep re-reads the manifest"}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an authored answer summary must be accepted: %s", toolResultText(res))

		require.GreaterOrEqual(t, len(fc.execMutations), 1)
		m := fc.execMutations[len(fc.execMutations)-1]
		got := m.GetSetFields()["summary"]
		assert.Equal(t, authored, got, "the author's summary must be forwarded untouched")
		assert.NotContains(t, got, "Research question: ", "the retired composition's prefix must not survive")
		assert.NotContains(t, got, "Conclusion: ", "the retired composition's conclusion clause must not survive")
		// The conclusion still lands as metadata — only the summary stopped being
		// composed from it.
		assert.Equal(t, "the sweep re-reads the manifest", m.GetSetMetadata()["conclusion"])
		assert.Equal(t, "answered", m.GetSetFields()["status"])
	})
}
