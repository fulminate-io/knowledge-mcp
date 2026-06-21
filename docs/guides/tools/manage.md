# manage

## Overview

`manage` is the operator console for the running server. Where `mutate` writes
graph content and `query` reads it, `manage` controls the machinery around the
graph: it reports pipeline status, pauses and resumes the summarize/embed
workers, garbage-collects tombstoned nodes, rebuilds caches and search segments,
manages code-graph branches, and configures log backends. It is dispatched by the
`operation` field.

There is intentionally no `manage(reindex)` — the pipeline discovers changed
nodes on its own, and re-running the relevant collector (`collect`) is how you
refresh source nodes.

## When & how to use

Reach for `manage` for housekeeping and diagnosis rather than content work:
checking whether summarization is keeping up, freeing space after large
deletions, recovering a corrupted cache, or wiring up a log backend.

`operation` is always required. The other required inputs by operation:

| Operation | Required (besides `operation`) | What it does |
| --- | --- | --- |
| `status` | — | Per-graph stats plus a durable LLM-coverage table (total / summarized / embedded / summary-fail / embed-fail); the runtime pipeline counters shown alongside are process-lifetime, not durable coverage. |
| `pipeline_status` | — | Whether the pipeline is RUNNING or PAUSED, plus the reason. |
| `pause_pipeline` | — | Latch both axes paused (`reason` optional). |
| `resume_pipeline` | — | Clear the paused latch (the only exit from a pause). |
| `clear_llm_failures` | — | Clear failure markers; `graph`/`name` scope it. |
| `prune` | `graph` | Hard-delete tombstoned nodes; `before` windows it. |
| `rebuild_cache` | `graph: "code"` or `graph: "knowledge"`, `name` | Re-derive a graph's summary/embed caches (free, no model calls). For `graph: "knowledge"` the `name` defaults to `"default"` (base layer only — no `@`-overlay names in v1). |
| `rebuild_segments` | `graph` — any embeddable type (`knowledge`, `code`, `cloud`, `cicd`, `practice`) or a registered custom type, `name` | Rebuild BM25+HNSW search segments from embedded nodes. For `graph: "knowledge"` the `name` defaults to `"default"` (base layer only — no `@`-overlay names in v1). |
| `prune-cache` | — | One-shot reclaim of orphaned L2 search segments (superseded `.seg` blobs the invalidation-driven reclaim never unlinked) across `knowledge/default` and every code repo. Previews by default; `execute: true` deletes. |
| `drop_graph` | `graph` (+ the family instance field) | Tear down a whole non-logs graph (store + loaded state) via one DROP_GRAPH mutation; `dry_run: true` previews. Use `discard_logs` for log graphs. |
| `list_branches` / `delete_branch` | `name` (+ `branch` for delete) | Manage code-graph branch overlays. |
| `link` | — | Run the image/Helm/Dockerfile linkers to create code-to-cloud edges. |
| `configure_log_backend` | `name`, `provider`, `url`, `auth_type` | Register a log backend (`credential` as needed; optional for `auth_type: "kubeconfig"`). |
| `list_log_backends` / `list_logs` | — | List configured backends / collected log graphs. |
| `discard_logs` | — | Drop one log graph (`name`) or all (omit `name`). |
| `pprof_start` / `pprof_stop` | — | Bracket a CPU profile of the stdio client; `pprof_stop` returns a fetch URL. |
| `set_metadata_overrides` | `graph`, `name` | Pin metadata keys to the scalar map (`force_scalar`) or value-node edges (`force_edge`); at least one non-empty. |
| `promote_metadata` | `graph`, `name` | Refresh cardinality stats and flip each key's representation per the hysteresis bands (`dry_run` reports without writing). |

Examples:

```jsonc
// Is summarization keeping up?
manage({ "operation": "status" })

// Reclaim space after deleting nodes
manage({ "operation": "prune", "graph": "knowledge" })

// Resume after an auto-pause (quota/auth wall)
manage({ "operation": "resume_pipeline" })
```

A full error round auto-pauses the pipeline and it does not self-heal —
`resume_pipeline` is the only way back. For the complete operation reference, run
`help("manage")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `auth_type` | string |  |  | Authentication mechanism for configure_log_backend (bearer, basic, aws_profile, api_key, service_account, kubeconfig, ...) |
| `before` | string |  |  | For prune: cutoff for which tombstoned nodes to hard-delete. A relative window ('24h', '2d') or an absolute RFC3339 timestamp; only tombstones tombstoned before it are pruned. Omit to prune ALL tombstoned nodes. |
| `branch` | string |  |  | Branch name (for delete_branch, list_branches) |
| `credential` | string |  |  | Credential value for configure_log_backend — stored encrypted at rest. Accepts the raw value (e.g., a bearer token, API key, or service account JSON) or a $ENV_VAR reference resolved at query time. Optional when auth_type=kubeconfig. |
| `default_branch` | string |  |  | Override default branch detection for reindex — treats this branch name as the default so the current branch gets a full reindex instead of branch overlay |
| `dry_run` | boolean |  |  | For promote_metadata: when true, run the decision pass and report intended actions without mutating the graph. For drop_graph: when true, render a 'would drop' preview and issue ZERO mutations. Default false (executes). |
| `execute` | boolean |  |  | For prune-cache: when true, DELETE the orphaned segments; default false renders a would-remove preview only. |
| `force` | boolean |  |  | For promote_metadata: when true, bypass the hysteresis bands and use the simple distinct<1000 rule. Operator one-shot path only. |
| `force_edge` | array of string |  |  | Metadata keys pinned to value-node edges for set_metadata_overrides. Replaces the existing list. |
| `force_edge[]` | string |  |  |  |
| `force_scalar` | array of string |  |  | Metadata keys pinned to the scalar map for set_metadata_overrides. Replaces the existing list. |
| `force_scalar[]` | string |  |  |  |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured) |
| `graph` | string |  |  | Target graph type for clear_llm_failures (knowledge, code, practice, cloud, cicd) |
| `kube_context` | string |  |  | Kubeconfig context name from ~/.kube/config. Required when provider=k8s and auth_type=kubeconfig. Auth is resolved via client-go using the operator's environment (gcloud/aws-iam-authenticator/service-account tokens). |
| `name` | string |  |  | Repository name (or log_backend name for configure_log_backend; or query_id for discard_logs) |
| `operation` | string | yes | status, pprof_start, pprof_stop, delete_branch, list_branches, link, configure_log_backend, list_log_backends, list_logs, discard_logs, set_metadata_overrides, promote_metadata, clear_llm_failures, pause_pipeline, resume_pipeline, pipeline_status, prune, prune-cache, rebuild_cache, rebuild_segments, drop_graph | Operation to perform |
| `precise_calls` | boolean |  |  | Enable precise Go call graph via RTA (slower but more accurate CALLS edges) |
| `provider` | string |  |  | Log backend provider for configure_log_backend (cloudwatch, loki, elasticsearch, stackdriver, k8s, ...) |
| `reason` | string |  |  | For pause_pipeline: optional operator reason surfaced by pipeline_status. Defaults to a generic 'manually paused by operator' string when omitted. |
| `root` | string |  |  | Root directory path for reindex |
| `url` | string |  |  | Log backend base URL for configure_log_backend |
<!-- END GENERATED: params -->
