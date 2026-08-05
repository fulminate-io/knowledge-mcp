// SPDX-License-Identifier: Apache-2.0

package tools

const helpOverview = `# Knowledge Graph — Tool Reference (first-class tools + generic primitives + sync)

## First-class tools (self-documenting schemas)

| Tool              | Purpose                                                                |
|-------------------|------------------------------------------------------------------------|
| thoughts          | Persistent reasoning graph: think / charge / recall / trace / propagate |
| create_project    | Create a project container node (holds tickets and plans)              |
| create_ticket     | Create a ticket node inside a project                                  |
| create_plan       | Batch-create plan → phases → steps → criteria                          |
| create_research   | Batch-create research with nested questions                            |
| create_test_plan  | Create a structured test plan with steps and criteria                  |
| record_decision   | Record a design decision with choice, rationale, alternatives          |
| search            | Unified search across code, knowledge, practice, cloud graphs          |
| file_symbols      | List all symbols in a file with optional source                        |
| assemble          | Type-aware context assembly for plans, agents, test plans              |
| collect           | Run local collectors (client-side indexer bootstrap)                   |
| help              | Reference docs for tools, node types, edge types, workflows            |

## Generic tools (use help("<name>") for full docs)

| Tool     | Purpose                                                              |
|----------|----------------------------------------------------------------------|
| query    | Search nodes, get by ID, browse by type, special modes (stats, etc.) |
| traverse | Edge-first graph traversal with direction + edge_types + graph       |
| mutate   | Create, update, link knowledge nodes                                 |
| delete   | Remove nodes by ID or prune by age                                   |
| manage   | Server ops: status, clear_llm_failures, branches, prune, rebuild_cache, rebuild_segments |
| ast      | Structural code search: tree-sitter pattern DSL, most indexed langs  |

## Sync tool (requires sync license scope)

| Tool | Purpose                                                          |
|------|------------------------------------------------------------------|
| sync | Bidirectional knowledge graph sync (push, pull, promote)         |

## Reference topics

  help("node_types")   — all node types, fields, and when to use each
  help("edge_types")   — all edge types grouped by category
  help("statuses")     — status values per node type
  help("workflows")    — common multi-tool patterns
  help("logs")         — ephemeral log graph workflow: configure → collect → query/search/traverse → discard
  help("patterns")     — pattern catalog (project + library practice graphs)
  help("recipes")      — recipe DSL grammar + semantics (graph→graph transformer)
  help("topology")     — analyzer registry, query(mode="topology") dispatch, adding new analyzers

Tool-specific topics: help("query"), help("traverse"), help("mutate"), help("delete"), help("manage"), help("ast"), help("thoughts")

## Quick-start pattern

  thoughts(recall) → thoughts(think) → implement → thoughts(charge) → mutate(status:completed)

## summary is required on every embed-only-knowledge node creator

Pipeline v2 marks LLM-authored knowledge nodes (decision, finding, document,
pattern, project, ticket, plan, phase, step, agent, skill, etc.) as embed-
only-no-summarize. The CREATOR (you) must author a search-optimized one-line
summary at creation time so search quality survives the pipeline change.
500-char cap, handler-side validation, structured error if missing.
record_decision / criterion / rule keep their auto-synthesized Summary;
think keeps the SymbolName / first-line-of-content convention.

## Read consistency — there is no stale-read window

Reads are read-your-writes and cross-session fresh. Writes are synchronous:
a successful mutate means the backend applied it, so every later read —
yours or another session's — sees it. Sessions are a client-side concept;
the server holds no per-session view of the graph.

Measured: one session writes, a second session's query(id) / search /
plan_tree / traverse reflects it in ~30 ms against a local file-backed
server and ~85 ms against a remote-backed one, flat under concurrent load.

So when you disagree with another agent about a node's contents, "I read
stale data" is not an available explanation. The likely one is read-then-
report skew: you loaded the text minutes ago and it was revised while you
reasoned. Re-fetch immediately before filing a finding or negating someone
else's claim, and cite the node's updated_at — rendered on every ID line in
plan_tree and assemble, and present in the by-id JSON — so a reader can
tell "read before the revision" from "read after".
`

