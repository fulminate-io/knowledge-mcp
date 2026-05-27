// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// scriptedCriterionGc records every gc.Call invocation and answers
// from a scripted dispatch table:
//   - query(id:stepID) → return the seeded step node JSON.
//   - mutate(operation:upsert) → return success (records the args).
//   - mutate(operation:link)   → return success unless linkErr is set.
type scriptedCriterionGc struct {
	stepNode    *knowledgev1.Node // returned by query(id) when ID matches; nil = not found
	upsertErr   error
	upsertRes   *kgtools.ToolResult // optional override of upsert result body
	linkErr     map[string]error    // keyed by relationship; nil = success
	linkResults map[string]*kgtools.ToolResult

	calls []scriptedCall
}

type scriptedCall struct {
	tool string
	args map[string]any
}

func (g *scriptedCriterionGc) Call(_ context.Context, tool string, raw json.RawMessage) (kgtools.ToolResult, error) {
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)
	g.calls = append(g.calls, scriptedCall{tool: tool, args: parsed})

	switch tool {
	case "query":
		// Use the {id, include_tombstones} shape the wire helpers
		// produce. We answer either the seeded step or a not-found
		// error result.
		id, _ := parsed["id"].(string)
		if g.stepNode != nil && g.stepNode.Id == id {
			return renderNodeWireJSON(g.stepNode), nil
		}
		return kgtools.ErrorResult("not found"), nil
	case "mutate":
		op, _ := parsed["operation"].(string)
		switch op {
		case "upsert":
			if g.upsertErr != nil {
				return kgtools.ToolResult{}, g.upsertErr
			}
			if g.upsertRes != nil {
				return *g.upsertRes, nil
			}
			return kgtools.TextResult("Upserted"), nil
		case "link":
			rel, _ := parsed["relationship"].(string)
			if g.linkErr != nil {
				if err, ok := g.linkErr[rel]; ok {
					return kgtools.ToolResult{}, err
				}
			}
			if g.linkResults != nil {
				if r, ok := g.linkResults[rel]; ok {
					return *r, nil
				}
			}
			return kgtools.TextResult("Linked"), nil
		}
	}
	return kgtools.ErrorResult("unexpected: " + tool), nil
}

