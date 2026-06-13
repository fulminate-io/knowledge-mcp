# query

## Overview

`query` is the generic read primitive. It does not have sub-commands of its own —
it dispatches on which fields you set: pass `id` for a direct node lookup, `text`
to search, `type` to browse every node of a kind, or `mode` to run one of the
special operations (graph stats, deep node inspection, plan-tree walks, topology
analyzers, reflection over the thought graph, and more).

Every form respects the `graph` selector, so the same tool reads the knowledge,
code, cloud, practice, cicd, linkage, and logs graphs — you pick the graph family
with `graph` and name the specific instance with its typed field (`repo`,
`account`, `language`, or `name`). `query` is read-only; writes go through
`mutate`.

## When & how to use

Use `query` when you already know what you want to look at — a node by id, every
decision, the statistics of a graph, the ancestry of one node — rather than a
fuzzy concept search (that is what `search` is for, with semantic ranking).

`query` has no globally required field; instead each dispatch form has its own
required input:

| Form (set this field) | Also required | What it does |
| --- | --- | --- |
| `id` | — | Direct node lookup. Add `include_edges` for relationships. |
| `text` | — | Text search across the selected graph. |
| `type` | — | Browse every node of a type (decision, rule, plan, …). |
| `mode: "examine"` | `id` | Deep inspection: ancestry, edges, version history. |
| `mode: "stats"` | — | Node-type and edge-type breakdown for the graph (the discovery primitive). |
| `mode: "plan_tree"` | `id` | Walk a project/ticket/plan/phase/step hierarchy. |
| `mode: "topology"` | `algorithm` + graph instance | Run a topology analyzer (e.g. `pagerank`, `scc`). |
| `mode: "pivot"` (logs) | `name`, `rows`, `cols` | Row×column matrix of log counts. |
| reflect modes | — | `personality`, `tensions`, `blind_spots`, `summary`, etc. over the thought graph. |

Some worked examples:

```jsonc
// Look up a node and its edges
query({ "id": "abc123", "include_edges": true })

// Discover a graph's vocabulary before traversing it
query({ "mode": "stats", "graph": "code", "repo": "knowledge" })

// Walk a plan hierarchy
query({ "mode": "plan_tree", "id": "plan_id" })

// Browse every decision
query({ "type": "decision" })
```

