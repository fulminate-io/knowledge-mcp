# custom_collector

## Overview

`custom_collector` registers and manages custom external collectors / plugins —
user-defined graph types backed by your own collector binary. Alongside the
built-in graph families (knowledge, code, cloud, cicd, practice, linkage, logs,
and the rest), you can define your own: one combined record that says both *how
to collect the graph* — an external collector binary plus the parameters it takes
— and *how to treat it* — whether its nodes are summarized, embedded, and synced,
with per-node-type overrides. It is dispatched by the `operation` field.

## When & how to use

Reach for `custom_collector` when the built-in graph families do not cover a source you
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
custom_collector({ "operation": "list" })

// Register a custom graph type backed by a collector binary
custom_collector({ "operation": "register", "name": "tickets",
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

## The collector plugin contract

Registering a `custom_collector` is only half the story: the other half is the
binary you point `collector.binary_path` at. This section is the contract that
binary must honor. Once a type is registered, you populate it with
[`collect`](collect.md) — `collect({ "type": "<your-type>", ... })` execs your
binary, reads one JSON envelope from its stdout, and streams the nodes and edges
to the server.

### The stdout envelope

Your binary must print exactly one JSON object on stdout — the envelope:

```jsonc
{
  "graph_type": "tickets",   // MUST equal the registered name
  "graph_name": "acme",      // the named graph this collection writes into
  "nodes": [ /* ... */ ],
  "edges": [ /* ... */ ]
}
```

- **`graph_type`** must equal the name you registered the type under. If the
  envelope's `graph_type` differs from the registered name, the collect fails
  loud — a plugin cannot write into a different (built-in or another registered)
  graph type.
- **`graph_name`** is the named graph the nodes and edges land in. It is
  **required**, with one convenience: if the envelope omits `graph_name`, the
  `collect` call's `id` supplies it (see [the id → graph_name default](#the-id--graph_name-default)).
  When the envelope sets its own `graph_name`, that explicit value wins. If
  **both** the envelope and the collect `id` are empty, the collect fails loud —
  there is no silent default.

### Node and edge fields a collector may set

Each entry in `nodes[]` and `edges[]` is a flat JSON object. A collector may set
the fields below; everything else is server-owned bookkeeping (creation /
update / tombstone timestamps, collect epoch, internal versioning) that the
collect-write path stamps — a collector cannot set those.

**Node fields** (every field except `id` and `type` is optional):

| Field | Type | Notes |
| --- | --- | --- |
| `id` | string | Stable identifier for the node within the graph. |
| `type` | string | Node type. **Required** — a node with an empty `type` fails the collect loud. |
| `symbol_name` | string | Display / symbol name. |
| `file_path` | string | Source path, when the node maps to a file. |
| `language` | string | Language tag. |
| `start_line` / `end_line` | int | Line span. |
| `content` | string | Full body text. |
| `signature` | string | Signature / one-line shape. |
| `summary` | string | Short summary (if the type is summarizable). |
| `description` | string | Human-facing description. |
| `source` | string | Provenance tag. |
| `status` | string | Open-string status. |
| `keywords` | string | BM25 keyword-token boost. |
| `is_exported` | bool | Visibility flag. |
| `metadata` | object (string→string) | Free-form domain data. **Any field not in this table rides here.** |

**Edge fields:**

| Field | Type | Notes |
| --- | --- | --- |
| `from_id` / `to_id` | string | Endpoint node IDs (the external contract references endpoints by ID, never by index). |
| `type` | string | Edge type. |
| `weight` | float | Optional. |
| `confidence` | float | Optional. |
| `method` | string | Optional — how the edge was derived. |
| `evidence` | string | Optional — backing evidence. |

Domain-specific data beyond these typed fields rides in each node's `metadata`
map, exactly as the built-in collectors do.

### How params reach the binary

The `param_transport` you registered decides how the `collect` call's `params`
object is delivered to the binary as a JSON document:

- **`stdin`** — the params JSON is written to the binary's stdin.
- **`flag:<name>`** — the params JSON is passed as `--<name> <json>` on the
  command line (e.g. `param_transport: "flag:params"` runs the binary with
  `--params '{"repo":"acme"}'`).

Before exec, the params are validated against the registered `param_schema` — a
map of `{ "<param>": { "type": "string"|"int"|"bool", "required": <bool> } }`.
A param key that is **not** declared in the schema, or a **required** param that
is missing, fails the collect loud before the binary runs. The type check is
deliberately loose (a JSON number satisfies `int`); the schema is a small
`{type, required}` contract, not a DSL.

### Execution and security model

Your binary runs under a deliberately constrained harness:

- **Provider API keys are scrubbed.** `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`,
  `GEMINI_API_KEY`, and `GOOGLE_API_KEY` are stripped from the binary's
  environment — a third-party collector has no business reading your LLM
  credentials.
- **`binary_path` must be absolute.** A relative path is rejected before exec.
- **The working directory is a temp dir** (`os.TempDir()`), so the binary does
  not auto-detect project config from the caller's cwd.
- **stdout is capped at 64 MiB.** A binary that emits more than the cap fails
  loud (over-cap output is never parsed) rather than buffering without bound.
- **Every failure fails loud.** Malformed JSON on stdout, a non-zero exit (the
  binary's stderr is surfaced verbatim in the error), an over-cap stdout, or a
  context deadline each return an error and write **nothing** — there is no
  partial collect. stdout is the data channel (the envelope); stderr is the log
  channel, so keep diagnostics on stderr or they will corrupt the parse.

### The id → graph_name default

The `collect` `id` is the per-type opaque identifier. For a registered custom
type, the `id` doubles as the **default `graph_name`**: when your envelope omits
`graph_name`, the collection writes into the named graph identified by the
collect `id`. An envelope that emits its own `graph_name` overrides this (the
explicit value wins). This means a minimal collector can leave `graph_name` out
of its envelope entirely and let the caller name the target graph via
`collect({ "type": "...", "id": "<graph-name>" })`.

## A worked example: register, then collect

This minimal collector reads its params on stdin and prints one envelope. Save
it as an executable file (`chmod +x`) at an absolute path:

```sh
#!/bin/sh
# /usr/local/bin/ticket-collector — a minimal custom collector.
# Reads the params JSON on stdin (param_transport "stdin"); we don't use it here.
cat > /dev/null
cat <<'EOF'
{"graph_type":"tickets","graph_name":"acme","nodes":[{"id":"T-1","type":"ticket"}],"edges":[]}
EOF
```

Register the type, pointing `binary_path` at it and declaring the params it
takes:

```jsonc
custom_collector({
  "operation": "register",
  "name": "tickets",
  "collector": {
    "binary_path": "/usr/local/bin/ticket-collector",
    "param_transport": "stdin",
    "param_schema": { "repo": { "type": "string", "required": true } }
  }
})
```

Then run a collection against it via [`collect`](collect.md):

```jsonc
collect({ "type": "tickets", "id": "acme", "params": { "repo": "acme/backend" } })
```

The `params` object (`{ "repo": "acme/backend" }`) is validated against the
registered `param_schema` and delivered to the binary on stdin; the binary
prints its envelope; the `tickets` nodes stream to the server.

In this example the envelope sets its own `graph_name` (`"acme"`), so that
explicit value is used and the collect `id` is not consulted for it. Had the
binary **omitted** `graph_name`, the collect `id` (`"acme"`) would have supplied
it instead — so `collect({ "type": "tickets", "id": "acme", ... })` would still
write into the `acme` graph. The collect `id` is therefore meaningful for a
registered type even when the binary names its own graph.
