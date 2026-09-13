# Local and cloud storage

Local use needs no account. While signed in, an operation without a storage target uses cloud storage, including graph creation. Local and cloud copies remain separate; signing in does not move or replicate graphs.

Use `storage: "local"` to select a local copy when a graph also exists in cloud storage. `storage: "cloud"` selects the cloud copy explicitly. A failed cloud operation does not fall back to local storage.

One MCP connection searches both configured stores. Results carry qualified references that identify the storage location, graph, branch where applicable, and cloud account. Pass the returned reference unchanged to query, traversal, or mutation tools. Equal node IDs in separate stores identify separate nodes. A search that cannot reach a required store returns an error instead of reporting an incomplete result as complete.

References use the versioned `kgref:2:` format. The reader also accepts version 1. Cloud references retain their account identity: switching accounts does not retarget them. If an older login has no selected account, run `knowledge accounts` followed by `knowledge account use <id|slug>`.

Links may connect local nodes to local or cloud nodes, and cloud nodes to cloud nodes. Cloud-to-local links are refused before writes. A local-to-cloud link is stored with the local graph; its target is hydrated from the referenced cloud account when read.

Collection runs, queued enrichment, working-set entries, and segment caches retain their storage and account identities. A login change does not move an in-flight collection to another store. Mixed destination mutation batches validate their references before writing, then apply each destination separately; they are not a distributed transaction.
