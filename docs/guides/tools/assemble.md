# assemble

## Overview

`assemble` walks a structured node's tree and renders everything you need to work
with it in one call. It is type-aware: given a plan it renders phases, steps,
criteria, and the decisions and findings that shaped them; given a test plan it
renders steps and criteria (and, with `new_run`, creates run nodes); given an
agent or skill it renders the tool guides and rules attached to it; given a
research node or a decision it follows the appropriate provenance edges.

## When & how to use

Reach for `assemble` instead of a series of `query` and `traverse` calls when you
want the full, rendered context for a known structured node — most often a plan
you are about to implement, or a test plan you are about to run.

Pass `id` for a direct lookup, or `name` plus `type` for a name-based one.

```jsonc
// Render a plan's full hierarchy
assemble({ "id": "plan_id" })

// Look up a test plan by name and start a run
assemble({ "name": "Auth integration tests", "type": "test_plan", "new_run": true })
```

For a test plan, `new_run: true` creates pending run nodes you can then record
results against. For the full reference, run `help("assemble")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured) |
| `id` | string |  |  | Node ID to assemble context for |
| `name` | string |  |  | Node name (used with type for name-based lookup) |
| `new_run` | boolean |  |  | For test_plan: create a new run session with pending test_run nodes |
| `run_session` | string |  |  | For test_plan: filter assembled test_runs by this run session UUID |
| `type` | string |  |  | Node type for name-based lookup (e.g. project, test_plan, research) |
<!-- END GENERATED: params -->
