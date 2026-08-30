// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestInterceptAddCriterion_StepIDMustBeRollupContainer pins the step_id target
// to the criteria-owning container vocabulary. A criterion attached to anything
// else is announced at close-out and holds nothing: the rollup walks only the
// container types, so a criterion hanging off a finding or a decision is never
// evaluated and never blocks a completion.
//
// The refusal message is asserted to render EVERY member of
// clientRollupContainerTypes by ranging the slice inside the assertion — a
// hand-typed list here would stop guarding the rendering the moment the
// vocabulary widened.
func TestInterceptAddCriterion_StepIDMustBeRollupContainer(t *testing.T) {
	gcWithTypedTarget := func(nt kgtypes.NodeType) *scriptedCriterionGc {
		return &scriptedCriterionGc{
			stepNode: &knowledgev1.Node{
				Id:         testStepID,
				Type:       string(nt),
				SymbolName: "ex-target",
				Status:     "pending",
			},
		}
	}
	payload := func(t *testing.T) []byte {
		t.Helper()
		return mustMarshal(t, map[string]any{
			"operation": "create", "type": "criterion", "step_id": testStepID,
			"description": "the suite is green", "summary": "the suite is green",
		})
	}

	t.Run("non-container is refused", func(t *testing.T) {
		gc := gcWithTypedTarget(kgtypes.NodeFinding)
		handled, res := InterceptAddCriterion(opCtx(), &logE2EDeps{gc: gc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: payload(t),
		})
		require.True(t, handled)
		require.True(t, res.IsError, "a criterion on a non-container target must be refused")

		msg := extractText(res)
		assert.Contains(t, msg, testStepID, "the refusal must name the supplied step_id")
		assert.Contains(t, msg, "finding", "the refusal must name the type actually found")
		for _, ct := range clientRollupContainerTypes {
			assert.Contains(t, msg, string(ct),
				"the refusal must render every accepted container type, including %s", ct)
		}
		require.Len(t, gc.calls, 1, "only the target lookup fired — no upsert, no links")
		assert.Equal(t, "query", gc.calls[0].tool)
	})

	// Flat loop rather than a nested t.Run per type: this leg is ONE subtest that
	// must hold for every member, and it is what stops the gate being written as a
	// step-only check.
	t.Run("every container type is accepted", func(t *testing.T) {
		for _, ct := range clientRollupContainerTypes {
			gc := gcWithTypedTarget(ct)
			handled, res := InterceptAddCriterion(opCtx(), &logE2EDeps{gc: gc}, kgtools.CallToolParams{
				Name: "mutate", Arguments: payload(t),
			})
			require.True(t, handled, "%s: the criterion create must be claimed", ct)
			require.False(t, res.IsError,
				"%s owns criteria and must be accepted: %v", ct, res.Content)
		}
	})
}
