# traverse

## Overview

`traverse` is edge-first graph traversal. Where `query` and `search` find nodes,
`traverse` walks the relationships between them: starting from one node, it
follows edges of the types you name, in the direction you choose, out to a given
depth. It works across every graph family via the `graph` selector, and the most
common use is the code graph's call graph — who calls a function, and what it
calls in turn.

## When & how to use

Reach for `traverse` whenever the question is about connections rather than
content: callers and callees of a function, the resources related to a cloud
node, the provenance chain behind a decision. It is far more precise than grepping
for a name, because it follows real edges rather than text matches.

`start` is the only required parameter. `direction` is `out` (default), `in`, or
`both` (a deduplicated union). Pick `edge_types` from the graph's vocabulary —
discover them first with `query({ "mode": "stats", "graph": "code" })`, since edge
type names differ per graph.

```jsonc
// Who calls this function?
traverse({ "start": "pkg/server.go:Handle", "graph": "code", "repo": "myrepo",
           "edge_types": ["calls"], "direction": "in" })

// What does it call?
traverse({ "start": "pkg/server.go:Handle", "graph": "code", "repo": "myrepo",
           "edge_types": ["calls"], "direction": "out" })
```

Code-graph method IDs are receiver-qualified — `path/file.go:Type.Method`, not
`file.go:Method`. Cross-graph proxies resolve automatically, so you can pass a
knowledge node id with `graph: "code"`. For the full parameter reference, run
`help("traverse")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `account` | string |  |  | Selects which inventoried external-provider account/org's resources to traverse within your own graph — an AWS/GCP account for graph='cloud', or a CI provider org (e.g. GitHub/GitLab) for graph='cicd'. Required for graph='cloud'/'cicd'; omit to list your available graphs. |
| `branch` | string |  |  | Branch name for graph='code' (optional). |
| `depth` | number |  |  | Max traversal depth (default 1) |
| `direction` | string |  | out, in, both | Edge direction to walk: 'out' (outgoing, default), 'in' (incoming), or 'both' (union deduped by node ID) |
| `edge_types` | array of string |  |  | Filter by edge types (optional; empty means any) |
| `edge_types[]` | string |  |  |  |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured) |
| `graph` | string |  |  | Target graph: '' or 'knowledge' (default), 'code', 'cloud', 'cicd', 'practice', 'logs', 'linkage'. |
| `include_edge_metadata` | boolean |  |  | When true, emit Weight/Confidence/Method/Evidence/LastValidated on every edge at every hop. Default off for all graphs. |
| `include_tombstones` | boolean |  |  | Include tombstoned (deleted) nodes in results. Default false. |
| `language` | string |  |  | Language slug for graph='practice' (e.g. 'go', 'python'). |
| `limit` | number |  |  | Max results to return (0 = no cap) |
| `name` | string |  |  | Graph identifier (e.g. query_id for graph='logs'). |
| `overlay` | string |  |  | Optional knowledge session overlay name; diagnostic scoping. |
| `repo` | string |  |  | Repo name for graph='code'. |
| `start` | string |  |  | Starting node ID |
<!-- END GENERATED: params -->
