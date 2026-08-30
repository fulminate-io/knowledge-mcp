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

// TestInterceptCreateResearch_AuthorSummaryClampsAndWarns asserts the
// author-supplied summary path is FORGIVING: an over-cap AUTHOR question summary
// is clamped at a word boundary (with a non-fatal warning naming the field), and
// the create SUCCEEDS rather than hard-rejecting. Fails-when-absent: if the
// over-cap author summary still errored, res.IsError would be true and the body
// would carry "exceeds 500 characters" instead of a clamp warning.
func TestInterceptCreateResearch_AuthorSummaryClampsAndWarns(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"research-1", "question-1"}}
	deps := interceptTestDeps{gc: fc}
	longSummary := strings.Repeat("a", 501)
	handled, res := InterceptCreateResearch(opCtx(), deps, kgtools.CallToolParams{
		Name: "create_research",
		Arguments: json.RawMessage(`{
			"name":"r","goal":"g","summary":"s","format":"json",
			"questions":[{"question":"short q","summary":"` + longSummary + `"}]
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "over-cap author summary must clamp + create, not error: %s", toolResultText(res))
	msg := toolResultText(res)
	assert.Contains(t, msg, "questions[0].summary")
	assert.Contains(t, msg, "clamped")
	assert.NotContains(t, msg, "exceeds 500 characters", "over-cap author summary must clamp, not hard-reject")
}

// TestInterceptCreateResearch_ValidAuthorSummaryCreates asserts a valid
// author-supplied question summary still creates successfully (no false
// rejection from the new loop branch).
func TestInterceptCreateResearch_ValidAuthorSummaryCreates(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"research-1", "question-1"}}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreateResearch(opCtx(), deps, kgtools.CallToolParams{
		Name: "create_research",
		Arguments: json.RawMessage(`{
			"name":"r","goal":"g","summary":"s","format":"json",
			"questions":[{"question":"short q","summary":"a concise question summary"}]
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "valid author summary must create: %s", toolResultText(res))
}

// TestCreateResearch_QuestionSummaryRequired asserts BOTH directions of the
// question-summary requirement in one test: a question omitting `summary` is
// REFUSED naming the indexed field path, and a question supplying one is created
// with that text stored verbatim on the question node.
//
// The second arm is what stops the first from being satisfied by a handler that
// refuses everything, and the verbatim comparison is what stops it being
// satisfied by one that accepts the field and then composes over it.
func TestCreateResearch_QuestionSummaryRequired(t *testing.T) {
	t.Run("a question with no summary is refused", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptCreateResearch(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "create_research",
			Arguments: json.RawMessage(`{
				"name":"r","goal":"g","summary":"a searchable research summary",
				"questions":[{"question":"why is it slow?","context":"hot path"}]
			}`),
		})
		require.True(t, handled)
		require.True(t, res.IsError, "a question with no summary must be refused, never derived")
		assert.Contains(t, toolResultText(res), "questions[0].summary is required and must be non-empty")
		assert.Empty(t, fc.execMutations, "the refusal must precede any write")
	})

	t.Run("a supplied question summary is stored verbatim", func(t *testing.T) {
		const authored = "the hot path re-reads the manifest on every request"
		fc := &fakeGraphCaller{mutateIDs: []string{"research-1", "question-1"}}
		handled, res := InterceptCreateResearch(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "create_research",
			Arguments: json.RawMessage(`{
				"name":"r","goal":"g","summary":"a searchable research summary","format":"json",
				"questions":[{"question":"why is it slow?","context":"hot path","summary":"` + authored + `"}]
			}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an authored question summary must be accepted: %s", toolResultText(res))

		require.Len(t, fc.execMutations, 1)
		bodies := fc.execMutations[0].GetNodeBodies()
		require.Len(t, bodies, 2, "the research node plus its one question")
		assert.Equal(t, authored, bodies[1].GetSummary(),
			"the author's summary must reach the question node untouched")
		// "Question: " was the retired derivation's prefix — its absence is what
		// tells a stored author summary from a composed one.
		assert.NotContains(t, bodies[1].GetSummary(), "Question: ")
	})
}
