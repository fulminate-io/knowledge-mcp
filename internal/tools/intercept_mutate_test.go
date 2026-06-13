// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestInterceptMutate_MultiIDStatusUpdate_Routes is the live-repro contract for
// the make-it-work mechanic: a PLAIN LOCAL NON-CONTAINER ids+status batch — the
// only batch shape that should reach the engine (Phase 2 adds the intercept gate
// that loud-rejects every other shape before this point) — must compile to a
// homogeneous MUTATION_KIND_UPDATE over the full Selection.Ids set + set_fields.
//
// The intercept returns (false,_) for the plain-local multi-id batch
// (intercept_mutate.go), so production routes it through the SAME generic
// engine.Compile dispatch this test drives. RED on HEAD: compileMutateByIDUpdate
// gates on a.ID=="" and denies the ids[]-only shape, so engine.Compile returns
// ok=false here until the compile gate is extended.
func TestInterceptMutate_MultiIDStatusUpdate_Routes(t *testing.T) {
	req, ok := engine.Compile("mutate", json.RawMessage(
		`{"operation":"update","ids":["local-1","local-2"],"status":"completed"}`))
	require.True(t, ok, "ids[]+status must compile to a MutationPlan")
	m := req.GetMutation()
	require.NotNil(t, m)
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, m.GetKind())
	assert.Equal(t, []string{"local-1", "local-2"}, m.GetSelection().GetIds())
	assert.Equal(t, "completed", m.GetSetFields()["status"])
}

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

// TestInterceptMutate_BatchWithBackendID_RejectsLoudly pins gate arm 1: an ids[]
// update batch containing ANY backend-backed id (all-backend AND mixed) rejects
// loudly with ZERO writes — the engine UPDATE never fires (fc.execMutations
// empty) and no tracker UpdateTicket runs. The batch engine path issues zero
// dispatch.Update calls, so a backend-backed id in a batch must be split out and
// updated per-id where the Linear write-through runs.
func TestInterceptMutate_BatchWithBackendID_RejectsLoudly(t *testing.T) {
	t.Run("all-backend batch rejects", func(t *testing.T) {
		fb := &fakeBackend{}
		fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
			"back-1": nodeResultJSON(t, "back-1", "ticket", map[string]string{"backend": "linear"}),
			"back-2": nodeResultJSON(t, "back-2", "ticket", map[string]string{"backend": "linear"}),
		}}
		deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": fb}, gc: fc}
		handled, res := InterceptMutate(deps, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"update","ids":["back-1","back-2"],"status":"completed"}`),
		})
		require.True(t, handled)
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, "per-id")
		assert.Contains(t, body, "back-1")
		assert.Empty(t, fc.execMutations, "no engine UPDATE may fire on a rejected batch")
		assert.Equal(t, 0, fb.updateTicketCalls, "no tracker UpdateTicket on a rejected batch")
	})

	t.Run("mixed local+backend batch rejects", func(t *testing.T) {
		fb := &fakeBackend{}
		fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
			"local-1": nodeResultJSON(t, "local-1", "ticket", map[string]string{}),
			"back-1":  nodeResultJSON(t, "back-1", "ticket", map[string]string{"backend": "linear"}),
		}}
		deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": fb}, gc: fc}
		handled, res := InterceptMutate(deps, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"update","ids":["local-1","back-1"],"status":"completed"}`),
		})
		require.True(t, handled)
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, "per-id")
		assert.Contains(t, body, "back-1")
		assert.Empty(t, fc.execMutations, "no engine UPDATE may fire on a rejected mixed batch")
		assert.Equal(t, 0, fb.updateTicketCalls, "no tracker UpdateTicket on a rejected mixed batch")
	})
}

// TestInterceptMutate_BatchWithPerTypeParam_RejectsLoudly pins gate arm 2: an
// ids[] batch carrying a per-type first-class param (command) rejects loudly with
// ZERO writes — those params are single-id-only and would silently vanish on the
// batch engine path.
func TestInterceptMutate_BatchWithPerTypeParam_RejectsLoudly(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"crit-1": nodeResultJSON(t, "crit-1", "criterion", map[string]string{}),
		"crit-2": nodeResultJSON(t, "crit-2", "criterion", map[string]string{}),
	}}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","ids":["crit-1","crit-2"],"command":"go test"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "per-id")
	assert.Empty(t, fc.execMutations, "no engine UPDATE may fire on a rejected per-type-param batch")
}

// TestInterceptMutate_BatchWithSource_RejectsLoudly pins gate arm 2b: an ids[]
// batch carrying source rejects loudly with ZERO writes. source is per-type/
// metadata (finding source lands in metadata, not the node field), so the batch
// field-route is a silent wrong-write — source is excluded from the batch contract.
func TestInterceptMutate_BatchWithSource_RejectsLoudly(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"finding-1": nodeResultJSON(t, "finding-1", "finding", map[string]string{}),
		"finding-2": nodeResultJSON(t, "finding-2", "finding", map[string]string{}),
	}}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","ids":["finding-1","finding-2"],"source":"manual"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	body := toolResultText(res)
	assert.Contains(t, body, "source")
	assert.Contains(t, body, "per-id")
	assert.Empty(t, fc.execMutations, "no engine UPDATE may fire on a rejected source batch")
}

// TestInterceptMutate_BatchContainerStatus_RejectsLoudly pins gate arm 3: an ids[]
// status batch touching a rollup-container id (plan) rejects loudly with ZERO
// writes — the descendant cascade is unreachable on the batch engine path, so a
// container-status batch would orphan descendants.
func TestInterceptMutate_BatchContainerStatus_RejectsLoudly(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"plan-1": nodeResultJSON(t, "plan-1", "plan", map[string]string{}),
		"plan-2": nodeResultJSON(t, "plan-2", "plan", map[string]string{}),
	}}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","ids":["plan-1","plan-2"],"status":"completed"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	body := toolResultText(res)
	assert.Contains(t, body, "rollup")
	assert.Contains(t, body, "plan-1")
	assert.Empty(t, fc.execMutations, "no engine UPDATE may fire on a rejected container-status batch")
}

// TestInterceptMutate_BatchPlainLocalStatus_Routes is the contract-valid happy
// path: a plain-local NON-container ids+status batch passes the gate (intercept
// returns (false,_)) and the (false,_) engine dispatch reduces it to a homogeneous
// MUTATION_KIND_UPDATE over the full Selection.Ids.
func TestInterceptMutate_BatchPlainLocalStatus_Routes(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"t-1": nodeResultJSON(t, "t-1", "finding", map[string]string{}),
		"t-2": nodeResultJSON(t, "t-2", "finding", map[string]string{}),
	}}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}
	// Gate passes → intercept returns (false,_); production then routes the batch
	// through the SAME engine.Compile dispatch this test asserts on.
	handled, _ := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","ids":["t-1","t-2"],"status":"closed"}`),
	})
	assert.False(t, handled, "the contract-valid plain-local batch must fall through to engine dispatch")

	req, ok := engine.Compile("mutate", json.RawMessage(`{"operation":"update","ids":["t-1","t-2"],"status":"closed"}`))
	require.True(t, ok, "the surviving plain-local batch reduces via compileMutateByIDUpdate")
	m := req.GetMutation()
	require.NotNil(t, m)
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, m.GetKind())
	assert.Equal(t, []string{"t-1", "t-2"}, m.GetSelection().GetIds())
	assert.Equal(t, "closed", m.GetSetFields()["status"])
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
