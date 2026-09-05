# delete

## Overview

`delete` removes nodes from the graph. You can delete specific nodes by id, or
prune nodes older than a given age. Deletion tombstones the node; to reclaim the
space afterwards, hard-delete the tombstones with `manage(prune)`.

## When & how to use

Reach for `delete` to clean up nodes you no longer want — stale findings,
abandoned plans, scratch nodes from an experiment. Pass `ids` to remove specific
nodes, or `older_than` together with `type` to prune by creation age. Age-based
pruning requires both `older_than` and `type` — it only ever removes nodes of the
named type, so a prune cannot sweep the whole graph by age alone. Always run a
large prune with `dry_run: true` first to preview what would go.

```jsonc
// Delete specific nodes
delete({ "ids": ["node_id1", "node_id2"] })

// Preview an age-based prune, then execute (older_than + type both required)
delete({ "older_than": "7d", "type": "session", "dry_run": true })
delete({ "older_than": "7d", "type": "session" })
```

Two things to keep in mind: deleting a node does not delete its edges, and
pruning runs against creation time, not last-updated time. Practice-graph
deletes require the `language` param. For the full reference, run
`help("delete")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `account` | string |  |  | Selects which inventoried external-provider account/org's resources to address within your own graph — an AWS/GCP account for graph='cloud', or a CI provider org (e.g. GitHub/GitLab) for graph='cicd'. REQUIRED for those two families. Writes to a collected graph are caller-owned: a later collect reconciles that graph from its source and overwrites hand-authored changes. |
| `dry_run` | boolean |  |  | Preview only: report the nodes that WOULD be deleted (count + ids/names) without deleting anything. Applies to the ids shape; on an older_than call it reports that prune-by-age has no retention-eligible type rather than previewing. Re-run without dry_run to actually delete. |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured). Honored on BOTH render paths — the dry-run preview and the completed delete. |
| `graph` | string |  |  | Target graph: 'knowledge' (default), 'code' (requires repo), 'cloud' / 'cicd' (require account), 'practice' (requires language), or 'checks'. checks is a singleton and takes neither language nor name. |
| `hard` | boolean |  |  | PERMANENT removal. Deletes are SOFT by default (tombstoned: hidden from reads, recoverable). hard:true removes the rows irrecoverably — reserve for deliberate permanent cleanup. A malformed value denies the delete. |
| `id` | string |  |  | Singular alias for a one-element `ids` — every other single-node op names its target with `id`, so deleting one node accepts that spelling too. Supplying both is additive (the two sets union), not a conflict. |
| `ids` | array of string |  |  | Node IDs to delete |
| `ids[]` | string |  |  |  |
| `language` | string |  |  | Language for practice graph operations (e.g. 'Go', 'JavaScript/TypeScript') |
| `older_than` | string |  |  | Prune-by-age window (e.g. '7d', '24h'). NOT CURRENTLY AVAILABLE — no node type is retention-eligible, so a call carrying older_than is refused rather than run. |
| `repo` | string |  |  | Code graph name — REQUIRED for graph='code'; it is never inferred from cwd. Writes to a collected graph are caller-owned: a later collect reconciles that graph from its source and overwrites hand-authored changes. |
| `session_id` | string |  |  | Restricts prune-by-age to one session. Inert while prune-by-age is unavailable. |
| `type` | string |  |  | Node type filter for prune-by-age. NOT CURRENTLY AVAILABLE — every type fails the retention-eligibility check, so no value here selects anything. |
<!-- END GENERATED: params -->
