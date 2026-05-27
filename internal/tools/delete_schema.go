// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// DeleteToolDef returns the unified delete tool definition.
//
// The MCP tool catalog is client-owned: loadSchemas composes this def into
// tools/list. Pure kgtools.MCPTool literal.
func DeleteToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "delete",
		Description: "Unified delete tool. Delete nodes by ID, or prune old session history. " +
			"Provide ids for direct deletion, or older_than (with type) for history pruning.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"ids":        {Type: "array", Description: "Node IDs to delete", Items: &kgtools.Property{Type: "string"}},
				"older_than": {Type: "string", Description: "Delete nodes of the given `type` older than this duration (e.g. '7d', '24h')"},
				"type":       {Type: "string", Description: "Node type filter for pruning (e.g. 'session')"},
				"session_id": {Type: "string", Description: "Only prune nodes from this session"},
				"dry_run":    {Type: "boolean", Description: "Preview what would be deleted without actually deleting"},
				"graph":      {Type: "string", Description: "Target graph: 'knowledge' (default), 'practice', or 'transformers'. Practice graph requires 'language'."},
				"language":   {Type: "string", Description: "Language for practice graph operations (e.g. 'Go', 'JavaScript/TypeScript')"},
			},
		},
	}
}
