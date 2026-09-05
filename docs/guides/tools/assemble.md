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
| `section_end` | integer |  |  | For a chunked plan: the last section index to return, zero-based and inclusive. Omit to run to the last section. Supplying either bound returns the section BODIES in that range with their annotations; supplying neither returns the plan's index and tree alone, IN BOTH FORMATS — a text read shows each section's first 120 characters in the tree, and a json read omits the body and marks it body_omitted with its body_bytes, so neither default returns a whole plan. A range on a node that is not a plan is refused in both formats, and so is a range on a plan that HAS no sections — a phase-and-step plan has nothing to page. |
| `section_start` | integer |  |  | For a chunked plan: the first section index to return, zero-based and inclusive. Omit to start at the first section. An out-of-bounds, negative or inverted range errors naming the bound — it is never clamped. |
| `type` | string |  |  | Node type for name-based lookup (e.g. project, test_plan, research) |
<!-- END GENERATED: params -->
