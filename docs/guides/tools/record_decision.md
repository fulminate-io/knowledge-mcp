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

A decision needs `name`, `choice`, `rationale`, and an author-supplied
`summary`; `alternatives` and `informed_by` are strongly encouraged. A record with no rationale and no
alternatives is not really a decision — if there were no alternatives, was a
decision even made?

```jsonc
record_decision({
  "name": "Use cursor pagination for the /orders list endpoint",
  "summary": "The orders list paginates by an opaque cursor over (created_at, id) instead of page numbers, so inserts during a scan never repeat or skip rows",
  "choice": "Opaque cursor encoding (created_at, id); page-number parameters are rejected with 400",
  "rationale": "Orders are inserted continuously, and offset pagination repeats or skips rows when the table shifts under a client mid-scan.",
  "alternatives": "Offset/limit: rejected — unstable under concurrent inserts. Keyset on id alone: rejected — ids are not time-ordered in this schema.",
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
| `links` | array of string |  |  | Node IDs to relate the decision to (node--relates-to-->target). Knowledge-graph IDs ride the atomic create; code/cloud IDs are linked post-create via the cross-graph linkage. An unresolvable ID is dropped with a warning, never blocking the write. |
| `links[]` | string |  |  |  |
| `name` | string | yes |  | Decision name (e.g., 'Keep HNSW in blob, drop only BM25') |
| `rationale` | string | yes |  | Why this was chosen |
| `session` | string |  |  | Session name to group the decision under via session--contains-->decision. Creates the session if new. |
| `summary` | string | yes |  | Required search-optimized one-line summary of the decision, max 500 chars. Over-cap values are clamped at a word boundary with a warning. (max length: 500) |
| `ticket_id` | string |  |  | Active ticket/project ID — born-linked as ticket--contains-->decision so the decision is grouped under the work item that produced it. An unresolvable ticket_id is dropped with a warning, never blocking the write. |
<!-- END GENERATED: params -->
