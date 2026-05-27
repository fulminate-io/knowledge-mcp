// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptLogsTraversal dispatches client-side log
// traverse calls.
//
// BCN11.3: traverse(graph:"logs") MCP calls land here. The server's
// gated handleTraverse returns errLogsHandledClientSide for the
// per-node walk (logs+name+start); we route to traverseLogs which uses
// the pre-fetched logState built by getOrFetchLogState. The graph-wide
// enumeration path (logs+name+no start) goes server-side via the
// wire-fetch helper inside getOrFetchLogState, so this intercept only
// fires for the formatted per-node traverse.

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
// Graph-wide enumeration (no start) deliberately falls through: those
// calls go server-side via the Phase 1 graph-wide handler. Only the
// per-node traversal — which needs zstd decompression of chunk content
// and engine-side alias resolution — runs client-side.
func InterceptLogsTraversal(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "traverse" {
		return false, kgtools.ToolResult{}
	}
	var a traverseArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Graph != "logs" {
		return false, kgtools.ToolResult{}
	}
	if a.Start == "" {
		// Graph-wide enumeration — handled server-side by the BCN11.3
		// loosened gate. Fall through.
		return false, kgtools.ToolResult{}
	}
	if a.Direction == "" {
		a.Direction = "out"
	}
	h := &Handler{Deps: deps}
	return true, h.traverseLogs(context.Background(), a)
}
