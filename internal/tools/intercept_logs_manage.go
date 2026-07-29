// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptLogsManage dispatches client-side log
// manage operations.
//
// Four arms, all client-handled over the generic Execute seam:
//   - list_logs → enumerate the loaded log graphs via a RETURN_MODE_GRAPH_NAMES
//     read; discard_logs → tear each graph down via a DROP_GRAPH Execute. Both
//     then format / clean up the local engine cache.
//   - configure_log_backend / list_log_backends → run the moved client-side
//     handlers (tools_logs_manage_backend.go), which issue executeMutate(upsert)
//     / executeQuery(type:"log-backend") over the Execute seam against the
//     server's generic mutate/query engine. The formatting +
//     validation moved off the server entirely; the server only stores nodes.
//
// Non-logs manage operations return (false, _) so the chain continues
// to InterceptManage (which owns status/pprof) and then to the bare
// server call.

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// InterceptLogsManage routes the four log-related manage operations.
// Returns (handled, result).
func InterceptLogsManage(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "manage" {
		return false, kgtools.ToolResult{}
	}
	var a manageArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	switch a.Operation {
	case "list_logs":
		h := &Handler{Deps: deps}
		return true, h.handleListLogs(ctx, a.Format)
	case "discard_logs":
		h := &Handler{Deps: deps}
		return true, h.handleDiscardLogs(ctx, a.Name)
	case "configure_log_backend":
		h := &Handler{Deps: deps}
		return true, h.handleConfigureLogBackend(ctx, a)
	case "list_log_backends":
		h := &Handler{Deps: deps}
		return true, h.handleListLogBackends(ctx, a.Format)
	}
	return false, kgtools.ToolResult{}
}
