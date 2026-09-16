# manage

## Overview

`manage` is the operator console for the running server. Where `mutate` writes
graph content and `query` reads it, `manage` controls the machinery around the
graph: it reports pipeline status, pauses and resumes the summarize/embed
workers, garbage-collects tombstoned nodes, rebuilds caches and search segments,
and manages code-graph branches. It is dispatched by the `operation` field.

There is intentionally no `manage(reindex)` — the pipeline discovers changed
nodes on its own, and re-running the relevant collector (`collect`) is how you
refresh source nodes.

## When & how to use

Reach for `manage` for housekeeping and diagnosis rather than content work:
checking whether summarization is keeping up, freeing space after large
deletions, or recovering a corrupted cache.

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
| `rebuild_segments` | `graph` — any embeddable type (`knowledge`, `code`, `practice`) or a registered custom type, `name` | Rebuild BM25+HNSW search segments from embedded nodes. For `graph: "knowledge"` the `name` defaults to `"default"` (base layer only — no `@`-overlay names in v1). |
| `prune-cache` | — | One-shot reclaim of orphaned L2 search segments (superseded `.seg` blobs the invalidation-driven reclaim never unlinked) across `knowledge/default` and every code repo. Previews by default; `execute: true` deletes. |
| `drop_graph` | `graph` (+ the family instance field) | Tear down a whole graph (store + loaded state) via one DROP_GRAPH mutation; `dry_run: true` previews. |
| `list_branches` / `delete_branch` | `name` (+ `branch` for delete) | Manage code-graph branch overlays. |
| `link` | — | Run the Dockerfile linker to create code-to-code edges. |
| `pprof_start` / `pprof_stop` | — | Bracket a CPU profile of the knowledge client (where collectors run); `pprof_stop` returns a fetch URL. |
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

## Quarantined segments, and how to get those documents back

A search segment whose stored bytes a reader refuses is QUARANTINED: the file is
moved into a `quarantine/` subdirectory beside the graph's segments and its index
entry is dropped, so the daemon stops serving bytes it cannot parse instead of
failing every query that touches them. Nothing re-fetches or re-indexes a
quarantined segment — this cache IS the segment store — so the documents that
segment held are unreachable by any search of that graph until its segments are
rebuilt.

`manage(status)` reports this. Each coverage row carries a per-format quarantine
count in its segment cell, and the `format: "json"` rows carry it as
`quarantined_segments` (per format) alongside `quarantined_impact`, the sentence
saying what the count costs. A graph that has lost nothing shows no `QUARANTINED`
term in its text cell; its JSON row still carries both keys — `quarantined_segments`
as the measured zeros per constructed engine, or `null` when nothing was measured,
and `quarantined_impact` as the empty string.

`manage(rebuild_segments)` is the cure, and it cures segments that are ALREADY
corrupt in the field: it rebuilds the graph's search segments from its embedded
nodes, so the documents a quarantined segment held become searchable again.

**Pass `reset: true`.** A default `rebuild_segments` scans only what changed since
the last rebuild that landed, and a quarantine changes neither the node set nor that
watermark — it moves a file aside. On a corpus whose nodes have not changed since the
last rebuild, the bare command therefore scans nothing, builds nothing, and reports a
clean run with the documents still unreachable:

```jsonc
manage({ "operation": "rebuild_segments", "graph": "code", "name": "knowledge", "reset": true })
```

When that rebuild's layer swap lands, the quarantine count for that graph and format
returns to zero and the withdrawn file is moved into a `quarantine/cured-<stamp>/`
subdirectory — kept as evidence, and never counted again, including across restarts.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `account` | string |  |  | For account_for_session / account_use: the id or slug of the Fulminate account to route to. There is NO session parameter beside it — account_for_session binds the session THIS call arrived on, and takes no other. |
| `before` | string |  |  | For prune: cutoff for which tombstoned nodes to hard-delete. A relative window ('24h', '2d') or an absolute RFC3339 timestamp; only tombstones tombstoned before it are pruned. Omit to prune ALL tombstoned nodes. |
| `branch` | string |  |  | Branch name (for delete_branch, list_branches). For repair_edges: repair ONLY that one branch overlay of name — branch REQUIRES name (branch with an empty name is an error), and the value is the BARE overlay name ('launch-fixes'), though the composed catalog key ('myrepo@launch-fixes') is accepted and normalized. Omit branch and a repair covers the base graph AND every branch overlay of each targeted repo. |
| `dry_run` | boolean |  |  | For promote_metadata: when true, run the decision pass and report intended actions without mutating the graph. For drop_graph: when true, render a 'would drop' preview and issue ZERO mutations. Default false (executes). |
| `execute` | boolean |  |  | For prune-cache: when true, DELETE the orphaned segments; default false renders a would-remove preview only. For repair_edges: when true, REMOVE the enumerated cross-file CONTAINS fossils; default false renders the preview only. |
| `force` | boolean |  |  | For promote_metadata: when true, bypass the hysteresis bands and use the simple distinct<1000 rule. Operator one-shot path only. |
| `force_edge` | array of string |  |  | Metadata keys pinned to value-node edges for set_metadata_overrides. Replaces the existing list. |
| `force_edge[]` | string |  |  |  |
| `force_scalar` | array of string |  |  | Metadata keys pinned to the scalar map for set_metadata_overrides. Replaces the existing list. |
| `force_scalar[]` | string |  |  |  |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured) |
| `graph` | string |  |  | Target graph type for clear_llm_failures (knowledge, code, practice) |
| `keys` | string |  |  | For promote_metadata: comma-separated metadata key filter. Only the named keys are considered for promotion/demotion; empty means every key the stats snapshot observed. |
| `name` | string |  |  | Graph instance name (the repo name to record for register_repo; the repo / account / language / name the target family is keyed by elsewhere) |
| `operation` | string | yes | graph_inventory, status, pprof_start, pprof_stop, delete_branch, list_branches, link, set_metadata_overrides, promote_metadata, migrate_embed_identity, clear_llm_failures, pause_pipeline, resume_pipeline, pipeline_status, prune, prune-cache, rebuild_cache, rebuild_segments, drop_graph, register_repo, repair_edges, import_style_rules, account_for_session, account_use | Operation to perform |
| `path` | string |  |  | For import_style_rules: an ABSOLUTE path to the rule-list JSON file. A relative path would resolve against the daemon's working directory rather than the caller's, so it is refused rather than guessed at. |
| `profile` | string |  |  | For migrate_embed_identity: the name of the embedder profile to migrate the graph TO. Must name a profile the config defines ([embedder.profile.<name>], or "default" for the single [embedder] table); an unknown name is refused naming the defined profiles. |
| `reason` | string |  |  | For pause_pipeline: optional operator reason surfaced by pipeline_status. Defaults to a generic 'manually paused by operator' string when omitted. |
| `reset` | boolean |  |  | For rebuild_segments: when true, ignore the stored watermark and rebuild the WHOLE corpus. Default false scans only what changed since the last rebuild that landed. |
| `root` | string |  |  | Absolute checkout directory for register_repo |
| `source` | string |  |  | For import_style_rules: the id of the `source` hub node every imported rule is grouped under. Spelled `source` rather than `source_hub` because the name is free on this schema — the practice hub selector publishes `source` wherever it is free and `source_hub` only on the arms (mutate, search) where `source` already means something else. |
<!-- END GENERATED: params -->
