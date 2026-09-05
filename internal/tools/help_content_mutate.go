// SPDX-License-Identifier: Apache-2.0

package tools

const helpMutate = `# mutate — Create, update, and link knowledge nodes

## operation: create
  mutate({ "operation": "create", "type": "finding", "name": "Title",
           "description": "...", "summary": "search-optimized one-liner" })
  mutate({ "operation": "create", "type": "rule", "name": "500-line limit",
           "description": "...", "scope": "*.go", "enforcement": "pre-commit hook" })

  Supported types: finding, research, rule, criterion, resource, event, memory,
                   document, agent, skill, tool_guide, test_plan, test_step

  summary is REQUIRED for embed-only-knowledge types (NodeType.Summarizable()=false):
    finding, document, pattern, tool_guide, reference, use_case, resource, example,
    memory, event. The cap and the non-empty rule behave DIFFERENTLY, and the
    difference is the one worth knowing:
      - an EMPTY summary is REJECTED with a structured error naming "mutate(create)".
      - an OVER-CAP summary is NOT rejected. It is clamped to 500 at a word
        boundary, the create SUCCEEDS, and a non-fatal warning is returned.
        The cap counts runes, not bytes. Author summaries that fit — detail
        past the cap is discarded, not stored.
  criterion, rule and research require an author-supplied summary too, on the
  same empty-rejected / over-cap-clamped terms as the embed-only list above.
  No type is exempt.

  Naming for retrieval: BM25 matches five weighted fields when this node is
  recalled later — name, summary and keywords at double weight, description
  and content at single. The WHOLE description is indexed, not just its first
  sentence, so length dilutes rather than truncates. State the concept in
  plain, searchable terms — a vague title ("Notes", "Fix", "Update") is
  search-invisible; name for the thing a future reader would type to find it.

  Context links (optional, born-linked, fail-tolerant): on ANY knowledge-graph create,
  pass ticket_id to group the node under its work item
  (ticket--contains-->node), session to group it under a working session, and
  links (node IDs) to relate it to the touched code/knowledge. An unresolvable
  target is dropped with a warning, never blocking the write.

  Special fields:
    criterion:  step_id (required), summary (required), criterion_type
                (automated|manual), command
    finding:    question_id (auto-links via "answers" edge)
    decision:   use record_decision instead (richer schema)

## operation: update
  mutate({ "operation": "update", "id": "node_id", "status": "completed" })
  mutate({ "operation": "update", "id": "node_id", "name": "New name", "description": "..." })
  mutate({ "operation": "update", "ids": ["id1", "id2"], "status": "completed" })

  Pipeline writeback fields (client-side LLM pipeline only):
    keywords:       sets Node.Keywords (top-level struct field, NOT a metadata
                    key). Indexed as its own double-weighted BM25 field, so a
                    term placed here outranks the same term in description.

  binary_vector is NOT a single-update field — there is no carrier for it on
  this path and supplying it is rejected. Install an embedding through
  update_batch items[].binary_vector below.

  SUMMARY ON UPDATE. An update that does not state a summary forwards none at
  all — whatever else it changes, the stored value is KEPT and the response says
  so. Pass an explicit summary to replace it; that always wins, verbatim and at
  any length. No type diverges, because nothing composes a summary on any path:
  every summary is written by its author. The response's "Summary:" line names
  which of the two happened.

## operation: update_batch
  mutate({ "operation": "update_batch", "items": [
    { "id": "n1", "summary": "...", "keywords": "...", "binary_vector": "<base64>" },
    { "id": "n2", "metadata": { "summary_failure_reason": "" } },
    { "id": "n3", "description": "the section body, rewritten" }
  ] })

  items[].description carries the node BODY, under the same set/unset contract as
  summary, keywords and status: absent leaves it untouched, and a present-but-empty
  value is a deliberate clear. It is what makes revising several sections of a
  chunked plan ONE call rather than one call per section. It is BM25-indexed, so an
  item that sets it is re-indexed with the batch.

  A key items[] does NOT declare is REFUSED naming it, rather than dropped: an
  undeclared key dies at decode, so accepting it would return success having
  written none of it. The declared set is {id, summary, keywords, description,
  binary_vector, metadata, status, embed_identity}.

  items[].binary_vector is the ONLY binary-embedding carrier: a base64-encoded
  binary embedding whose decoded length must equal 32 bytes (256-bit), installed
  via PutBinaryVector on the write target's overlay layer. A mismatched length
  rejects with a structured error and no write is performed.

  all-or-nothing: every item is applied inside a single store transaction with
  one commit at the end. Per-item validation mirrors single-item update —
  binary_vector length check, missing id rejection, backend-tagged metadata
  rejection. ANY validation failure rejects the whole batch before any write
  (zero commits observed when validation fails).

  Used by the client-side LLM pipeline for high-throughput writeback so the
  per-batch RPC count stays at 1 (not N). The backend-tagged check reads the
  ITEM's own metadata, NOT the target node: an item carrying a "backend" key
  is rejected with a pointer at the single-item update path, but a node that
  is itself tracker-backed is not detected here and its edit lands locally
  without syncing. Update tracker-backed nodes one at a time, through
  mutate(update), so the client intercept fires.

## operation: create_batch
  mutate({ "operation": "create_batch",
           "nodes": [{ "type": "step", "name": "...", "summary": "..." },
                     { "type": "criterion", "name": "...", "summary": "..." }],
           "edges": [{ "from_idx": 0, "to_idx": 1, "type": "contains" },
                     { "from_idx": 1, "to_idx": 0, "type": "verifies" }] })

  N nodes + M edges in ONE atomic store transaction; returns {ids:[...]} of
  length len(nodes). Knowledge-graph only. An edge endpoint is EITHER a slot
  index into nodes[] (from_idx/to_idx) OR an existing node ID (from_id/to_id).
  ATTACHING A CRITERION TO A STEP TAKES THE PAIR ABOVE, not one edge —
  step--contains-->criterion AND criterion--verifies-->step, the same pair
  create_plan emits. plan_tree walks contains only, so a criterion attached by
  verifies alone is invisible in the rendered tree; a batch carrying one
  direction without its partner is REJECTED pre-write naming the missing edge,
  never auto-completed.
  It has NO context-linking: ticket_id, session and links are REJECTED pre-write
  naming the field, because born-linking is a capability this path lacks rather
  than a param it silently drops.

  AN EDGE ALSO CARRIES the optional metadata fields weight, confidence, method,
  evidence and last_validated, stored verbatim. A relates-to edge whose source is
  a plan_annotation MUST carry that annotation's kind and tier on method and
  evidence, in either endpoint spelling, or the batch is refused naming the exact
  values to send: the annotation's severity is readable from the edge, and an edge
  disagreeing with its node reports a severity nobody wrote. Attaching an
  annotation with mutate(create, type:"plan_annotation", links:["<section id>"])
  writes both carriers for you.

## operation: upsert
  mutate({ "operation": "upsert", "id": "caller-supplied-id", "type": "worker", ... })

  Create-or-update by a caller-supplied id. FIVE types bypass create-time
  validation — proxy, worker, graph_type_def, log-backend, criterion — because
  they are tool-owned config records whose producers already validated them.
  Every OTHER type runs the full create-time validation (summary/name plus the
  system-managed-type guard), so upsert is NOT a general escape hatch around
  create.

## operation: bulk_update_metadata
  mutate({ "operation": "bulk_update_metadata", "updates": [
    { "id": "n1", "metadata": { "cluster_id": "c7" } },
    { "id": "n2", "metadata": { "cluster_id": "c7" } }
  ] })

  Per-item {id, metadata} — both required, and the metadata map must be
  non-empty. One store transaction wraps every item (all-or-nothing);
  backend-tagged metadata rejects the whole batch. Used by client-side cluster
  persistence + propagation writeback so the per-batch RPC count stays at 1
  regardless of node count.

## operation: delete
  mutate({ "operation": "delete", "ids": ["n1", "n2"] })

  The same delete the standalone delete tool performs — both lower through one
  shared compile path. Use whichever spelling suits the call site; see
  help("delete") for the soft/hard semantics and the prune-by-age status.

## operation: link
  mutate({ "operation": "link", "from": "finding_id", "to": "question_id", "relationship": "answers" })
  mutate({ "operation": "link", "from": "step_id", "to": "path/to/file.go", "relationship": "implements" })
  mutate({ "operation": "link", "from": "agent_id", "to": "guide_id", "relationship": "uses" })
  mutate({ "operation": "link", "from": "rule_id", "to": "agent_id", "relationship": "constrains" })

## operation: unlink
  mutate({ "operation": "unlink", "from": "ticket_id", "to": "pattern_id", "relationship": "uses" })
  Idempotent — returns success even if no matching edge existed. Use to
  retract a misattached pattern, fix a wrong informed-by, or remove any
  stale edge identified by (from, to, relationship).

## operation: answer
  mutate({ "operation": "answer", "id": "question_id", "concludes": true,
           "conclusion": "Finding: X", "findings": "fid1,fid2" })

## Practice graph operations
  mutate({ "operation": "create", "type": "finding", "name": "...", "graph": "practice", "language": "go" })
  mutate({ "operation": "update", "id": "node_id", "description": "...", "graph": "practice", "language": "go" })
  mutate({ "operation": "link", "from": "a", "to": "b", "relationship": "relates-to", "graph": "practice", "language": "go" })

  Cross-graph linking (knowledge node → practice node, creates proxy):
  mutate({ "operation": "link", "from": "agent_id", "to": "practice_node_id",
           "relationship": "uses", "graph": "practice", "language": "go" })

## Gotchas
  - "ids" (plural) for batch update; "id" (singular) for single update
  - link relationship is NOT validated against a vocabulary: an unrecognized
    string is stored as a new edge type rather than rejected, so a typo
    silently creates one. Use help("edge_types") to pick an existing type.
  - a link target that is not a knowledge node is resolved against the other
    graphs. A code-graph node ID — the repo-relative path of a whole source
    file, or that path plus a colon plus the symbol name for a single symbol —
    materializes a proxy in the knowledge graph, and the edge is written to
    that proxy.
  - the target must be a code-graph node ID from YOUR OWN indexed repo. A
    target that resolves in no graph is REJECTED with
    "link endpoint(s) ... not found"; it is never silently created.
  - associating a step with its source paths needs no link at all: create_plan
    persists a step's paths as file_paths metadata.
  - graph:"practice" requires language param for all operations
`

