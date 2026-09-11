# mutate

## Overview

`mutate` is the single write primitive for the knowledge graph. Everything that
creates, edits, or connects a node goes through it, dispatched by the
`operation` field: `create` and `create_batch` add nodes, `update` and
`update_batch` edit fields or status, `bulk_update_metadata` rewrites metadata in
bulk, `upsert` creates-or-updates a node by a caller-supplied id, `link` and
`unlink` add and remove edges, `answer` resolves a research question, and
`delete` tombstones nodes.

The same tool writes to other graph families through the `graph` selector —
`graph: "practice"` (with `source_hub` on a create) edits the one combined
practice graph, and a `link` with a `graph` selector creates the cross-graph
proxy for you rather than duplicating the node. Reads belong to `query`, `search`, and `traverse`; `mutate`
is exclusively the write side.

## When & how to use

Reach for `mutate` whenever you want to record something durable: a finding from
an investigation, a rule the codebase should follow, a status change on a plan
step, or an edge that ties a decision to the code it shaped. Search or recall
first so you extend an existing node instead of creating a duplicate.

`operation` is the only always-required field. Each operation then has its own
required set:

| Operation | Required (besides `operation`) | Notes |
| --- | --- | --- |
| `create` | `type` | `summary` is also required for embed-only types (finding, document, resource, memory, event, …). `criterion` additionally needs `step_id`. |
| `create_batch` | `nodes` | Optional `edges` are created atomically in the same transaction; an edge endpoint is either a `*_idx` slot into `nodes` or an existing `*_id`. |
| `update` | `id` or `ids` | `ids` (plural) is the batch form; `id` (singular) is the single form. |
| `update_batch` | `items` | All-or-nothing; any per-item validation failure rejects the whole batch. |
| `bulk_update_metadata` | `updates` | Each entry needs `id` plus a non-empty `metadata` map. |
| `upsert` | `id` and `type` | Restricted to an allowlist of tool-owned config types. |
| `link` | `from`, `to`, `relationship` | A `to` of the form `file:<path>` records the edge against that path-keyed target id (the convention project builders use to tie a step to the files it touches). |
| `unlink` | `from`, `to`, `relationship` | Idempotent — succeeds even if no matching edge existed. |
| `answer` | `id` (or `question_id`) | Use `concludes`, `conclusion`, and `findings` to record the resolution. |
| `delete` | `id` or `ids` | Tombstones the node(s). |

A few worked examples:

```jsonc
// Record a finding
mutate({ "operation": "create", "type": "finding", "name": "Cache key collision",
         "description": "...", "summary": "two tenants share a cache key under X" })

// Mark a step done
mutate({ "operation": "update", "id": "step_id", "status": "completed" })

// Link a decision to the code it shaped
mutate({ "operation": "link", "from": "decision_id", "to": "file:internal/cache/key.go",
         "relationship": "informed-by" })
```