const helpNodeTypes = `# Node Types

## Pipeline types (created by code indexer)
  file          — source file (one per indexed file)
  package       — directory/package (created by hierarchy builder)
  branch        — branch metadata node

## Knowledge types (created by users/LLM)
  project       — top-level container for related work; holds tickets and plans. Fields: name, description, status
  ticket        — unit of work within a project; holds plans. Fields: name, description, status, metadata.project_id
  plan          — body of work; contains phases. Hierarchy: project→ticket→plan→phase→step→criterion. Fields: name, description, status
  phase         — stage within a plan; contains steps. Fields: name, description, status
  step          — implementation task; contains criteria. Fields: name, description, status, metadata.file_paths
  criterion     — success criterion. Fields: name, metadata.type (automated|manual), metadata.command
  decision      — design choice. Fields: name, description, metadata: choice, rationale, alternatives
  finding       — discovery from research. Fields: name, description, metadata.evidence
  memory        — persistent fact or preference
  research      — research project container; contains questions
  question      — research sub-question. status: open | investigating | answered
  reference     — external source: paper, URL, tool
  resource      — code artifact reference: file, package, function
  event         — something that happened: commit, deploy, incident
  document      — general document: plan, spec, notes
  github_repo   — root anchor for a github materialization (owner, repo, ref); emitted by the web collector. Metadata: owner, repo, ref, source_url, materialized_at
  session       — groups tool calls within one Claude session
  rule          — codebase constraint. Fields: name, description, scope, enforcement
  test_plan     — structured test plan with steps
  test_step     — individual test step within a test plan
  test_run      — execution instance. status: pending|pass|fail|skip. metadata.run_session
  agent         — AI agent definition with phases and tool guides
  skill         — reusable skill or capability
  tool_guide    — guidance doc for using a specific tool
  pattern       — canonical architectural shape. Project graph (practice/<project>-architecture.bin) stores concrete instantiations; library graph (practice/design-patterns.bin) stores generic templates. Fields: name, summary, shape, when_to_use, when_not_to_use, anti_patterns; project entries also: exemplar_ids, file_locations, registration_snippet
  reuse_check   — search-before-implement audit, linked to a plan step. Fields: searches_run, top_results, decision, step_id

## Thought graph types
  thought         — unit of reasoning. status: hypothesized | validated | invalidated
  charge          — evidence charge on a thought (polarity: positive|negative, weight: 1-10)
  thought_session — groups thoughts about one concern

## Multi-root types
  proxy     — lightweight reference to a node in another graph
  tombstone — marks a node deleted in a branch overlay

## Cloud types (created by cloud collectors)
  cloud-resource — cloud infrastructure resource (EC2, VPC, IAM role, GCS bucket, etc.)
`

const helpEdgeTypes = `# Edge Types

## Code edges (uppercase, from static analysis)
  CALLS, IMPORTS, CONTAINS, USES_TYPE

## Knowledge edges (lowercase)
  contains        — parent → child (plan→phase, phase→step, step→criterion)
  depends-on      — must complete before (step ordering)
  blocks          — prevents progress
  verifies        — criterion → step it verifies
  informed-by     — decision ← finding/research that drove it
  contradicts     — new finding → old finding it contradicts
  supersedes      — new node → old node it replaces
  supports        — evidence → decision it supports
  answers         — finding → research question it answers
  relates-to      — general association
  implements      — step → code resource it creates/modifies
  references      — finding → external reference (paper/URL)
  produced-by     — output of a work item
  used-in         — knowledge applied somewhere
  uses            — agent/skill → tool_guide it relies on; also plan → pattern it extends
  constrains      — rule → agent/skill it governs
  instantiates    — project pattern → library pattern it instantiates (per-project concrete → generic template)

## Thought edges
  next, branches-from, charged-by, evidenced-by, produced

## Cloud edges (uppercase)
  MOUNTS_SECRET, MOUNTS_CONFIGMAP, USES_SA, USES_PVC, SELECTS, ROUTES_TO,
  RESTRICTS, SCALES, BINDS_ROLE, BINDS_SUBJECT, USES_STORAGE_CLASS,
  GRANTS, USES_NETWORK, USES_SUBNET, USES_SECURITY_GROUP, TARGETS,
  ASSUMES_ROLE, WORKLOAD_IDENTITY, ISSUED_BY, USES_MIDDLEWARE, SINKS_TO
`

