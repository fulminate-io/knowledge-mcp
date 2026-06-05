// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestInterceptCreatePlan_WrongTool_FallsThrough verifies the chain
// falls through when the tool name is not create_plan.
func TestInterceptCreatePlan_WrongTool_FallsThrough(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, _ := InterceptCreatePlan(deps, kgtools.CallToolParams{Name: "query"})
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
	handled, res := InterceptCreatePlan(deps, kgtools.CallToolParams{
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
	handled, res := InterceptCreatePlan(deps, kgtools.CallToolParams{
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
	_, _ = InterceptCreatePlan(deps, kgtools.CallToolParams{
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
	handled, res := InterceptCreatePlan(deps, kgtools.CallToolParams{
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
		// Listing query — return empty so what_next short-circuits.
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
