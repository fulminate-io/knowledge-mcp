// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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

// createdNodeBody returns the single node body carried by the first CREATE
// MutationPlan the fake captured.
func createdNodeBody(t *testing.T, fc *fakeGraphCaller) *knowledgev1.NodeBody {
	t.Helper()
	require.NotEmpty(t, fc.execMutations, "a create must have been issued")
	bodies := fc.execMutations[0].GetNodeBodies()
	require.Len(t, bodies, 1, "exactly one node body")
	return bodies[0]
}

// TestInterceptMutate_CreateFinding_RoutesContentStatusMetadata pins that a
// finding create carrying content, status and metadata PERSISTS all three on the
// created node body.
//
// This SUPERSEDES the earlier assertion that the same shape rejected pre-write.
// That reject was the deliberately-temporary first step — it converted a silent
// drop into a loud error; this step converts the loud error into the write the
// caller asked for. Do not restore the reject.
//
// The caller's metadata is seeded BEFORE the derived evidence/source keys, so a
// collision resolves in the derivation's favor — asserted here directly.
func TestInterceptMutate_CreateFinding_RoutesContentStatusMetadata(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"fnd-1"}}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"finding","name":"fixture-finding",` +
			`"summary":"a searchable finding summary","content":"the body","status":"open",` +
			`"evidence":"derived-evidence","metadata":{"owner":"me","evidence":"caller-loses"}}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "the combined create must apply, not reject: %s", toolResultText(res))

	body := createdNodeBody(t, fc)
	assert.Equal(t, "the body", body.GetContent())
	assert.Equal(t, "open", body.GetStatus())
	assert.Equal(t, "me", body.GetMetadata()["owner"], "caller metadata must persist")
	assert.Equal(t, "derived-evidence", body.GetMetadata()["evidence"],
		"the derived key must win on a collision with caller metadata")
}

// TestInterceptMutate_CreateRule_RoutesContentStatusMetadata is the rule-arm
// twin: content, status and metadata persist, and the derived scope key wins a
// collision with caller metadata.
func TestInterceptMutate_CreateRule_RoutesContentStatusMetadata(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"rule-1"}}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"rule","name":"fixture-rule",` +
			`"summary":"a searchable rule summary","content":"the body","status":"active",` +
			`"scope":"*.go","metadata":{"owner":"me","scope":"caller-loses"}}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "the combined create must apply, not reject: %s", toolResultText(res))

	body := createdNodeBody(t, fc)
	assert.Equal(t, "the body", body.GetContent())
	assert.Equal(t, "active", body.GetStatus())
	assert.Equal(t, "me", body.GetMetadata()["owner"], "caller metadata must persist")
	assert.Equal(t, "*.go", body.GetMetadata()["scope"],
		"the derived key must win on a collision with caller metadata")
}

// TestInterceptMutate_CreateResearch_RoutesDescriptionStatusMetadata pins that a
// research create honors a caller description and status instead of
// overwriting them with the question text and the hardcoded "open", and carries
// caller metadata — while omitting them keeps the long-standing defaults and
// SymbolName stays the question either way.
func TestInterceptMutate_CreateResearch_RoutesDescriptionStatusMetadata(t *testing.T) {
	t.Run("supplied description, status and metadata all persist", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateIDs: []string{"res-1"}}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create","type":"research","name":"Why slow?",` +
				`"summary":"a searchable research summary","content":"context",` +
				`"description":"a fuller framing","status":"in_progress","metadata":{"owner":"me"}}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "the combined create must apply: %s", toolResultText(res))

		body := createdNodeBody(t, fc)
		assert.Equal(t, "a fuller framing", body.GetDescription(), "a caller description must not be overwritten")
		assert.Equal(t, "in_progress", body.GetStatus(), "a caller status must beat the open default")
		assert.Equal(t, "me", body.GetMetadata()["owner"])
		assert.Equal(t, "Why slow?", body.GetName(), "the research node's name is always the question")
	})

	t.Run("omitting them keeps the question and open defaults", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateIDs: []string{"res-2"}}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create","type":"research","name":"Why slow?",` +
				`"summary":"a searchable research summary","content":"context"}`),
		})
		require.False(t, res.IsError, "the plain create must still succeed: %s", toolResultText(res))

		body := createdNodeBody(t, fc)
		assert.Equal(t, "Why slow?", body.GetDescription(), "absent description defaults to the question")
		assert.Equal(t, "open", body.GetStatus(), "absent status defaults to open")
	})
}