const helpStatuses = `# Status Values

## Work nodes (plan, phase, step, criterion)
  pending    — not started (default)
  active     — currently being worked on
  completed  — done and verified
  blocked    — cannot proceed (waiting on dependency or external)
  skipped    — intentionally not done

## Project nodes
  active     — project is active (default when empty)
  completed  — project is finished
  archived   — project is archived

## Ticket nodes
  open        — not started (default)
  in_progress — currently being worked on
  closed      — done

## Thought nodes
  hypothesized — initial state; not yet validated (default)
  validated    — confirmed by evidence (positive charges)
  invalidated  — disproven; spawn a branches-from thought instead of editing

## Research question nodes
  open         — not started
  investigating — being researched
  answered     — has a finding linked via "answers" edge

## Test run nodes
  pending | pass | fail | skip

## Updating status
  mutate({ "operation": "update", "id": "node_id", "status": "completed" })
  mutate({ "operation": "update", "ids": ["id1", "id2"], "status": "completed" })
`

const helpWorkflows = `# Common Multi-Tool Workflows

## Research → Plan → Implement
  1. create_research({ "name": "...", "questions": [...] })
  2. search / query to gather findings
  3. mutate(operation:"create", type:"finding") for each discovery
  4. mutate(operation:"link", from:finding_id, to:question_id, relationship:"answers")
  5. record_decision() for key choices
  6. create_plan({ "name": "...", "phases": [...] })
  7. assemble({ "id": plan_id }) → implement each step → mutate(status:"completed")

## Project → Ticket → Plan hierarchy
  1. create_project({ "name": "My Product" })        — top-level container
  2. create_ticket({ "name": "Feature X", "project_id": "proj_id" })  — unit of work
  3. create_plan({ "name": "Impl plan", "ticket_id": "ticket_id", "phases": [...] })
  4. query({ "mode": "plan_tree", "id": "plan_id" }) → implement steps

## Think → Charge → Recall (reasoning loop)
  1. think({ "content": "hypothesis", "summary": "one-line searchable gist", "session": "topic" })  → returns thought_id (summary REQUIRED)
  2. [do work, gather evidence]
  3. charge({ "thought": thought_id, "polarity": "positive|negative", "weight": 1-10, "reasoning": "..." })
  4. recall({ "query": "topic" })  — before starting new work, check past reasoning

## Test Plan → Run → Track results
  1. create_test_plan({ "name": "...", "steps": [...] })
  2. assemble({ "id": test_plan_id, "new_run": true })  — creates test_run nodes
  3. Execute tests manually or automatically
  4. mutate(operation:"update", id:run_id, status:"pass|fail|skip") for each run

## Creating instruction nodes (agents, skills, tool guides)
  1. mutate(operation:"create", type:"agent", name:"...", description:"...")
  2. mutate(operation:"create", type:"tool_guide", name:"query usage", description:"...")
  3. mutate(operation:"link", from:agent_id, to:tool_guide_id, relationship:"uses")
  4. mutate(operation:"link", from:rule_id, to:agent_id, relationship:"constrains")
  5. assemble({ "id": agent_id })  — renders full agent tree with guides + rules

## Finding → Decision → Plan traceability
  1. mutate(operation:"create", type:"finding", ...)
  2. record_decision({ "name": "...", "choice": "...", "rationale": "...", "informed_by": finding_id })
  3. create_plan({ ... })
  4. query({ "mode": "evidence", "id": decision_id })
`

