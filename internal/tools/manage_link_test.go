// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestInterceptManage_LinkDispatch_RunsClientLinker asserts manage(link)
// flows through InterceptManage → handleClientLinker → clientlinker.RunAll
// and ultimately issues query calls against the wire (the fake GraphCaller
// observes them). Because no code/cloud graphs are seeded, RunAll completes
// with zero link counts and no errors.
func TestInterceptManage_LinkDispatch_RunsClientLinker(t *testing.T) {
	gc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{},
		mutateResult:   kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}},
	}
	deps := interceptTestDeps{gc: gc}

	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"link"}`),
	})
	require.True(t, handled, "manage(link) must be intercepted client-side")
	require.False(t, res.IsError, "RunAll against an empty store should not error")
	// Linker text-result format: "Linker complete: 0 total links ...".
	assert.Contains(t, toolResultText(res), "Linker complete",
		"text result should describe linker completion")

	// fakeGraphCaller defaults non-query tools to a generic success; the
	// linker should have issued at least one query call (listing graphs).
	sawQuery := false
	for _, c := range gc.calls {
		if c.tool == "query" {
			sawQuery = true
			break
		}
	}
	assert.True(t, sawQuery, "linker should have issued at least one query call")
}

// TestInterceptManage_LinkDispatch_NilGraphCaller_Errors guards the
// degraded-mode path: when the client wasn't wired with a GraphCaller,
// manage(link) must surface a clean error rather than panic.
func TestInterceptManage_LinkDispatch_NilGraphCaller_Errors(t *testing.T) {
	deps := interceptTestDeps{} // gc is nil
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"link"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "GraphCaller is unavailable")
}
