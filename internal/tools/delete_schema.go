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
				"graph":      {Type: "string", Description: "Target graph: 'knowledge' (default), 'code' (requires repo), 'practice', or 'checks'. practice and checks are singletons and take neither language nor name; a practice delete narrows by `source` instead."},
				"language":   {Type: "string", Description: "LEGACY read-only practice selector naming a pre-singleton practice graph. It is REFUSED on a delete: practice is one combined graph, so narrow by `source` instead."},
				"source":     {Type: "string", Description: "Practice SOURCE HUB id — deletes every practice node grouped under that hub AND the hub itself, in ONE write: a hub carries its own id under the membership key, so a single predicate sweeps the collection whole and leaves no empty hub behind. This is how an abandoned or failed collection is removed as a unit. The id must name a LIVE hub — a `source` node in the combined practice graph — and an id that names a member, a node of another type, a tombstoned hub or nothing at all is refused, naming the id, rather than run against whatever it does match. Mutually exclusive with ids/id: a call carrying both selection axes is denied rather than resolved."},
				"repo":       {Type: "string", Description: "Code graph name — REQUIRED for graph='code'; it is never inferred from cwd. Writes to a collected graph are caller-owned: a later collect reconciles that graph from its source and overwrites hand-authored changes."},
				"account":    {Type: "string", Description: "NO BUILT-IN FAMILY IS KEYED BY ACCOUNT. It was the instance key of the retired cloud and cicd families; a collected inventory graph is a registered custom type now, addressed by name. Consumed by nothing today."},
				"format":     {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured). Honored on BOTH render paths — the dry-run preview and the completed delete."},
			},
		},
	}
}
