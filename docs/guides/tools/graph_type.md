# graph_type

## Overview

`graph_type` registers and manages user-defined graph types. Alongside the
built-in graph families (knowledge, code, cloud, cicd, practice, linkage, logs,
and the rest), you can define your own: one combined record that says both *how
to collect the graph* — an external collector binary plus the parameters it takes
— and *how to treat it* — whether its nodes are summarized, embedded, and synced,
with per-node-type overrides. It is dispatched by the `operation` field.

## When & how to use

Reach for `graph_type` when the built-in graph families do not cover a source you
want to bring into the graph and you have a collector binary that can emit nodes
for it. Most developers never need this — the built-in types handle code,
knowledge, cloud, and CI/CD already.

`operation` is always required. The other required inputs by operation:

| Operation | Required (besides `operation`) | What it does |
| --- | --- | --- |
| `register` | `name`, `collector.binary_path` (absolute), `collector.param_transport` (`"stdin"` or `"flag:<name>"`) | Register a new graph type. |
| `update` | `name` | Edit an existing record — a full re-write, so supply every field you want kept. |
| `delete` | `name` | Remove a registered graph type. |
| `list` | — | Enumerate registered graph types with their collector and behavior. |

The registered `name` must not collide with a built-in graph type. The optional
`behavior` block carries tri-state booleans — omit a field to inherit, set
`true`/`false` to pin — and `node_types` lets one node type override the graph
defaults.

```jsonc
// List what is registered
graph_type({ "operation": "list" })

// Register a custom graph type backed by a collector binary
graph_type({ "operation": "register", "name": "tickets",
             "collector": { "binary_path": "/usr/local/bin/ticket-collector",
                            "param_transport": "stdin" },
             "behavior": { "summarizable": true, "embeddable": true } })
```

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `behavior` | object |  |  | Graph-level behavior defaults (the cascade defaults; per-node-type overrides go in node_types). Booleans are tri-state: omit to leave unset (inherit), set true/false to pin. |
| `behavior.bm25_fields` | array of string |  |  | Node fields that participate in BM25. |
| `behavior.bm25_fields[]` | string |  |  |  |
| `behavior.embed_fields` | array of string |  |  | Node fields that participate in embedding. |
| `behavior.embed_fields[]` | string |  |  |  |
| `behavior.embeddable` | boolean |  |  | Whether nodes of this type are embedded. |
| `behavior.extra` | object |  |  | Forward-compat map of additional graph-behavior keys (string->string). |
| `behavior.summarizable` | boolean |  |  | Whether nodes of this type are summarized. |
| `behavior.summarize_fields` | array of string |  |  | Node fields that participate in summarization. |
| `behavior.summarize_fields[]` | string |  |  |  |
| `behavior.syncable` | boolean |  |  | Whether graphs of this type may be synced. |
| `collector` | object |  |  | Collector spec: how to populate the graph. Required for register. |
| `collector.binary_path` | string |  |  | Absolute path to the collector binary. |
| `collector.param_schema` | object |  |  | Map of param name -> {type: string\|int\|bool, required: bool}. |
| `collector.param_transport` | string |  |  | How params reach the binary: "stdin" or "flag:<name>". |
| `description` | string |  |  | Human-facing one-liner describing the graph type (optional; used by register/update). |
| `format` | string |  |  | Output format: 'text' (default) or 'json'. |
| `name` | string |  |  | Graph-type name (identity / lookup key — required for register/update/delete). Must not collide with a built-in graph type. |
| `node_types` | object |  |  | Map of node-type -> behavior override {summarizable?, embeddable?, embed_fields?, summarize_fields?, bm25_fields?}. Any subset; unset means inherit the graph default. |
| `operation` | string | yes | register, update, delete, list | Operation to perform |
<!-- END GENERATED: params -->
