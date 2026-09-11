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
pruning runs against creation time, not last-updated time. A practice-graph
delete takes no `language` — the param is refused — and narrows by `source` (a hub
id) instead. For the full reference, run
`help("delete")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `account` | string |  |  | NO BUILT-IN FAMILY IS KEYED BY ACCOUNT. It was the instance key of the retired cloud and cicd families; a collected inventory graph is a registered custom type now, addressed by name. Consumed by nothing today. |
| `dry_run` | boolean |  |  | Preview only: report the nodes that WOULD be deleted (count + ids/names) without deleting anything. Applies to the ids shape; on an older_than call it reports that prune-by-age has no retention-eligible type rather than previewing. Re-run without dry_run to actually delete. |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured). Honored on BOTH render paths — the dry-run preview and the completed delete. |
| `graph` | string |  |  | Target graph: 'knowledge' (default), 'code' (requires repo), 'practice', or 'checks'. practice and checks are singletons and take neither language nor name; a practice delete narrows by `source` instead. |
| `hard` | boolean |  |  | PERMANENT removal. Deletes are SOFT by default (tombstoned: hidden from reads, recoverable). hard:true removes the rows irrecoverably — reserve for deliberate permanent cleanup. A malformed value denies the delete. |
| `id` | string |  |  | Singular alias for a one-element `ids` — every other single-node op names its target with `id`, so deleting one node accepts that spelling too. Supplying both is additive (the two sets union), not a conflict. |
| `ids` | array of string |  |  | Node IDs to delete |
| `ids[]` | string |  |  |  |
| `language` | string |  |  | REFUSED on a practice delete, and it addresses no other family's graph. Practice is ONE combined graph, so narrow by `source` (a hub id) instead. |
| `older_than` | string |  |  | Prune-by-age window (e.g. '7d', '24h'). NOT CURRENTLY AVAILABLE — no node type is retention-eligible, so a call carrying older_than is refused rather than run. |
| `repo` | string |  |  | Code graph name — REQUIRED for graph='code'; it is never inferred from cwd. Writes to a collected graph are caller-owned: a later collect reconciles that graph from its source and overwrites hand-authored changes. |
| `session_id` | string |  |  | Restricts prune-by-age to one session. Inert while prune-by-age is unavailable. |
| `source` | string |  |  | Practice SOURCE HUB id — deletes every practice node grouped under that hub AND the hub itself, in ONE write: a hub carries its own id under the membership key, so a single predicate sweeps the collection whole and leaves no empty hub behind. This is how an abandoned or failed collection is removed as a unit. The id must name a LIVE hub — a `source` node in the combined practice graph — and an id that names a member, a node of another type, a tombstoned hub or nothing at all is refused, naming the id, rather than run against whatever it does match. Mutually exclusive with ids/id: a call carrying both selection axes is denied rather than resolved. |
| `type` | string |  |  | Node type filter for prune-by-age. NOT CURRENTLY AVAILABLE — every type fails the retention-eligibility check, so no value here selects anything. |
<!-- END GENERATED: params -->
