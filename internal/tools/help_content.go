// SPDX-License-Identifier: Apache-2.0

package tools

const helpOverview = `# Knowledge Graph — Tool Reference (first-class tools + generic primitives + sync)

## First-class tools (self-documenting schemas)

| Tool              | Purpose                                                                |
|-------------------|------------------------------------------------------------------------|
| thoughts          | Persistent reasoning graph: think / charge / recall / trace / propagate |
| create_project    | Create a project container node (holds tickets and plans)              |
| create_ticket     | Create a ticket node inside a project                                  |
| create_plan       | Batch-create a plan: phases → steps → criteria, or chunked sections    |
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
| sync | Knowledge graph sync with Fulminate Cloud (push, pull, list)     |

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

NO creator synthesizes a Summary any more. Every one takes it from you, and
record_decision — the last that composed its own, from the choice — now requires
one like the rest. criterion, rule, thoughts(think) and thoughts(charge) run the
same empty-rejects / over-cap-clamps validation as the embed-only types, so a
create with no summary is refused rather than filled in. See help("mutate") for
the exact terms.

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
  plan          — body of work, in EITHER of two shapes: phases (project→ticket→plan→phase→step→criterion) or chunked sections (plan→plan_section). Fields: name, description, status
  plan_section  — one part of a CHUNKED plan; its description IS that part's body, written and read alone. A section read (assemble of a section id) returns its body in FULL and its annotations, in BOTH text and json; a plan read returns the index and tree with NO section body unless section_start/section_end asks for one, in both formats. Joined to the plan root by a contains edge carrying a zero-based position on its Evidence, mirrored on metadata.position. Emits no depends-on edge — a chain would override the positions
  plan_annotation — a reviewer's note ON one plan_section, joined to it by relates-to. Fields: metadata.annotation_kind (correct|finding|needed change), metadata.annotation_tier (required on a finding), metadata.reviewer_lane, metadata.replacement_text (required on a needed change; the replacement text IS this metadata value — nothing reads it out of the body). Annotations never enter the contains tree. THE EDGE CARRIES THE SEVERITY TOO: its Method is "plan-annotation" and its Evidence is {"annotation_kind":...,"annotation_tier":...}, so a traverse with edge metadata from a section answers "which sections have unresolved findings, and how bad" without hydrating one annotation node. The node keeps both fields; the edge is a second carrier of the same two facts at the layer that is cheap to read. BECAUSE THE FACT IS STORED TWICE IT IS WRITE-ONCE: mutate(create, type:"plan_annotation", links:["<section id>"], metadata:{...}) writes both carriers in one batch, and every later write that could move one without the other is refused. A metadata write of annotation_kind or annotation_tier through update/update_batch/bulk_update_metadata is refused naming the key. mutate(upsert) of a plan_annotation is refused BY TYPE whatever its body, because upsert writes no edge at all. A create_batch relates-to edge from an annotation, in either the from_idx or the from_id spelling, must carry the annotation's own kind and tier on the edge's method and evidence — create_batch's edges[] DOES accept those carriers — or it is refused naming the exact values to send; a mutate(link) attaching one is held to the same rule. To change a severity, create the replacement annotation and delete the old one
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
  CALLS           — caller → callee (Weight is the call count)
  TEST_CALLS      — same, but the CALLER is test code; a distinct type so every
                    CALLS consumer keeps seeing production structure only
  IMPORTS         — file → dependency path
  CONTAINS        — file → symbol, and container → member
  USES_TYPE       — declaration → a type it references
  EMBEDS          — a struct's embedded fields and an interface's embedded
                    elements → the embedded type
  IMPLEMENTS      — interface → concrete type, and interface method spec → the
                    method satisfying it. Callers of an interface method are a
                    direct CALLS walk; its implementers are ONE hop out over
                    IMPLEMENTS. Method carries "method-set:<N>", the interface's
                    expanded method-set size, so a one-method edge can be
                    weighted as low-information
  LANGUAGE        — symbol → its per-language hub node

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

## Criterion evaluated-pass class
  pass       — the check was RUN and it PASSED. This is the canonical spelling;
               write it. "passed", "verified", "satisfied" and "met" are the
               four other spellings the corpus already carries and are read as
               the same class, so a criterion carrying any of the five is
               settled: it neither holds its container on a completed cascade
               nor is announced as something still to run.

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
