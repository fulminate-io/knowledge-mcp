// SPDX-License-Identifier: Apache-2.0

// Package tools — help topics for the two read primitives, query and traverse.
// Split out of help_content.go so neither file creeps over the 500-line hard
// cap; the constants are unchanged homes, not new content.
package tools

const helpQuery = `# query — Unified search and node lookup

Design: query is a generic primitive. It dispatches on params: 'id' → direct lookup, 'text' → search, 'type' → browse, 'mode' → special operations. All modes respect the 'graph' selector (knowledge|code|cloud|practice|cicd|checks|linkage|logs). Composite shortcuts ('mode: "plan_tree" | "lineage" | "evidence"') are exceptions justified by frequent use.

## Modes

### Text search (default)
  query({ "text": "authentication", "limit": 10 })
  query({ "text": "cache invalidation", "type": "decision" })

  mode:"hybrid" (the default) fuses the BM25 and vector arms; mode:"text" runs
  BM25 only.

### Direct node lookup
  query({ "id": "abc123" })
  query({ "id": "abc123", "include_edges": true })

### Browse by type
  query({ "type": "decision" })
  query({ "type": "rule", "limit": 50 })

### Special modes
  query({ "mode": "stats" })                            — node-type + edge-type breakdowns for the current graph (the canonical discoverability primitive; works for any graph via 'graph:' selector)
  query({ "mode": "examine", "id": "x" })               — deep node inspection
  query({ "mode": "file_symbols", "path_prefix": "..." }) — code file symbols
  query({ "mode": "modules" })                          — code module listing
  query({ "mode": "timeline" })                         — thought timeline
  query({ "mode": "charges" })                          — thought charge summary
  query({ "mode": "clusters" })                         — thought cluster summary (thought-only; it takes no type/all_types filter)
  query({ "mode": "metadata_stats", "graph": "cloud", "name": "aws-prod" })  — per-graph cardinality histogram for every metadata key

  For an ALL-NODE-TYPE cluster summary the spelling is the thoughts tool, not a
  query filter:
  thoughts({ "operation": "recall", "mode": "clusters", "all_types": true })

### Reflect modes
  query({ "mode": "personality" })   — reasoning personality profile
  query({ "mode": "influence" })     — most influential thoughts
  query({ "mode": "tensions" })      — conflicting thoughts
  query({ "mode": "blind_spots" })   — per-thought epistemic-risk facets (confident-but-untested, foundational-but-unexamined, fragile single-point, stale confidence, belief reversal); served O(1) from the reflection-loop cache
  query({ "mode": "summary" })       — concise thought summary
  query({ "mode": "evolution", "cluster_a": "...", "cluster_b": "..." })  — scalar evolution between two clusters (both cluster_a and cluster_b required)
  query({ "mode": "simulate", "action": "remove_charge|invalidate_thought|add_charge", "target": "node_id" })  — counterfactual over the charge graph

  Each reflect mode consumes a NARROW param set and REJECTS the rest by name
  rather than ignoring them — e.g. mode:"clusters" reads only graph/mode/format,
  so any identity, paging or code param on that call is refused with a message
  naming the param and the handler that does not route it.

### Pre-embedded query vector (client-side LLM pipeline)
  query({ "text": "authentication", "query_vector": "<base64>" })

  Optional base64-encoded binary embedding (32 bytes / 256-bit decoded). When
  set, the server skips its local embedder and uses the supplied vector for
  hybrid search. Wired by the client-side LLM pipeline's InterceptQuery so the
  server stays unencumbered by Voyage API keys post-Phase-5. Decoded length
  mismatches return the same structured validation error as
  mutate(update_batch, items[].binary_vector=...).

### Recency-boosted search
  query({ "mode": "recent", "text": "authentication" })
  query({ "mode": "recent", "text": "cache eviction", "limit": 20 })
  query({ "mode": "recent" })                                  — most-recently-updated nodes, all types
  query({ "mode": "recent", "types": ["project", "ticket", "plan", "phase", "step", "question"] })

  With a text query: hybrid BM25+vector search with exponential temporal decay
  (half-life 30 days) — recently-updated nodes rank higher than semantically-equal
  but stale ones. Useful when you want the freshest relevant nodes — e.g., recent
  decisions, active plan steps, or newly created findings.

  Omit text for a pure recency browse (no search): the most-recently-updated
  nodes by UpdatedAt. Add types to scope it — e.g. a lightweight view of the
  most-recently-touched work items.

## Practice graph queries
  query({ "graph": "practice" })                                    — list all practice graphs
  query({ "graph": "practice", "language": "go" })                  — browse a language's practice graph
  query({ "graph": "practice", "language": "go", "text": "errors" }) — search within a practice graph
  query({ "id": "node_id", "graph": "practice", "language": "go" }) — look up a specific practice node

### Cloud graph queries
  query({ "graph": "cloud" })                                          — list all cloud graphs
  query({ "graph": "cloud", "account": "aws-prod" })                   — browse cloud resources
  query({ "graph": "cloud", "account": "aws-prod", "text": "web" })    — search within a cloud graph
  query({ "graph": "cloud", "account": "aws-prod", "resource_type": "ec2" }) — filter by resource type prefix
  query({ "id": "arn:aws:...", "graph": "cloud" })                     — look up a specific cloud resource (scans all accounts)
  query({ "id": "arn:aws:...", "graph": "cloud", "account": "aws-prod" }) — look up in a specific account

### Linkage graph queries
  query({ "graph": "linkage" })                                           — list linkage graph info
  query({ "graph": "linkage", "mode": "stats" })                         — linkage graph statistics with proxy breakdown
  query({ "graph": "linkage", "text": "deploy" })                        — search within the linkage graph
  query({ "id": "proxy:cloud:aws-prod:arn:...", "graph": "linkage" })     — look up a specific linkage proxy

### Log graph queries (ephemeral, per-query_id)
  query({ "graph": "logs", "name": "<query_id>" })                          — label overview ranked by error count
  query({ "graph": "logs", "name": "<query_id>", "text": "app=api severity>=WARN" }) — drill-down (AND-only label filters + severity range)
  query({ "graph": "logs", "name": "<query_id>", "id": "<template_id>" })   — template detail with decompressed example entries
  query({ "graph": "logs", "name": "<query_id>", "mode": "pivot",
          "rows": "reporting_instance", "cols": "reason" })                  — row×col matrix of log counts
  query({ "graph": "logs", "name": "<query_id>", "mode": "correlations" })   — every CORRELATES_WITH edge, sorted by score desc
  query({ "graph": "logs", "name": "<query_id>", "mode": "timeline" })        — templates ordered by FirstSeen (T+offset, alias, count, span)
  query({ "graph": "logs", "name": "<query_id>", "mode": "timeline",
          "extra": { "bucket": "10s" } })                                     — histogram rollup into fixed-width windows
  query({ "graph": "logs", "name": "<query_id>", "mode": "explain",
          "id": "<template_alias_or_id>" })                                   — per-correlation breakdown for one template
  query({ "graph": "logs", "name": "<query_id>", "mode": "explain",
          "extra": { "a": "<tplA>", "b": "<tplB>" } })                         — explain a specific correlation pair
  query({ "graph": "logs", "name": "<query_id>", "mode": "resolver" })         — per-stream cloud-resolution trace (resolved + unresolved)

  See help("logs") for the drill-down grammar, pivot defaults, alias
  conventions, and the full collect → query → traverse workflow.

### Topology mode (analyzer dispatch)
  query({ "mode": "topology", "graph": "code", "algorithm": "pagerank", "repo": "myrepo", "top_k": 20 })
  query({ "mode": "topology", "graph": "cloud", "algorithm": "scc", "account": "aws-prod" })

  Both 'graph' and 'algorithm' are REQUIRED — there is no default sweep, no
  paramless dump, and no linkage fallback. topology mode dispatches to a
  registered analyzer in the topology package: provide 'algorithm' (omit it
  to get the registered analyzer list in the error), plus the standard graph
  instance params ('repo' for code, 'account' for cloud, none for knowledge).
  'repo' is REQUIRED for the code graph — topology runs over a NAMED code graph
  and never infers one from cwd. Optional 'top_k' caps ranked findings;
  'path_prefix' is honored only by the corpus-scan analyzer and is REFUSED for
  any other algorithm rather than ignored; for cloud, 'resource_type' restricts
  to nodes whose 'resource_type' meta starts with the prefix. Output is
  JSON-encoded foundation.Finding objects. See help("topology").

### Cross-graph link augmentation
  query({ "id": "any_node_id", "include_cross_links": true })
  Augments any node lookup with cross-graph links from the linkage graph.

## Key parameters
  text, id, type, mode, graph (knowledge|code|cloud|cicd|practice|checks|linkage|logs), limit, offset,
  include_edges, include_cross_links, since, session, status, valence_min/max, magnitude_min,
  consistency_max, connected_to, language (for practice graph),
  account (for cloud graph), resource_type (for cloud graph), name (query_id for log graph)

## Gotchas
  - "examine" mode requires id
  - Thought filters (valence, session, magnitude) only apply in knowledge graph
  - There is NO cross-graph selector. graph:"all" is not a graph type and is
    REFUSED, like any other unknown value, with an error naming it and the
    accepted vocabulary. Name the graph you mean — for code, that also means
    naming 'repo' (or repo:"all", the cross-REPO fan-out within the code graph;
    language:"all" is its practice-graph counterpart).
  - Practice graph queries require language param (except when listing all practice graphs)
  - Cloud graph queries require account param for search/browse (omit to list all cloud graphs)
  - include_cross_links only works with id-based queries (not browse or search)
`

