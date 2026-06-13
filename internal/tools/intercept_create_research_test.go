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

// TestInterceptCreateResearch_DerivedQuestionOverflow asserts that a question
// with NO author summary whose DERIVED summary (question + context) overflows
// 500 runes fails create_research FAST client-side with the questions
// fieldPath, the derivation explanation, the overflow amount, and a quoted
// prefix. Fails-when-absent: before this loop change the derived fallback was
// never validated and the over-long node died on the server's context-free
// exceeds-500 backstop.
func TestInterceptCreateResearch_DerivedQuestionOverflow(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	// "Question: " (10 runes) + 495-char question = 505 runes > 500.
	longQ := strings.Repeat("q", 495)
	handled, res := InterceptCreateResearch(deps, kgtools.CallToolParams{
		Name: "create_research",
		Arguments: json.RawMessage(`{
			"name":"r","goal":"g","summary":"s",
			"questions":[{"question":"` + longQ + `"}]
		}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	msg := toolResultText(res)
	assert.Contains(t, msg, "questions[0].summary")
	assert.Contains(t, msg, "derived from question + context")
	assert.Contains(t, msg, "over by")
	assert.Contains(t, msg, "Derived prefix:")
}

// TestInterceptCreateResearch_AuthorSummaryPathUnchanged asserts the
// author-supplied summary path is preserved: an over-long AUTHOR summary fails
// with the ORIGINAL validate.Summary message (no "derived from"), proving the
// derived branch is not taken when q.Summary is set.
func TestInterceptCreateResearch_AuthorSummaryPathUnchanged(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	longSummary := strings.Repeat("a", 501)
	handled, res := InterceptCreateResearch(deps, kgtools.CallToolParams{
		Name: "create_research",
		Arguments: json.RawMessage(`{
			"name":"r","goal":"g","summary":"s",
			"questions":[{"question":"short q","summary":"` + longSummary + `"}]
		}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	msg := toolResultText(res)
	assert.Contains(t, msg, "questions[0].summary")
	assert.Contains(t, msg, "exceeds 500 characters")
	assert.NotContains(t, msg, "derived from", "author summary path must NOT use the derived-summary error")
}

// TestInterceptCreateResearch_ValidAuthorSummaryCreates asserts a valid
// author-supplied question summary still creates successfully (no false
// rejection from the new loop branch).
func TestInterceptCreateResearch_ValidAuthorSummaryCreates(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"research-1", "question-1"}}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreateResearch(deps, kgtools.CallToolParams{
		Name: "create_research",
		Arguments: json.RawMessage(`{
			"name":"r","goal":"g","summary":"s","format":"json",
			"questions":[{"question":"short q","summary":"a concise question summary"}]
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "valid author summary must create: %s", toolResultText(res))
}

// TestInterceptCreateResearch_ValidDerivedQuestionCreates asserts a question
// with no author summary but a short question/context (derived summary under
// cap) still creates successfully.
func TestInterceptCreateResearch_ValidDerivedQuestionCreates(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"research-1", "question-1"}}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreateResearch(deps, kgtools.CallToolParams{
		Name: "create_research",
		Arguments: json.RawMessage(`{
			"name":"r","goal":"g","summary":"s","format":"json",
			"questions":[{"question":"why is it slow?","context":"hot path"}]
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "valid derived question must create: %s", toolResultText(res))
}
