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
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestInterceptCreatePlan_WrongTool_FallsThrough verifies the chain
// falls through when the tool name is not create_plan.
func TestInterceptCreatePlan_WrongTool_FallsThrough(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, _ := InterceptCreatePlan(opCtx(), deps, kgtools.CallToolParams{Name: "query"})
	assert.False(t, handled)
}

// TestInterceptCreatePlan_HappyPath_TextFormat asserts the text format
// renders the "Plan created" header with the graph suffix.
func TestInterceptCreatePlan_HappyPath_TextFormat(t *testing.T) {
	// mutateResult feeds the create_batch call.
	// queryResponses feeds the post-create FetchNode walk.
	fc := &fakePlanGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["plan-1","phase-1","step-1","step-2"]}`}},
		},
		queryResponses: map[string]kgtools.ToolResult{
			"plan-1": nodeResultJSON(t, "plan-1", "plan", map[string]string{}),
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreatePlan(opCtx(), deps, kgtools.CallToolParams{
		Name: "create_plan",
		Arguments: json.RawMessage(`{
			"name":"fixture-plan",
			"goal":"fixture plan goal",
			"summary":"fixture summary",
			"no_patterns_reason":"trivial doc edit",
			"phases":[{"name":"phase-1","overview":"phase 1 overview","summary":"phase 1 summary","steps":[{"name":"step-1","description":"step 1 description body","summary":"step 1 summary"}]}]
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "happy path should not error: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "Plan created: fixture-plan")
	assert.Contains(t, body, "[graph: knowledge/default]")
}

