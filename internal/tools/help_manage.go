// SPDX-License-Identifier: Apache-2.0

package tools

const helpManage = `# manage — Server operations

## Server status
  manage({ "operation": "status" })  — pipeline metrics (summary/embed queued/running/succeeded/failed) per graph

## Code graph management
  manage({ "operation": "list_branches", "name": "myrepo" })
  manage({ "operation": "delete_branch", "name": "myrepo", "branch": "feature-x" })

  Note: There is no operator-driven manage(reindex) command. The pipeline picks
  up changed nodes automatically.
  Re-run the appropriate collector (collect type:code / collect type:cloud /
  collect type:cicd) to refresh source nodes; the per-graph collector inside
  the server discovers gaps (missing summary, missing vector) and the worker
  pool drains them on the next tick.

## LLM pipeline operator commands

  manage({ "operation": "clear_llm_failures" })                               — clear summary_failure_reason + embed_failure_reason markers across every loaded graph
  manage({ "operation": "clear_llm_failures", "graph": "code" })              — scope to one graph type
  manage({ "operation": "clear_llm_failures", "graph": "code", "name": "knowledge" })  — scope to one named graph

## Advanced
  manage({ "operation": "rebuild_hnsw" })                                    — knowledge graph
  manage({ "operation": "rebuild_hnsw", "graph": "code" })                   — code graph
  manage({ "operation": "rebuild_hnsw", "graph": "practice", "name": "go" }) — practice graph
  manage({ "operation": "rebuild_hnsw", "graph": "cloud", "name": "acct" })  — cloud graph

## Garbage collection
  manage({ "operation": "prune", "graph": "knowledge" })                       — hard-delete ALL tombstoned nodes from the knowledge graph
  manage({ "operation": "prune", "graph": "code", "name": "myrepo" })          — GC tombstoned code nodes for one repo
  manage({ "operation": "prune", "graph": "practice", "name": "go", "before": "30d" })  — GC tombstones older than 30 days

  prune works GENERICALLY on any graph type (knowledge, code, cloud, cicd,
  practice, logs, web, pdf, linkage, transformers) — it deletes tombstoned
  nodes and nothing else. There is no graph-type allowlist; just name the graph.
  before accepts a relative window ("24h"/"2d") or an absolute RFC3339 timestamp;
  only tombstones tombstoned before it are pruned. Omit before to prune all.

## Cross-graph linking
  manage({ "operation": "link" })  — run image, Helm, and Dockerfile linkers to create code-to-cloud edges

## Log backend + graph management
  manage({ "operation": "configure_log_backend", "name": "prod-loki",
           "provider": "loki", "url": "https://loki.example.com",
           "auth_type": "bearer", "credential": "$LOKI_TOKEN" })
  manage({ "operation": "list_log_backends" })
  manage({ "operation": "list_logs" })
  manage({ "operation": "discard_logs", "name": "<query_id>" })  — drop one log graph
  manage({ "operation": "discard_logs" })                          — drop all log graphs

  credential accepts a raw value (bearer token, API key, service account JSON, ...)
  OR a $ENV_VAR reference resolved at query time. Raw values are stored encrypted
  at rest and redacted in tool responses. Use whichever is convenient.
  See help("logs") for the full collect → query → search → traverse workflow.

## Gotchas
  - rebuild_hnsw for practice graphs requires graph:"practice" and name (language slug)
  - rebuild_hnsw for cloud/cicd graphs uses name as the account/cluster identifier
  - clear_llm_failures clears markers but does NOT re-discover; the pipeline picks the node up on its next tick (default 250ms)
  - log graphs are ephemeral; they live under ~/.knowledge/logs/ and are not LLM-summarized/embedded
  - prune requires an explicit graph (it never defaults to knowledge); without before it hard-deletes EVERY tombstoned node in that graph
`
