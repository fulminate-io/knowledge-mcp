// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func TestInterceptMutate_LocalOnlyUpdate_FallsThrough(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"local-1": nodeResultJSON(t, "local-1", "ticket", map[string]string{}),
	}}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}
	handled, _ := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"local-1","name":"new name"}`),
	})
	assert.False(t, handled, "local-only update must fall through")
}

func TestInterceptMutate_BackendBackedUpdate_CallsLinearThenForwards(t *testing.T) {
	fb := &fakeBackend{}
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"back-1": nodeResultJSON(t, "back-1", "ticket", map[string]string{
				"backend":      "linear",
				"linear_id":    "uuid-back-1",
				"external_url": "https://example.invalid/back-1",
			}),
		},
		mutateResult: kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: "ok"}}},
	}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": fb}, gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"back-1","name":"renamed"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "success expected: %s", toolResultText(res))
	assert.Equal(t, 1, fb.updateTicketCalls, "Linear UpdateTicket should fire once")
	// The local forward runs AFTER the Linear dispatch, via the Execute carrier
	// seam (by-id UPDATE). Assert on the compiled MutationPlan.
	require.GreaterOrEqual(t, len(fc.execMutations), 1)
	m := fc.execMutations[len(fc.execMutations)-1]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, m.GetKind())
	assert.Equal(t, []string{"back-1"}, m.GetSelection().GetIds())
	assert.Equal(t, "renamed", m.GetSetFields()["name"])
}

func TestInterceptMutate_MixedBatch_GuardRejects(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"local-1":   nodeResultJSON(t, "local-1", "ticket", map[string]string{}),
		"backend-1": nodeResultJSON(t, "backend-1", "ticket", map[string]string{"backend": "linear"}),
	}}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","ids":["local-1","backend-1"],"status":"done"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "backend-1")
}

func TestInterceptMutate_BackendBackedDelete_CallsLinearArchive(t *testing.T) {
	fb := &fakeBackend{}
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"back-d": nodeResultJSON(t, "back-d", "ticket", map[string]string{
				"backend":      "linear",
				"linear_id":    "uuid-back-d",
				"external_url": "https://example.invalid/back-d",
			}),
		},
		mutateResult: kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: "ok"}}},
	}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": fb}, gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"delete","id":"back-d"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "success expected: %s", toolResultText(res))
	assert.Equal(t, 1, fb.archiveTicketCalls)
}

func TestInterceptMutate_UpdateLinearSucceedsForwardFails(t *testing.T) {
	fb := &fakeBackend{}
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"back-1": nodeResultJSON(t, "back-1", "ticket", map[string]string{
				"backend":      "linear",
				"linear_id":    "uuid-back-1",
				"external_url": "https://example.invalid/back-1",
			}),
		},
		mutateError: errors.New("connect: refused"),
	}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": fb}, gc: fc}
	originalArgs := []byte(`{"operation":"update","id":"back-1","name":"renamed"}`)
	originalCopy := append([]byte(nil), originalArgs...)
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: originalArgs,
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	body := toolResultText(res)
	assert.Contains(t, body, "Linear update succeeded")
	assert.Contains(t, body, "local update failed")
	assert.Contains(t, body, "back-1")
	// Caller-arg-safety: originalArgs byte slice must be unchanged.
	assert.True(t, bytes.Equal(originalCopy, originalArgs), "caller's args bytes must be byte-identical after intercept")
}

func TestInterceptMutate_DeleteAllLinearSucceedForwardFails(t *testing.T) {
	fb := &fakeBackend{}
	makeMeta := map[string]string{
		"backend":      "linear",
		"linear_id":    "uuid-x",
		"external_url": "https://example.invalid/x",
	}
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"id-a": nodeResultJSON(t, "id-a", "ticket", makeMeta),
			"id-b": nodeResultJSON(t, "id-b", "ticket", makeMeta),
			"id-c": nodeResultJSON(t, "id-c", "ticket", makeMeta),
		},
		mutateError: errors.New("connect: refused"),
	}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": fb}, gc: fc}
	originalArgs := []byte(`{"operation":"delete","ids":["id-a","id-b","id-c"]}`)
	originalCopy := append([]byte(nil), originalArgs...)
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: originalArgs,
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	body := toolResultText(res)
	assert.Contains(t, body, "Linear archive succeeded for")
	assert.Contains(t, body, "3")
	assert.Contains(t, body, "id-a")
	assert.Contains(t, body, "id-b")
	assert.Contains(t, body, "id-c")
	assert.Contains(t, body, "local delete failed")
	assert.Contains(t, body, linearArchiveRetryGuidance)
	assert.True(t, bytes.Equal(originalCopy, originalArgs), "caller's args bytes must be byte-identical after intercept")
}

func TestInterceptMutate_DeleteLinearSucceedsForBackendResolutionFails(t *testing.T) {
	// 2 succeed on linear, 3rd id's backend resolution fails.
	fb := &fakeBackend{}
	makeMeta := map[string]string{
		"backend":      "linear",
		"linear_id":    "uuid-x",
		"external_url": "https://example.invalid/x",
	}
	missingMeta := map[string]string{
		"backend":      "unconfigured-backend",
		"linear_id":    "uuid-x",
		"external_url": "https://example.invalid/x",
	}
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"id-a":    nodeResultJSON(t, "id-a", "ticket", makeMeta),
			"id-b":    nodeResultJSON(t, "id-b", "ticket", makeMeta),
			"id-fail": nodeResultJSON(t, "id-fail", "ticket", missingMeta),
		},
	}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": fb}, gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"delete","ids":["id-a","id-b","id-fail"]}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	body := toolResultText(res)
	assert.Contains(t, body, "unconfigured-backend")
	assert.Contains(t, body, "not currently configured")
	assert.Contains(t, body, "id-a")
	assert.Contains(t, body, "id-b")
	assert.Contains(t, body, "Linear archive succeeded for 2", "should name the 2 successful archives")
}