// TestInterceptMutate_CreateBuilders_CallerMetadataNotMutated proves every create
// builder COPIES the caller's metadata map rather than aliasing it. Aliasing
// would leak the builder's derived keys back into the caller's own map, so a
// caller that reuses the map for a retry would silently send different arguments
// the second time. Each case supplies a key the builder also derives, which is
// exactly where an aliasing bug shows up.
func TestInterceptMutate_CreateBuilders_CallerMetadataNotMutated(t *testing.T) {
	t.Run("finding", func(t *testing.T) {
		callerMeta := map[string]string{"owner": "me"}
		fc := &fakeGraphCaller{mutateIDs: []string{"fnd-1"}}
		res := handleClientMutateCreateFinding(context.Background(), interceptTestDeps{gc: fc}, withRawArgs(mutateArgs{
			Operation: "create", Type: "finding", Name: "f", Summary: "a searchable finding summary",
			Evidence: "derived-evidence", Source: "derived-source", Metadata: callerMeta,
		}, `{"operation":"create","type":"finding","name":"f","summary":"a searchable finding summary",`+
			`"evidence":"derived-evidence","source":"derived-source","metadata":{"owner":"me"}}`))
		require.False(t, res.IsError, "create must succeed: %s", toolResultText(res))
		assert.Equal(t, map[string]string{"owner": "me"}, callerMeta,
			"the caller's map must be byte-identical — never gaining the derived evidence/source keys")
	})

	t.Run("rule", func(t *testing.T) {
		callerMeta := map[string]string{"owner": "me"}
		fc := &fakeGraphCaller{mutateIDs: []string{"rule-1"}}
		res := handleClientMutateCreateRule(context.Background(), interceptTestDeps{gc: fc}, withRawArgs(mutateArgs{
			Operation: "create", Type: "rule", Name: "r", Summary: "a searchable rule summary",
			Scope: "*.go", Enforcement: "lint", Metadata: callerMeta,
		}, `{"operation":"create","type":"rule","name":"r","summary":"a searchable rule summary",`+
			`"scope":"*.go","enforcement":"lint","metadata":{"owner":"me"}}`))
		require.False(t, res.IsError, "create must succeed: %s", toolResultText(res))
		assert.Equal(t, map[string]string{"owner": "me"}, callerMeta,
			"the caller's map must be byte-identical — never gaining the derived scope/enforcement keys")
	})

	t.Run("research", func(t *testing.T) {
		callerMeta := map[string]string{"owner": "me"}
		fc := &fakeGraphCaller{mutateIDs: []string{"res-1"}}
		res := handleClientMutateCreateResearch(context.Background(), interceptTestDeps{gc: fc}, withRawArgs(mutateArgs{
			Operation: "create", Type: "research", Name: "Why?", Summary: "a searchable research summary",
			Metadata: callerMeta,
		}, `{"operation":"create","type":"research","name":"Why?","summary":"a searchable research summary",`+
			`"metadata":{"owner":"me"}}`))
		require.False(t, res.IsError, "create must succeed: %s", toolResultText(res))
		assert.Equal(t, map[string]string{"owner": "me"}, callerMeta)
	})
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

// TestInterceptMutate_Answer_RoutesMetadata_RejectsBodyFields pins the answer
// arm's accounting. Caller metadata now rides the update beside the derived
// conclusion key instead of vanishing. The body fields stay rejected for two
// distinct reasons the errors must state: the operation writes status and
// summary itself, so routing a caller value would fight its own derivation;
// name/description/content are ordinary body edits belonging on mutate(update).
func TestInterceptMutate_Answer_RoutesMetadata_RejectsBodyFields(t *testing.T) {
	researchGc := func(t *testing.T) *fakeGraphCaller {
		t.Helper()
		payload := struct {
			ID         string            `json:"id"`
			Type       string            `json:"type"`
			SymbolName string            `json:"symbol_name"`
			Metadata   map[string]string `json:"metadata"`
		}{ID: "q-1", Type: "research", SymbolName: "fixture-question?", Metadata: map[string]string{}}
		rawNode, merr := json.Marshal(payload)
		require.NoError(t, merr)
		return &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"q-1": {Content: []kgtools.ContentBlock{{Type: "text", Text: string(rawNode)}}},
			},
			mutateResult: kgtools.ToolResult{
				Content: []kgtools.ContentBlock{{Type: "text", Text: "updated"}},
			},
		}
	}

	// Driven through InterceptMutate, not the handler: the handler-direct case
	// below bypasses the dispatch gate entirely, so on its own it could not tell
	// a consumed metadata cell from one the gate silently rejects before the
	// handler ever runs.
	t.Run("metadata survives the full dispatch path, not just the handler", func(t *testing.T) {
		fc := researchGc(t)
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"answer","id":"q-1","conclusion":"the answer",` +
				`"metadata":{"owner":"me"}}`),
		})
		require.True(t, handled, "the answer arm claims the call")
		require.False(t, res.IsError, "metadata must not be rejected by the gate: %s", toolResultText(res))
		require.GreaterOrEqual(t, len(fc.execMutations), 1)
		m := fc.execMutations[len(fc.execMutations)-1]
		assert.Equal(t, "me", m.GetSetMetadata()["owner"])
		assert.Equal(t, "the answer", m.GetSetMetadata()["conclusion"])
	})

	t.Run("caller metadata persists beside the derived conclusion key", func(t *testing.T) {
		callerMeta := map[string]string{"owner": "me", "conclusion": "caller-loses"}
		fc := researchGc(t)
		res := handleClientMutateAnswer(context.Background(), interceptTestDeps{gc: fc}, withRawArgs(mutateArgs{
			Operation: "answer", ID: "q-1", Conclusion: "the answer", Metadata: callerMeta,
		}, `{"operation":"answer","id":"q-1","conclusion":"the answer",`+
			`"metadata":{"owner":"me","conclusion":"caller-loses"}}`))
		require.False(t, res.IsError, "answer must succeed: %s", toolResultText(res))
		require.GreaterOrEqual(t, len(fc.execMutations), 1)
		m := fc.execMutations[len(fc.execMutations)-1]
		assert.Equal(t, "me", m.GetSetMetadata()["owner"], "caller metadata must persist")
		assert.Equal(t, "the answer", m.GetSetMetadata()["conclusion"],
			"the derived conclusion key must win on a collision with caller metadata")
		assert.Equal(t, map[string]string{"owner": "me", "conclusion": "caller-loses"}, callerMeta,
			"the caller's map must be byte-identical — never gaining the derived conclusion")
	})

	// Each param is checked against the reason its own class carries, so a
	// justification wired to the wrong key cannot pass.
	for param, wantReason := range map[string]string{
		"status":      "sets status and summary itself",
		"summary":     "sets status and summary itself",
		"name":        "body edit",
		"description": "body edit",
		"content":     "body edit",
	} {
		t.Run(param+" rejects with zero mutations and explains why", func(t *testing.T) {
			fc := researchGc(t)
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate",
				Arguments: json.RawMessage(
					`{"operation":"answer","id":"q-1","conclusion":"the answer","` + param + `":"x"}`),
			})
			require.True(t, handled, "a rejected param must be claimed, not fall through")
			require.True(t, res.IsError)
			body := toolResultText(res)
			assert.Contains(t, body, param, "the rejection must name the offending param")
			assert.Contains(t, body, wantReason, "the rejection must explain this param's class")
			assert.Empty(t, fc.execMutations, "zero writes on a rejected answer")
		})
	}
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
