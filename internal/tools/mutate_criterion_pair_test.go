// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestGuardCreateBatchCriterionPair drives the pair gate directly over payloads.
//
// BOTH DIRECTIONS FAIL AND THE PAIR PASSES, in one table. A test that only drove
// the verifies-only case would be green against a gate that rejected every
// criterion edge, and one that only drove the pair would be green against a gate
// that rejected nothing — the accept and reject cases are each other's control.
//
// The accepting cases are not padding: they are the scope guard. This gate sits
// on the create_batch path every batch takes, so a rule that over-reached would
// break plan-shaped, thought-shaped and born-link-shaped batches that have
// nothing to do with criteria.
func TestGuardCreateBatchCriterionPair(t *testing.T) {
	tests := []struct {
		name string
		// payload is the caller's verbatim create_batch arguments.
		payload string
		// wantErr empty = the batch is accepted; otherwise every substring the
		// rejection must carry.
		wantErr []string
	}{
		{
			name: "the full pair is accepted (slot endpoints)",
			payload: `{"operation":"create_batch",
				"nodes":[{"type":"step","name":"s"},{"type":"criterion","name":"c"}],
				"edges":[{"from_idx":1,"to_idx":0,"type":"verifies"},
				         {"from_idx":0,"to_idx":1,"type":"contains"}]}`,
		},
		{
			name: "verifies with no contains partner is REJECTED naming the missing edge",
			payload: `{"operation":"create_batch",
				"nodes":[{"type":"step","name":"s"},{"type":"criterion","name":"c"}],
				"edges":[{"from_idx":1,"to_idx":0,"type":"verifies"}]}`,
			wantErr: []string{
				"edges[0]",
				"criterion--verifies-->step (nodes[1] → nodes[0])",
				"not its partner step--contains-->criterion (nodes[0] → nodes[1])",
				"never auto-completed",
			},
		},
		{
			name: "contains to an in-batch criterion with no verifies partner is REJECTED",
			payload: `{"operation":"create_batch",
				"nodes":[{"type":"step","name":"s"},{"type":"criterion","name":"c"}],
				"edges":[{"from_idx":0,"to_idx":1,"type":"contains"}]}`,
			wantErr: []string{
				"edges[0]",
				"step--contains-->criterion (nodes[0] → nodes[1])",
				"not its partner criterion--verifies-->step (nodes[1] → nodes[0])",
			},
		},
		{
			name: "the full pair against an EXISTING step by id is accepted",
			payload: `{"operation":"create_batch",
				"nodes":[{"type":"criterion","name":"c"}],
				"edges":[{"from_idx":0,"to_id":"00000000000000000000000000000aaa","type":"verifies"},
				         {"from_id":"00000000000000000000000000000aaa","to_idx":0,"type":"contains"}]}`,
		},
		{
			name: "verifies-only against an existing step by id is REJECTED",
			payload: `{"operation":"create_batch",
				"nodes":[{"type":"criterion","name":"c"}],
				"edges":[{"from_idx":0,"to_id":"00000000000000000000000000000aaa","type":"verifies"}]}`,
			wantErr: []string{
				"edges[0]",
				"criterion--verifies-->step (nodes[0] → 00000000000000000000000000000aaa)",
				"step--contains-->criterion (00000000000000000000000000000aaa → nodes[0])",
			},
		},
		{
			name: "verifies between two EXISTING ids is still checked (verifies is criterion→step by definition)",
			payload: `{"operation":"create_batch","nodes":[],
				"edges":[{"from_id":"crit-1","to_id":"step-1","type":"verifies"}]}`,
			wantErr: []string{"criterion--verifies-->step (crit-1 → step-1)"},
		},
		{
			name: "a plan-shaped batch carrying both pairs is accepted",
			payload: `{"operation":"create_batch",
				"nodes":[{"type":"plan","name":"p"},{"type":"phase","name":"ph"},{"type":"step","name":"s"},
				         {"type":"criterion","name":"c1"},{"type":"criterion","name":"c2"}],
				"edges":[{"from_idx":0,"to_idx":1,"type":"contains"},
				         {"from_idx":1,"to_idx":2,"type":"contains"},
				         {"from_idx":3,"to_idx":2,"type":"verifies"},
				         {"from_idx":2,"to_idx":3,"type":"contains"},
				         {"from_idx":4,"to_idx":2,"type":"verifies"},
				         {"from_idx":2,"to_idx":4,"type":"contains"}]}`,
		},
		{
			name: "a plan-shaped batch whose SECOND criterion lost its contains is REJECTED naming that edge",
			payload: `{"operation":"create_batch",
				"nodes":[{"type":"plan","name":"p"},{"type":"phase","name":"ph"},{"type":"step","name":"s"},
				         {"type":"criterion","name":"c1"},{"type":"criterion","name":"c2"}],
				"edges":[{"from_idx":0,"to_idx":1,"type":"contains"},
				         {"from_idx":1,"to_idx":2,"type":"contains"},
				         {"from_idx":3,"to_idx":2,"type":"verifies"},
				         {"from_idx":2,"to_idx":3,"type":"contains"},
				         {"from_idx":4,"to_idx":2,"type":"verifies"}]}`,
			wantErr: []string{"edges[4]", "criterion--verifies-->step (nodes[4] → nodes[2])"},
		},
		{
			name: "contains between two non-criterion nodes is untouched",
			payload: `{"operation":"create_batch",
				"nodes":[{"type":"phase","name":"ph"},{"type":"step","name":"s"}],
				"edges":[{"from_idx":0,"to_idx":1,"type":"contains"}]}`,
		},
		{
			name: "a born-link contains to an existing id is untouched",
			payload: `{"operation":"create_batch",
				"nodes":[{"type":"thought","name":"t"}],
				"edges":[{"from_id":"00000000000000000000000000000bbb","to_idx":0,"type":"contains"}]}`,
		},
		{
			name: "unrelated edge types are untouched",
			payload: `{"operation":"create_batch",
				"nodes":[{"type":"step","name":"s1"},{"type":"step","name":"s2"},{"type":"criterion","name":"c"}],
				"edges":[{"from_idx":1,"to_idx":0,"type":"depends-on"},
				         {"from_idx":2,"to_idx":0,"type":"relates-to"}]}`,
		},
		{
			name:    "an edgeless batch is untouched",
			payload: `{"operation":"create_batch","nodes":[{"type":"criterion","name":"c"}]}`,
		},
		{
			// THE DOCUMENTED RESIDUAL, asserted so it is a stated boundary rather
			// than an untested assumption: a contains edge whose TO endpoint is an
			// id carries nothing identifying it as a criterion, so it is accepted.
			// Covering it needs a node-type read inside a pre-write gate.
			name: "contains to a criterion that exists OUTSIDE the batch is accepted (documented residual)",
			payload: `{"operation":"create_batch","nodes":[],
				"edges":[{"from_id":"step-1","to_id":"crit-1","type":"contains"}]}`,
		},
		{
			name:    "a payload that does not decode is left to the engine's own decode",
			payload: `{"operation":"create_batch","edges":"not-an-array"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := guardCreateBatchCriterionPair(json.RawMessage(tt.payload))
			if len(tt.wantErr) == 0 {
				require.NoError(t, err, "this batch shape must be accepted")
				return
			}
			require.Error(t, err, "a one-directional step/criterion attachment must be rejected")
			for _, want := range tt.wantErr {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestGuardCreateBatchCriterionPair_SlotAndIDEndpointsAreNotConflated pins that
// the partner search compares endpoint IDENTITY rather than position: a contains
// edge addressing the step by id does NOT satisfy a verifies edge addressing it
// by slot, even though both name the same node in a real batch. Conflating them
// would let a half-formed batch through whenever the caller mixed the two forms.
//
// This is the case an implementation keyed on "some contains edge exists" passes
// and a correct one rejects.
func TestGuardCreateBatchCriterionPair_SlotAndIDEndpointsAreNotConflated(t *testing.T) {
	const payload = `{"operation":"create_batch",
		"nodes":[{"type":"step","name":"s"},{"type":"criterion","name":"c"}],
		"edges":[{"from_idx":1,"to_idx":0,"type":"verifies"},
		         {"from_id":"some-other-step","to_idx":1,"type":"contains"}]}`

	err := guardCreateBatchCriterionPair(json.RawMessage(payload))
	require.Error(t, err, "a contains edge from a DIFFERENT endpoint is not the verifies edge's partner")
	assert.Contains(t, err.Error(), "criterion--verifies-->step (nodes[1] → nodes[0])")
}

// TestGuardCreateBatchCriterionPair_AbsentIdxIsNotSlotZero is the sentinel guard.
// An absent from_idx must read as "use from_id", NOT as slot 0 — the Go zero
// value trap engine.edgeBody's UnmarshalJSON exists to avoid. If this gate read
// absent as 0, the contains edge below would be taken as running FROM nodes[0]
// (the criterion) and would silently satisfy nothing correctly.
func TestGuardCreateBatchCriterionPair_AbsentIdxIsNotSlotZero(t *testing.T) {
	// nodes[0] is the criterion; the step is an existing id. The pair is complete
	// and both id-side endpoints omit their idx entirely.
	const complete = `{"operation":"create_batch",
		"nodes":[{"type":"criterion","name":"c"}],
		"edges":[{"from_idx":0,"to_id":"step-1","type":"verifies"},
		         {"from_id":"step-1","to_idx":0,"type":"contains"}]}`
	require.NoError(t, guardCreateBatchCriterionPair(json.RawMessage(complete)),
		"an absent from_idx alongside from_id must resolve to the id, not to slot 0")

	// The same batch missing its contains partner must still reject — proving the
	// acceptance above came from the partner and not from an inert gate.
	const halfFormed = `{"operation":"create_batch",
		"nodes":[{"type":"criterion","name":"c"}],
		"edges":[{"from_idx":0,"to_id":"step-1","type":"verifies"}]}`
	require.Error(t, guardCreateBatchCriterionPair(json.RawMessage(halfFormed)))
}

// TestInterceptMutate_CreateBatchCriterionPair_EndToEnd proves the gate is WIRED
// into the dispatch path, not merely defined. The direct-call table above passes
// against a guard nobody calls; this fails in that case.
//
// The accepted half asserts the decline (handled==false) that routes a valid
// batch on to the engine — so a gate that claimed every create_batch, or one that
// rejected the pair, is caught here too.
func TestInterceptMutate_CreateBatchCriterionPair_EndToEnd(t *testing.T) {
	t.Run("a verifies-only criterion attachment is rejected pre-write", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create_batch",
				"nodes":[{"type":"step","name":"s"},{"type":"criterion","name":"c"}],
				"edges":[{"from_idx":1,"to_idx":0,"type":"verifies"}]}`),
		})
		require.True(t, handled, "the rejection is a claim — the batch must not fall through silently")
		require.True(t, res.IsError, "a one-directional attachment must reject: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "step--contains-->criterion",
			"the rejection names the MISSING partner edge")
		assert.Empty(t, fc.execMutations, "a pre-write rejection issues ZERO mutations")
	})

	t.Run("the full pair declines to the engine, unrejected", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create_batch",
				"nodes":[{"type":"step","name":"s"},{"type":"criterion","name":"c"}],
				"edges":[{"from_idx":1,"to_idx":0,"type":"verifies"},
				         {"from_idx":0,"to_idx":1,"type":"contains"}]}`),
		})
		assert.False(t, handled, "a well-formed batch declines to the engine create_batch arm")
		assert.False(t, res.IsError, "the pair must not be rejected: %s", toolResultText(res))
	})
}

// TestMutateSchema_EdgesDocumentsTheCriterionPair pins the DOCUMENTATION half of
// the fix. The gate rejects a malformed batch; the schema is what stops the
// caller authoring one — and a rejection with no documented convention behind it
// is a caller reading the error and guessing.
func TestMutateSchema_EdgesDocumentsTheCriterionPair(t *testing.T) {
	edges, ok := mutateProperties()["edges"]
	require.True(t, ok, "edges must be a declared mutate param")

	for _, want := range []string{
		"step--contains-->criterion",
		"criterion--verifies-->step",
		"plan_tree walks contains",
		"REJECTED pre-write",
	} {
		assert.Containsf(t, edges.Description, want,
			"the edges[] schema text must state the pair convention (missing %q)", want)
	}
}