The `relationship` on a `link` must be a real edge type — run `help("edge_types")`
to see the vocabulary. For the full operation reference, run `help("mutate")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `account` | string |  |  | NO BUILT-IN FAMILY IS KEYED BY ACCOUNT. It was the instance key of the retired cloud and cicd families; a collected inventory graph is a registered custom type now, addressed by name. Consumed by nothing today. |
| `binary_vector` | string |  |  | Base64-encoded binary embedding to install on the node via PutBinaryVector. Decoded payload length must equal 32 bytes (256-bit). Used by the client-side LLM pipeline writeback path. Mismatched lengths return a structured validation error and no write is performed. |
| `branches_from` | string |  |  | Thought ID this branches from (mutate(create, type=thought) only). Adds an edge from the new thought to its parent for trace lineage. |
| `charge_evidence` | array of string |  |  | Evidence node IDs backing the charge (mutate(create, type=charge) only). Renamed from `evidence` on the wire to avoid collision with the finding evidence field. |
| `charge_evidence[]` | string |  |  |  |
| `cited_range` | string |  |  | Optional locality hint accompanying verified_quote on a negation-class call, as "path/file.go:start-end". When set, the verbatim substring must resolve to the cited path; when empty the gate checks existence and currency only. TOP-LEVEL param, consumed by the gate before any write and never persisted. |
| `command` | string |  |  | Verification command for criterion |
| `concludes` | boolean |  |  | If true and question_id is set, marks the question as answered |
| `conclusion` | string |  |  | Conclusion for answer operation |
| `confidence` | number |  |  | Edge metadata (operation=link only). 0.0-1.0 caller-asserted confidence. Routed into store.Edge.Confidence via LinkBatch. |
| `content` | string |  |  | Full content. For research create: context/background. |
| `criterion_type` | string |  |  | Criterion type: automated or manual |
| `description` | string |  |  | Node description |
| `edge_evidence` | string |  |  | Edge metadata (operation=link only). Caller-supplied evidence string (file path, snippet, URL) backing the edge. Renamed from `evidence` on the wire to avoid collision with the finding `evidence` field. Routed into store.Edge.Evidence. |
| `edges` | array of object |  |  | For operation=create_batch: per-edge array; each entry carries {from_idx, to_idx, from_id, to_id, type} plus the optional edge-metadata carriers {weight, confidence, method, evidence, last_validated}. An endpoint is either a slot index into nodes[] (from_idx/to_idx >= 0) OR an existing node ID (from_id/to_id). Use -1 / absent for the slot index when supplying an ID instead. Created atomically inside the same store.Txn as the nodes payload. ATTACHING A CRITERION TO A STEP TAKES A PAIR OF EDGES, not one: step--contains-->criterion AND criterion--verifies-->step, the same pair create_plan and mutate(create, type:criterion) both emit. plan_tree walks contains only, so a criterion attached by verifies alone is invisible in the rendered tree. A batch carrying one direction without its partner is REJECTED pre-write, naming the missing edge — the pair is never auto-completed. |
| `edges[]` | object |  |  | Per-edge shape: {from_idx?, to_idx?, from_id?, to_id?, type (required), weight?, confidence?, method?, evidence?, last_validated?} |
| `edges[].confidence` | number |  |  | Caller-asserted 0.0-1.0 confidence, stored verbatim. |
| `edges[].evidence` | string |  |  | Evidence backing the edge (a file path, a snippet, a JSON payload). A relates-to edge from a plan_annotation MUST carry that annotation's kind and tier here, or the write is refused naming the exact value to send. |
| `edges[].from_id` | string |  |  | Existing node ID for the edge source (alternative to from_idx) |
| `edges[].from_idx` | integer |  |  | Slot index into nodes[] for the edge source (-1/absent when using from_id) |
| `edges[].last_validated` | string |  |  | RFC3339 timestamp the linker stamps when (re-)asserting the edge. |
| `edges[].method` | string |  |  | Short tag describing how the edge was derived (e.g. 'manual', 'plan-section'). A relates-to edge from a plan_annotation MUST carry 'plan-annotation'. |
| `edges[].to_id` | string |  |  | Existing node ID for the edge target (alternative to to_idx) |
| `edges[].to_idx` | integer |  |  | Slot index into nodes[] for the edge target (-1/absent when using to_id) |
| `edges[].type` | string |  |  | Relationship type (required) |
| `edges[].weight` | number |  |  | Caller-asserted edge weight, stored verbatim. |
| `enforcement` | string |  |  | Rule enforcement mechanism |
| `evidence` | string |  |  | Supporting evidence (for findings) |
| `expand_to_descendants` | boolean |  |  | When updating status to a TERMINAL status on a project/ticket/plan/phase/step/test_plan/test_step, also walk the contains tree and write the mapped descendant status to every unsettled descendant whose own type is one of those seven container types. Every other descendant — criterion, question, finding, research, decision, thought, and any other type — is left unchanged: those nodes record evidence rather than task progress, so a container closing above them is evidence of none of it. The response names every id it wrote and every node it left alone. Default true. Set false to update only the named node. This is a SINGLE-ID container path — a batch of container ids carrying a status (ids:[...]) is rejected, so issue container status updates per-id. The cascade never completes a descendant that still has criteria that are not yet evaluated: those nodes are held, named in the response, and completed explicitly once their criteria are marked. The mapped descendant status is completed for completed/done/closed/archived, skipped for canceled/cancelled/wont_do/failed, and superseded for superseded; it applies for a tracker-backed container too. Has no effect for a non-terminal status or a non-container type. |
| `findings` | string |  |  | Comma-separated finding node IDs for answer operation |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured per operation: create→{ids}; link→{from, to, relationship}; answer→{id, name, conclusion}; update→{ids, fields}; delete→{deleted, total, ids}). |
| `from` | string |  |  | Source node ID for link operation |
| `graph` | string |  |  | Target graph for the operation (default: knowledge). Use 'practice' for prose guidance — ONE combined graph that takes no language on a write, where a create groups its node under an origin with source_hub — or 'checks' — a single graph, no language or name — for deterministic corpus checks and their fixture example nodes; each check carries its own 'language' metadata key, and a check write is admitted only after its fixtures run. 'linkage' is valid HERE on operation=unlink — it is how a cross-graph linkage edge is retracted — and the endpoint must be the PROXY id the link materialized (to:"proxy:knowledge:<code-id>"), because unlink resolves no raw foreign id the way link does. On operation=link the linkage graph is named by link_graph instead, and link accepts the raw foreign id. A collected graph is addressed the same way every other family is, by its own instance selector: 'code' with repo, and a registered custom type with name. |
| `id` | string |  |  | Target node ID for update or answer (operation=answer also accepts question_id as an alias) |
| `ids` | array of string |  |  | List of node IDs for a batch update over PLAIN LOCAL NON-CONTAINER nodes — the same universal-scalar set_fields (e.g. status) are applied uniformly to every id. Tracker-backed nodes, container nodes (project/ticket/plan/phase/step/test_plan/test_step — for status updates of ANY value), per-type params (command/scope/...), and source must be updated per-id; those batch shapes are rejected. For heterogeneous per-id bodies use update_batch. |
| `ids[]` | string |  |  |  |
| `items` | array of object |  |  | For operation=update_batch: per-item array; each entry carries {id, summary, keywords, description, binary_vector (base64), metadata, status, embed_identity}. Single store.Txn wraps every item — all-or-nothing. Per-item validation mirrors single-item update (length checks on binary_vector, backend-tagged metadata rejection). An entry carrying any OTHER key is REFUSED naming it — an undeclared key is dropped at decode, so accepting it would return success having written none of it. Used by the client-side LLM pipeline for high-throughput writeback so per-batch RPC count stays at 1. |
| `items[]` | object |  |  | Per-item shape: {id (required), summary?, keywords?, description?, binary_vector? (base64 → 32 bytes), metadata?, status?, embed_identity?} |
| `items[].binary_vector` | string |  |  | Base64-encoded binary embedding (32 bytes / 256-bit decoded) |
| `items[].description` | string |  |  | Node body to set (unset = untouched, present-and-empty = a deliberate clear). This is the per-item carrier for a plan section's body, so revising several sections of a chunked plan is one batch rather than one call each. It is BM25-indexed, so an item setting it is re-indexed. |
| `items[].embed_identity` | object |  |  | The embedder that produced binary_vector. A writeback under an identity the target graph did not record is refused rather than stored; unset on summary and metadata writes, which produce no vector. |
| `items[].id` | string |  |  | Target node ID (required) |
| `items[].keywords` | string |  |  | BM25 keyword-token boost string |
| `items[].metadata` | object |  |  | Key-value metadata pairs merged per-key |
| `items[].status` | string |  |  | Status to set on this item (nil = untouched) |
| `items[].summary` | string |  |  | Search-optimized one-line summary (max length: 500) |
| `keywords` | string |  |  | Sets Node.Keywords (top-level struct field, NOT a metadata key). Powers the search BM25 keyword-token boost and the keywords display facet. Wired for the client-side LLM pipeline writeback path; carrying the value in metadata would land it in the inline map and bypass the search-side reader. |
| `language` | string |  |  | REFUSED on every practice mutate arm: practice is ONE combined graph, so a write has no per-language graph to land in, and a create groups its node under an origin with source_hub instead. It is READ by the checks family, where it is the corpus language of the check node being written. |
| `last_validated` | string |  |  | Edge metadata (operation=link only). RFC3339 timestamp the linker stamps when (re-)asserting an edge. Routed into store.Edge.LastValidated. |
| `link_graph` | string |  |  | Optional graph selector for operation=link (e.g. 'linkage' for the cross-graph linkage view). When set, the link is dispatched via store.LinkBatch against the named graph rather than the default knowledge graph. LINK ONLY: operation=unlink does not route this param and rejects it — an unlink names the linkage graph with `graph` and targets the proxy id (see the graph param). |
| `links` | array of string |  |  | Node IDs to relate the new node to — any single knowledge-graph create routes it, as does a thought create. Knowledge-graph IDs ride the atomic create as a node--relates-to-->target edge; foreign-graph IDs are linked post-create via the cross-graph linkage. An unresolvable ID is dropped with a warning, never blocking the write. create_batch rejects it pre-write naming the field — context-linking is a capability that path does not have, not a param it drops. |
| `links[]` | string |  |  |  |
| `metadata` | object |  |  | Arbitrary key-value metadata pairs (string→string). On create: sets the node's initial metadata map. On update: merged per-key into existing metadata — keys in the payload overwrite, absent keys are preserved. Retrievable via Node.Value(key). |
| `method` | string |  |  | Edge metadata (operation=link only). Short tag describing how the edge was derived (e.g. 'image-target', 'dockerfile-copy', 'manual'). Routed into store.Edge.Method. |
| `name` | string |  |  | Node name or title |
| `nodes` | array of object |  |  | For operation=create_batch: per-node array; each entry carries {type, name, description, summary, content, status, metadata}. Created in a single store.Txn alongside the edges[] payload — all-or-nothing. Returns {ids:[...]} of length len(nodes). Not knowledge-graph-only: the arm runs on the practice graph as well, where source_hub groups the created nodes under their origin hub. It also honors a per-node id (which becomes the created node's id) and a per-node source, neither of which this item shape declares. |
| `nodes[]` | object |  |  | Per-node shape: {type (required), name, description, summary, content, status, metadata} |
| `nodes[].content` | string |  |  | Full content body |
| `nodes[].description` | string |  |  | Node description |
| `nodes[].metadata` | object |  |  | Initial key-value metadata pairs |
| `nodes[].name` | string |  |  | Node name or title |
| `nodes[].status` | string |  |  | Initial status |
| `nodes[].summary` | string |  |  | Search-optimized one-line summary (max length: 500) |
| `nodes[].type` | string |  |  | Node type (required) |
| `operation` | string | yes | create, create_batch, update, update_batch, bulk_update_metadata, upsert, link, unlink, answer, delete | What to do |
| `polarity` | string |  | positive, negative | Charge polarity (mutate(create, type=charge) only). Must be 'positive' or 'negative'. |
| `question_id` | string |  |  | Research question node ID this finding answers |
| `reasoning` | string |  |  | Why this charge applies (mutate(create, type=charge) only). |
| `references` | array of object |  |  | Citations for findings: [{url, title, summary}, {file, title, summary}, or {node_id, title}] |
| `references[]` | object |  |  | Reference object |
| `references[].file` | string |  |  | File path cited (alternative to url) |
| `references[].node_id` | string |  |  | Knowledge node ID cited (alternative to url/file) |
| `references[].summary` | string |  |  | Required search-optimized one-line summary of the cited reference, max 500 chars. NOT accepted on a node_id entry, which creates no node. (max length: 500) |
| `references[].title` | string |  |  | Human-readable title of the citation |
| `references[].url` | string |  |  | URL of the cited source |
| `relationship` | string |  |  | Relationship type for link (e.g., depends-on, contains, informed-by, relates-to) |
| `repo` | string |  |  | Code graph name — REQUIRED for graph='code'; it is never inferred from cwd. Writes to a collected graph are caller-owned: a later collect reconciles that graph from its source and overwrites hand-authored changes. |
| `scope` | string |  |  | Rule scope (e.g., '*.go', 'pkg/', 'commits') |
| `session` | string |  |  | Session name to group the created node under via session--contains-->node — any single knowledge-graph create routes it, as does a thought create. Creates the session if new. create_batch rejects it pre-write naming the field — context-linking is a capability that path does not have, not a param it drops. |
| `source` | string |  |  | Source of the knowledge (the node's provenance). NOT the practice source hub — that is `source_hub`. |
| `source_hub` | string |  |  | Practice SOURCE HUB id. On a CREATE it groups the write under one origin: it stamps the hub id onto the node's `source_hub` metadata and links the node to the hub by a `sourced-from` edge, and it REPLACES `language`, which is refused on every practice write arm. On a DELETE it is a SELECTION axis instead — it removes every practice node grouped under that hub, and the hub with them, which is how an abandoned or failed collection is cleaned up as a unit. It is ONE write: a hub carries its OWN id under the `source_hub` key, so the single metadata predicate that selects the members selects the hub too, atomically. Mutually exclusive with ids/id there: a delete carrying both selection axes is denied rather than resolved. On a practice LINK or UNLINK it is neither: it SCOPES THE ENDPOINTS, so `from` and `to` must both already be grouped under that hub and an endpoint under another hub, under none, or absent is refused naming the hub, the endpoint and its actual hub or absence. On a practice UPDATE, UPDATE_BATCH or BULK_UPDATE_METADATA it scopes the TARGETS the same way: every id the call names — `id`, each of `ids`, each `items[].id`, each `updates[].id` — must already be grouped under that hub, and one that is not refuses the whole call with nothing written. On a practice UPSERT it scopes the key the same way when the key RESOLVES. An UPSERT never GROUPS: a hub-carrying upsert whose `id` resolves to no practice node is REFUSED naming `mutate(create, graph:"practice", source_hub:...)` as the arm that groups, because grouping writes the `source_hub` key AND the `sourced-from` edge together and only a create plan carries both. Omitted on any of them means the whole practice graph, and nothing hub-related reaches the wire except on the create shapes, where the grouping IS the write, and on an upsert of an existing member. AN UPSERT NEVER UN-GROUPS: it REPLACES the body's metadata rather than merging into it, so a member's stored hub is carried onto the body whether or not the call names one, and an ordinary hub-less field edit keeps its membership instead of dropping the `source_hub` key and leaving the `sourced-from` edge behind. That costs the call one read. A body that names the hub the node is ALREADY under is a second spelling and is left as written; a body naming any OTHER hub is REFUSED naming both, because a body key rides no `sourced-from` edge and would leave the node filed under one hub and linked to another. A MEMBER IS NEVER MOVED BETWEEN HUBS on this surface: no arm rewrites both carriers together. An upsert of a new id or of a node grouped under no hub is unchanged. THE HUB MUST NAME A HUB: the id is resolved before any arm acts on it and must be a live node of type `source` in the combined practice graph, so an id that resolves to nothing, names a deleted node, or names a node of another type is REFUSED naming the hub and the arm. A HUB IS GROUPED UNDER NOTHING BUT ITSELF: it carries its OWN id under the `source_hub` key, which is the contract every producer writes and what makes the by-hub delete one predicate; a `source` node keyed to some OTHER node is refused, because that would make the hub a node belongs to a chain rather than a value. THESE RULES RUN IN THE ENGINE, beneath every caller — the mutate tool, the standalone `delete` tool and the direct compile-and-execute callers alike — so no route reaches a practice write without them; that resolution is one read, folded into the read the hub-scoped arms already pay. AND A BODY ALONE NEVER GROUPS: on a CREATE or CREATE_BATCH naming no `source_hub` parameter, a `source_hub` metadata key inside a body is REFUSED naming this parameter, because the body key writes the metadata without the edge. THE MEMBERSHIP EDGE IS NEVER WRITTEN BY HAND: on graph practice a `link` or `unlink` naming `relationship`="sourced-from" is REFUSED whether or not this parameter is present, because membership is recorded on the metadata key AND that edge together and an edge arm carries only one of them; a node is grouped by a create and a whole collection removed by the by-hub delete. Any other relationship is untouched. A BODY MAY NOT NAME A DIFFERENT HUB: on CREATE, CREATE_BATCH, UPSERT, UPDATE, UPDATE_BATCH and BULK_UPDATE_METADATA, a `source_hub` metadata key inside a body — the top-level `metadata`, a `nodes[]` body, an `items[]` or `updates[]` entry — that names a DIFFERENT hub than this parameter is REFUSED naming the body's path, its hub and this one, with nothing written. A body naming the SAME hub is a second spelling of one fact and lands byte-identically to a body naming none, which is stamped. IT IS A PRACTICE-FAMILY PARAMETER ON EVERY ARM. Every operation carrying it — create, create_batch, update, update_batch, bulk_update_metadata, upsert, link, unlink, answer, delete — is REFUSED naming the family on every family but practice: "knowledge", "code", "linkage", "checks", "web", "pdf", and any registered custom family. An omitted `graph` is the knowledge family and is refused with it. It is never consumed and dropped, because no other family has source hubs for it to name. The standalone `delete` tool publishes the same axis as `source`, and it runs the same resolution: a `source` that does not name a live hub is refused there too, never deleted. |
| `status` | string |  |  | Status to set (for update operation). An explicit empty string CLEARS the status to blank on a local node — the one param whose empty value is a write rather than an omission. Rejected on tracker-backed nodes, whose tracker has no blank state. |
| `step_id` | string |  |  | Step node ID to attach a criterion to |
| `summary` | string |  |  | Required search-optimized one-line summary, max 500 chars, when create-ing an embed-only-knowledge node type (NodeType.Summarizable()=false). Handler-side enforcement returns an error when missing/empty/whitespace or > 500 chars. A criterion create requires it on the same terms as every other embed-only-knowledge type — criterion summaries are author-supplied, never composed from the description and command. mutate(answer) requires it too: supply a summary describing the CONCLUDED state of the question, since answering replaces the summary the question was created with. (max length: 500) |
| `supports` | string |  |  | Node ID this finding supports — draws a finding--supports-->node edge as the finding is created. Read ONLY by mutate(create, type=finding); every other operation rejects it. |
| `thought_parent` | string |  |  | Parent thought ID the charge attaches to (mutate(create, type=charge) only). |
| `ticket_id` | string |  |  | Active ticket/project ID — born-linked as ticket--contains-->node so the created node is grouped under the work item that produced it; any single knowledge-graph create routes it. An unresolvable ticket_id is dropped with a warning, never blocking the write. create_batch rejects it pre-write naming the field — born-linking a batch is a capability that path does not have, not a param it drops. |
| `to` | string |  |  | Target node ID for link operation |
| `type` | string |  |  | Node type for create (finding, research, rule, criterion, resource, event, memory, document). criterion is knowledge-graph-only: criteria attach to the plan/step verifies structure, which no other graph family carries, so a criterion create naming any graph — including an explicit graph:"knowledge" — is rejected pre-write rather than routed. |
| `updates` | array of object |  |  | For operation=bulk_update_metadata: per-item array; each entry carries {id (required), metadata (required, non-empty)}. Single store.Txn wraps every item — all-or-nothing. Backend-tagged metadata rejects the whole batch. Used by client-side cluster persistence + propagation writeback so per-batch RPC count stays at 1 regardless of node count. |
| `updates[]` | object |  |  | Per-item shape: {id (required), metadata (required, non-empty map)} |
| `updates[].id` | string |  |  | Target node ID (required) |
| `updates[].metadata` | object |  |  | Key-value metadata pairs (required, non-empty) |
| `verified_quote` | string |  |  | Negation-gate proof of work — a TOP-LEVEL param on the call, NOT a metadata key and NOT edge_evidence. REQUIRED for negation-class calls: mutate(link, relationship:"contradicts") and mutate(update, status:"invalidated"). Must be a verbatim substring of the TARGET node's CURRENT source (whitespace-normalized before matching). Consumed by the gate before any write and never persisted; supplying it on a non-negation call is rejected. |
| `weight` | number |  |  | Charge weight 1-10 (mutate(create, type=charge) only). Significance of the evidence. |
<!-- END GENERATED: params -->
