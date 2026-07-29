// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptHelp claims the help MCP call client-side.
// help is pure static-map lookup over hardcoded documentation strings —
// no graph access, no I/O, no server-side state. Per the relocation cleanup
// the entire surface (schema + handler + content) lives client-side.

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// helpTopics maps topic names to their documentation content.
var helpTopics = map[string]string{
	"overview":         helpOverview,
	"node_types":       helpNodeTypes,
	"edge_types":       helpEdgeTypes,
	"statuses":         helpStatuses,
	"workflows":        helpWorkflows,
	"logs":             helpLogs,
	"patterns":         helpPatterns,
	"recipes":          helpRecipes,
	"topology":         helpTopology,
	"query":            helpQuery,
	"traverse":         helpTraverse,
	"mutate":           helpMutate,
	"delete":           helpDelete,
	"manage":           helpManage,
	"thoughts":         helpThoughts,
	"create_project":   helpCreateProject,
	"create_ticket":    helpCreateTicket,
	"create_plan":      helpCreatePlan,
	"create_research":  helpCreateResearch,
	"create_test_plan": helpCreateTestPlan,
	"record_decision":  helpRecordDecision,
	"search":           helpSearchCode,
	"file_symbols":     helpFileSymbols,
	"help":             helpHelp,
	"assemble":         helpAssemble,
	"sync":             helpSync,
	"ast":              helpAst,
}

// handleHelpClient returns hardcoded documentation for the requested topic.
func handleHelpClient(args json.RawMessage) kgtools.ToolResult {
	var a struct {
		Topic string `json:"topic"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return errorResult("invalid arguments: " + err.Error())
		}
	}
	if a.Topic == "" {
		a.Topic = "overview"
	}
	if content, ok := helpTopics[a.Topic]; ok {
		return textResult(content)
	}
	return errorResult("unknown help topic: " + a.Topic + ". Call help() with no args for the full topic list.")
}

// InterceptHelp claims the help MCP call client-side. Returns (true, result)
// when params.Name == "help"; (false, _) otherwise so the intercept chain
// falls through. ClientDeps is unused — help is pure static lookup — but
// the signature mirrors the rest of the intercept family.
func InterceptHelp(_ context.Context, _ ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "help" {
		return false, kgtools.ToolResult{}
	}
	return true, handleHelpClient(params.Arguments)
}
