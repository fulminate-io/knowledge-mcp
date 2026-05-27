// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// This file hosts the four "first-class" tool schemas: thoughts, search,
// file_symbols, collect. The MCP tool catalog is client-owned: loadSchemas
// composes these defs into tools/list. The bodies are pure kgtools.MCPTool
// literals.
//
// repoProp and thoughtsToolDescription are co-relocated package-level
// values the server-side schema producers (searchTools/codeTools/
// thoughtsTools) referenced; they are pure literals carried over so the
// client copies compile standalone.

// repoProp is the shared repo parameter added to the code tools. Moved
// verbatim from the server-side cmd/knowledge-server/tools/tools_code.go.
var repoProp = kgtools.Property{Type: "string", Description: "Repository name (default: all repos). Pass a specific repo name to limit to one repo."}

// ThoughtsToolDef returns the unified MCP tool definition for the thought
// graph. Five operations cover the full reasoning-cycle surface (think /
// charge / recall / trace / propagate) plus the bulk adjacency /
// charges_for reads. Moved verbatim from the server-side thoughtsTools().
func ThoughtsToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name:        "thoughts",
		Description: thoughtsToolDescription,
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"operation": {Type: "string", Description: "Which thoughts op to run.", Enum: []string{"think", "charge", "recall", "trace", "propagate", "adjacency", "charges_for"}},

				// think
				"content":       {Type: "string", Description: "(think) The thought content — what you're thinking and why."},
				"summary":       {Type: "string", MaxLength: 500, Description: "(think, REQUIRED) Search-optimized one-line summary of the thought, max 500 chars. Authored deliberately — this is what makes the thought findable via recall. NOT auto-derived from content."},
				"session":       {Type: "string", Description: "(think, recall filter) Session name to group related thoughts (e.g., 'backend-auth-design'). Creates session if new on think."},
				"branches_from": {Type: "string", Description: "(think) Thought ID this branches from (usually after invalidation of the original)."},
				"links":         {Type: "array", Description: "(think) Node IDs to link this thought to (decisions, findings, code, etc.).", Items: &kgtools.Property{Type: "string"}},
				"status":        {Type: "string", Description: "(think initial status / recall filter) Default hypothesized for think.", Enum: []string{"hypothesized", "validated", "invalidated"}},

				// charge
				"thought":   {Type: "string", Description: "(charge, trace) Thought node ID. Required for charge and trace."},
				"polarity":  {Type: "string", Description: "(charge) Charge direction.", Enum: []string{"positive", "negative"}},
				"weight":    {Type: "number", Description: "(charge) Charge significance (1-10). Higher = stronger evidence."},
				"reasoning": {Type: "string", Description: "(charge) WHY this charge is being applied — what evidence supports it."},
				"evidence":  {Type: "array", Description: "(charge) Node IDs of evidence (tests, PRs, incidents, other charges).", Items: &kgtools.Property{Type: "string"}},

				// recall
				"query":           {Type: "string", Description: "(recall) Semantic search text (optional — omit to browse all thoughts)."},
				"valence_min":     {Type: "number", Description: "(recall) Minimum valence (-1.0 to 1.0)."},
				"valence_max":     {Type: "number", Description: "(recall) Maximum valence (-1.0 to 1.0)."},
				"magnitude_min":   {Type: "number", Description: "(recall) Minimum magnitude (significance threshold)."},
				"consistency_max": {Type: "number", Description: "(recall) Maximum consistency (low values find contested thoughts)."},
				"connected_to":    {Type: "string", Description: "(recall) Must be connected to this node ID."},
				"time_start":      {Type: "string", Description: "(recall) Start of time range (ISO date, e.g. 2026-03-01)."},
				"time_end":        {Type: "string", Description: "(recall) End of time range (ISO date)."},
				"mode":            {Type: "string", Description: "(recall) Output format.", Enum: []string{"search", "timeline", "charges", "graph", "clusters"}},
				"limit":           {Type: "number", Description: "(recall) Max results (default 20)."},
				"format":          {Type: "string", Description: "(recall) Output format: 'text' (default) or 'json' (structured)."},

				// trace
				"direction":         {Type: "string", Description: "(trace) Traversal direction.", Enum: []string{"forward", "backward", "both"}},
				"depth":             {Type: "number", Description: "(trace) Max hops (default 5)."},
				"include_charges":   {Type: "boolean", Description: "(trace) Include charge nodes in the trace."},
				"include_artifacts": {Type: "boolean", Description: "(trace) Include linked artifacts (code, decisions, PRs)."},

				// adjacency
				"scope":       {Type: "string", Description: "(adjacency) Which adjacency view to build. 'all' = NodeThought-filtered with session-sibling expansion (cluster detection on thoughts only). 'all_types' = every node except NodeProxy with no edge filter (cross-type cluster detection).", Enum: []string{"all", "all_types"}},
				"thought_ids": {Type: "array", Description: "(adjacency, charges_for) Optional subset filter (adjacency) / required charge sources (charges_for). When set on adjacency, response is projected down to just these IDs.", Items: &kgtools.Property{Type: "string"}},
			},
			Required: []string{"operation"},
		},
	}
}

