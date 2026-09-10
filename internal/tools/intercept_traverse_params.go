// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// intercept_traverse_params.go — the `traverse` tool's undeclared-parameter
// gate, and nothing else.
//
// WHY IT EXISTS AS ITS OWN INTERCEPT. Every tool refuses a top-level parameter
// its schema does not declare, client-side, before the call reaches a handler:
// a misspelled parameter that is silently ignored produces a confidently wrong
// answer from a request the caller believes they narrowed. For fifteen tools
// that check sits inside the intercept that serves the tool. traverse had no
// such intercept of its own — the client owns traverse RENDERING but the server
// serves the walk — so its gate lived inside the logs traversal claim, which was
// the one client-side traverse entry point there was.
//
// THAT CLAIM WAS DELETED WITH THE LOG COLLECTORS, and the gate would have gone
// with it: every traverse call in the product would have started accepting
// undeclared parameters, with nothing in the suite to say so except a residue
// test that stopped compiling. The gate is therefore lifted out to stand alone,
// which is where it should have been — it is a property of the TOOL, not of the
// one graph family that happened to have a client-side handler.
//
// IT CLAIMS NOTHING. On a well-formed call it returns handled=false and the
// chain continues to the server exactly as before; only a refusal is a claim.

// InterceptTraverseParams refuses a traverse call carrying a top-level parameter
// the traverse schema does not declare, and otherwise declines the call so the
// chain continues.
func InterceptTraverseParams(_ context.Context, _ ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "traverse" {
		return false, kgtools.ToolResult{}
	}
	if err := rejectUndeclaredParams("traverse", "", TraverseToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	return false, kgtools.ToolResult{}
}
