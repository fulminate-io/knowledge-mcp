// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// traverse_param_gate_chain_test.go — THE CHAIN WIRING of the traverse tool's
// undeclared-parameter gate.
//
// WHY IT IS HERE AND NOT BESIDE THE GATE. The gate itself is a pure function and
// the tools package already drives it directly, which proves it REFUSES. It does
// not prove the chain CALLS it, and that half is the one this ticket put at
// risk: the gate used to live inside the logs traversal intercept, which was
// wired into runInterceptChainInner, and lifting it out meant re-wiring it. A
// gate present in the tree and absent from the chain accepts every undeclared
// parameter with nothing red — measured, not supposed: unwiring it while this
// test did not exist left the whole suite green.
//
// IT DRIVES THE REAL CHAIN, so what it observes is dispatch rather than
// construction: a chain that stopped calling the gate returns handled=false and
// this test fails on that alone.
func TestInterceptChain_TraverseUndeclaredParamIsRefused(t *testing.T) {
	ctx := opCtx()
	url, _ := startCountingEngine(t)
	c := closeRouterOnCleanup(t, buildE2EClient(graphclient.NewGraphClientForURL(url), "http://cloud.invalid", newFakeAuthStore(), time.Hour))

	// A traverse call carrying a parameter the traverse schema does not declare.
	_, handled, res := c.runInterceptChain(ctx, kgtools.CallToolParams{
		Name:      "traverse",
		Arguments: json.RawMessage(`{"start":"n1","graph":"knowledge","nosuchparam":"x"}`),
	})
	require.True(t, handled,
		"the chain must ANSWER a traverse call carrying an undeclared parameter — a fall-through "+
			"here means the gate is not wired, and every traverse call in the product silently "+
			"accepts misspelled parameters")
	require.True(t, res.IsError)
	body := toolText(res)
	assert.Contains(t, body, "nosuchparam", "the refusal names the offending parameter")

	// THE CONTROL, same chain and same tool: a well-formed traverse is NOT
	// claimed by the gate and falls through to the server. Without it, a gate
	// that claimed every traverse call would satisfy the assertion above while
	// breaking the tool outright.
	_, handled, _ = c.runInterceptChain(ctx, kgtools.CallToolParams{
		Name:      "traverse",
		Arguments: json.RawMessage(`{"start":"n1","graph":"knowledge"}`),
	})
	assert.False(t, handled,
		"control: a well-formed traverse must fall through — the gate claims refusals only")
}

// toolText reads the first text block out of a tool result.
func toolText(r kgtools.ToolResult) string {
	for _, c := range r.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}
