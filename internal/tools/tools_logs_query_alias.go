// SPDX-License-Identifier: Apache-2.0

// Package tools — alias resolution for the graph='logs' id path.
//
// Split out from tools_logs_query.go so the dispatcher file stays under
// the 300-line soft cap. The id path is template-detail-only; passing a
// stream identifier returns a guidance message pointing the caller at
// `traverse` instead of failing with a confusing "template not found".
package tools

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
)

// dispatchLogsByID accepts a raw hex template ID OR a template alias OR
// a stream alias (or hex stream ID) and routes to the right handler.
func dispatchLogsByID(
	queryID string,
	engine *logs.QueryEngine,
	st *logState,
	id string,
) kgtools.ToolResult {
	if canonical, ok := engine.ResolveTemplateID(id); ok {
		return handleLogsTemplateDetail(queryID, engine, st, canonical)
	}
	if streamID, ok := engine.ResolveStreamID(id); ok {
		return kgtools.ErrorResult(fmt.Sprintf(
			"%q resolves to a stream (%s), not a template. "+
				"Use traverse({ graph: 'logs', name: %q, start: %q, direction: 'both' }) "+
				"to explore a stream.",
			id, streamID, queryID, id,
		))
	}
	// Fall through to the legacy template handler so the existing
	// "template not found" error path triggers for unknown IDs.
	return handleLogsTemplateDetail(queryID, engine, st, id)
}
