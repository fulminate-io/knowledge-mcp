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
		Description: "Unified delete tool. Delete nodes by ID. " +
			"Deletes are SOFT by default (tombstoned: hidden from reads, recoverable); pass hard:true for permanent removal. " +
			"A soft delete leaves the node's edges in place; a hard delete sweeps every incident edge with it. " +
			"Required params: ids (or its singular alias id). " +
			"Prune-by-age (older_than + type) is NOT CURRENTLY AVAILABLE: it selects on node type and no node type is retention-eligible today, so every older_than form is refused rather than run.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"ids":        {Type: "array", Description: "Node IDs to delete", Items: &kgtools.Property{Type: "string"}},
				"id":         {Type: "string", Description: "Singular alias for a one-element `ids` — every other single-node op names its target with `id`, so deleting one node accepts that spelling too. Supplying both is additive (the two sets union), not a conflict."},
				"older_than": {Type: "string", Description: "Prune-by-age window (e.g. '7d', '24h'). NOT CURRENTLY AVAILABLE — no node type is retention-eligible, so a call carrying older_than is refused rather than run."},
				"type":       {Type: "string", Description: "Node type filter for prune-by-age. NOT CURRENTLY AVAILABLE — every type fails the retention-eligibility check, so no value here selects anything."},
				"session_id": {Type: "string", Description: "Restricts prune-by-age to one session. Inert while prune-by-age is unavailable."},
				"dry_run":    {Type: "boolean", Description: "Preview only: report the nodes that WOULD be deleted (count + ids/names) without deleting anything. Applies to the ids shape; on an older_than call it reports that prune-by-age has no retention-eligible type rather than previewing. Re-run without dry_run to actually delete."},
				"hard":       {Type: "boolean", Description: "PERMANENT removal. Deletes are SOFT by default (tombstoned: hidden from reads, recoverable). hard:true removes the rows irrecoverably — reserve for deliberate permanent cleanup. A malformed value denies the delete."},
				"graph":      {Type: "string", Description: "Target graph: 'knowledge' (default), 'code' (requires repo), 'cloud' / 'cicd' (require account), 'practice' (requires language), or 'checks'. checks is a singleton and takes neither language nor name."},
				"language":   {Type: "string", Description: "Language for practice graph operations (e.g. 'Go', 'JavaScript/TypeScript')"},
				"repo":       {Type: "string", Description: "Code graph name — REQUIRED for graph='code'; it is never inferred from cwd. Writes to a collected graph are caller-owned: a later collect reconciles that graph from its source and overwrites hand-authored changes."},
				"account":    {Type: "string", Description: "Selects which inventoried external-provider account/org's resources to address within your own graph — an AWS/GCP account for graph='cloud', or a CI provider org (e.g. GitHub/GitLab) for graph='cicd'. REQUIRED for those two families. Writes to a collected graph are caller-owned: a later collect reconciles that graph from its source and overwrites hand-authored changes."},
				"format":     {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured). Honored on BOTH render paths — the dry-run preview and the completed delete."},
			},
		},
	}
}
