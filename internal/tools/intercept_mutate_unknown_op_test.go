// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestInterceptMutate_UnknownOp_TerminalError pins the ninth instance of the
// unknown-operation defect. Before the head guard, an operation outside the
// schema enum fell through every arm and was answered by the engine's
// tool-level deny — which says `mutate` has no client intercept, false on its
// face since InterceptMutate is one and it is the OPERATION that is unknown.
//
// Both cases assert zero mutations issued: naming an unknown operation is a
// pre-dispatch rejection, the same guarantee
// TestInterceptMutate_DeclinedLinkGraphLink_IsRejectedNotDropped makes for an
// unroutable link_graph.
func TestInterceptMutate_UnknownOp_TerminalError(t *testing.T) {
	t.Run("operation outside the enum", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"definitely-not-an-op","id":"n-1"}`),
		})
		require.True(t, handled, "an unknown operation must be answered here, not deferred to the engine deny")
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res),
			`mutate: unknown operation "definitely-not-an-op" — valid operations: `)
		assert.Empty(t, fc.execMutations, "a pre-dispatch rejection issues ZERO mutations")
	})

	// The schema marks operation required, so an ABSENT one is outside the
	// declared vocabulary and takes the same arm. Pinned deliberately rather
	// than left to chance: the guard is a membership test and "" is a
	// non-member, so the diagnostic names the empty string — a more honest
	// answer than the engine deny. A future bespoke "operation is required"
	// message is a decision that gets made HERE.
	t.Run("absent operation", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"id":"n-1"}`),
		})
		require.True(t, handled, "an absent operation must be answered here too")
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), `mutate: unknown operation "" — valid operations: `)
		assert.Empty(t, fc.execMutations, "a pre-dispatch rejection issues ZERO mutations")
	})
}

// TestInterceptMutate_DeclaredOperationsStillRoute is the named catcher for the
// compile-reducible trap. mutate is engine-reducible, so its default arm
// legitimately routes six declared operations (create_batch, upsert,
// update_batch, bulk_update_metadata, link, unlink) onward to engine.Compile.
// A guard written to reject whatever the switch below it does not claim breaks
// exactly those six, and this is the only test in the suite that would notice.
//
// Reading the enum from the live schema rather than a hand-written list is what
// keeps the catcher honest when an operation is added. The assertion is
// deliberately narrow: a handler that rejects a minimal payload on its own
// merits still satisfies it, because all this pins is that the operation was
// not answered with the unknown-operation diagnostic.
func TestInterceptMutate_DeclaredOperationsStillRoute(t *testing.T) {
	opProp, ok := mutateProperties()["operation"]
	require.True(t, ok, "operation must be a declared mutate param")
	require.NotEmpty(t, opProp.Enum, "operation must declare its enum")

	for _, op := range opProp.Enum {
		t.Run(op, func(t *testing.T) {
			fc := &fakeGraphCaller{}
			_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name:      "mutate",
				Arguments: json.RawMessage(`{"operation":"` + op + `","id":"n-1"}`),
			})
			assert.NotContainsf(t, toolResultText(res), "unknown operation",
				"%q is declared by the schema — the terminal arm must not reject it", op)
		})
	}
}
