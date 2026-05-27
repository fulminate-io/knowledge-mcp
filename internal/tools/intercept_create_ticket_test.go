// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func TestInterceptCreateTicket_LocalOnlyParent_ClaimsLocally(t *testing.T) {
	// FUL-246 Phase 3a: local-only parent is now claimed client-side.
	// The intercept composes the local-graph mirror via PersistBatch.
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"proj-local": nodeResultJSON(t, "proj-local", "project", map[string]string{}),
		},
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["ticket-x"]}`}},
		},
	}
	deps := interceptTestDeps{
		backend: &fakeBackend{},
		byName:  map[string]backends.Backend{"linear": &fakeBackend{}},
		gc:      fc,
	}
	handled, res := InterceptCreateTicket(deps, kgtools.CallToolParams{
		Name:      "create_ticket",
		Arguments: json.RawMessage(`{"name":"t","project_id":"proj-local","description":"d","summary":"s","no_patterns_reason":"trivial"}`),
	})
	require.True(t, handled, "local-only parent path is now claimed client-side")
	require.False(t, res.IsError, "local-only create must succeed: %s", toolResultText(res))
}

func TestInterceptCreateTicket_BackendNotConfigured_Errors(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"proj-back": nodeResultJSON(t, "proj-back", "project", map[string]string{
			"backend":          "linear",
			"linear_id":        "proj-uuid",
			"external_url":     "https://example.invalid/p",
			"linear_group_id":  "team-uuid",
			"linear_group_key": "FUL",
		}),
	}}
	deps := interceptTestDeps{
		byName: map[string]backends.Backend{}, // linear NOT registered
		gc:     fc,
	}
	handled, res := InterceptCreateTicket(deps, kgtools.CallToolParams{
		Name:      "create_ticket",
		Arguments: json.RawMessage(`{"name":"t","project_id":"proj-back","description":"d","summary":"s","no_patterns_reason":"trivial"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	body := toolResultText(res)
	assert.Contains(t, body, "linear")
	assert.Contains(t, body, "not currently configured")
}

func TestInterceptCreateTicket_LinearError_Surfaced(t *testing.T) {
	fb := &fakeBackend{createTicketErr: errors.New("linear: rate limited")}
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"proj-back": nodeResultJSON(t, "proj-back", "project", map[string]string{
			"backend":          "linear",
			"linear_id":        "proj-uuid",
			"external_url":     "https://example.invalid/p",
			"linear_group_id":  "team-uuid",
			"linear_group_key": "FUL",
		}),
	}}
	deps := interceptTestDeps{
		byName: map[string]backends.Backend{"linear": fb},
		gc:     fc,
	}
	handled, res := InterceptCreateTicket(deps, kgtools.CallToolParams{
		Name:      "create_ticket",
		Arguments: json.RawMessage(`{"name":"t","project_id":"proj-back","description":"d","summary":"s","no_patterns_reason":"trivial"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "rate limited")
}

func TestInterceptCreateTicket_Success_StampsBackendMetadata(t *testing.T) {
	fb := &fakeBackend{
		createTicketRef: backends.RemoteRef{
			ID:         "ticket-uuid",
			URL:        "https://example.invalid/t",
			Identifier: "FUL-42",
		},
	}
	// FUL-246: the client no longer forwards create_ticket — instead
	// it issues mutate(create_batch) to persist the local mirror.
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"proj-back": nodeResultJSON(t, "proj-back", "project", map[string]string{
				"backend":          "linear",
				"linear_id":        "proj-uuid",
				"external_url":     "https://example.invalid/p",
				"linear_group_id":  "team-uuid",
				"linear_group_key": "FUL",
			}),
		},
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["ticket-local-id"]}`}},
		},
	}
	deps := interceptTestDeps{
		byName: map[string]backends.Backend{"linear": fb},
		gc:     fc,
	}
	handled, res := InterceptCreateTicket(deps, kgtools.CallToolParams{
		Name:      "create_ticket",
		Arguments: json.RawMessage(`{"name":"t","project_id":"proj-back","description":"d","summary":"s","no_patterns_reason":"trivial"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "success should not be an error: %s", toolResultText(res))
	// Verify backend received the inherited parent group + ref.
	assert.Equal(t, "FUL", fb.createTicketArg.GroupKey)
	assert.Equal(t, "proj-uuid", fb.createTicketArg.ProjectRef.ID)
	// Verify the local mirror's CREATE Mutation Execute carried the backend
	// metadata stamped on the ticket NodeBody via BuildTicketNode (T-GTB3 Phase 6
	// carrier path: parent lookup + create both ride Execute now).
	require.Len(t, fc.execMutations, 1)
	m := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, m.GetKind())
	require.NotEmpty(t, m.GetNodeBodies())
	md := m.GetNodeBodies()[0].GetMetadata()
	assert.Equal(t, "linear", md["backend"])
	assert.Equal(t, "ticket-uuid", md["linear_id"])
	assert.Equal(t, "https://example.invalid/t", md["external_url"])
	assert.Equal(t, "proj-uuid", md["linear_project_id"])
	assert.Equal(t, "team-uuid", md["linear_group_id"])
	assert.Equal(t, "FUL", md["linear_group_key"])
	assert.Equal(t, "FUL-42", md["external_id"], "ref.Identifier should fill external_id when caller didn't supply one")
}
