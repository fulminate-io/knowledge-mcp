// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptLogsTraversal dispatches client-side log
// traverse calls.
//
// traverse(graph:"logs") MCP calls reach this chain step (wired in
// bootstrap/dream.go). There is no server-side counterpart to hand off to or
// fall back on: the server holds no traverse tool — its traverse file keeps only
// MergeBothTraversals for the engine's both-direction walk — and the client owns
// traverse rendering end to end.
//
// This intercept claims the PER-NODE walk (logs + name + start) and routes it to
// traverseLogs, which reads the pre-fetched logState built by
// getOrFetchLogState. A start-less logs traverse is claimed by nobody and is
// DENIED; the fall-through inside InterceptLogsTraversal records the measured
// path and what is still open about it.

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// InterceptLogsTraversal routes graph='logs' traverse calls to the
// client-side traverseLogs handler. Returns (handled, result). When
// the call is not a graph='logs' traverse, returns (false, _) so the
// chain continues to the bare server call.
//
// It claims ONLY the per-node traversal — the shape that needs zstd
// decompression of chunk content and engine-side alias resolution. A start-less
// logs traverse falls through to a deny; see the comment on that branch.
func InterceptLogsTraversal(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "traverse" {
		return false, kgtools.ToolResult{}
	}
	if err := rejectUndeclaredParams("traverse", "", TraverseToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	var a traverseArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Graph != "logs" {
		return false, kgtools.ToolResult{}
	}
	if a.Start == "" {
		// A start-less logs traverse is NOT served anywhere — measured, not
		// inferred. Nothing downstream claims it: engine.dispatchGraphWideEdges
		// declines graph=="logs", and compileTraverse declines it too, so the call
		// reaches Dispatch's Compile-miss arm and is DENIED. Falling through here
		// is what produces that deny; do not restate it as "handled server-side",
		// because there is no server-side traverse handler to handle it.
		//
		// The deny is the CURRENT disposition, not a settled one: its message is
		// the generic unrecognized-shape text rather than one naming the missing
		// start, and whether a logs graph-wide enumeration should be served at all
		// is an open question rather than a decided no.
		return false, kgtools.ToolResult{}
	}
	if a.Direction == "" {
		// Normalizes the empty value to the default the tool documents. It reaches
		// no walk decision today: traverseLogs picks its walk from the START
		// NODE'S TYPE (template or stream) and never reads Direction. Left in
		// place rather than removed because removing it would pre-judge the open
		// question of whether a logs walk should honor direction at all.
		a.Direction = "out"
	}
	h := &Handler{Deps: deps}
	return true, h.traverseLogs(ctx, a)
}