// thoughtsToolDescription is split out so the tool def stays scannable.
// Moved verbatim from the server-side tools_thought.go.
const thoughtsToolDescription = `Persistent reasoning graph: hypothesize, charge with evidence, recall, trace chains, propagate. Seven operations:

  - think       : Record a thought (hypothesis / observation / plan). Required: content, summary (search-optimized one-line, max 500 chars). Optional: session, branches_from, links, status.
  - charge      : Add positive/negative evidence to a thought. Required: thought, polarity, weight, reasoning. Optional: evidence.
  - recall      : Search thoughts by composable filters (semantic query, valence/magnitude/consistency thresholds, status, session, time, connected-to). Modes: search (default), timeline, charges, graph, clusters.
  - trace       : Follow reasoning chains forward/backward from a starting thought. Required: thought. Optional: direction, depth, include_charges, include_artifacts.
  - propagate   : Manually trigger DeGroot valence/magnitude propagation across all thoughts. Normally runs automatically in the background.
  - adjacency   : Bulk graph adjacency read used by client-side cluster detection. Required: scope ('all' | 'all_types'). Optional: thought_ids (subset projection).
  - charges_for : Bulk per-thought charge fetch. Required: thought_ids. Returns {charges_by_thought: {tid: [charge_node, ...]}}.

Common cycle: recall → think → (work) → charge → recall (again) to confirm the hypothesis landed. Examine a single thought via query(mode: "examine", id: thought_id). Link a thought to another node via mutate(operation: "link", from: thought_id, to: node_id, relationship: "informed-by"|"supports"|"contradicts"|"relates-to"|"produced").`

