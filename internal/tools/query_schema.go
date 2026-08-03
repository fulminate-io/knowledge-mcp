// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// queryToolDescription is the advertised prose for the query tool. It lives
// beside the def rather than inside it so the literal stays within the function
// length budget.
const queryToolDescription = "Unified query tool. Search knowledge, code, cloud, CI/CD, practice, linkage, or log graphs. " +
	"Specify 'id' for direct node lookup, 'text' for search, 'type' to browse by type, " +
	"or 'mode' for special operations (stats, examine, file_symbols, modules, reflect modes, simulate, recent). " +
	"For graph='logs' (ephemeral log graphs keyed by query_id): no text → label overview; " +
	"text=\"key=value severity>=WARN\" → drill-down into matching streams and templates; " +
	"id=template_id → template detail with decompressed example entries; " +
	"mode='pivot' with rows=<labelKey> cols=<labelKey> → row×col matrix of log counts; " +
	"mode='correlations' → every CORRELATES_WITH edge for the query_id, sorted by score desc; " +
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
	"On NON-logs graphs mode='correlations' applies a top-by-confidence cap of 100 rows, " +
	"widenable with 'limit' up to 1000, and the underlying edge scan is itself capped at 50000 edges — " +
	"past that the ranking is a SAMPLE of an unordered walk (the output says so; narrow with edge_type for an exact ranking). " +
	"On NON-logs graphs mode='timeline' returns the earliest 500 nodes by time, widenable with 'limit' up to 5000. " +
	"format='json' returns structured rows instead of markdown."