// Execute satisfies render.Executor (T-GTB3 Phase 6 + T-GTB6): the step lookup
// (render.FetchNode), the criterion upsert, and the verifies/contains links all
// ride the Execute carrier seam. It reconstructs the (tool, args) shape the
// test's 4-RPC sequence assertions expect from the compiled QueryPlan /
// MutationPlan and honors the seeded upsertErr / linkErr.
func (g *scriptedCriterionGc) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		return g.executeMutation(m)
	}
	id := req.GetQuery().GetById()
	g.calls = append(g.calls, scriptedCall{tool: "query", args: map[string]any{"id": id}})
	if g.stepNode != nil && g.stepNode.Id == id {
		resp := enginetest.ResponseWithNodes([]*knowledgev1.Node{g.stepNode}...)
		return resp, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// executeMutation reconstructs the upsert / link call-shape from the compiled
// MutationPlan, records it, and returns the seeded outcome (Ids/AffectedCount or
// the seeded error).
func (g *scriptedCriterionGc) executeMutation(m *knowledgev1.MutationPlan) (*knowledgev1.ExecuteResponse, error) {
	switch m.GetKind() {
	case knowledgev1.MutationPlan_MUTATION_KIND_UPSERT:
		body := m.GetNodeBodies()[0]
		g.calls = append(g.calls, scriptedCall{tool: "mutate", args: map[string]any{
			"operation":   "upsert",
			"type":        body.GetType(),
			"id":          body.GetId(),
			"description": body.GetDescription(),
		}})
		if g.upsertErr != nil {
			return nil, g.upsertErr
		}
		return &knowledgev1.ExecuteResponse{Ids: []string{body.GetId()}}, nil
	case knowledgev1.MutationPlan_MUTATION_KIND_LINK:
		rel := m.GetEdgeSpec().GetRelationship()
		from := ""
		if ids := m.GetSelection().GetIds(); len(ids) > 0 {
			from = ids[0]
		}
		g.calls = append(g.calls, scriptedCall{tool: "mutate", args: map[string]any{
			"operation":    "link",
			"relationship": rel,
			"from":         from,
			"to":           m.GetEdgeSpec().GetToId(),
		}})
		if g.linkErr != nil {
			if err, ok := g.linkErr[rel]; ok {
				return nil, err
			}
		}
		return &knowledgev1.ExecuteResponse{AffectedCount: 1}, nil
	default:
		return &knowledgev1.ExecuteResponse{}, nil
	}
}

const testStepID = "00000000000000000000000000000aaa"

func seededStepGc() *scriptedCriterionGc {
	return &scriptedCriterionGc{
		stepNode: &knowledgev1.Node{
			Id:         testStepID,
			Type:       string(kgtypes.NodeStep),
			SymbolName: "ex-step",
			Status:     "pending",
		},
	}
}

// Success path: 4 RPCs in correct order, success message returned.
func TestInterceptAddCriterion_Success(t *testing.T) {
	gc := seededStepGc()
	deps := &logE2EDeps{gc: gc}

	args := mustMarshal(t, map[string]any{
		"operation":   "create",
		"type":        "criterion",
		"step_id":     testStepID,
		"description": "Test that the thing works",
	})
	handled, res := InterceptAddCriterion(deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept error: %v", res.Content)

	got := extractText(res)
	assert.Contains(t, got, "Criterion added: Test that the thing works → ID:")

	// Verify the 4-RPC sequence in order:
	// (1) query for step, (2) mutate upsert, (3) mutate link verifies, (4) mutate link contains.
	require.Len(t, gc.calls, 4)
	assert.Equal(t, "query", gc.calls[0].tool)
	assert.Equal(t, testStepID, gc.calls[0].args["id"])

	assert.Equal(t, "mutate", gc.calls[1].tool)
	assert.Equal(t, "upsert", gc.calls[1].args["operation"])
	assert.Equal(t, "criterion", gc.calls[1].args["type"])
	assert.Equal(t, "Test that the thing works", gc.calls[1].args["description"])
	upsertedID, _ := gc.calls[1].args["id"].(string)
	require.NotEmpty(t, upsertedID, "upsert must supply caller-generated ID")

	assert.Equal(t, "mutate", gc.calls[2].tool)
	assert.Equal(t, "link", gc.calls[2].args["operation"])
	assert.Equal(t, "verifies", gc.calls[2].args["relationship"])
	assert.Equal(t, upsertedID, gc.calls[2].args["from"])
	assert.Equal(t, testStepID, gc.calls[2].args["to"])

	assert.Equal(t, "mutate", gc.calls[3].tool)
	assert.Equal(t, "link", gc.calls[3].args["operation"])
	assert.Equal(t, "contains", gc.calls[3].args["relationship"])
	assert.Equal(t, testStepID, gc.calls[3].args["from"])
	assert.Equal(t, upsertedID, gc.calls[3].args["to"])
}

// Validation order: step_id checked first.
func TestInterceptAddCriterion_EmptyStepID(t *testing.T) {
	gc := seededStepGc()
	deps := &logE2EDeps{gc: gc}

	args := mustMarshal(t, map[string]any{
		"operation":   "create",
		"type":        "criterion",
		"description": "anything",
	})
	handled, res := InterceptAddCriterion(deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, extractText(res), "step_id is required")
	assert.Empty(t, gc.calls, "no RPC must fire when step_id is empty")
}

// Validation order: step-exists fires BEFORE description so
// combined-violation parity holds with server-side handleAddCriterion.
func TestInterceptAddCriterion_StepNotFound(t *testing.T) {
	gc := &scriptedCriterionGc{stepNode: nil} // any query → not found
	deps := &logE2EDeps{gc: gc}

	args := mustMarshal(t, map[string]any{
		"operation":   "create",
		"type":        "criterion",
		"step_id":     "missing-step-id",
		"description": "anything",
	})
	handled, res := InterceptAddCriterion(deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, extractText(res), "step missing-step-id not found")
	// Exactly one RPC fired (the step lookup); no upsert/link.
	require.Len(t, gc.calls, 1)
	assert.Equal(t, "query", gc.calls[0].tool)
}

// Combined violation: both step_id missing AND description missing →
// step_id error fires first (mirrors server-side ordering).
func TestInterceptAddCriterion_BothMissing_StepIDFirst(t *testing.T) {
	gc := seededStepGc()
	deps := &logE2EDeps{gc: gc}

	args := mustMarshal(t, map[string]any{
		"operation": "create",
		"type":      "criterion",
	})
	handled, res := InterceptAddCriterion(deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, extractText(res), "step_id is required")
	assert.NotContains(t, extractText(res), "description is required")
}

// Empty description after step verification.
func TestInterceptAddCriterion_EmptyDescription_AfterStepCheck(t *testing.T) {
	gc := seededStepGc()
	deps := &logE2EDeps{gc: gc}

	args := mustMarshal(t, map[string]any{
		"operation":   "create",
		"type":        "criterion",
		"step_id":     testStepID,
		"description": "   ",
	})
	handled, res := InterceptAddCriterion(deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, extractText(res), "description is required")
	// Step was looked up but no upsert/link fired.
	require.Len(t, gc.calls, 1)
	assert.Equal(t, "query", gc.calls[0].tool)
}

// Upsert RPC fails → intercept surfaces the error verbatim and skips
// the link calls.
func TestInterceptAddCriterion_UpsertFailure(t *testing.T) {
	gc := seededStepGc()
	gc.upsertErr = errors.New("wire timeout")
	deps := &logE2EDeps{gc: gc}

	args := mustMarshal(t, map[string]any{
		"operation":   "create",
		"type":        "criterion",
		"step_id":     testStepID,
		"description": "desc",
	})
	handled, res := InterceptAddCriterion(deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, extractText(res), "create criterion: wire timeout")
	// query + upsert fired, no link RPCs.
	require.Len(t, gc.calls, 2)
}

// Link RPC failure → success message still returned (matches the
// server's slog.Warn-and-continue tolerance at tools_walk.go:355).
func TestInterceptAddCriterion_LinkFailure_StillSucceeds(t *testing.T) {
	gc := seededStepGc()
	gc.linkErr = map[string]error{"verifies": errors.New("transient")}
	deps := &logE2EDeps{gc: gc}

	args := mustMarshal(t, map[string]any{
		"operation":   "create",
		"type":        "criterion",
		"step_id":     testStepID,
		"description": "desc",
	})
	handled, res := InterceptAddCriterion(deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "link failure must not turn into an error result")
	assert.Contains(t, extractText(res), "Criterion added: desc → ID:")
	require.Len(t, gc.calls, 4, "all 4 RPCs fired even though link failed")
}

// Wrong tool → fall through.
func TestInterceptAddCriterion_WrongTool_FallsThrough(t *testing.T) {
	gc := seededStepGc()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"operation": "create", "type": "criterion"})
	handled, _ := InterceptAddCriterion(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled)
}

// Wrong type → fall through.
func TestInterceptAddCriterion_WrongType_FallsThrough(t *testing.T) {
	gc := seededStepGc()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"operation": "create", "type": "finding"})
	handled, _ := InterceptAddCriterion(deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	assert.False(t, handled)
}

// Wrong operation (update) → fall through.
func TestInterceptAddCriterion_WrongOperation_FallsThrough(t *testing.T) {
	gc := seededStepGc()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"operation": "update", "type": "criterion"})
	handled, _ := InterceptAddCriterion(deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	assert.False(t, handled)
}
