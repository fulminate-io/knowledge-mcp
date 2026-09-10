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
		Description: "Knowledge graph sync with Fulminate Cloud (push, pull, list). " +
			"pull: overwrite the local graph from your cloud account (full snapshot, all sync-eligible types). " +
			"list: print a table of sync-eligible local graphs showing cloud sync status + last-synced time. " +
			"Required params: operation only; graph and name are optional (default knowledge/default) for every operation.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"operation": {
					Type:        "string",
					Enum:        []string{"push", "pull", "list"},
					Description: "Operation to perform",
				},
				"graph": {
					Type:        "string",
					Description: "Graph type (knowledge, code, practice, etc.); defaults to 'knowledge'",
				},
				"name": {
					Type: "string",
					Description: "Graph name; defaults to 'default'. For graph='practice' a name addresses a LEGACY " +
						"per-language practice graph (the combined graph is the default); it must be the canonical " +
						"spelling, and a non-canonical one is refused naming the spelling that would have worked. " +
						"Whether a legacy name is ACCEPTED is the destination server's own rule rather than this " +
						"client's: a server from before the practice graphs were combined accepts a canonical legacy " +
						"name, a newer one may refuse it, and its refusal is surfaced verbatim.",
				},
			},
			Required: []string{"operation"},
		},
	}
}