const helpQuery = `# query — Unified search and node lookup

Design: query is a generic primitive. It dispatches on params: 'id' → direct lookup, 'text' → search, 'type' → browse, 'mode' → special operations. All modes respect the 'graph' selector (knowledge|code|cloud|practice|cicd|linkage|logs). Composite shortcuts ('mode: "plan_tree" | "lineage" | "evidence"') are exceptions justified by frequent use.

## Modes

### Text search (default)
  query({ "text": "authentication", "limit": 10 })
  query({ "text": "cache invalidation", "type": "decision" })

### Direct node lookup
  query({ "id": "abc123" })
  query({ "id": "abc123", "include_edges": true })

### Browse by type
  query({ "type": "decision" })
  query({ "type": "rule", "limit": 50 })

### Special modes
  query({ "mode": "stats" })                            — node-type + edge-type breakdowns for the current graph (the canonical discoverability primitive; works for any graph via 'graph:' selector)
  query({ "mode": "examine", "id": "x" })               — deep node inspection
  query({ "mode": "file_symbols", "path_prefix": "..." }) — code file symbols
  query({ "mode": "modules" })                          — code module listing
  query({ "mode": "timeline" })                         — thought timeline
  query({ "mode": "charges" })                          — thought charge summary
  query({ "mode": "clusters" })                         — thought cluster summary
  query({ "mode": "clusters", "type": "all" })          — all-node cluster summary (across all node types)

### Reflect modes
  query({ "mode": "personality" })   — reasoning personality profile
  query({ "mode": "influence" })     — most influential thoughts
  query({ "mode": "tensions" })      — conflicting thoughts
  query({ "mode": "blind_spots" })   — per-thought epistemic-risk facets (confident-but-untested, foundational-but-unexamined, fragile single-point, stale confidence, belief reversal); served O(1) from the reflection-loop cache
  query({ "mode": "summary" })       — concise thought summary

### Pre-embedded query vector (client-side LLM pipeline)
  query({ "text": "authentication", "query_vector": "<base64>" })

  Optional base64-encoded binary embedding (32 bytes / 256-bit decoded). When
  set, the server skips its local embedder and uses the supplied vector for
  hybrid search. Wired by the client-side LLM pipeline's InterceptQuery so the
  server stays unencumbered by Voyage API keys post-Phase-5. Decoded length
  mismatches return the same structured validation error as
  mutate(update_batch, items[].binary_vector=...).

### Recency-boosted search
  query({ "mode": "recent", "text": "authentication" })
  query({ "mode": "recent", "text": "cache eviction", "limit": 20 })
  query({ "mode": "recent" })                                  — most-recently-updated nodes, all types
  query({ "mode": "recent", "types": ["project", "ticket", "plan", "phase", "step", "question"] })

  With a text query: hybrid BM25+vector search with exponential temporal decay
  (half-life 30 days) — recently-updated nodes rank higher than semantically-equal
  but stale ones. Useful when you want the freshest relevant nodes — e.g., recent
  decisions, active plan steps, or newly created findings.

  Omit text for a pure recency browse (no search): the most-recently-updated
  nodes by UpdatedAt. Add types to scope it — e.g. a lightweight view of the
  most-recently-touched work items.

### Practice graph queries
  query({ "graph": "practice" })                                    — list all practice graphs
  query({ "graph": "practice", "language": "go" })                  — browse a language's practice graph
  query({ "graph": "practice", "language": "go", "text": "errors" }) — search within a practice graph
  query({ "id": "node_id", "graph": "practice", "language": "go" }) — look up a specific practice node

### Cloud graph queries
  query({ "graph": "cloud" })                                          — list all cloud graphs
  query({ "graph": "cloud", "account": "aws-prod" })                   — browse cloud resources
  query({ "graph": "cloud", "account": "aws-prod", "text": "web" })    — search within a cloud graph
  query({ "graph": "cloud", "account": "aws-prod", "resource_type": "ec2" }) — filter by resource type prefix
  query({ "id": "arn:aws:...", "graph": "cloud" })                     — look up a specific cloud resource (scans all accounts)
  query({ "id": "arn:aws:...", "graph": "cloud", "account": "aws-prod" }) — look up in a specific account

### Linkage graph queries
  query({ "graph": "linkage" })                                           — list linkage graph info
  query({ "graph": "linkage", "mode": "stats" })                         — linkage graph statistics with proxy breakdown
  query({ "graph": "linkage", "text": "deploy" })                        — search within the linkage graph
  query({ "id": "proxy:cloud:aws-prod:arn:...", "graph": "linkage" })     — look up a specific linkage proxy

### Log graph queries (ephemeral, per-query_id)
  query({ "graph": "logs", "name": "<query_id>" })                          — label overview ranked by error count
  query({ "graph": "logs", "name": "<query_id>", "text": "app=api severity>=WARN" }) — drill-down (AND-only label filters + severity range)
  query({ "graph": "logs", "name": "<query_id>", "id": "<template_id>" })   — template detail with decompressed example entries
  query({ "graph": "logs", "name": "<query_id>", "mode": "pivot",
          "rows": "reporting_instance", "cols": "reason" })                  — row×col matrix of log counts
  query({ "graph": "logs", "name": "<query_id>", "mode": "correlations" })   — every CORRELATES_WITH edge, sorted by score desc
  query({ "graph": "logs", "name": "<query_id>", "mode": "timeline" })        — templates ordered by FirstSeen (T+offset, alias, count, span)
  query({ "graph": "logs", "name": "<query_id>", "mode": "timeline",
          "extra": { "bucket": "10s" } })                                     — histogram rollup into fixed-width windows
  query({ "graph": "logs", "name": "<query_id>", "mode": "explain",
          "id": "<template_alias_or_id>" })                                   — per-correlation breakdown for one template
  query({ "graph": "logs", "name": "<query_id>", "mode": "explain",
          "extra": { "a": "<tplA>", "b": "<tplB>" } })                         — explain a specific correlation pair
  query({ "graph": "logs", "name": "<query_id>", "mode": "resolver" })         — per-stream cloud-resolution trace (resolved + unresolved)

  See help("logs") for the drill-down grammar, pivot defaults, alias
  conventions, and the full collect → query → traverse workflow.

### Topology mode (analyzer dispatch)
  query({ "mode": "topology", "graph": "code", "algorithm": "pagerank", "repo": "myrepo", "top_k": 20 })
  query({ "mode": "topology", "graph": "cloud", "algorithm": "scc", "account": "aws-prod" })

  Both 'graph' and 'algorithm' are REQUIRED — there is no default sweep, no
  paramless dump, and no linkage fallback. topology mode dispatches to a
  registered analyzer in the topology package: provide 'algorithm' (omit it
  to get the registered analyzer list in the error), plus the standard graph
  instance params ('repo' for code, 'account' for cloud, none for knowledge).
  Optional 'top_k' caps ranked findings; for
  code, 'path_prefix' restricts the analysis subset to nodes whose FilePath
  starts with the prefix; for cloud, 'resource_type' restricts to nodes
  whose 'resource_type' meta starts with the prefix. Output is JSON-encoded
  topology.Finding objects.

### Cross-graph link augmentation
  query({ "id": "any_node_id", "include_cross_links": true })
  Augments any node lookup with cross-graph links from the linkage graph.

## Key parameters
  text, id, type, mode, graph (knowledge|code|cloud|practice|linkage|logs|all), limit, offset,
  include_edges, include_cross_links, since, session, status, valence_min/max, magnitude_min,
  consistency_max, connected_to, language (for practice graph),
  account (for cloud graph), resource_type (for cloud graph), name (query_id for log graph)

## Gotchas
  - "examine" mode requires id
  - Thought filters (valence, session, magnitude) only apply in knowledge graph
  - graph:"all" searches both knowledge and code graphs simultaneously
  - Practice graph queries require language param (except when listing all practice graphs)
  - Cloud graph queries require account param for search/browse (omit to list all cloud graphs)
  - include_cross_links only works with id-based queries (not browse or search)
`