`graph: "all"` searches the knowledge and code graphs together; practice queries
require `language`; cloud queries require `account` (omit it to list the available
cloud graphs). For the full mode catalog, run `help("query")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `account` | string |  |  | Selects which inventoried external-provider account/org's resources to query within your own graph — an AWS/GCP account for graph=cloud, or a CI provider org (e.g. GitHub/GitLab) for graph=cicd. Required for graph=cloud/cicd; omit to list your available graphs. |
| `action` | string |  |  | Action for simulate mode (remove_charge, invalidate_thought, add_charge) |
| `algorithm` | string |  |  | Topology analyzer name for mode=topology (e.g. 'pagerank', 'scc'). Use topology.All for the registered list. |
| `cluster` | string |  |  | Cluster filter for reflect personality mode |
| `cluster_a` | string |  |  | First cluster for reflect evolution mode |
| `cluster_b` | string |  |  | Second cluster for reflect evolution mode |
| `cols` | string |  |  | Column label key for graph='logs' mode='pivot' (e.g. 'reason'). Defaults sniffed from the graph when omitted. |
| `connected_to` | string |  |  | Filter thoughts connected to this node ID |
| `consistency_max` | number |  |  | Maximum thought consistency (low = contested) |
| `edge_type` | array of string |  |  | Filter correlations/explain output to edges of these types (e.g. ["relates-to"], ["CORRELATES_WITH"]). Empty = any edge type. |
| `edge_type[]` | string |  |  |  |
| `extra` | object |  |  | Per-analyzer config knobs as string-valued keys (e.g. {"damping": "0.85", "tolerance": "1e-6"} for pagerank). Keys depend on the analyzer; values are parsed by the analyzer (numbers passed as strings). |
| `fields` | array of string |  |  | Field projection (format=json only): list of fields to include per result, dramatically shrinking response size for high-volume queries. Top-level: id, name, type, status, description. Per-metadata-key: 'metadata.<key>' (e.g. 'metadata.dsl_pattern'). Bare 'metadata' includes the whole metadata map. Empty/absent = full hydration. Unknown field names silently dropped. |
| `fields[]` | string |  |  |  |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured). Recognized by several modes. |
| `graph` | string |  |  | Which graph to search: knowledge, code, cloud, cicd, practice, linkage, logs, or all (default: knowledge). Practice graphs are per-language (use 'language' param); log graphs are per-query (use 'name' for the query_id). |
| `group_by_file` | boolean |  |  | Group code search results by file |
| `id` | string |  |  | Direct node lookup by ID. If graph=code, runs analyze_node instead. If graph=logs, returns template detail with decompressed example entries. |
| `ids` | array of string |  |  | Bulk hydrate-by-id: pass a list of node IDs and receive {label, nodes:[]} in one call (JSON output). Mutually exclusive with id. Used by client-side reflective code where K query(id:...) round trips would otherwise be needed. |
| `ids[]` | string |  |  |  |
| `include_cross_links` | boolean |  |  | Augment node query with cross-graph links from the linkage graph |
| `include_edges` | boolean |  |  | Include edges in node results |
| `include_source` | boolean |  |  | Include source code in results (code graph) |
| `include_tests` | boolean |  |  | Include test code (test/benchmark/example/fuzz/setup/teardown/fixture/mock/helper) in results. Default true. Code graph only — silently ignored on other graphs. Note: until per-language predicate-population tickets land, all code nodes have is_test=false so this filter is currently a no-op. |
| `include_tombstones` | boolean |  |  | Include tombstoned (deleted) nodes in results. Default false. |
| `language` | string |  |  | Language code (e.g. 'go', 'python', 'typescript'). Two uses: (1) practice graph selector — omit to list all practice graphs; (2) topology analyzer filter — code-graph analyzers like god_object scope to a single language. Empty means no filter for topology, all-graphs for practice. |
| `limit` | number |  |  | Max results (default: 10) |
| `magnitude_min` | number |  |  | Minimum thought magnitude |
| `meta` | object |  |  | Metadata equality filter, applied to type-browse and text-search dispatch. Map of metadata key to required value; a value of "*" matches any non-empty value (i.e., "the key is set"). Example: {"dsl_pattern": "*"} returns every node carrying a dsl_pattern. Multiple keys are AND'd. |
| `mode` | string |  | hybrid, text, stats, examine, file_symbols, modules, personality, influence, tensions, blind_spots, evolution, summary, simulate, timeline, charges, clusters, recent, topology, pivot, correlations, explain, resolver, lineage, evidence, plan_tree, metadata_stats | Search mode or special operation |
| `name` | string |  |  | Graph name selector (e.g. query_id for graph='logs'). |
| `offset` | number |  |  | Skip first N results for pagination (default: 0). Use with limit to page through results. |
| `overlay` | string |  |  | Optional: target a specific knowledge session overlay name (e.g. session-073797a4-...). When set, the query reads from base + that single overlay only, ignoring the live overlay list. Useful for diagnosing cross-session visibility issues. |
| `path_prefix` | string |  |  | File path filter (code graph). Also used as file_path for file_symbols mode. |
| `path_prefixes` | array of string |  |  | List form of path_prefix for file_symbols mode — query symbols across multiple files in one call. Combined with path_prefix when both supplied. |
| `path_prefixes[]` | string |  |  |  |
| `polarity` | string |  |  | Polarity for simulate add_charge (positive or negative) |
| `queries` | array of string |  |  | Batch search queries (code graph only) |
| `queries[]` | string |  |  |  |
| `query_vector` | string |  |  | Optional base64-encoded binary embedding for the text query (32 bytes / 256-bit decoded). Client-supplied: set by the client-side LLM pipeline's InterceptQuery on text-search modes (hybrid, recent/temporal). The server never embeds — when query_vector is unset the text-search modes run BM25-only (no server-side embedding fallback). Decoded length mismatches return a structured validation error and no query is performed. |
| `repo` | string |  |  | Code repository name (default: active repo). Use 'all' for all repos. |
| `repos` | array of string |  |  | Search specific repos (alternative to repo='all') |
| `repos[]` | string |  |  |  |
| `resource_type` | string |  |  | Cloud resource type filter prefix |
| `rows` | string |  |  | Row label key for graph='logs' mode='pivot' (e.g. 'reporting_instance'). Defaults sniffed from the graph when omitted. |
| `session` | string |  |  | Filter thoughts by session name |
| `since` | string |  |  | Time filter for date-ranged queries (RFC3339 or relative like '24h', '7d') |
| `sort` | string |  | influence, composite | Display ordering for the EVIDENCED section of mode=influence. Selection is evidence-aware: charged thoughts are ranked by influence×(1+chargeWeight) into the evidenced top-N, while zero-charge structural hubs are returned in a separate labeled backfill section. 'influence' (default) keeps the influence×(1+chargeWeight) selection order; 'composite' reorders the already-selected evidenced set by influence×magnitude FOR DISPLAY — a within-set reorder that does NOT change which thoughts are selected and does not touch the backfill section. |
| `status` | string |  |  | Status filter for thought recall |
| `target` | string |  |  | Target node ID for simulate mode |
| `test_kinds` | array of string |  |  | Filter set for test classification kinds: any of test, benchmark, example, fuzz, setup, teardown, fixture, mock, helper. Empty/absent means no filter. Code graph only. Note: until per-language predicate-population tickets land, all code nodes have test_kind="" so this filter is currently a no-op. |
| `test_kinds[]` | string |  |  |  |
| `text` | string |  |  | Search query text |
| `time_field` | string |  |  | Node field or metadata key to use as timestamp for timeline mode (e.g. CreatedAt, UpdatedAt, or a metadata key). Defaults to FirstSeen for logs graphs. |
| `top_k` | number |  |  | Cap on findings returned by topology analyzers (0 = no cap) |
| `type` | string |  |  | Node type filter (e.g. decision, rule, plan, research, document) |
| `types` | array of string |  |  | Filter mode=recent results to these node types (e.g. ["project", "ticket", "plan", "step"]). Knowledge graph, mode=recent only — ignored for other modes. |
| `types[]` | string |  |  |  |
| `valence_max` | number |  |  | Maximum thought valence (-1.0 to 1.0) |
| `valence_min` | number |  |  | Minimum thought valence (-1.0 to 1.0) |
| `weight` | number |  |  | Weight for simulate add_charge (1-10) |
<!-- END GENERATED: params -->
