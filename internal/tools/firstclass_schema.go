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
var repoProp = kgtools.Property{Type: "string", Description: "Repository (code graph) name — REQUIRED for graph=code; it is never inferred from cwd. search accepts 'all' to span every code repo. Not used by the knowledge graph."}

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
				"operation": {Type: "string", Description: "Which thoughts op to run.", Enum: []string{"think", "charge", "recall", "trace", "propagate", "adjacency", "charges_for", "similarity_report"}},

				// think
				"content":       {Type: "string", Description: "(think) The thought content — what you're thinking and why."},
				"summary":       {Type: "string", MaxLength: 500, Description: "(think and charge, REQUIRED on both) Search-optimized one-line summary, max 500 chars, authored deliberately — this is what makes the node findable via recall, and nothing composes one for you. On think it summarizes the THOUGHT'S CLAIM; on charge it summarizes the EVIDENCE the charge records, which is a different sentence from the thought it attaches to."},
				"session":       {Type: "string", Description: "(think, recall filter) Session name to group related thoughts (e.g., 'backend-auth-design'). Creates session if new on think."},
				"ticket_id":     {Type: "string", Description: "(think) Active ticket/project ID — born-linked as ticket--contains-->thought so the thought is grouped under the work item that produced it. An unresolvable ticket_id is dropped with a warning, never blocking the think."},
				"branches_from": {Type: "string", Description: "(think) Thought ID this branches from (usually after invalidation of the original)."},
				"links":         {Type: "array", Description: "(think) Node IDs to link this thought to (decisions, findings, code, etc.).", Items: &kgtools.Property{Type: "string"}},
				"status":        {Type: "string", Description: "(think initial status / recall filter) Default hypothesized for think.", Enum: []string{"hypothesized", "validated", "invalidated"}},
				"origin":        {Type: "string", Description: "(think) Developer-origin role of the agent recording this thought — conventional values planner|implementer|reviewer|researcher|tester|orchestrator|main; absent => main. Open string (flex-parsed, NOT an enum gate): a custom value is stored as-is. Stamped as origin metadata, and when it resolves to a user-authored agent node, an agent--produced-->thought hub edge is written."},

				// negation gate (think supersession)
				"verified_quote": {Type: "string", Description: "(think) Negation-gate proof of work — a TOP-LEVEL param on the call. REQUIRED whenever branches_from is set (a supersession): a verbatim substring of the superseded node's CURRENT source. Consumed by the gate before any write and never persisted."},
				"cited_range":    {Type: "string", Description: "(think) Optional locality hint accompanying verified_quote on a supersession, as \"path/file.go:start-end\". When set, the verbatim substring must resolve to the cited path. TOP-LEVEL param, consumed by the gate before any write and never persisted."},

				// charge
				"thought":    {Type: "string", Description: "(charge, trace) Thought node ID. Required for charge and trace."},
				"thought_id": {Type: "string", Description: "(charge) Singular alias for `thought` — the charge arm accepts either spelling for the thought node ID."},
				"polarity":   {Type: "string", Description: "(charge) Whether the evidence SUPPORTS the thought's claim (\"positive\") or CONTRADICTS it (\"negative\"). NOT good-news/bad-news about the subject — sentiment about the subject belongs in reasoning/content text.", Enum: []string{"positive", "negative"}},
				"weight":     {Type: "number", Description: "(charge) Charge significance (1-10). Higher = stronger evidence."},
				"reasoning":  {Type: "string", Description: "(charge) WHY this charge applies — the specific evidence that supports or contradicts the thought's claim. Put any sentiment about the subject HERE, never in the polarity sign."},
				"evidence":   {Type: "array", Description: "(charge) Node IDs of evidence — tests, PRs, incidents, related thoughts, or other charges. Citing a related thought records a charge→thought evidenced-by edge that feeds cross-cluster trust differentiation.", Items: &kgtools.Property{Type: "string"}},

				// recall
				"query":           {Type: "string", Description: "(recall) Semantic search text (optional — omit to browse all thoughts)."},
				"valence_min":     {Type: "number", Description: "(recall) Minimum valence (-1.0 to 1.0)."},
				"valence_max":     {Type: "number", Description: "(recall) Maximum valence (-1.0 to 1.0)."},
				"magnitude_min":   {Type: "number", Description: "(recall) Minimum magnitude (significance threshold)."},
				"consistency_max": {Type: "number", Description: "(recall) Maximum consistency (low values find contested thoughts)."},
				"connected_to":    {Type: "string", Description: "(recall) Must be connected to this node ID."},
				"time_start":      {Type: "string", Description: "(recall) Start of time range (ISO date, e.g. 2026-03-01)."},
				"time_end":        {Type: "string", Description: "(recall) End of time range (ISO date)."},
				"mode":            {Type: "string", Description: "(recall) Which recall view to run. search (default), timeline and charges are renders of the thought pipeline, and graph renders as search does; clusters and context are separate arms — clusters runs cluster detection, and context composes the five-section session-start context pack (cross-type seed search, bounded edge expansion, charge state, recency overlay, open tickets), so it is NOT thought-only.", Enum: []string{"search", "timeline", "charges", "graph", "clusters", "context"}},
				"limit":           {Type: "number", Description: "(recall) Max results (default 20, max 50)."},
				"format":          {Type: "string", Description: "(recall) Output format: 'text' (default) or 'json' (structured)."},

				// trace
				"direction":         {Type: "string", Description: "(trace) Traversal direction.", Enum: []string{"forward", "backward", "both"}},
				"depth":             {Type: "number", Description: "(trace) Max hops (default 5)."},
				"include_charges":   {Type: "boolean", Description: "(trace) Include charge nodes in the trace."},
				"include_artifacts": {Type: "boolean", Description: "(trace) Include linked artifacts (code, decisions, PRs)."},

				// adjacency
				"scope":       {Type: "string", Description: "(adjacency) Which adjacency view to build. 'all' = NodeThought-filtered with session-sibling expansion (cluster detection on thoughts only). 'all_types' = every node except NodeProxy with no edge filter (cross-type cluster detection).", Enum: []string{"all", "all_types"}},
				"thought_ids": {Type: "array", Description: "(adjacency, charges_for) Optional subset filter (adjacency) / required charge sources (charges_for). When set on adjacency, response is projected down to just these IDs.", Items: &kgtools.Property{Type: "string"}},
				"all_types":   {Type: "boolean", Description: "(recall, mode:'clusters') Run cluster detection over EVERY node type rather than thoughts only — the boolean spelling of the adjacency arm's scope:'all_types'."},

				// propagate
				"force_full":          {Type: "boolean", Description: "(propagate) Run the full-corpus backstop pass now — bypasses the quiet-tick skip and incremental scoping, recomputes every component, and resets the backstop cadence. Use for an on-demand full reflection (ops/debug) instead of waiting for the periodic backstop tick. Errors if the reflection loop is not running in this process."},
				"similarity":          {Type: "boolean", Description: "(propagate) Trigger the topic-similarity lever ASYNCHRONOUSLY (drain → centroids → reconcile → merge cascade → summaries → drift → links). Returns immediately with a similarity_report fetch call and a duration estimate; only one pass runs at a time and a second trigger coalesces."},
				"link_threshold":      {Type: "number", Description: "(propagate, similarity:true) Per-call override for the topic-link similarity threshold. Absent uses the package default. Accepts a number or its quoted-string form; any other value is surfaced loudly rather than defaulted."},
				"merge_threshold":     {Type: "number", Description: "(propagate, similarity:true) Per-call override for the topic-merge cascade threshold. Absent uses the package default. Accepts a number or its quoted-string form."},
				"densify_threshold":   {Type: "number", Description: "(propagate, similarity:true) Similarity floor for the post-link within-topic kNN densification phase. Absent uses the package default."},
				"densify_k":           {Type: "number", Description: "(propagate, similarity:true) Neighbor count per node for the densification phase. Absent uses the package default."},
				"densify_edge_budget": {Type: "number", Description: "(propagate, similarity:true) Cap on edges the densification phase may add in one pass. Absent uses the package default."},

				// similarity_report
				"id": {Type: "string", Description: "(similarity_report) Optional id of a specific past similarity pass to fetch. Omit to fetch the LATEST pass (running → in-progress + estimate; completed → the full rendered report; failed → the failure)."},
			},
			Required: []string{"operation"},
		},
	}
}

