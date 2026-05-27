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

// TestInterceptWhatNext_WrongTool_FallsThrough verifies the chain
// falls through when the tool name is not what_next.
func TestInterceptWhatNext_WrongTool_FallsThrough(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, _ := InterceptWhatNext(deps, kgtools.CallToolParams{Name: "query"})
	assert.False(t, handled)
}

// TestInterceptWhatNext_MissingProject_Errors asserts the
// project-not-found path produces the byte-identical error string.
func TestInterceptWhatNext_MissingProject_Errors(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, res := InterceptWhatNext(deps, kgtools.CallToolParams{
		Name:      "what_next",
		Arguments: json.RawMessage(`{"project_id":"missing-project"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Equal(t,
		`what_next: project "missing-project" not found (use query(type:"project") to list available projects)`,
		toolResultText(res))
}

// TestInterceptWhatNext_NoActionable_TextFormat asserts the
// "0 actionable" path renders the header byte-identical.
func TestInterceptWhatNext_NoActionable_TextFormat(t *testing.T) {
	// Listing query returns empty — no candidates.
	fc := &fakeWhatNextCaller{} // empty listNodes → 0 actionable
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptWhatNext(deps, kgtools.CallToolParams{
		Name:      "what_next",
		Arguments: json.RawMessage(`{}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "0-actionable should not error: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Equal(t, "Next actionable steps (0):\n\n", body)
}

// TestInterceptWhatNext_JSONFormat asserts the JSON output shape.
func TestInterceptWhatNext_JSONFormat(t *testing.T) {
	fc := &fakeWhatNextCaller{} // empty listNodes → 0 actionable
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptWhatNext(deps, kgtools.CallToolParams{
		Name:      "what_next",
		Arguments: json.RawMessage(`{"format":"json"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError)
	var parsed struct {
		Total int `json:"total"`
		Steps []struct {
			ID string `json:"id"`
		} `json:"steps"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &parsed))
	assert.Equal(t, 0, parsed.Total)
	assert.Empty(t, parsed.Steps)
}

// fakeWhatNextCaller answers the what_next type-browse reads over the Execute
// carrier seam (T-GTB6) with a fixed listNodes slice via the nodes_json carrier
// (empty by default → 0 actionable candidates).
type fakeWhatNextCaller struct {
	listNodes []knowledgev1.Node
}

// Call satisfies the interface; the what_next intercept routes through Execute.
func (f *fakeWhatNextCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (f *fakeWhatNextCaller) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	resp := enginetest.ResponseWithNodes(nodePtrs(f.listNodes)...)
	resp.Total = int64(len(f.listNodes))
	return resp, nil
}
