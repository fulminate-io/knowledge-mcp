// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// QueryToolDef returns the unified query tool definition.
//
// The MCP tool catalog is client-owned: loadSchemas composes this def into
// tools/list. The body is a pure kgtools.MCPTool literal — no Handler or store
// references.
func QueryToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "query",
		Description: "Unified query tool. Search knowledge, code, cloud, CI/CD, practice, linkage, or log graphs. " +
			"Specify 'id' for direct node lookup, 'text' for search, 'type' to browse by type, " +
			"or 'mode' for special operations (stats, examine, file_symbols, modules, reflect modes, simulate, recent). " +
			"For graph='logs' (ephemeral log graphs keyed by query_id): no text → label overview; " +
			"text=\"key=value severity>=WARN\" → drill-down into matching streams and templates; " +
			"id=template_id → template detail with decompressed example entries; " +
			"mode='pivot' with rows=<labelKey> cols=<labelKey> → row×col matrix of log counts; " +
			"mode='correlations' → every CORRELATES_WITH edge sorted by score desc; " +
			"mode='timeline' (optional extra.bucket='10s') → templates ordered by FirstSeen; " +
			"mode='explain' (id=<tplA> or extra={a:<tplA>, b:<tplB>}) → per-correlation breakdown; " +
			"mode='resolver' → per-stream cloud-resolution status (resolved vs unresolved). " +
			"Composite shortcuts (exceptions — the primitive is `traverse`): " +
			"mode='lineage' traces provenance chains upward; " +
			"mode='evidence' follows informed-by edges from a decision; " +
			"mode='plan_tree' walks the plan hierarchy downward (default depth 10). " +
			"mode='metadata_stats' returns a per-graph cardinality histogram for every metadata key " +
			"(graph='cloud'|'cicd' requires name=<account>; graph='practice' requires language=<lang>; " +
			"graph='logs' requires name=<query_id>; graph='knowledge' or empty uses the default knowledge graph). " +
			"format='json' returns structured rows instead of markdown.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"graph":               {Type: "string", Description: "Which graph to search: knowledge, code, cloud, cicd, practice, linkage, logs, or all (default: knowledge). Practice graphs are per-language (use 'language' param); log graphs are per-query (use 'name' for the query_id)."},
				"name":                {Type: "string", Description: "Graph name selector (e.g. query_id for graph='logs')."},
				"id":                  {Type: "string", Description: "Direct node lookup by ID. If graph=code, runs analyze_node instead. If graph=logs, returns template detail with decompressed example entries."},
				"ids":                 {Type: "array", Description: "Bulk hydrate-by-id: pass a list of node IDs and receive {label, nodes:[]} in one call (JSON output). Mutually exclusive with id. Used by client-side reflective code where K query(id:...) round trips would otherwise be needed.", Items: &kgtools.Property{Type: "string"}},
				"text":                {Type: "string", Description: "Search query text"},
				"queries":             {Type: "array", Description: "Batch search queries (code graph only)", Items: &kgtools.Property{Type: "string"}},
				"type":                {Type: "string", Description: "Node type filter (e.g. decision, rule, plan, research, document)"},
				"status":              {Type: "string", Description: "Status filter for thought recall"},
				"path_prefix":         {Type: "string", Description: "File path filter (code graph). Also used as file_path for file_symbols mode."},
				"path_prefixes":       {Type: "array", Description: "List form of path_prefix for file_symbols mode — query symbols across multiple files in one call. Combined with path_prefix when both supplied.", Items: &kgtools.Property{Type: "string"}},
				"mode":                {Type: "string", Description: "Search mode or special operation", Enum: []string{"hybrid", "text", "stats", "examine", "file_symbols", "modules", "personality", "influence", "tensions", "blind_spots", "evolution", "summary", "simulate", "timeline", "charges", "clusters", "graph_reach", "recent", "topology", "pivot", "correlations", "explain", "resolver", "lineage", "evidence", "plan_tree", "metadata_stats"}},
				"rows":                {Type: "string", Description: "Row label key for graph='logs' mode='pivot' (e.g. 'reporting_instance'). Defaults sniffed from the graph when omitted."},
				"cols":                {Type: "string", Description: "Column label key for graph='logs' mode='pivot' (e.g. 'reason'). Defaults sniffed from the graph when omitted."},
				"since":               {Type: "string", Description: "Time filter for date-ranged queries (RFC3339 or relative like '24h', '7d')"},
				"valence_min":         {Type: "number", Description: "Minimum thought valence (-1.0 to 1.0)"},
				"valence_max":         {Type: "number", Description: "Maximum thought valence (-1.0 to 1.0)"},
				"magnitude_min":       {Type: "number", Description: "Minimum thought magnitude"},
				"consistency_max":     {Type: "number", Description: "Maximum thought consistency (low = contested)"},
				"session":             {Type: "string", Description: "Filter thoughts by session name"},
				"connected_to":        {Type: "string", Description: "Filter thoughts connected to this node ID"},
				"include_source":      {Type: "boolean", Description: "Include source code in results (code graph)"},
				"include_edges":       {Type: "boolean", Description: "Include edges in node results"},
				"include_cross_links": {Type: "boolean", Description: "Augment node query with cross-graph links from the linkage graph"},
				"group_by_file":       {Type: "boolean", Description: "Group code search results by file"},
				"repo":                {Type: "string", Description: "Code repository name (default: active repo). Use 'all' for all repos."},
				"repos":               {Type: "array", Description: "Search specific repos (alternative to repo='all')", Items: &kgtools.Property{Type: "string"}},
				"language":            {Type: "string", Description: "Language code (e.g. 'go', 'python', 'typescript'). Two uses: (1) practice graph selector — omit to list all practice graphs; (2) topology analyzer filter — code-graph analyzers like god_object scope to a single language. Empty means no filter for topology, all-graphs for practice."},
				"account":             {Type: "string", Description: "Cloud account key to search"},
				"resource_type":       {Type: "string", Description: "Cloud resource type filter prefix"},
				"limit":               {Type: "number", Description: "Max results (default: 10)"},
				"offset":              {Type: "number", Description: "Skip first N results for pagination (default: 0). Use with limit to page through results."},
				"cluster":             {Type: "string", Description: "Cluster filter for reflect personality mode"},
				"cluster_a":           {Type: "string", Description: "First cluster for reflect evolution mode"},
				"cluster_b":           {Type: "string", Description: "Second cluster for reflect evolution mode"},
				"action":              {Type: "string", Description: "Action for simulate mode (remove_charge, invalidate_thought, add_charge)"},
				"target":              {Type: "string", Description: "Target node ID for simulate mode"},
				"polarity":            {Type: "string", Description: "Polarity for simulate add_charge (positive or negative)"},
				"weight":              {Type: "number", Description: "Weight for simulate add_charge (1-10)"},
				"algorithm":           {Type: "string", Description: "Topology analyzer name for mode=topology (e.g. 'pagerank', 'scc'). Use topology.All for the registered list."},
				"top_k":               {Type: "number", Description: "Cap on findings returned by topology analyzers (0 = no cap)"},
				"extra":               {Type: "object", Description: "Per-analyzer config knobs as string-valued keys (e.g. {\"damping\": \"0.85\", \"tolerance\": \"1e-6\"} for pagerank). Keys depend on the analyzer; values are parsed by the analyzer (numbers passed as strings)."},
				"meta":                {Type: "object", Description: "Metadata equality filter, applied to type-browse and text-search dispatch. Map of metadata key to required value; a value of \"*\" matches any non-empty value (i.e., \"the key is set\"). Example: {\"dsl_pattern\": \"*\"} returns every node carrying a dsl_pattern. Multiple keys are AND'd."},
				"fields":              {Type: "array", Description: "Field projection (format=json only): list of fields to include per result, dramatically shrinking response size for high-volume queries. Top-level: id, name, type, status, description. Per-metadata-key: 'metadata.<key>' (e.g. 'metadata.dsl_pattern'). Bare 'metadata' includes the whole metadata map. Empty/absent = full hydration. Unknown field names silently dropped.", Items: &kgtools.Property{Type: "string"}},
				"edge_type":           {Type: "array", Description: "Filter correlations/explain output to edges of these types (e.g. [\"relates-to\"], [\"CORRELATES_WITH\"]). Empty = any edge type.", Items: &kgtools.Property{Type: "string"}},
				"time_field":          {Type: "string", Description: "Node field or metadata key to use as timestamp for timeline mode (e.g. CreatedAt, UpdatedAt, or a metadata key). Defaults to FirstSeen for logs graphs."},
				"overlay":             {Type: "string", Description: "Optional: target a specific knowledge session overlay name (e.g. session-073797a4-...). When set, the query reads from base + that single overlay only, ignoring the live overlay list. Useful for diagnosing cross-session visibility issues."},
				"format":              {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured). Recognized by several modes."},
				"include_tombstones":  {Type: "boolean", Description: "Include tombstoned (deleted) nodes in results. Default false."},
				"include_tests":       {Type: "boolean", Description: "Include test code (test/benchmark/example/fuzz/setup/teardown/fixture/mock/helper) in results. Default true. Code graph only — silently ignored on other graphs. Note: until per-language predicate-population tickets land, all code nodes have is_test=false so this filter is currently a no-op."},
				"test_kinds":          {Type: "array", Description: "Filter set for test classification kinds: any of test, benchmark, example, fuzz, setup, teardown, fixture, mock, helper. Empty/absent means no filter. Code graph only. Note: until per-language predicate-population tickets land, all code nodes have test_kind=\"\" so this filter is currently a no-op.", Items: &kgtools.Property{Type: "string"}},
				"query_vector":        {Type: "string", Description: "Optional base64-encoded binary embedding for the text query (32 bytes / 256-bit decoded). Client-supplied: set by the client-side LLM pipeline's InterceptQuery on text-search modes (hybrid, ppr/graph_reach, recent/temporal). The server never embeds — when query_vector is unset the text-search modes run BM25-only (no server-side embedding fallback). Decoded length mismatches return a structured validation error and no query is performed."},
			},
		},
	}
}
