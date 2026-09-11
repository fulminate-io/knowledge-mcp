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
				"start":                 {Type: "string", Description: "Starting node ID. OPTIONAL: an EMPTY start is not an error — it selects the graph-wide enumeration of the target graph instead of a walk, reporting the graph's node and edge totals in text and its node/edge rows under format='json'. "},
				"direction":             {Type: "string", Description: "Edge direction to walk: 'out' (outgoing, default), 'in' (incoming), or 'both' (union deduped by node ID)", Enum: []string{"out", "in", "both"}},
				"edge_types":            {Type: "array", Description: "Filter by edge types (optional; empty means any)", Items: &kgtools.Property{Type: "string"}},
				"depth":                 {Type: "number", Description: "Max traversal depth (default 1)"},
				"limit":                 {Type: "number", Description: "Max results to return (0 = no cap). ON A RAW DOCUMENT GRAPH THE SLICE IS TAKEN IN NODE-ID ORDER, NOT DOCUMENT ORDER: the traversal never decodes the document position, which lives on edge Evidence, so a limited traverse over a collected document returns an arbitrary slice rather than the first N sections. Drop the limit, or use a recipe extract, which materializes the whole source graph and can therefore order it."},
				"graph":                 {Type: "string", Description: "Target graph: '' or 'knowledge' (default), 'code', 'practice', 'checks', 'linkage', or a registered custom graph type (the name a custom_collector registration was made under)."},
				"name":                  {Type: "string", Description: "Graph identifier, for the families keyed by name."},
				"language":              {Type: "string", Description: "REFUSED on a practice traverse, and it addresses no other family's graph. Practice is ONE combined graph: omit this to traverse it, or narrow to one origin with 'source' (a hub id)."},
				"source":                {Type: "string", Description: "Practice SOURCE HUB id — narrows the traverse to the nodes grouped under that hub. Omit it to traverse the whole combined practice graph."},
				"account":               {Type: "string", Description: "NO BUILT-IN FAMILY IS KEYED BY ACCOUNT. It was the instance key of the retired cloud and cicd families; a collected inventory graph is a registered custom type now, addressed by name. Consumed by nothing today."},
				"repo":                  {Type: "string", Description: "Repo name for graph='code'."},
				"branch":                {Type: "string", Description: "Branch name for graph='code' (optional)."},
				"include_edge_metadata": {Type: "boolean", Description: "When true, emit Weight/Confidence/Method/Evidence/LastValidated on every edge at every hop. Default off for all graphs."},
				"format":                {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured)"},
				"include_tombstones":    {Type: "boolean", Description: "Include tombstoned (deleted) nodes in results. Default false. Edge endpoints are always tombstone-filtered regardless of this flag: the flag governs NODES."},
			},
			// NO required set, deliberately: `start` is optional and nothing
			// here declares otherwise. An empty `start` selects the graph-wide
			// enumeration, which engine.dispatchGraphWideEdges claims BEFORE
			// Compile (cmd/knowledge/internal/engine/dispatch_graphwide.go) for
			// every graph, and which compileTraverse refuses
			// (compile_traverse.go) so it can never become a from_id walk. No
			// `name` is needed for it — a start-less traverse on the default
			// knowledge graph enumerates that graph.
			//
			// The only validation this schema drives is by param NAME:
			// rejectUndeclaredParams (intercept_traverse_params.go) checks every
			// traverse call's arguments against the Properties above.
		},
	}
}
