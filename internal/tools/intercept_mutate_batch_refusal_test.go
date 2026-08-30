// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_batch_refusal_test.go covers the CONTENT of the batch-shape
// refusals — which ids a refusal names, and which arm fires when two apply.
//
// SPLIT FROM intercept_mutate_test.go, which owns the batch-shape BEHAVIOUR tests
// (what is rejected, and that nothing is written) and sits within a few lines of
// the repo's 500-line file convention. The two halves are read together: the
// behaviour tests there assert the zero-write property a message-content test
// cannot see, and these assert the message a behaviour test does not read.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestBatchContainerStatus_RefusalNamesEveryOffendingID (FAILS-WHEN-ABSENT) drives
// a batch with MORE THAN ONE container and asserts the refusal names EVERY one.
//
// A SINGLE-CONTAINER FIXTURE CANNOT DETECT THIS DEFECT: the arm returned on the
// FIRST container it found, so a caller who split that id off and re-sent the rest
// hit the identical refusal on the next one, one round trip at a time. Only a
// multi-container batch distinguishes the two behaviours.
//
// SCOPE: this is a MESSAGE-CONTENT change. The per-id contract stands, no batch is
// widened, and nothing auto-splits — the zero-write property is asserted by the
// landed TestInterceptMutate_BatchContainerStatus_RejectsLoudly, which a
// message-content test cannot see.
func TestBatchContainerStatus_RefusalNamesEveryOffendingID(t *testing.T) {
	t.Run("every container id is named, the non-container id is not", func(t *testing.T) {
		fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
			"plan-1":    nodeResultJSON(t, "plan-1", "plan", map[string]string{}),
			"finding-1": nodeResultJSON(t, "finding-1", "finding", map[string]string{}),
			"step-9":    nodeResultJSON(t, "step-9", "step", map[string]string{}),
		}}
		deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}
		handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(
				`{"operation":"update","ids":["plan-1","finding-1","step-9"],"status":"completed"}`),
		})
		require.True(t, handled)
		require.True(t, res.IsError)
		body := toolResultText(res)

		assert.Contains(t, body, "plan-1", "the FIRST container is named")
		assert.Contains(t, body, "step-9",
			"and so is the SECOND — naming only the first is the defect this test exists for")
		assert.NotContains(t, body, "finding-1",
			"the non-container id is NOT named as an offender; listing the whole batch would stop the "+
				"message being actionable")
		assert.Empty(t, fc.execMutations, "still zero writes")
	})

	t.Run("BOTH DIRECTIONS: a purely non-container batch is not refused by this arm", func(t *testing.T) {
		// Without this, "the refusal names every container" is satisfiable by a guard
		// that became a blanket batch refusal.
		fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
			"finding-1": nodeResultJSON(t, "finding-1", "finding", map[string]string{}),
			"finding-2": nodeResultJSON(t, "finding-2", "finding", map[string]string{}),
		}}
		deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}
		handled, _ := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(
				`{"operation":"update","ids":["finding-1","finding-2"],"status":"closed"}`),
		})
		assert.False(t, handled, "a plain-local non-container batch still falls through to engine dispatch")
	})

	t.Run("ARM PRECEDENCE PRESERVED: a backend-backed id still dominates", func(t *testing.T) {
		// A tracker-backed node is often ALSO a container (a Linear ticket is both),
		// and the tracker-sync skip is the more severe silent failure. Without this
		// leg an implementer restructuring the arm can invert the precedence and lose
		// the tracker protection while every other leg here stays green.
		fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
			"tkt-linear": nodeResultJSON(t, "tkt-linear", "ticket", map[string]string{"backend": "linear"}),
			"plan-1":     nodeResultJSON(t, "plan-1", "plan", map[string]string{}),
		}}
		deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}
		handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(
				`{"operation":"update","ids":["tkt-linear","plan-1"],"status":"completed"}`),
		})
		require.True(t, handled)
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, "tracker", "the BACKEND reject is what fires, not the container one")
		assert.NotContains(t, body, "rollup cascade",
			"the container message must not pre-empt the more severe tracker-sync reject")
	})
}
