// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// AssembleToolDef returns the assemble MCP tool definition. Relocated
// client-side per FUL-251 — the rendering surface lives in
// cmd/knowledge/internal/projects/render and the wire intercept lives
// in intercept_assemble.go; the server has no assemble handler. Wired
// into tools/list via the client's loadSchemas augmentation.
func AssembleToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name:        "assemble",
		Description: "Type-aware context assembly. Given a node (by id or type+name), assembles everything needed to work with it. For test plans, new_run=true creates a run session with pending test_run nodes.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"id":          {Type: "string", Description: "Node ID to assemble context for"},
				"name":        {Type: "string", Description: "Node name (used with type for name-based lookup)"},
				"type":        {Type: "string", Description: "Node type for name-based lookup (e.g. project, test_plan, research)"},
				"new_run":     {Type: "boolean", Description: "For test_plan: create a new run session with pending test_run nodes"},
				"run_session": {Type: "string", Description: "For test_plan: filter assembled test_runs by this run session UUID"},
				"format":      {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured)"},
			},
		},
	}
}
