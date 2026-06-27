# search

## Overview

`search` is unified search across every graph family — code, knowledge, practice,
cloud, cicd, linkage, and logs — selected with the `graph` param. It combines BM25
keyword matching with semantic vector search (the `hybrid` default), so it finds
results by meaning as well as by exact term. Pass a single `query`, or a `queries`
array to cover several terms in one call.

## When & how to use

Reach for `search` when you are looking for something by concept and don't already
have its id — "where is the auth middleware", "what handles cache invalidation",
"is there code that does X". It is the right first tool for code exploration,
ahead of grep, because it understands the call graph and ranks semantically.

`query` (or `queries`) is the core input. Use `mode` to switch between `hybrid`
(default), `text` (BM25 only), and `vector` (semantic only). For the code graph,
`repo: "all"` searches across every indexed repo.

```jsonc
// Batch several related terms in one call
search({ "queries": ["auth middleware", "auth handler", "token validation"], "limit": 8 })

// Search the knowledge graph for past decisions
search({ "query": "cache eviction policy", "graph": "knowledge" })
```

Code-graph results carry a staleness indicator — if the index is far behind
HEAD, re-run `collect` before trusting them. Practice searches need `language`;
cloud searches need `account`. For the full parameter reference, run
`help("search")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `account` | string |  |  | Selects which inventoried external-provider account/org's resources to search within your own graph — an AWS/GCP account for graph=cloud, or a CI provider org (e.g. GitHub/GitLab) for graph=cicd. Required for graph=cloud/cicd; omit to list your available graphs. |
| `branch` | string |  |  | Branch name for overlay search. Code graph only. |
| `commits_behind` | number |  |  | Commits between sync_commit and HEAD (auto-populated when staleness:true). Code graph only. |
| `current_head` | string |  |  | Current git HEAD SHA (auto-populated by client intercept when staleness:true). Code graph only. |
| `explain` | boolean |  |  | Append per-result match-field annotations (which fields contain the literal query tokens) and a search-mode disclosure footer. Off by default — adds context without changing ranking. |
| `fields` | array of string |  |  | Field projection (format=json only): list of fields to include per result, dramatically shrinking response size for high-volume queries. Top-level: id, name, type, score, description, source, status. Per-metadata-key: 'metadata.<key>' (e.g. 'metadata.dsl_pattern'). Bare 'metadata' includes the whole metadata map. Empty/absent = full hydration (current default). Unknown field names silently dropped. |
| `fields[]` | string |  |  |  |
| `format` | string |  |  | Output format: 'text' (default, markdown) or 'json' (structured). JSON returns {results:[{id,name,type,score,...}]} instead of markdown text. |
| `graph` | string |  |  | Which graph to search: code (default), knowledge, practice, cloud, cicd, linkage, or logs. |
| `group_by_file` | boolean |  |  | Group results by file (default: false). Code graph only. |
| `include_comments` | boolean |  |  | Include comment nodes in code search results (default: false). Comments are excluded by default to reduce noise. |
| `include_source` | boolean |  |  | Include full source code (default: true). Code graph only. |
| `include_tests` | boolean |  |  | Include test code (test/benchmark/example/fuzz/setup/teardown/fixture/mock/helper) in results. Default true. Code graph only — silently ignored on other graphs (mirrors path_prefix). Set false to exclude all test code from impl-style queries. Note: until per-language predicate-population tickets land, all code nodes have is_test=false so this filter is currently a no-op. |
| `include_tombstones` | boolean |  |  | Include tombstoned (deleted) nodes in results. Default false. |
| `limit` | number |  |  | Max results per query (default: 10, max: 50). |
| `mode` | string |  |  | Search mode: 'hybrid', 'text', 'vector' (code); 'recent'/'temporal' (knowledge recency boost); 'similar' (knowledge graph). mode:'similar' takes a node_id and returns that node's nearest corpus neighbors by searching the node's OWN STORED vector (its embedding already on disk — NOT a fresh embedding of any query text), with the node itself EXCLUDED from results. Results are ranked by the client engine's reciprocal-rank fusion over the stored-vector (HNSW) arm — with no query text the order is pure stored-vector proximity — NOT a raw cosine similarity score. |
| `name` | string |  |  | Graph identifier. Required when graph=logs (the per-query log graph queryID). Ignored for other graph types. |
| `node_id` | string |  |  | The node whose nearest stored-vector neighbors to return when mode:'similar' is set (knowledge graph). The named node is resolved to its on-disk embedding and excluded from its own results. |
| `overlay` | string |  |  | Optional: target a specific knowledge session overlay name. When set, the search reads from base + that single overlay only, ignoring the live overlay list. Useful for diagnosing cross-session visibility issues. |
| `path_prefix` | string |  |  | Filter to files under this path. Code graph only. |
| `queries` | array of string |  |  | Batch search: array of query strings. Results deduplicated and merged. |
| `queries[]` | string |  |  |  |
| `query` | string |  |  | Single search query (keywords, function names, concepts). |
| `query_vector` | string |  |  | Optional base64-encoded binary embedding for the query text (32 bytes / 256-bit decoded). Client-supplied: set by the client-side LLM pipeline's InterceptSearch so the server can serve hybrid-search results without holding a Voyage key. The server never embeds — when query_vector is unset the search runs BM25-only (no server-side embedding fallback). Decoded length mismatches return a structured validation error and no search is performed. |
| `repo` | string |  |  | Repository (code graph) name — REQUIRED for graph=code; it is never inferred from cwd. search accepts 'all' to span every code repo. Not used by the knowledge graph. |
| `repos` | array of string |  |  | Search specific repos (e.g. ["agent","knowledge"]). Alternative to repo='all'. Code graph only. |
| `repos[]` | string |  |  |  |
| `rerank` | boolean |  |  | Apply post-fusion rerank when configured. Default true. Set false for cheap exact-symbol-name lookups where fan-in scoring suffices. |
| `resource_type` | string |  |  | Cloud resource type filter prefix (e.g. 'ec2', 'ec2:instance'). Cloud graph only. |
| `staleness` | boolean |  |  | Include index staleness info (default: false). Code graph only. |
| `test_kinds` | array of string |  |  | Filter set for test classification kinds: any of test, benchmark, example, fuzz, setup, teardown, fixture, mock, helper. Empty/absent means no filter (combined with include_tests=true: all results pass; with include_tests=false: tests of any kind are dropped). Code graph only. Note: until per-language predicate-population tickets land, all code nodes have test_kind="" so this filter is currently a no-op. |
| `test_kinds[]` | string |  |  |  |
| `types` | array of string |  |  | Filter results by node type (e.g. ["thought","decision","finding"]). Knowledge graph only. |
| `types[]` | string |  |  |  |
| `uncommitted_count` | number |  |  | Count of uncommitted files (auto-populated when staleness:true). Code graph only. |
<!-- END GENERATED: params -->