// SearchToolDef returns the unified search tool schema definition. Moved
// verbatim from the server-side searchTools().
func SearchToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "search",
		Description: "Search the codebase, knowledge graph, or practice graphs using text and semantic search. " +
			"Returns matching results ranked by relevance. Modes: 'hybrid' (default), 'text' (BM25 only), 'vector' (semantic only). " +
			"Supports BATCH queries via 'queries' array. Set 'graph' to route: code (default), knowledge, practice, cloud, cicd, linkage, or logs.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"query":              {Type: "string", Description: "Single search query (keywords, function names, concepts)."},
				"queries":            {Type: "array", Description: "Batch search: array of query strings. Results deduplicated and merged.", Items: &kgtools.Property{Type: "string"}},
				"graph":              {Type: "string", Description: "Which graph to search: code (default), knowledge, practice, cloud, cicd, linkage, or logs."},
				"name":               {Type: "string", Description: "Graph identifier. Required when graph=logs (the per-query log graph queryID). Ignored for other graph types."},
				"language":           {Type: "string", Description: "Language for practice graph search (e.g. 'go', 'python'). Required when graph=practice."},
				"account":            {Type: "string", Description: "Cloud account key to search. Required when graph=cloud (omit to list available cloud graphs)."},
				"resource_type":      {Type: "string", Description: "Cloud resource type filter prefix (e.g. 'ec2', 'ec2:instance'). Cloud graph only."},
				"limit":              {Type: "number", Description: "Max results per query (default: 10, max: 50)."},
				"include_source":     {Type: "boolean", Description: "Include full source code (default: true). Code graph only."},
				"include_comments":   {Type: "boolean", Description: "Include comment nodes in code search results (default: false). Comments are excluded by default to reduce noise."},
				"mode":               {Type: "string", Description: "Search mode: 'hybrid', 'text', 'vector' (code); 'ppr'/'graph_reach' (knowledge PPR reranking); 'recent'/'temporal' (knowledge recency boost)."},
				"group_by_file":      {Type: "boolean", Description: "Group results by file (default: false). Code graph only."},
				"path_prefix":        {Type: "string", Description: "Filter to files under this path. Code graph only."},
				"repo":               repoProp,
				"repos":              {Type: "array", Description: "Search specific repos (e.g. [\"agent\",\"knowledge\"]). Alternative to repo='all'. Code graph only.", Items: &kgtools.Property{Type: "string"}},
				"branch":             {Type: "string", Description: "Branch name for overlay search. Code graph only."},
				"staleness":          {Type: "boolean", Description: "Include index staleness info (default: false). Code graph only."},
				"current_head":       {Type: "string", Description: "Current git HEAD SHA (auto-populated by client intercept when staleness:true). Code graph only."},
				"uncommitted_count":  {Type: "number", Description: "Count of uncommitted files (auto-populated when staleness:true). Code graph only."},
				"commits_behind":     {Type: "number", Description: "Commits between sync_commit and HEAD (auto-populated when staleness:true). Code graph only."},
				"overlay":            {Type: "string", Description: "Optional: target a specific knowledge session overlay name. When set, the search reads from base + that single overlay only, ignoring the live overlay list. Useful for diagnosing cross-session visibility issues."},
				"types":              {Type: "array", Description: "Filter results by node type (e.g. [\"thought\",\"decision\",\"finding\"]). Knowledge graph only.", Items: &kgtools.Property{Type: "string"}},
				"include_tests":      {Type: "boolean", Description: "Include test code (test/benchmark/example/fuzz/setup/teardown/fixture/mock/helper) in results. Default true. Code graph only — silently ignored on other graphs (mirrors path_prefix). Set false to exclude all test code from impl-style queries. Note: until per-language predicate-population tickets land, all code nodes have is_test=false so this filter is currently a no-op."},
				"test_kinds":         {Type: "array", Description: "Filter set for test classification kinds: any of test, benchmark, example, fuzz, setup, teardown, fixture, mock, helper. Empty/absent means no filter (combined with include_tests=true: all results pass; with include_tests=false: tests of any kind are dropped). Code graph only. Note: until per-language predicate-population tickets land, all code nodes have test_kind=\"\" so this filter is currently a no-op.", Items: &kgtools.Property{Type: "string"}},
				"format":             {Type: "string", Description: "Output format: 'text' (default, markdown) or 'json' (structured). JSON returns {results:[{id,name,type,score,...}]} instead of markdown text."},
				"explain":            {Type: "boolean", Description: "Append per-result match-field annotations (which fields contain the literal query tokens) and a search-mode disclosure footer. Off by default — adds context without changing ranking."},
				"include_tombstones": {Type: "boolean", Description: "Include tombstoned (deleted) nodes in results. Default false."},
				"rerank":             {Type: "boolean", Description: "Apply post-fusion rerank when configured. Default true. Set false for cheap exact-symbol-name lookups where fan-in scoring suffices."},
				"fields":             {Type: "array", Description: "Field projection (format=json only): list of fields to include per result, dramatically shrinking response size for high-volume queries. Top-level: id, name, type, score, description, source, status. Per-metadata-key: 'metadata.<key>' (e.g. 'metadata.dsl_pattern'). Bare 'metadata' includes the whole metadata map. Empty/absent = full hydration (current default). Unknown field names silently dropped.", Items: &kgtools.Property{Type: "string"}},
				"query_vector":       {Type: "string", Description: "Optional base64-encoded binary embedding for the query text (32 bytes / 256-bit decoded). Client-supplied: set by the client-side LLM pipeline's InterceptSearch so the server can serve hybrid-search results without holding a Voyage key. The server never embeds — when query_vector is unset the search runs BM25-only (no server-side embedding fallback). Decoded length mismatches return a structured validation error and no search is performed."},
			},
		},
	}
}

// FileSymbolsToolDef returns the file_symbols tool schema definition. Moved
// verbatim from the server-side codeTools().
func FileSymbolsToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name:        "file_symbols",
		Description: "List all code symbols defined in a specific file with metadata and source code.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"file_path":          {Type: "string", Description: "File path (partial paths work)."},
				"file_paths":         {Type: "array", Description: "Multiple file paths in one call (combined with file_path).", Items: &kgtools.Property{Type: "string"}},
				"include_source":     {Type: "boolean", Description: "Include source code (default: true)."},
				"include_tombstones": {Type: "boolean", Description: "Include tombstoned (deleted) symbols in results. Default false."},
				"format":             {Type: "string", Description: "Output format: 'text' (default, markdown) or 'json' (structured rows: {id, symbol_name, type, file_path, start_line, end_line, signature, summary})."},
				"repo":               repoProp,
			},
			Required: []string{"file_path"},
		},
	}
}

