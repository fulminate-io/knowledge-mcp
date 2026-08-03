// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptAssemble claims the assemble MCP call
// and routes it through cmd/knowledge/internal/projects/render
// (Handle). The server-side assemble handler collapses
// to a client-intercept-required sentinel in Phase 5; this intercept must
// be wired BEFORE Phase 5 lands so production traffic never reaches
// the stub.
//
// Mirrors the minimal-claim-by-name shape of InterceptLogsQuery:
// short args parse, gate on the tool name, hand the raw arguments
// to the render package which walks the wire as needed via the
// supplied GraphCaller. Non-assemble calls return (false, _) — chain
// continues to InterceptWorker / InterceptCreateProject / ...

package tools

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// InterceptAssemble routes assemble calls to the client-side
// render.Handle. Returns (handled, result). When the call is not
// `assemble`, returns (false, _) so the chain continues.
//
// Failure modes:
//   - params.Name != "assemble" → (false, _) (chain continues).
//   - gc == nil → (true, error) — render.Handle would panic on a nil
//     receiver; surface a clean error instead.
//   - All other failures (bad args, not-found, render error) flow
//     through render.Handle and surface as kgtools.ErrorResult; the
//     intercept still returns handled=true so the chain doesn't
//     double-dispatch to the (already-stubbed in Phase 5) server.
func InterceptAssemble(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "assemble" {
		return false, kgtools.ToolResult{}
	}
	// Above the GraphCaller lookup deliberately: assemble never decodes its
	// payload here, so the name gate is the only anchor, and accounting must
	// stay reachable when the graph client is unavailable.
	if err := rejectUndeclaredParams("assemble", "", AssembleToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, kgtools.ErrorResult("assemble: graph client unavailable")
	}
	return true, render.Handle(ctx, gc, params.Arguments)
}
