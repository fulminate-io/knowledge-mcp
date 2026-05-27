// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptLogsQuery dispatches client-side log queries.
//
// BCN11.3: production query(graph:"logs") MCP calls reach the server's
// gated routeQueryByTarget, which now lets raw reads fall through. But
// formatted modes (pivot/correlations/timeline/explain/resolver) AND the
// default overview/drill-down/template-detail shapes all live client-
// side post-BCN11. InterceptLogsQuery is the chain step that routes
// those calls to the moved handleLogsQuery before any wire trip.
//
// Non-logs queries return (false, _) — caller falls through to the next
// chain step (InterceptQuery's embed gate, then the bare server call).

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// InterceptLogsQuery routes graph='logs' queries to the client-side
// handleLogsQuery handler. Returns (handled, result). When the call is
// not a graph='logs' query, returns (false, _) so the chain continues.
func InterceptLogsQuery(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		// Bad args — let the server respond with the canonical
		// invalid-arguments error rather than swallowing here.
		return false, kgtools.ToolResult{}
	}
	if a.Graph != "logs" {
		return false, kgtools.ToolResult{}
	}
	h := &Handler{Deps: deps}
	return true, h.handleLogsQuery(context.Background(), a)
}
