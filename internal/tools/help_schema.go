// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// HelpToolDef returns the help MCP tool definition. Client-owned alongside the
// handler + content, wired into tools/list via the client's loadSchemas
// augmentation.
func HelpToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "help",
		Description: "Get documentation about tools, node types, edge types, statuses, and common workflows. " +
			"Call with no topic for an overview of all available tools.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"topic": {
					Type: "string",
					Description: "Topic to get help on. Omit for overview. Tool names: query, traverse, mutate, delete, manage, ast, " +
						"thoughts, create_project, create_ticket, create_plan, create_research, create_test_plan, " +
						"what_next, record_decision, search, file_symbols, help, assemble, sync. " +
						"Reference topics: node_types, edge_types, statuses, workflows, logs, patterns, recipes, topology.",
					Enum: []string{
						"overview",
						"node_types", "edge_types", "statuses", "workflows", "logs", "patterns",
						"recipes", "topology",
						"query", "traverse", "mutate", "delete", "manage", "ast",
						"thoughts",
						"create_project", "create_ticket",
						"create_plan", "create_research", "create_test_plan",
						"what_next", "record_decision",
						"search", "file_symbols",
						"help", "assemble", "sync",
					},
				},
			},
		},
	}
}
