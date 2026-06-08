# help

## Overview

`help` is the built-in documentation tool. Call it with no topic for a full index
of every tool, or with a `topic` for a deep dive — the operation vocabulary,
parameters, and worked examples for a specific tool, or a reference topic like the
node types, edge types, statuses, and common workflows. It is the fastest way to
get authoritative, in-context detail without leaving the session.

## When & how to use

Reach for `help` whenever you need the precise vocabulary or parameter set for a
tool or concept: the operations a dispatched tool supports, the valid edge types
for a `link`, the node-type catalog, or the recipe DSL grammar.

Call it with no argument for the overview, or pass a single `topic`:

```jsonc
// Full tool index
help()

// Deep dive on a specific tool
help("traverse")

// Reference topics
help("edge_types")
help("workflows")
```

Tool topics cover `query`, `traverse`, `mutate`, `delete`, `manage`, `ast`,
`thoughts`, `record_decision`, `search`, `file_symbols`, `assemble`, `sync`, the
`create_*` batch creators, and `help` itself. Reference topics cover
`node_types`, `edge_types`, `statuses`, `workflows`, `logs`, `patterns`,
`recipes`, and `topology`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `topic` | string |  | overview, node_types, edge_types, statuses, workflows, logs, patterns, recipes, topology, query, traverse, mutate, delete, manage, ast, thoughts, create_project, create_ticket, create_plan, create_research, create_test_plan, record_decision, search, file_symbols, help, assemble, sync | Topic to get help on. Omit for overview. Tool names: query, traverse, mutate, delete, manage, ast, thoughts, create_project, create_ticket, create_plan, create_research, create_test_plan, record_decision, search, file_symbols, help, assemble, sync. Reference topics: node_types, edge_types, statuses, workflows, logs, patterns, recipes, topology. |
<!-- END GENERATED: params -->