const helpDelete = `# delete — Remove nodes or prune history

Deletes are SOFT by default: the node is tombstoned — hidden from every normal
read, but the data survives and is recoverable. Pass hard:true for PERMANENT,
irrecoverable removal (reserve for deliberate cleanup).

## Delete specific nodes
  delete({ "ids": ["node_id1", "node_id2"] })            — soft (tombstone)
  delete({ "ids": ["node_id1"], "hard": true })          — permanent removal

## Prune by age — NOT CURRENTLY AVAILABLE
  Prune-by-age selects on node TYPE, and no node type is retention-eligible
  today, so every older_than form is refused rather than run: the real call is
  denied at compile, and the dry_run form answers "dry_run requires either
  ids[] or a valid older_than + a retention-eligible type". Delete by ids.

## Practice graph deletion
  delete({ "ids": ["node_id"], "graph": "practice", "language": "go" })

## Gotchas
  - a SOFT delete does NOT remove the node's edges — only the node itself is
    tombstoned. A HARD delete sweeps every incident edge along with the node,
    so the two paths differ here and the difference is not recoverable.
  - dry_run previews an ids delete: it reports what WOULD be removed and
    removes nothing
  - A malformed hard value DENIES the delete (it never guesses on a destructive op)
  - graph:"practice" requires language param
`
