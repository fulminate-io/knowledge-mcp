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
			"Provide ids for direct deletion, or older_than (with type) for history pruning. " +
			"Deletes are SOFT by default (tombstoned: hidden from reads, recoverable); pass hard:true for permanent removal. " +
			"Required params (one of two shapes): direct deletion requires ids; history pruning requires older_than + type.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"ids":        {Type: "array", Description: "Node IDs to delete", Items: &kgtools.Property{Type: "string"}},
				"older_than": {Type: "string", Description: "Delete nodes of the given `type` older than this duration (e.g. '7d', '24h')"},
				"type":       {Type: "string", Description: "Node type filter for pruning (e.g. 'session')"},
				"session_id": {Type: "string", Description: "Only prune nodes from this session"},
				"dry_run":    {Type: "boolean", Description: "Preview only: report the nodes that WOULD be deleted (count + ids/names) without deleting anything. Works for BOTH shapes — ids deletion and older_than pruning. Re-run without dry_run to actually delete."},
				"hard":       {Type: "boolean", Description: "PERMANENT removal. Deletes are SOFT by default (tombstoned: hidden from reads, recoverable). hard:true removes the rows irrecoverably — reserve for deliberate permanent cleanup. A malformed value denies the delete."},
				"graph":      {Type: "string", Description: "Target graph: 'knowledge' (default), 'practice', or 'transformers'. Practice graph requires 'language'."},
				"language":   {Type: "string", Description: "Language for practice graph operations (e.g. 'Go', 'JavaScript/TypeScript')"},
			},
		},
	}
}