// TestInterceptCreatePlan_JSONFormat asserts the JSON output shape.
func TestInterceptCreatePlan_JSONFormat(t *testing.T) {
	fc := &fakePlanGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["plan-1","phase-1","step-1"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreatePlan(opCtx(), deps, kgtools.CallToolParams{
		Name: "create_plan",
		Arguments: json.RawMessage(`{
			"name":"fixture-plan",
			"goal":"fixture plan goal",
			"summary":"fixture summary",
			"no_patterns_reason":"trivial",
			"phases":[{"name":"phase-1","overview":"o","summary":"s","steps":[{"name":"step-1","description":"step 1 description body","summary":"s"}]}],
			"format":"json"
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "happy path should not error: %s", toolResultText(res))
	var parsed struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		NodeIDs []string `json:"node_ids"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &parsed))
	assert.Equal(t, "plan-1", parsed.ID)
	assert.Equal(t, "fixture-plan", parsed.Name)
	assert.Equal(t, []string{"phase-1", "step-1"}, parsed.NodeIDs)
}

// TestInterceptCreatePlan_CreateRidesMutationExecute asserts the create rides a
// single CREATE Mutation Execute (the carrier path). NOTE:
// the version-overlay bundle_id anchor is NOT carried by the
// engine MutationPlan create path — bundle grouping is no longer expressible on
// the carrier path, so this no longer asserts a bundle_id (the prior behavior).
func TestInterceptCreatePlan_CreateRidesMutationExecute(t *testing.T) {
	fc := &fakePlanGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["plan-1","phase-1","step-1"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	_, _ = InterceptCreatePlan(opCtx(), deps, kgtools.CallToolParams{
		Name: "create_plan",
		Arguments: json.RawMessage(`{
			"name":"p","goal":"g","summary":"s","no_patterns_reason":"x",
			"phases":[{"name":"ph","overview":"o","summary":"s","steps":[{"name":"st","description":"step 1 description body","summary":"s"}]}],
			"format":"json"
		}`),
	})
	require.NotEmpty(t, fc.calls)
	// First call is the create_batch Mutation Execute.
	assert.Equal(t, "mutate", fc.calls[0].tool)
}

// TestInterceptCreatePlan_ValidateName_Empty asserts validation
// gates fire client-side (parity with the deleted server-side
// validateName call).
func TestInterceptCreatePlan_ValidateName_Empty(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, res := InterceptCreatePlan(opCtx(), deps, kgtools.CallToolParams{
		Name: "create_plan",
		Arguments: json.RawMessage(`{
			"name":"",
			"goal":"g","summary":"s","no_patterns_reason":"x",
			"phases":[{"name":"ph","overview":"o","summary":"s","steps":[{"name":"st","description":"step 1 description body","summary":"s"}]}]
		}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "create_plan: name is required")
}

// TestInterceptCreatePlan_CriterionSummary pins the author-supplied criterion
// summary on create_plan: required non-empty at the indexed path, stored
// verbatim, and clamped-not-rejected over the cap. The over-cap subtest asserts
// the PERSISTED body rather than a local, because a `for k, c := range` loop
// clamps a copy and ships the unclamped summary into PersistBatch.
func TestInterceptCreatePlan_CriterionSummary(t *testing.T) {
	const seededIDs = `{"ids":["plan-1","phase-1","step-1","crit-1"]}`
	planArgs := func(critFields string) json.RawMessage {
		return json.RawMessage(`{
			"name":"p","goal":"g","summary":"s","no_patterns_reason":"x",
			"phases":[{"name":"ph","overview":"o","summary":"s","steps":[{"name":"st","description":"step 1 description body","summary":"s","criteria":[{"description":"short criterion",` + critFields + `}]}]}],
			"format":"json"
		}`)
	}
	criterionBody := func(t *testing.T, fc *fakeGraphCaller) *knowledgev1.NodeBody {
		t.Helper()
		require.Len(t, fc.execMutations, 1)
		for _, b := range fc.execMutations[0].GetNodeBodies() {
			if b.GetType() == string(kgtypes.NodeCriterion) {
				return b
			}
		}
		t.Fatal("no criterion body reached the persisted batch")
		return nil
	}

	t.Run("empty criterion summary is refused at the indexed path", func(t *testing.T) {
		deps := interceptTestDeps{gc: &fakeGraphCaller{}}
		handled, res := InterceptCreatePlan(opCtx(), deps, kgtools.CallToolParams{
			Name: "create_plan", Arguments: planArgs(`"summary":""`),
		})
		require.True(t, handled)
		require.True(t, res.IsError, "a criterion with no summary must be refused, never derived")
		assert.Contains(t, toolResultText(res), "phases[0].steps[0].criteria[0].summary")
	})

	t.Run("explicit summary reaches the persisted body", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: seededIDs}},
		}}
		const authored = "the census reports zero remaining sites"
		handled, res := InterceptCreatePlan(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "create_plan", Arguments: planArgs(`"summary":"` + authored + `"`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an authored criterion summary must be accepted: %s", toolResultText(res))
		body := criterionBody(t, fc)
		assert.Equal(t, authored, body.GetSummary())
		// "criterion: " is the retired derivation's signature.
		assert.NotContains(t, body.GetSummary(), "criterion: ")
	})

	t.Run("over-cap summary clamps in the persisted body", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: seededIDs}},
		}}
		handled, res := InterceptCreatePlan(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "create_plan", Arguments: planArgs(`"summary":"` + strings.Repeat("word ", 120) + `"`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an over-cap criterion summary CLAMPS — never a hard reject: %s", toolResultText(res))
		assert.LessOrEqual(t, utf8.RuneCountInString(criterionBody(t, fc).GetSummary()), 500)
		assert.Contains(t, toolResultText(res), "clamped")
	})
}

// TestInterceptCreatePlan_UnderCapAuthorSummariesPass asserts under-cap and
// at-cap criteria/questions are NOT falsely rejected: the create proceeds to the
// mutate RPC. Guards against the criterion and open-question clamp loops
// over-rejecting.
func TestInterceptCreatePlan_UnderCapAuthorSummariesPass(t *testing.T) {
	fc := &fakePlanGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["plan-1","phase-1","step-1"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreatePlan(opCtx(), deps, kgtools.CallToolParams{
		Name: "create_plan",
		Arguments: json.RawMessage(`{
			"name":"p","goal":"g","summary":"s","no_patterns_reason":"x",
			"phases":[{"name":"ph","overview":"o","summary":"s","steps":[{"name":"st","description":"step 1 description body","summary":"s","criteria":[{"description":"short criterion","summary":"the short criterion","command":"go test ./..."}]}]}],
			"open_questions":[{"question":"short question?","summary":"the short question","context":"some context"}]
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "under-cap author summaries must not be rejected: %s", toolResultText(res))
}

// TestInterceptCreatePlan_AuthorSummariesClampAndWarn proves the pointer-receiver
// validatePlanSummaries clamps the top-level, phase, and step AUTHOR summaries in
// place and the create SUCCEEDS, with a warning naming each clamped field path.
// It also inspects the persisted MutationPlan node bodies to prove the clamp
// mutated the PERSISTED fields (slice-index assign-back), not range-value copies:
// every node body summary is <=500 runes. Fails-when-absent: a range-value
// assign-back would leave the phase/step node bodies over-cap, and a hard-reject
// would make res.IsError true. format:"json" skips the post-create FetchNode walk
// so the shared fakeGraphCaller (which records execMutations + returns the seeded
// ids) is sufficient.
func TestInterceptCreatePlan_AuthorSummariesClampAndWarn(t *testing.T) {
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["plan-1","phase-1","step-1"]}`}},
	}}
	deps := interceptTestDeps{gc: fc}
	over := strings.Repeat("a", 600)
	args := `{
		"name":"p","goal":"g","summary":"` + over + `","no_patterns_reason":"x",
		"phases":[{"name":"ph","overview":"o","summary":"` + over + `","steps":[{"name":"st","description":"step 1 description body","summary":"` + over + `"}]}],
		"format":"json"
	}`
	handled, res := InterceptCreatePlan(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_plan",
		Arguments: json.RawMessage(args),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "over-cap author summaries must clamp + create, not error: %s", toolResultText(res))
	// The JSON result carries a warnings array naming each clamped field path.
	var parsed struct {
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &parsed))
	joined := strings.Join(parsed.Warnings, "\n")
	assert.Contains(t, joined, "summary", "top-level summary clamp warning expected")
	assert.Contains(t, joined, "phases[0].summary", "phase summary clamp warning expected")
	assert.Contains(t, joined, "phases[0].steps[0].summary", "step summary clamp warning expected")
	for _, w := range parsed.Warnings {
		assert.Contains(t, w, "clamped")
	}
	// Every persisted node body summary must be clamped to <=500 runes, proving
	// the slice-index assign-back mutated the persisted fields (not copies).
	require.Len(t, fc.execMutations, 1)
	bodies := fc.execMutations[0].GetNodeBodies()
	require.NotEmpty(t, bodies)
	for _, b := range bodies {
		if s := b.GetSummary(); s != "" {
			assert.LessOrEqual(t, utf8.RuneCountInString(s), 500,
				"persisted node %q summary must be clamped to <=500 runes", b.GetName())
		}
	}
}

// fakePlanGraphCaller is a fake that routes mutate calls through
// mutateResult and query calls through queryResponses. Extends the
// shared fakeGraphCaller with explicit per-tool dispatch.
type fakePlanGraphCaller struct {
	mutateResult   kgtools.ToolResult
	mutateError    error
	queryResponses map[string]kgtools.ToolResult
	calls          []recordedCall
}

func (f *fakePlanGraphCaller) Call(_ context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error) {
	f.calls = append(f.calls, recordedCall{tool: tool, args: append(json.RawMessage(nil), args...)})
	if tool == "query" {
		var a struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(args, &a)
		if a.ID != "" {
			if r, ok := f.queryResponses[a.ID]; ok {
				return r, nil
			}
			return kgtools.ToolResult{IsError: true, Content: []kgtools.ContentBlock{{Type: "text", Text: "not found"}}}, nil
		}
		// Listing query — return empty so the create_plan listing read short-circuits.
		return kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"nodes":[]}`}},
		}, nil
	}
	if tool == "mutate" {
		return f.mutateResult, f.mutateError
	}
	return kgtools.ToolResult{}, nil
}

// Execute satisfies render.Executor. PersistBatch (create_batch)
// now rides a Mutation Execute → answer with the ids parsed from the seeded
// mutateResult {ids:[...]} body. The post-create FetchNode walk + rollup
// TraverseDescendants ride Query Executes answered from queryResponses /
// traversal carriers.
func (f *fakePlanGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		f.calls = append(f.calls, recordedCall{tool: "mutate"})
		if f.mutateError != nil {
			return nil, f.mutateError
		}
		var parsed struct {
			IDs []string `json:"ids"`
		}
		if len(f.mutateResult.Content) > 0 {
			_ = json.Unmarshal([]byte(f.mutateResult.Content[0].Text), &parsed)
		}
		return &knowledgev1.ExecuteResponse{Ids: parsed.IDs, AffectedCount: int64(len(parsed.IDs))}, nil
	}
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
		return &knowledgev1.ExecuteResponse{}, nil // rollup walk: no descendants seeded.
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	id := q.GetById()
	f.calls = append(f.calls, recordedCall{tool: "query"})
	res, ok := f.queryResponses[id]
	if !ok || len(res.Content) == 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var n knowledgev1.Node
	if uerr := json.Unmarshal([]byte(res.Content[0].Text), &n); uerr != nil {
		return &knowledgev1.ExecuteResponse{}, nil //nolint:nilerr // malformed seed → not found
	}
	resp := enginetest.ResponseWithNodes([]*knowledgev1.Node{&n}...)
	return resp, nil
}

