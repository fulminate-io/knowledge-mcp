// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// TraverseToolDef returns the unified traverse tool definition.
//
// The MCP tool catalog is client-owned: loadSchemas composes this def into
// tools/list. Pure kgtools.MCPTool literal.
//
// Edge-first schema: walk edges from `start` filtered by `edge_types` in the
// given `direction` (in|out|both). `direction="both"` unions inbound and
// outbound edges and dedupes by node ID (one row per unique node, minimum
// distance wins). `graph_selector` fields (graph/name/language/account/repo/
// branch) pick the target graph via ResolveGraphDB. Cross-graph auto-
// resolution: when start is a node from a different graph than the selector,
// the primitive resolves via version head or linkage proxies transparently.
// `include_edge_metadata=true` surfaces Weight/Confidence/Method/Evidence/
// LastValidated on every edge along the walk at any depth (full fidelity).
func TraverseToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "traverse",
		Description: "Edge-first graph traversal. Walks edges from `start` filtered by `edge_types` in the given " +
			"`direction` (in|out|both). `direction=\"both\"` unions inbound and outbound edges and dedupes by node ID " +
			"(one row per unique node, minimum distance wins). Graph-selector fields (graph/name/language/account/" +
			"repo/branch) pick the target graph. Cross-graph auto-resolution: when start is a node from a different " +
			"graph than the selector, the primitive resolves via version head or linkage proxies transparently. " +
			"`include_edge_metadata=true` surfaces Weight/Confidence/Method/Evidence/LastValidated on every edge " +
			"along the walk at any depth (full fidelity).",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"start":                 {Type: "string", Description: "Starting node ID"},
				"direction":             {Type: "string", Description: "Edge direction to walk: 'out' (outgoing, default), 'in' (incoming), or 'both' (union deduped by node ID)", Enum: []string{"out", "in", "both"}},
				"edge_types":            {Type: "array", Description: "Filter by edge types (optional; empty means any)", Items: &kgtools.Property{Type: "string"}},
				"depth":                 {Type: "number", Description: "Max traversal depth (default 1)"},
				"limit":                 {Type: "number", Description: "Max results to return (0 = no cap)"},
				"graph":                 {Type: "string", Description: "Target graph: '' or 'knowledge' (default), 'code', 'cloud', 'cicd', 'practice', 'logs', 'linkage'."},
				"name":                  {Type: "string", Description: "Graph identifier (e.g. query_id for graph='logs')."},
				"language":              {Type: "string", Description: "Language slug for graph='practice' (e.g. 'go', 'python')."},
				"account":               {Type: "string", Description: "Selects which inventoried external-provider account/org's resources to traverse within your own graph — an AWS/GCP account for graph='cloud', or a CI provider org (e.g. GitHub/GitLab) for graph='cicd'. Required for graph='cloud'/'cicd'; omit to list your available graphs."},
				"repo":                  {Type: "string", Description: "Repo name for graph='code'."},
				"branch":                {Type: "string", Description: "Branch name for graph='code' (optional)."},
				"overlay":               {Type: "string", Description: "Optional knowledge session overlay name; diagnostic scoping."},
				"include_edge_metadata": {Type: "boolean", Description: "When true, emit Weight/Confidence/Method/Evidence/LastValidated on every edge at every hop. Default off for all graphs."},
				"format":                {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured)"},
				"include_tombstones":    {Type: "boolean", Description: "Include tombstoned (deleted) nodes in results. Default false."},
			},
			// `start` is required EXCEPT when (graph, name) are both set and
			// `start` is empty — that combination means graph-wide edge
			// enumeration, which validateTraverseStart enforces.
		},
	}
}