// thoughtsToolDescription is split out so the tool def stays scannable.
// Moved verbatim from the server-side tools_thought.go.
const thoughtsToolDescription = `Persistent reasoning graph: hypothesize, charge with evidence, recall, trace chains, propagate. Eight operations:

  - think       : Record a thought (hypothesis / observation / plan). Required: content, summary (search-optimized one-line, max 500 chars). Optional: session, ticket_id, branches_from, links, status, origin (developer-origin role: planner|implementer|reviewer|researcher|tester|orchestrator|main; absent => main).
  - charge      : Attach evidence that SUPPORTS (positive) or CONTRADICTS (negative) the thought's claim. Required: thought, polarity, weight, reasoning. Optional: evidence.
  - recall      : Search thoughts by composable filters (semantic query, valence/magnitude/consistency thresholds, status, session, time, connected-to). Modes: search (default), timeline, charges, graph, clusters, context. mode:"context" is the SESSION-START pack and is NOT thought-only — it composes a cross-type seed search, a bounded edge expansion, charge state, a recency overlay and the open tickets into one bounded read.
  - trace       : Follow reasoning chains forward/backward from a starting thought. Required: thought. Optional: direction, depth, include_charges, include_artifacts.
  - propagate   : Manually trigger DeGroot valence/magnitude propagation across all thoughts. Normally runs automatically in the background. Optional: force_full (run the full-corpus backstop pass now — bypasses the quiet-tick skip + incremental scoping, resets the backstop cadence). With similarity:true it triggers the topic-similarity lever ASYNCHRONOUSLY: it starts the pass in the background and returns immediately with a copy-pasteable thoughts({"operation":"similarity_report"}) fetch call + a duration estimate (the pass can outlive the tool-call timeout); only one pass runs at a time (a second trigger coalesces).
  - adjacency   : Bulk graph adjacency read used by client-side cluster detection. Required: scope ('all' | 'all_types'). Optional: thought_ids (subset projection).
  - charges_for : Bulk per-thought charge fetch. Required: thought_ids. Returns {charges_by_thought: {tid: [charge_node, ...]}}.
  - similarity_report : Fetch the result of the async topic-similarity pass triggered by propagate+similarity. Optional: id (a specific past pass; omit for the latest). running → in-progress + elapsed + estimate; completed → the full rendered report; failed → the failure; no pass yet → a clear empty-state message.

Common cycle: recall → think → (work) → charge → recall (again) to confirm the hypothesis landed. Examine a single thought via query(mode: "examine", id: thought_id). Link a thought to another node via mutate(operation: "link", from: thought_id, to: node_id, relationship: "informed-by"|"supports"|"contradicts"|"relates-to"|"produced").`

