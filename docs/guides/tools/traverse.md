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
content: callers and callees of a function, the resources related to an indexed
node, the provenance chain behind a decision. It is far more precise than grepping
for a name, because it follows real edges rather than text matches.

`traverse` declares no required parameters. `start` names the node to walk from,
and it is NOT unconditionally required: an EMPTY `start` is the graph-wide
enumeration of the target graph — its node and edge totals — rather than a walk.
`direction` is `out` (default), `in`, or
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
| `account` | string |  |  | NO BUILT-IN FAMILY IS KEYED BY ACCOUNT. It was the instance key of the retired cloud and cicd families; a collected inventory graph is a registered custom type now, addressed by name. Consumed by nothing today. |
| `branch` | string |  |  | Branch name for graph='code' (optional). |
| `depth` | number |  |  | Max traversal depth (default 1) |
| `direction` | string |  | out, in, both | Edge direction to walk: 'out' (outgoing, default), 'in' (incoming), or 'both' (union deduped by node ID) |
| `edge_types` | array of string |  |  | Filter by edge types (optional; empty means any) |
| `edge_types[]` | string |  |  |  |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured) |
| `graph` | string |  |  | Target graph: '' or 'knowledge' (default), 'code', 'practice', 'checks', 'linkage', or a registered custom graph type (the name a custom_collector registration was made under). |
| `include_edge_metadata` | boolean |  |  | When true, emit Weight/Confidence/Method/Evidence/LastValidated on every edge at every hop. Default off for all graphs. |
| `include_tombstones` | boolean |  |  | Include tombstoned (deleted) nodes in results. Default false. Edge endpoints are always tombstone-filtered regardless of this flag: the flag governs NODES. |
| `language` | string |  |  | REFUSED on a practice traverse, and it addresses no other family's graph. Practice is ONE combined graph: omit this to traverse it, or narrow to one origin with 'source' (a hub id). |
| `limit` | number |  |  | Max results to return (0 = no cap). ON A RAW DOCUMENT GRAPH THE SLICE IS TAKEN IN NODE-ID ORDER, NOT DOCUMENT ORDER: the traversal never decodes the document position, which lives on edge Evidence, so a limited traverse over a collected document returns an arbitrary slice rather than the first N sections. Drop the limit, or use a recipe extract, which materializes the whole source graph and can therefore order it. |
| `name` | string |  |  | Graph identifier, for the families keyed by name. |
| `repo` | string |  |  | Repo name for graph='code'. |
| `source` | string |  |  | Practice SOURCE HUB id — narrows the traverse to the nodes grouped under that hub. Omit it to traverse the whole combined practice graph. |
| `start` | string |  |  | Starting node ID. OPTIONAL: an EMPTY start is not an error — it selects the graph-wide enumeration of the target graph instead of a walk, reporting the graph's node and edge totals in text and its node/edge rows under format='json'. |
<!-- END GENERATED: params -->
