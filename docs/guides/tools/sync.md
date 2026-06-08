# sync

## Overview

`sync` mirrors your local knowledge graph to and from Fulminate Cloud. It is part
of the optional paid cloud tier and requires the `sync` license scope — the local
knowledge graph works fully on its own without it. When enabled, `sync` lets you
back up your graph to your cloud account and restore it on another machine. It is
dispatched by the `operation` field.

## When & how to use

Reach for `sync` when you want your graph available beyond a single machine —
backing it up, or moving it to a new workstation. It has three operations:

| Operation | Direction | What it does |
| --- | --- | --- |
| `push` | local → cloud | Upload the local graph; the cloud copy merges it in. |
| `pull` | cloud → local | Full overwrite of the local graph from the cloud copy. Requires a local server as the destination. |
| `list` | — | Show sync-eligible local graphs with their cloud status and last-synced time. |

`operation` is the only required field; `graph` defaults to `knowledge` and `name`
to `default`.

```jsonc
// See what can be synced and its status
sync({ "operation": "list" })

// Back the local graph up to your cloud account
sync({ "operation": "push" })
```

`pull` is a full overwrite of the local graph, so use it to restore or to seed a
fresh machine — not to merge two diverged copies. For the full reference, run
`help("sync")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `graph` | string |  |  | Graph type (knowledge, code, cloud, etc.); defaults to 'knowledge' |
| `name` | string |  |  | Graph name; defaults to 'default' |
| `operation` | string | yes | push, pull, list | Operation to perform |
<!-- END GENERATED: params -->