const helpTraverse = `# traverse — Edge-first graph traversal

## Primitive
  traverse(start, direction, edge_types[], graph, depth, limit, include_edge_metadata)

  direction: "out" (default), "in", or "both" (union deduped by node ID)
  edge_types: filter to specific edge types (omit for any edge)
  graph: target graph — "" or "knowledge" (default), "code", "cloud", "cicd",
         "practice", "logs", "linkage"

## Discovery
  Don't memorize edge types — discover them:
  query({ "mode": "stats", "graph": "code" })   — shows all node types and edge types

## Examples
  # Find callers of a function (code graph, incoming "calls" edges)
  traverse({ "start": "pkg/server.go:Handle", "graph": "code", "repo": "myrepo",
             "edge_types": ["calls"], "direction": "in" })

  # Find callees (outgoing "calls" edges)
  traverse({ "start": "pkg/server.go:Handle", "graph": "code",
             "edge_types": ["calls"], "direction": "out" })

  # Walk cloud resource relationships
  traverse({ "start": "arn:aws:ec2:...", "graph": "cloud", "account": "prod",
             "direction": "both", "include_edge_metadata": true })

  # Log graph: template → chunks
  traverse({ "graph": "logs", "name": "<query_id>",
             "start": "<template_id>", "direction": "out" })

  # Log graph: stream → labels + chunks + cloud proxies
  traverse({ "graph": "logs", "name": "<query_id>",
             "start": "<stream_id>", "direction": "both" })

## Composite shortcuts (query modes, not traverse)
  query({ "mode": "plan_tree", "id": "plan_id" })    — walk plan → phases → steps → criteria
  query({ "mode": "lineage", "id": "node_id" })      — trace provenance chain
  query({ "mode": "evidence", "id": "decision_id" }) — follow evidence for a decision

## Parameters
  start (required), direction, edge_types, depth, limit, graph, name, language,
  account, repo, branch, include_edge_metadata, format

## Gotchas
  - depth:1 gives only immediate neighbors (default)
  - Log graph traversal starts only at NodeLogTemplate or NodeLogStream
  - Cross-graph proxies auto-resolve — no special direction needed
`
