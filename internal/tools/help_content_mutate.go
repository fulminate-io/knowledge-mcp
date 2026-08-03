// SPDX-License-Identifier: Apache-2.0

package tools

const helpMutate = `# mutate — Create, update, and link knowledge nodes

## operation: create
  mutate({ "operation": "create", "type": "finding", "name": "Title",
           "description": "...", "summary": "search-optimized one-liner" })
  mutate({ "operation": "create", "type": "rule", "name": "500-line limit",
           "description": "...", "scope": "*.go", "enforcement": "pre-commit hook" })

  Supported types: finding, research, rule, criterion, resource, event, observation,
                   memory, document, agent, skill, tool_guide, test_plan, test_step

  summary is REQUIRED for embed-only-knowledge types (NodeType.Summarizable()=false):
    finding, document, pattern, tool_guide, reference, use_case, resource, example,
    memory, event. Handler enforces non-empty + 500-char cap and returns a structured
    error naming "mutate(create)" if missing.
  Q5 locked: rule and criterion KEEP auto-synthesized Summary (no summary required).
  Q2 locked: research goes through handleRecordResearch (auto-synthesized).

  Naming for retrieval: the node "name" and the first sentence of "description"
  are what BM25 matches when this node is recalled later. State the concept in
  plain, searchable terms — a vague title ("Notes", "Fix", "Update") is
  search-invisible; name for the thing a future reader would type to find it.

  Context links (optional, born-linked, fail-tolerant): on finding/research/rule
  create, pass ticket_id to group the node under its work item
  (ticket--contains-->node), session to group it under a working session, and
  links (node IDs) to relate it to the touched code/knowledge. An unresolvable
  target is dropped with a warning, never blocking the write.

  Special fields:
    criterion:  step_id (required), criterion_type (automated|manual), command
    finding:    question_id (auto-links via "answers" edge)
    decision:   use record_decision instead (richer schema)

## operation: update
  mutate({ "operation": "update", "id": "node_id", "status": "completed" })
  mutate({ "operation": "update", "id": "node_id", "name": "New name", "description": "..." })
  mutate({ "operation": "update", "ids": ["id1", "id2"], "status": "completed" })

  Pipeline writeback fields (client-side LLM pipeline only):
    keywords:       sets Node.Keywords (top-level struct field, NOT a metadata
                    key). Powers the BM25 keyword-token boost and the keywords
                    facet.

  binary_vector is NOT a single-update field — there is no carrier for it on
  this path and supplying it is rejected. Install an embedding through
  update_batch items[].binary_vector below.

## operation: update_batch
  mutate({ "operation": "update_batch", "items": [
    { "id": "n1", "summary": "...", "keywords": "...", "binary_vector": "<base64>" },
    { "id": "n2", "metadata": { "summary_failure_reason": "" } }
  ] })

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
  per-batch RPC count stays at 1 (not N). backend-backed nodes are rejected
  with a pointer at the single-item update path — pipeline writeback never
  targets them by construction.

## operation: link
  mutate({ "operation": "link", "from": "finding_id", "to": "question_id", "relationship": "answers" })
  mutate({ "operation": "link", "from": "step_id", "to": "file:tools/help.go", "relationship": "implements" })
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
  - link relationship must be a valid edge type (see help("edge_types"))
  - file: prefix in link target auto-creates a resource node by path
  - graph:"practice" requires language param for all operations
`

const helpDelete = `# delete — Remove nodes or prune history

Deletes are SOFT by default: the node is tombstoned — hidden from every normal
read, but the data survives and is recoverable. Pass hard:true for PERMANENT,
irrecoverable removal (reserve for deliberate cleanup).

## Delete specific nodes
  delete({ "ids": ["node_id1", "node_id2"] })            — soft (tombstone)
  delete({ "ids": ["node_id1"], "hard": true })          — permanent removal

## Prune by age
  delete({ "older_than": "7d", "dry_run": true })  — preview
  delete({ "older_than": "7d" })                   — execute

## Practice graph deletion
  delete({ "ids": ["node_id"], "graph": "practice", "language": "go" })

## Gotchas
  - Deleting a node does NOT delete its edges
  - Pruning runs against creation time, not update time
  - Use dry_run first on large prunes
  - A malformed hard value DENIES the delete (it never guesses on a destructive op)
  - graph:"practice" requires language param
`
