# record_decision

## Overview

`record_decision` records an architectural decision with its rationale. It is a
richer-schema cousin of `mutate(create)` specialized for decisions: it captures
not just *what* was decided but *why*, and what alternatives were weighed and
rejected. The result is a durable, searchable decision record that later work can
trace back to.

## When & how to use

Reach for `record_decision` whenever you make a design choice you'll want to
justify later — which storage engine, which transport, which API shape. Search
first so you extend or supersede an existing decision instead of duplicating it.

A decision needs `name`, `choice`, and `rationale`; `alternatives` and
`informed_by` are strongly encouraged. A record with no rationale and no
alternatives is not really a decision — if there were no alternatives, was a
decision even made?

```jsonc
record_decision({
  "name": "Use Badger v4 for graph storage",
  "choice": "Badger v4 with custom serialization",
  "rationale": "Concurrent read performance is markedly better and there are no file locks.",
  "alternatives": "SQLite: rejected — file locking is incompatible with the MCP server model.",
  "informed_by": "finding_id1,finding_id2"
})
```

Name the decision for retrieval — the `name` and first sentence of the rationale
are what later searches match. For the full parameter reference, run
`help("record_decision")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `alternatives` | string |  |  | Other options considered (comma-separated) |
| `choice` | string | yes |  | What was decided |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured: {id, name, warnings}). |
| `informed_by` | string |  |  | Comma-separated node IDs of findings/research that informed this decision |
| `name` | string | yes |  | Decision name (e.g., 'Keep HNSW in blob, drop only BM25') |
| `rationale` | string | yes |  | Why this was chosen |
<!-- END GENERATED: params -->