// QueryToolDef returns the unified query tool definition.
//
// The MCP tool catalog is client-owned: loadSchemas composes this def into
// tools/list. The body is a pure kgtools.MCPTool literal — no Handler or store
// references.
func QueryToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name:        "query",
		Description: queryToolDescription,
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
				"types":               {Type: "array", Description: "Filter results to these node types (e.g. [\"project\", \"ticket\", \"plan\", \"step\"]). Honored by mode='recent', by the default (mode-less) plural-type browse, by the knowledge text-search arm, and by the registered custom-graph text-search arm (both arms: mode='text', mode='hybrid', or the default mode carrying text). When both types and the singular type are supplied, types wins, mirroring the engine's browse precedence. Supplying types alongside id or ids is REFUSED with an error naming the field: a by-id read is a lookup and applies no filter.", Items: &kgtools.Property{Type: "string"}},
				"status":              {Type: "string", Description: "Status filter for thought recall"},
				"path_prefix":         {Type: "string", Description: "File path filter (code graph). Also used as file_path for file_symbols mode."},
				"path_prefixes":       {Type: "array", Description: "List form of path_prefix for file_symbols mode — query symbols across multiple files in one call. Combined with path_prefix when both supplied.", Items: &kgtools.Property{Type: "string"}},
				"mode":                {Type: "string", Description: "Search mode or special operation", Enum: []string{"hybrid", "text", "stats", "examine", "file_symbols", "modules", "personality", "influence", "tensions", "blind_spots", "evolution", "summary", "simulate", "timeline", "charges", "clusters", "recent", "topology", "pivot", "correlations", "explain", "resolver", "lineage", "evidence", "plan_tree", "metadata_stats"}},
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
				"repo":                {Type: "string", Description: "Code graph name — REQUIRED for graph=code (it is never inferred from cwd). Use 'all' to query every code repo."},
				"repos":               {Type: "array", Description: "Search specific repos (alternative to repo='all')", Items: &kgtools.Property{Type: "string"}},
				"language":            {Type: "string", Description: "Language code (e.g. 'go', 'python', 'typescript'). Two uses: (1) practice graph selector — omit to list all practice graphs; (2) topology analyzer filter — code-graph analyzers like god_object scope to a single language. Empty means no filter for topology, all-graphs for practice."},
				"account":             {Type: "string", Description: "Selects which inventoried external-provider account/org's resources to query within your own graph — an AWS/GCP account for graph=cloud, or a CI provider org (e.g. GitHub/GitLab) for graph=cicd. Required for graph=cloud/cicd; omit to list your available graphs."},
				"resource_type":       {Type: "string", Description: "Cloud resource type filter prefix"},
				"limit":               {Type: "number", Description: "Max results (default: 10). The server serves EVERY read at a row ceiling — 50,000 rows for an edges read and 10,000 rows for a node browse — so omitting limit, or setting it above the ceiling, returns a result bounded at that ceiling rather than a complete one. The response carries a `truncated` boolean, true whenever a ceiling engaged, so you never have to infer completeness by comparing counts: when it is true, re-run with an explicit limit and page until a short page. For mode='correlations' and mode='timeline' on non-logs graphs limit widens the ranked cap instead (correlations up to 1000 rows, timeline up to 5000); an oversized value is clamped to that ceiling."},
				"offset":              {Type: "number", Description: "Skip first N results for pagination (default: 0). Use with limit to page through results."},
				"cluster":             {Type: "string", Description: "Cluster filter for reflect personality mode"},
				"cluster_a":           {Type: "string", Description: "First cluster for reflect evolution mode"},
				"cluster_b":           {Type: "string", Description: "Second cluster for reflect evolution mode"},
				"sort":                {Type: "string", Description: "Display ordering for the EVIDENCED section of mode=influence. Selection is evidence-aware: charged thoughts are ranked by influence×(1+chargeWeight) into the evidenced top-N, while zero-charge structural hubs are returned in a separate labeled backfill section. 'influence' (default) keeps the influence×(1+chargeWeight) selection order; 'composite' reorders the already-selected evidenced set by influence×magnitude FOR DISPLAY — a within-set reorder that does NOT change which thoughts are selected and does not touch the backfill section.", Enum: []string{"influence", "composite"}},
				"action":              {Type: "string", Description: "Action for simulate mode (remove_charge, invalidate_thought, add_charge)"},
				"target":              {Type: "string", Description: "Target node ID for simulate mode"},
				"polarity":            {Type: "string", Description: "Polarity for simulate add_charge (positive or negative)"},
				"weight":              {Type: "number", Description: "Weight for simulate add_charge (1-10)"},
				"algorithm":           {Type: "string", Description: "Topology analyzer name for mode=topology (e.g. 'pagerank', 'scc'). Use topology.All for the registered list."},
				"top_k":               {Type: "number", Description: "Cap on findings returned by topology analyzers (0 = no cap)"},
				"extra":               {Type: "object", Description: "Per-analyzer config knobs as string-valued keys (e.g. {\"damping\": \"0.85\", \"tolerance\": \"1e-6\"} for pagerank). Keys depend on the analyzer; values are parsed by the analyzer (numbers passed as strings)."},
				"meta":                {Type: "object", Description: "Metadata equality filter, applied to type-browse and text-search dispatch. Map of metadata key to required value; a value of \"*\" matches any non-empty value (i.e., \"the key is set\"). Example: {\"dsl_pattern\": \"*\"} returns every node carrying a dsl_pattern. Multiple keys are AND'd."},
				"fields":              {Type: "array", Description: "Field projection (format=json only): list of fields to include per result, dramatically shrinking response size for high-volume queries. Top-level: id, name, type, status, description. Per-metadata-key: 'metadata.<key>' (e.g. 'metadata.dsl_pattern'). Bare 'metadata' includes the whole metadata map. Empty/absent = full hydration. Unknown field names silently dropped. A per-metadata-key projection is OMITTED ENTIRELY from any result whose node lacks that key — absent from the row, never an empty string. So zero projected values across a result set means the key is unset on those nodes; it is NOT evidence that a write failed.", Items: &kgtools.Property{Type: "string"}},
				"edge_type":           {Type: "array", Description: "Filter correlations/explain output to edges of these types (e.g. [\"relates-to\"], [\"CORRELATES_WITH\"]). Empty = any edge type.", Items: &kgtools.Property{Type: "string"}},
				"time_field":          {Type: "string", Description: "Node field or metadata key to use as timestamp for timeline mode (e.g. CreatedAt, UpdatedAt, or a metadata key). Defaults to FirstSeen for logs graphs."},
				"format":              {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured). Recognized by several modes."},
				"include_tombstones":  {Type: "boolean", Description: "Include tombstoned (deleted) nodes in results. Default false."},
				"include_tests":       {Type: "boolean", Description: "Include test code (test/benchmark/example/fuzz/setup/teardown/fixture/mock/helper) in results. Default true. Code graph only — silently ignored on other graphs. Note: until per-language predicate-population tickets land, all code nodes have is_test=false so this filter is currently a no-op."},
				"test_kinds":          {Type: "array", Description: "Filter set for test classification kinds: any of test, benchmark, example, fuzz, setup, teardown, fixture, mock, helper. Empty/absent means no filter. Code graph only. Note: until per-language predicate-population tickets land, all code nodes have test_kind=\"\" so this filter is currently a no-op.", Items: &kgtools.Property{Type: "string"}},
				"query_vector":        {Type: "string", Description: "Optional base64-encoded binary embedding for the text query (32 bytes / 256-bit decoded). Client-supplied: set by the client-side LLM pipeline's InterceptQuery on text-search modes (hybrid, recent/temporal). The server never embeds — when query_vector is unset the text-search modes run BM25-only (no server-side embedding fallback). Decoded length mismatches return a structured validation error and no query is performed."},
				"branch":              {Type: "string", Description: "Code-graph branch overlay to read instead of the base graph. Auto-filled from the machine-local repo manifest when the caller omits it and repo is not 'all' — the repo's recorded on-disk directory drives the branch detection, so the branch that gets read is the one that repo is actually on. Supply it explicitly to pin a specific overlay. Read by the analyze, code-search, file-symbols, modules/code-stats and topology arms."},
				"caller_depth":        {Type: "number", Description: "How many levels of CALLERS to walk for query(graph='code', id=...) analyze. Clamped to 1..3: a value at or below zero reads as 1, anything above 3 reads as 3."},
				"callee_depth":        {Type: "number", Description: "How many levels of CALLEES to walk for query(graph='code', id=...) analyze. Clamped to 1..3 on the same rule as caller_depth."},
				"file_path":           {Type: "string", Description: "Single file to list symbols for in mode='file_symbols'. This is the file_symbols tool's own spelling, accepted here for parity; the query-native spelling is path_prefix, and both reach the same reader."},
				"file_paths":          {Type: "array", Description: "Several files to list symbols for in mode='file_symbols', in one call. The file_symbols tool's own spelling, accepted here for parity; the query-native spelling is path_prefixes, and both reach the same reader.", Items: &kgtools.Property{Type: "string"}},
				"granularity":         {Type: "string", Description: "Which reflect view mode='summary' and mode='personality' render: 'cluster' (default) rolls up by detected cluster, 'topic' rolls up by topic membership and displays topic summaries as the names.", Enum: []string{"cluster", "topic"}},
				"include_comments":    {Type: "boolean", Description: "Include comment nodes in code search results (default: false). Comments are excluded by default to reduce noise."},
				"samples":             {Type: "boolean", Description: "Add bounded per-type sample node names to a mode='stats' render. Honored by every stats arm: knowledge, cloud/cicd, practice/linkage, code, and logs."},
				"scope":               {Type: "string", Description: "Substring filter applied by the query(type='rule') browse over each rule's scope metadata and its description, case-insensitively."},
			},
		},
	}
}