// SearchToolDef returns the unified search tool schema definition. Moved
// verbatim from the server-side searchTools().
func SearchToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "search",
		Description: "Search the codebase, knowledge graph, or practice graphs using text and semantic search. " +
			"Returns matching results ranked by relevance. Modes: 'hybrid' (default, BM25 and vector fused), " +
			"'text' (BM25 only — the query is not embedded and no rerank runs), " +
			"'vector' (vector only — requires a configured embedder). " +
			"Supports BATCH queries via 'queries' array. Set 'graph' to route: code (default), knowledge, practice, cloud, cicd, linkage, or logs.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"query":             {Type: "string", Description: "Single search query (keywords, function names, concepts)."},
				"queries":           {Type: "array", Description: "Batch search: array of query strings. Results deduplicated and merged.", Items: &kgtools.Property{Type: "string"}},
				"graph":             {Type: "string", Description: "Which graph to search: code (default), knowledge, practice, cloud, cicd, linkage, or logs."},
				"name":              {Type: "string", Description: "Graph identifier. Required when graph=logs (the per-query log graph queryID). Ignored for other graph types."},
				"account":           {Type: "string", Description: "Selects which inventoried external-provider account/org's resources to search within your own graph — an AWS/GCP account for graph=cloud, or a CI provider org (e.g. GitHub/GitLab) for graph=cicd. Required for graph=cloud/cicd; omit to list your available graphs."},
				"resource_type":     {Type: "string", Description: "Cloud resource type filter prefix (e.g. 'ec2', 'ec2:instance'). Cloud graph only."},
				"limit":             {Type: "number", Description: "Max results per query (default: 10, max: 50)."},
				"include_source":    {Type: "boolean", Description: "Include full source code (default: true). Code graph only."},
				"include_comments":  {Type: "boolean", Description: "Include comment nodes in code search results (default: false). Comments are excluded by default to reduce noise."},
				"mode":              {Type: "string", Description: "Search mode, honored on the knowledge and registered custom-graph arms: 'hybrid' (default — BM25 and vector fused), 'text' (BM25 only — no query embedding and no rerank), 'vector' (vector only — requires an embedder). 'recent'/'temporal' are one recency boost (knowledge graph). Not honored on the code arm, which always fuses BM25 and vector when an embedder is available. 'similar' (knowledge graph). mode:'similar' takes a node_id and returns that node's nearest corpus neighbors by searching the node's OWN STORED vector (its embedding already on disk — NOT a fresh embedding of any query text), with the node itself EXCLUDED from results. Results are ranked by the client engine's reciprocal-rank fusion over the stored-vector (HNSW) arm — with no query text the order is pure stored-vector proximity — NOT a raw cosine similarity score."},
				"node_id":           {Type: "string", Description: "The node whose nearest stored-vector neighbors to return when mode:'similar' is set (knowledge graph). The named node is resolved to its on-disk embedding and excluded from its own results."},
				"group_by_file":     {Type: "boolean", Description: "Group results by file (default: false). Code graph only."},
				"path_prefix":       {Type: "string", Description: "Filter to files under this path. Code graph only."},
				"repo":              repoProp,
				"repos":             {Type: "array", Description: "Search specific repos (e.g. [\"agent\",\"knowledge\"]). Alternative to repo='all'. Code graph only.", Items: &kgtools.Property{Type: "string"}},
				"language":          {Type: "string", Description: "Practice graph selector: names ONE practice graph to search (e.g. 'go', 'go-idioms'). Omit it, or pass 'all', to fan out across every loaded practice graph — that fan-out is the default and stays the default. Practice graph only; the same spelling query uses."},
				"branch":            {Type: "string", Description: "Branch name for overlay search. Code graph only."},
				"staleness":         {Type: "boolean", Description: "Include index staleness info (default: false). Code graph only."},
				"current_head":      {Type: "string", Description: "Current git HEAD SHA (auto-populated by client intercept when staleness:true). Code graph only."},
				"uncommitted_count": {Type: "number", Description: "Count of uncommitted files (auto-populated when staleness:true). Code graph only."},
				"types":             {Type: "array", Description: "Filter results by node type (e.g. [\"thought\",\"decision\",\"finding\"]). Applied on the knowledge graph and on registered custom graphs.", Items: &kgtools.Property{Type: "string"}},
				"include_tests":     {Type: "boolean", Description: "Include test code (test/benchmark/example/fuzz/setup/teardown/fixture/mock/helper) in results. Default true. Code graph only — silently ignored on other graphs (mirrors path_prefix). Set false to exclude all test code from impl-style queries. Note: until per-language predicate-population tickets land, all code nodes have is_test=false so this filter is currently a no-op."},
				"test_kinds":        {Type: "array", Description: "Filter set for test classification kinds: any of test, benchmark, example, fuzz, setup, teardown, fixture, mock, helper. Empty/absent means no filter (combined with include_tests=true: all results pass; with include_tests=false: tests of any kind are dropped). Code graph only. Note: until per-language predicate-population tickets land, all code nodes have test_kind=\"\" so this filter is currently a no-op.", Items: &kgtools.Property{Type: "string"}},
				"format":            {Type: "string", Description: "Output format: 'text' (default, markdown) or 'json' (structured). JSON returns {results:[{id,name,type,score,...}]} instead of markdown text."},
				// Three keys were retired from this schema — include_tombstones,
				// explain and commits_behind. Each was declared here and read by no
				// decode site on any search arm, so the tool accepted them and
				// dropped them; commits_behind additionally advertised an
				// auto-population that populateStaleness documents as removed.
				// Undeclaring them hands them to rejectUndeclaredParams, which
				// already runs over this map, so a caller supplying one is now
				// refused by name instead of silently ignored. Tombstoned nodes are
				// reachable through query, whose arm registry genuinely routes the
				// flag; file_symbols keeps its own declaration below because it
				// genuinely consumes it.
				"rerank":       {Type: "boolean", Description: "Apply post-fusion rerank when configured. Default true. Set false for cheap exact-symbol-name lookups where fan-in scoring suffices."},
				"fields":       {Type: "array", Description: "Field projection (format=json only): list of fields to include per result, dramatically shrinking response size for high-volume queries. Top-level keys accepted: content, created_at, description, file_path, graph, graph_instance, id, keywords, language, line, metadata, name, score, signature, source, status, summary, symbol_name, test_kind, tombstoned_at, type, updated_at — every search read is a ranked-search read, so the three hit properties (score, graph, graph_instance) are always available here. Per-metadata-key: 'metadata.<key>' (e.g. 'metadata.dsl_pattern'). Bare 'metadata' includes the whole metadata map. Empty/absent = full hydration (current default). An unsupported key is REFUSED, naming the offending key and the accepted vocabulary. A named top-level key is ALWAYS returned when you request it — empty string for an unset text field, 0 for an unset timestamp, empty map for absent metadata — so \"present and unset\" stays distinguishable from \"not in your projection\". tombstoned_at is the ONE exception to that always-returned rule: it is OMITTED ENTIRELY for a live node rather than returned as 0, because a sentinel 0 is indistinguishable at the wire from a real tombstone stamp. created_at and updated_at project as RAW int64 unix nanos, and an unset stamp returns 0 on these projection arms — UNLIKE query's mode:\"examine\" and mode:\"plan_tree\", which omit the key entirely when the stamp is zero. A per-metadata-key projection is OMITTED ENTIRELY from any result whose node lacks that key — absent from the row, never an empty string. So zero projected values across a result set means the key is unset on those nodes; it is NOT evidence that a write failed.", Items: &kgtools.Property{Type: "string"}},
				"query_vector": {Type: "string", Description: "Optional base64-encoded binary embedding for the query text (32 bytes / 256-bit decoded). Client-supplied: set by the client-side LLM pipeline's InterceptSearch so the server can serve hybrid-search results without holding a Voyage key. The server never embeds — when query_vector is unset the search runs BM25-only (no server-side embedding fallback). Decoded length mismatches return a structured validation error and no search is performed."},
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
				"limit":              {Type: "number", Description: "Cap the RENDERED symbol rows. It bounds the response, not the read — the per-file fetch is unchanged. Omit to render every symbol. Across several paths the cap is a total spent in request order."},
				"format":             {Type: "string", Description: "Output format: 'text' (default, markdown) or 'json' (structured rows: {id, symbol_name, type, file_path, start_line, end_line, signature, summary})."},
				"repo":               repoProp,
				"branch":             {Type: "string", Description: "Code-graph branch overlay to read instead of the base graph. Auto-filled by the client from the machine-local repo manifest when the caller omits it and repo is not 'all', so it arrives on the call even when unset by hand; supply it explicitly to pin a specific overlay."},
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
			"graph server returns an error; the knowledge MCP client intercepts and runs the collector " +
			"locally with a RemoteUploadSink. " +
			"Required params: type is always required; id is required for every type except type=\"logs\".",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"type":               {Type: "string", Description: "Collector name (e.g., \"code\", \"aws\", \"gcp\", \"logs\", \"web\", \"pdf\")."},
				"id":                 {Type: "string", Description: "Opaque identifier parsed by the collector (path, account:region, web source slug, absolute path to a .pdf, etc.). Optional for type=\"logs\"."},
				"force":              {Type: "boolean", Description: "Skip safety check for existing indexed graphs."},
				"promote":            {Type: "boolean", Description: "Code only: promote this branch to the base graph — land in base regardless of the recorded default branch, overwrite the recorded default branch to the collected branch, and delete the now-redundant same-name overlay. No effect for non-code collectors."},
				"params":             {Type: "object", Description: "Registered custom_collector types only: opaque param object forwarded to the external collector binary, validated against its param_schema before exec. Built-in types ignore it."},
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
				"max_path_segments":  {Type: "integer", Description: "Web only: cap on the number of non-empty URL path segments a followed link may have; catches recursive-path traps like /a/b/a/b/.... 0 = off (unbounded), the default."},
				"max_pages_per_host": {Type: "integer", Description: "Web only: cap on pages fetched from any single host within the crawl, independent of max_pages. 0 = off (no per-host cap). When both fire, the crawl stops for a host once either cap hits first."},
				"politeness_ms":      {Type: "integer", Description: "Web only: per-host request delay in milliseconds."},
				"user_agent":         {Type: "string", Description: "Web only: override for the HTTP User-Agent header."},
				"transformer":        {Type: "string", Description: "Web/PDF only: optional transformer name."},
				"recipe":             {Type: "string", Description: "Web/PDF only, transformer=\"recipe\" only: name of a recipe node. The recipe's source_graph_type metadata must match `type`."},
				"dry_run":            {Type: "boolean", Description: "Web/PDF only, transformer=\"recipe\" only: compute emissions but write nothing."},
				"extract":            {Type: "boolean", Description: "Web/PDF only, transformer=\"recipe\" only: EXTRACT MODE — write nothing and return the emitted rows for inspection. Bounded by max_rows and max_bytes, with any truncation disclosed in the response."},
				"recipe_body":        {Type: "string", Description: "Web/PDF only, transformer=\"recipe\" only: an INLINE recipe body to run instead of a saved recipe named by `recipe`. Requires extract=true — a write target comes from a saved recipe node, so to freeze an extraction save the same body as a recipe node and run it by name. Mutually exclusive with `recipe`."},
				"max_rows":           {Type: "integer", Description: "Web/PDF only, transformer=\"recipe\" only, extract mode: cap on rows returned. 0 selects the default (200); the response reports rows matched alongside rows returned, so a truncated extract is never mistaken for a short one."},
				"max_bytes":          {Type: "integer", Description: "Web/PDF only, transformer=\"recipe\" only, extract mode: cap on the rendered response size in bytes. 0 selects the default (65536). Truncation is stated in the response rather than applied silently."},
				"max_download_bytes": {Type: "integer", Description: "Web only: per-(owner,repo,ref) cap on github materialization downloads. 0=default (50 MiB), -1=unlimited, >0=explicit cap (uncompressed bytes)."},
			},
			Required: []string{"type"},
		},
	}
}
