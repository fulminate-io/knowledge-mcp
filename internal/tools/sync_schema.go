// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// SyncToolDef returns the MCP tool definition for the sync tool.
//
// The MCP tool catalog is client-owned: loadSchemas composes this def into
// tools/list. Pure kgtools.MCPTool literal.
func SyncToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "sync",
		Description: "Knowledge graph sync with Fulminate Cloud (push, list). " +
			"list: print a table of sync-eligible local graphs showing cloud sync status + last-synced time. " +
			"Required params: operation only; graph and name are optional (default knowledge/default) for every operation.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"operation": {
					Type:        "string",
					Enum:        []string{"push", "list", "pull", "promote"},
					Description: "Operation to perform",
				},
				"graph": {
					Type:        "string",
					Description: "Graph type (knowledge, code, cloud, etc.); defaults to 'knowledge'",
				},
				"name": {
					Type:        "string",
					Description: "Graph name; defaults to 'default'",
				},
			},
			Required: []string{"operation"},
		},
	}
}