// CollectToolDef returns the collect tool schema definition. Moved verbatim
// from the server-side collectTools(). Collection runs client-side after the
// binary split; the schema lives client-side now too.
func CollectToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "collect",
		Description: "Collect data from an external source into a graph. " +
			"Each collector type handles a specific source (e.g., code repositories, cloud accounts). " +
			"The collector discovers, chunks, and writes nodes/edges to the appropriate graph. " +
			"When type=\"logs\", queries a configured log backend (see manage configure_log_backend) " +
			"via the logs Pipeline. When type=\"web\", fetches the seed URL(s), strips chrome with " +
			"readability, walks the DOM, and emits typed page/section/paragraph/... nodes into a " +
			"per-source graph under GraphWebRaw keyed by id. When type=\"pdf\", opens the PDF at " +
			"id (absolute path), runs the chunker, and emits typed document/section/paragraph/" +
			"code_block/list_item/table/block nodes into a per-source graph under GraphPDFRaw. " +
			"Collection runs client-side after the binary split — invoking this tool against the " +
			"graph server returns an error; the MCP stdio client intercepts and runs the collector " +
			"locally with a RemoteUploadSink.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"type":               {Type: "string", Description: "Collector name (e.g., \"code\", \"aws\", \"gcp\", \"logs\", \"web\", \"pdf\")."},
				"id":                 {Type: "string", Description: "Opaque identifier parsed by the collector (path, account:region, web source slug, absolute path to a .pdf, etc.). Optional for type=\"logs\"."},
				"force":              {Type: "boolean", Description: "Skip safety check for existing indexed graphs."},
				"backend":            {Type: "string", Description: "Logs only: name of a configured log_backend node."},
				"provider":           {Type: "string", Description: "Logs only: provider identifier (e.g., cloudwatch, loki, stackdriver, k8s)."},
				"url":                {Type: "string", Description: "Logs only: backend base URL."},
				"credential":         {Type: "string", Description: "Logs only: credential value when passing provider inline."},
				"auth_type":          {Type: "string", Description: "Logs only: auth mechanism (bearer, basic, aws_profile, api_key, service_account, kubeconfig)."},
				"kube_context":       {Type: "string", Description: "Logs only: kubeconfig context name."},
				"source":             {Type: "string", Description: "Logs only: provider-specific log source selector."},
				"start":              {Type: "string", Description: "Logs only: RFC3339 start timestamp."},
				"end":                {Type: "string", Description: "Logs only: RFC3339 end timestamp."},
				"text_filter":        {Type: "string", Description: "Logs only: free-text substring or pattern applied to log messages."},
				"severity_min":       {Type: "string", Description: "Logs only: minimum severity to include (DEBUG|INFO|WARN|ERROR)."},
				"max_entries":        {Type: "integer", Description: "Logs only: cap on entries pulled from the provider."},
				"filters":            {Type: "object", Description: "Logs only: exact-match label filters applied to log entries."},
				"raw_query":          {Type: "string", Description: "Logs only: provider-native query overriding structured fields."},
				"seed_urls":          {Type: "array", Description: "Web only: starting URL(s) for the crawl.", Items: &kgtools.Property{Type: "string"}},
				"follow_patterns":    {Type: "array", Description: "Web only: regex allowlist for internal links.", Items: &kgtools.Property{Type: "string"}},
				"max_depth":          {Type: "integer", Description: "Web only: BFS depth bound from a seed URL."},
				"max_pages":          {Type: "integer", Description: "Web only: cap on total pages fetched across the crawl."},
				"politeness_ms":      {Type: "integer", Description: "Web only: per-host request delay in milliseconds."},
				"user_agent":         {Type: "string", Description: "Web only: override for the HTTP User-Agent header."},
				"transformer":        {Type: "string", Description: "Web/PDF only: optional transformer name."},
				"recipe":             {Type: "string", Description: "Web/PDF only, transformer=\"recipe\" only: name of a recipe node. The recipe's source_graph_type metadata must match `type`."},
				"dry_run":            {Type: "boolean", Description: "Web/PDF only, transformer=\"recipe\" only: compute emissions but write nothing."},
				"max_download_bytes": {Type: "integer", Description: "Web only: per-(owner,repo,ref) cap on github materialization downloads. 0=default (50 MiB), -1=unlimited, >0=explicit cap (uncompressed bytes)."},
			},
			Required: []string{"type"},
		},
	}
}
