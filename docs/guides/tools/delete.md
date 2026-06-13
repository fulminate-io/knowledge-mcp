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
| `dry_run` | boolean |  |  | Preview only: report the nodes that WOULD be deleted (count + ids/names) without deleting anything. Works for BOTH shapes — ids deletion and older_than pruning. Re-run without dry_run to actually delete. |
| `graph` | string |  |  | Target graph: 'knowledge' (default), 'practice', or 'transformers'. Practice graph requires 'language'. |
| `hard` | boolean |  |  | PERMANENT removal. Deletes are SOFT by default (tombstoned: hidden from reads, recoverable). hard:true removes the rows irrecoverably — reserve for deliberate permanent cleanup. A malformed value denies the delete. |
| `ids` | array of string |  |  | Node IDs to delete |
| `ids[]` | string |  |  |  |
| `language` | string |  |  | Language for practice graph operations (e.g. 'Go', 'JavaScript/TypeScript') |
| `older_than` | string |  |  | Delete nodes of the given `type` older than this duration (e.g. '7d', '24h') |
| `session_id` | string |  |  | Only prune nodes from this session |
| `type` | string |  |  | Node type filter for pruning (e.g. 'session') |
<!-- END GENERATED: params -->