const helpTraverse = `# traverse — Edge-first graph traversal

## Primitive
  traverse(start, direction, edge_types[], graph, depth, limit, include_edge_metadata)

  direction: "out" (default), "in", or "both" (union deduped by node ID)
  edge_types: filter to specific edge types (omit for any edge)
  graph: target graph — "" or "knowledge" (default), "code", "cloud", "cicd",
         "practice", "checks", "logs", "linkage"

## Discovery
  Don't memorize edge types — discover them:
  query({ "mode": "stats", "graph": "code" })   — shows all node types and edge types

## Examples
  # Find callers of a function (code graph, incoming "calls" edges)
  traverse({ "start": "pkg/server.go:Handle", "graph": "code", "repo": "myrepo",
             "edge_types": ["calls"], "direction": "in" })

  # Find callees (outgoing "calls" edges)
  traverse({ "start": "pkg/server.go:Handle", "graph": "code",
             "edge_types": ["calls"], "direction": "out" })

  # Walk cloud resource relationships
  traverse({ "start": "arn:aws:ec2:...", "graph": "cloud", "account": "prod",
             "direction": "both", "include_edge_metadata": true })

  # Log graph: template → chunks
  traverse({ "graph": "logs", "name": "<query_id>",
             "start": "<template_id>", "direction": "out" })

  # Log graph: stream → labels + chunks + cloud proxies
  traverse({ "graph": "logs", "name": "<query_id>",
             "start": "<stream_id>", "direction": "both" })

  # Graph-wide edge enumeration: omit start entirely
  traverse({ "graph": "knowledge" })   — node + edge totals for the whole graph

## Composite shortcuts (query modes, not traverse)
  query({ "mode": "plan_tree", "id": "plan_id" })    — walk plan → phases → steps → criteria
  query({ "mode": "lineage", "id": "node_id" })      — trace provenance chain
  query({ "mode": "evidence", "id": "decision_id" }) — follow evidence for a decision

## Parameters
  start, direction, edge_types, depth, limit, graph, name, language,
  account, repo, branch, include_edge_metadata, include_tombstones, format

  start names the node to walk from. It is NOT unconditionally required: an
  EMPTY start is the graph-wide enumeration above, which reports the target
  graph's node and edge totals instead of walking from a node.

## Gotchas
  - depth:1 gives only immediate neighbors (default)
  - Log graph traversal starts only at NodeLogTemplate or NodeLogStream
  - Cross-graph proxies auto-resolve — no special direction needed
`
