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

  manage({ "operation": "pipeline_status" })                                  — report whether the summary/embed pipeline is RUNNING or PAUSED (+ reason + how to resume)
  manage({ "operation": "pause_pipeline", "reason": "quota investigation" })  — manually latch BOTH axes paused (reason optional, surfaced by pipeline_status)
  manage({ "operation": "resume_pipeline" })                                  — clear the paused latch and re-enable the workers

  The pipeline AUTO-PAUSES after a full error round — every LLM call erroring
  with zero successes across both axes (a quota/auth wall or repeated timeouts).
  It latches PAUSED with NO self-heal: resume_pipeline is the ONLY exit, whether
  the pause was an auto-trip or a manual pause_pipeline. The search staleness
  footer ALSO surfaces the paused state loudly. Pause/resume state is global +
  in-memory and is cleared on restart.

## Garbage collection
  manage({ "operation": "prune", "graph": "knowledge" })                       — hard-delete ALL tombstoned nodes from the knowledge graph
  manage({ "operation": "prune", "graph": "code", "name": "myrepo" })          — GC tombstoned code nodes for one repo
  manage({ "operation": "prune", "graph": "practice", "name": "go", "before": "30d" })  — GC tombstones older than 30 days

  prune works GENERICALLY on any graph type (knowledge, code, cloud, cicd,
  practice, logs, web, pdf, linkage, transformers) — it deletes tombstoned
  nodes and nothing else. There is no graph-type allowlist; just name the graph.
  before accepts a relative window ("24h"/"2d") or an absolute RFC3339 timestamp;
  only tombstones tombstoned before it are pruned. Omit before to prune all.

## Content-hash cache rebuild (free re-derivation)
  manage({ "operation": "rebuild_cache", "graph": "code", "name": "myrepo" })  — drop + re-derive a repo's summary/embed caches from base nodes

  rebuild_cache DROPS a builtin graph's per-graph content-hash caches (summary +
  embed) and RE-DERIVES them from the CURRENT base-graph nodes with ZERO model
  calls. It is a FREE re-derivation, NOT a "clear" (a clear would guarantee a full
  re-pay for LLM/Voyage). The caches let a merged-and-recollected node reuse the
  summary/embedding it earned on a branch overlay instead of re-summarizing/
  re-embedding byte-identical content. Use rebuild_cache for: recovery (lost/
  corrupted cache), manual invalidation (the model/prompt-change lever), or
  backfill/migration (graphs populated before the feature shipped). Builtin-graph
  only: requires graph=code (name=repo) or graph=knowledge (name defaults to
  "default" — BASE layer only, no "@"-overlay names in v1); practice/cloud/cicd are
  not supported.
  ASYNC: the server drops + re-derives on a background goroutine and returns a
  STARTED acknowledgement immediately (a large repo's walk would otherwise exceed
  the edge timeout); confirm completion via the server logs ("rebuild_cache.complete").

## Search-segment rebuild (client-driven backfill, no re-embed)
  manage({ "operation": "rebuild_segments", "graph": "code", "name": "myrepo" })  — rebuild a repo's BM25+HNSW search segments from already-embedded nodes

  rebuild_segments BACKFILLS a code repo's BM25 + HNSW search segments from nodes
  that are ALREADY embedded but have ZERO shipped segments (embedded before the
  segment-ship path existed, or after a SegmentStore prune) — WITHOUT re-embedding.
  The server is engine-free, so the WORK is CLIENT-driven: the client pages the
  already-embedded nodes (with their stored vector + server-composed BM25 fields),
  rebuilds the segments DETERMINISTICALLY (fixed seed + serial-within / concurrent-
  across), and ships them to the server SegmentStore. Requires graph=code (name=repo),
  the builtin knowledge graph (name defaults to "default" — BASE layer only, no
  "@"-overlay names in v1), or a registered custom graph type + name. Single-flight
  per (graph,name). IDEMPOTENT: a deterministic build means a
  re-run over an unchanged node set is a byte-identical, content-hash-diffed NO-OP
  (the first rebuild over an embed-segmented graph ships the deterministic segments
  and prunes the superseded embed ones; every rebuild after is a no-op). Runs
  SYNCHRONOUSLY and reports the scanned/built/pruned counts.

## Orphaned-segment cache prune (one-shot backlog reclaim)
  manage({ "operation": "prune-cache" })                  — PREVIEW: report the orphaned L2 segments per graph+format, delete nothing
  manage({ "operation": "prune-cache", "execute": true }) — DELETE the orphaned L2 segments

  prune-cache is a ONE-TIME (not periodic) reclaim of orphaned client-side L2
  segment blobs: the accumulated superseded .seg files the invalidation-driven
  reclaim never unlinked. It enumerates the segment-bearing graphs (knowledge/default
  + every code repo) and, per graph+format, diffs the on-disk .seg ids against that
  graph's COMPLETE current live set, removing the orphans. It PREVIEWS by default
  (execute=false renders a would-remove report and deletes NOTHING); execute=true
  performs the removal.
  The complete-live-set diff is the safety: each graph is FORCE-FULL-LOADED so an
  unloaded-but-live segment still counts (never false-pruned), the HNSW embed +
  deterministic engines' live sets are UNIONED (they share one cache root), and the
  computed live set is cross-checked against the server's List(0) — any pool whose
  live set is not a subset is SKIPPED untouched and surfaced in the report rather than
  pruned. Honors format=json for the structured report.

## Repo manifest registration
  manage({ "operation": "register_repo", "name": "myrepo", "root": "/abs/path/to/myrepo" })  — record a repo name → checkout dir

  register_repo records a repo name → absolute checkout directory in the
  machine-local manifest (~/.knowledge/repos.json) — the same registry a code
  collect populates. It is PURELY CLIENT-SIDE and MACHINE-LOCAL: the mapping is
  written to this machine's disk and never forwarded to the server, because the
  path is machine-specific and must never leave this host. It is IDEMPOTENT:
  re-registering a name overwrites its recorded path, so a name can be re-pointed
  at a moved or renamed checkout.
  Use it so a cross-repo ast walk (repo:<name>) can resolve a bare repo name to
  its real directory WITHOUT having to collect the repo first. root must be an
  existing absolute directory; name is the repo name to record.

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
  - clear_llm_failures clears markers but does NOT re-discover; the pipeline picks the node up on its next tick (default 250ms)
  - a circuit-break trip (auto-pause) does NOT self-heal — diagnose the cause (quota/auth/timeouts), then run resume_pipeline to re-enable; pause state is lost on restart
  - log graphs are ephemeral; they live under ~/.knowledge/logs/ and are not LLM-summarized/embedded
  - prune requires an explicit graph (it never defaults to knowledge); without before it hard-deletes EVERY tombstoned node in that graph
`
