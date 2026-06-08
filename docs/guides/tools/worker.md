# worker

## Overview

`worker` manages background workers — small autonomous agents that run a ReAct
loop against an allowlisted set of MCP tools, fired by triggers (a tool finishing,
another worker completing, a cron schedule, or a manual call). A worker is a
durable record: its system prompt, model, tool allowlist, iteration and wallclock
limits, and trigger rules. It is dispatched by the `operation` field.

Workers are how the graph enriches itself in the background — for example a
code-smell scanner that runs whenever a repo is recollected.

## When & how to use

Reach for `worker` to register, inspect, trigger, or cancel background work. Most
developers start with `list` to see what is registered and `status` to see recent
runs; `create` is for standing up a new background job.

`operation` is always required. The other required inputs by operation:

| Operation | Required (besides `operation`) | What it does |
| --- | --- | --- |
| `list` | — | Enumerate registered workers. |
| `create` | `name`, `system_prompt`, `provider`, `model`, `tool_allowlist` (non-empty) | Register a worker. |
| `update` | `name` | Edit a worker record. |
| `delete` | `name` | Remove a worker. |
| `trigger` | `name` | Fire a worker now; `payload` is forwarded to its first turn. |
| `status` | `name` | Recent invocations (`limit` caps the count). |
| `running` | — | List in-flight invocations (`name` optional filter). |
| `cancel` | `invocation` or `name` | Cancel an in-flight run (`invocation` wins if both given). |

```jsonc
// See what's registered
worker({ "operation": "list" })

// Trigger a scan, narrowing it with a payload
worker({ "operation": "trigger", "name": "code-smell-scanner",
         "payload": { "code_graph": "knowledge", "language": "go" } })
```

For local development, prefer `provider: "claude-cli"` (free) when creating a
worker — registering an `anthropic`/`openai`/`gemini` worker bills the
corresponding API account. CLI providers cannot drive tool-use, so a worker that
must call tools needs an API provider.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `base_url` | string |  |  | Optional LLM endpoint override for this worker (used by create/update); overrides the [dream]/[default] base_url. Ignored for CLI providers. |
| `description` | string |  |  | Worker description (used by create/update) |
| `enabled` | boolean |  |  | Whether the worker is enabled (used by create/update). |
| `format` | string |  |  | Output format: 'text' (default) or 'json'. |
| `invocation` | string |  |  | Per-run UUID for cancel: target a specific in-flight invocation. Discover IDs via worker(operation:"running") or the invocation_id field on worker:status start records. Either invocation or name is required for cancel; if both supplied, invocation wins. |
| `limit` | integer |  |  | For status: max recent invocations to return (default 10). |
| `max_iterations` | integer |  |  | Max ReAct loop iterations per invocation (used by create/update). 0 means use the package default. |
| `max_wallclock_seconds` | integer |  |  | Max wallclock seconds per invocation (used by create/update). 0 means use the package default. |
| `model` | string |  |  | Model identifier (provider-specific, used by create/update). Empty falls back to the [dream] section in ~/.knowledge/config. |
| `name` | string |  |  | Worker name (identity / lookup key — required for create/update/delete/trigger/status; optional for running/cancel) |
| `operation` | string | yes | list, create, update, delete, trigger, status, running, cancel | Operation to perform |
| `payload` | object |  |  | User-prompt payload for the trigger operation. Forwarded to the worker's first ReAct turn as JSON. |
| `provider` | string |  |  | LLM provider: anthropic \| openai \| gemini \| claude-cli \| codex-cli (used by create/update). CLI providers cannot drive tool-use — see tool description. |
| `system_prompt` | string |  |  | System prompt fed verbatim to the LLM at the start of every run (used by create/update) |
| `tool_allowlist` | array of string |  |  | Allowed MCP tool names — required and non-empty for create. Used by create/update. |
| `tool_allowlist[]` | string |  |  |  |
| `triggers` | array of object |  |  | Trigger entries (used by create/update). Each entry: {event, filter, schedule}. Event ∈ {tool-started, tool-completed, worker-started, worker-completed, cron, manual}. Filter is an AND-of-equality match on event metadata; schedule is a cron expression on Event=cron. |
| `triggers[]` | object |  |  | Trigger entry: {event, filter, schedule} |
| `triggers[].event` | string |  | tool-started, tool-completed, worker-started, worker-completed, cron, manual | Event that fires the trigger |
| `triggers[].filter` | object |  |  | AND-of-equality match on event metadata |
| `triggers[].schedule` | string |  |  | Cron expression (only for event=cron) |
<!-- END GENERATED: params -->