// TestCreatePlan_OpenQuestionSummaryRequired pins the author-supplied summary on
// create_plan's open_questions, in both directions: an entry omitting it is
// refused at the indexed path, and a supplied one reaches the persisted question
// node verbatim.
//
// The second arm is the control that stops the first from being satisfied by a
// handler that refuses everything, and the verbatim comparison stops it being
// satisfied by one that accepts the field and composes over it.
func TestCreatePlan_OpenQuestionSummaryRequired(t *testing.T) {
	planArgs := func(questionFields string) json.RawMessage {
		return json.RawMessage(`{
			"name":"p","goal":"g","summary":"s","no_patterns_reason":"x",
			"phases":[{"name":"ph","overview":"o","summary":"s","steps":[{"name":"st","description":"step 1 description body","summary":"s"}]}],
			"open_questions":[{"question":"which backend owns the sweep?",` + questionFields + `}],
			"format":"json"
		}`)
	}
	questionBody := func(t *testing.T, fc *fakeGraphCaller) *knowledgev1.NodeBody {
		t.Helper()
		require.Len(t, fc.execMutations, 1)
		for _, b := range fc.execMutations[0].GetNodeBodies() {
			if b.GetType() == string(kgtypes.NodeQuestion) {
				return b
			}
		}
		t.Fatal("no question body reached the persisted batch")
		return nil
	}

	t.Run("an open question with no summary is refused", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptCreatePlan(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "create_plan", Arguments: planArgs(`"context":"the sweep runs nightly"`),
		})
		require.True(t, handled)
		require.True(t, res.IsError, "an open question with no summary must be refused, never derived")
		assert.Contains(t, toolResultText(res), "open_questions[0].summary is required and must be non-empty")
		assert.Empty(t, fc.execMutations, "the refusal must precede any write")
	})

	t.Run("a supplied open-question summary reaches the persisted body", func(t *testing.T) {
		const authored = "ownership of the nightly sweep is unassigned"
		fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["plan-1","phase-1","step-1","q-1"]}`}},
		}}
		handled, res := InterceptCreatePlan(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "create_plan", Arguments: planArgs(`"context":"the sweep runs nightly","summary":"` + authored + `"`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an authored open-question summary must be accepted: %s", toolResultText(res))
		body := questionBody(t, fc)
		assert.Equal(t, authored, body.GetSummary(),
			"the author's summary must reach the question node untouched")
		// "Question: " was the retired derivation's prefix.
		assert.NotContains(t, body.GetSummary(), "Question: ")
	})
}
