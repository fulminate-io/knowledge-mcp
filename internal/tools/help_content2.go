// SPDX-License-Identifier: Apache-2.0

package tools

const helpCreateProject = `# create_project — Create a project container node

Projects are top-level containers for related work. A project holds tickets,
and tickets hold plans. Hierarchy: project → ticket → plan → phase → step → criterion.

## Key fields
  name        — project name (required, max 255 chars — the Linear project-name cap)
  description — project description (required, must stay under 250 chars for Linear)
  summary     — search-optimized one-line summary (required, max 500 chars).
                An empty summary is REJECTED; an over-cap one is clamped at a
                word boundary with a non-fatal warning.
  group       — backend group key (Linear team key, Jira project key, GitHub repo).
                Required when a tracker backend is configured AND several groups
                exist; auto-defaults when there is only one; ignored with no backend.
  format      — "text" (default) or "json" ({id, name})

## Example
  create_project({ "name": "Auth overhaul",
                   "description": "Refactor the auth system",
                   "summary": "Auth system refactor: sessions, tokens, middleware" })
`

const helpCreateTicket = `# create_ticket — Create a ticket node inside a project

Tickets are units of work within a project. A ticket holds plans.
Hierarchy: project → ticket → plan → phase → step → criterion.

## Key fields
  name        — ticket name (required, max 255 chars — the Linear issue-title cap)
  project_id  — parent project node ID (required; links ticket to project via contains edge)
  description — ticket description (required)
  summary     — search-optimized one-line summary (required, max 500 chars; empty
                is REJECTED, over-cap is clamped with a warning)
  external_id — external tracker ID (e.g. JIRA-123, GH-456)
  priority    — e.g. high, medium, low
  labels      — comma-separated labels or tags
  format      — "text" (default) or "json" ({id, name, warnings})

## The architecture-pattern tristate (also required)
  EXACTLY ONE of pattern_ids, no_patterns_reason, or proposed_patterns must be
  supplied. Supplying none — or two — is a hard error, not a warning:
  "exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be
  set (got N)".

  pattern_ids        — canonical pattern node IDs this ticket extends (ticket→pattern uses edges)
  no_patterns_reason — the audited escape hatch when no pattern applies
  proposed_patterns  — [{name, sketch}] for not-yet-cataloged patterns; each
                       creates a pattern node with status="emerging"

  Broken/unknown pattern IDs are a NON-FATAL warning surfaced under a
  "## Warnings" section, not an error.

  language_patterns is INDEPENDENT of that tristate — any subset including none.
  See help("patterns").

## Example
  create_ticket({ "name": "Add OAuth2 login",
                  "project_id": "proj_abc",
                  "description": "Add an OAuth2 authorization-code login path",
                  "summary": "OAuth2 authorization-code login for the public API",
                  "no_patterns_reason": "straight extension of the existing auth handler" })
`

const helpCreatePlan = `# create_plan — Batch-create a plan, in either of two shapes

Creates a full plan tree in one call, in EITHER of two shapes. Supply exactly
one of phases or sections; supplying both, or neither, is a hard error naming
which you supplied.

  PHASES   plan → phases → steps → criteria. Phases are ordered by array
           position and chained with depends-on edges; steps within a phase
           likewise. This is the shape every existing plan is in and it is
           unchanged.
  SECTIONS a CHUNKED plan: a root carrying the goal plus one plan_section child
           per part, on positioned contains edges with no chaining. Each section
           body is written and read ALONE, so revising one part is one write
           against one node and every other node stays byte-identical. Reviewers
           attach plan_annotation nodes to a SECTION rather than to the whole
           plan, and assemble pages the sections with section_start/section_end.

## Required fields
  name, goal, summary — PLUS exactly one of phases or sections, and
  exactly one of pattern_ids, no_patterns_reason, or proposed_patterns
  (the architecture-pattern tristate; supplying none or two is a hard error, see
  help("create_ticket") for the terms and help("patterns") for the catalog).
  Every phase requires name + summary; every step requires name + description +
  summary; every section requires name + body + summary.

## Key fields
  name                            — plan name
  goal                            — what the plan aims to achieve
  summary                         — search-optimized one-line summary, max 500 chars
  ticket_id                       — optional parent ticket node ID (links via contains edge)
  research_id                     — optional research project ID (creates informed-by edge)
  pattern_ids / no_patterns_reason / proposed_patterns — the required tristate above
  language_patterns               — defensive language patterns/findings to be vigilant of
                                    (plan→node audits edges). INDEPENDENT of the
                                    tristate; any subset including none
  open_questions                  — [{question, context}] uncertainties for the user
                                    to answer before implementation; creates question
                                    nodes (status: open) linked to the plan
  format                          — "text" (default) or "json" ({id, name, node_ids, warnings})
  phases[].name                   — phase name
  phases[].overview               — phase overview
  phases[].summary                — phase summary
  phases[].steps[].name           — step name
  phases[].steps[].description    — full implementation description
  phases[].steps[].summary        — step summary
  phases[].steps[].file_paths     — comma-separated file paths this step modifies
  phases[].steps[].criteria[].description — criterion text
  phases[].steps[].criteria[].type        — "automated" or "manual"
  phases[].steps[].criteria[].command     — shell command for automated criteria
  sections[].name                 — section name (required)
  sections[].body                 — the section's full text (required). The plan
                                    root carries none of it
  sections[].summary              — required search-optimized one-line summary
  sections[].position             — explicit zero-based position (optional).
                                    When any section supplies one EVERY section
                                    must, and the values must be unique. GAPS
                                    ARE LEGAL: deleting a section leaves a hole,
                                    and closing it would mean rewriting every
                                    later section — the whole-plan rewrite the
                                    chunked shape exists to remove

## Example
  create_plan({
    "name": "Add auth",
    "goal": "Ship JWT-based auth for the public API",
    "summary": "JWT middleware + handler plumbing + integration tests",
    "no_patterns_reason": "straight middleware insertion, no new architectural shape",
    "phases": [{
      "name": "Phase 1", "overview": "Wire middleware", "summary": "middleware",
      "steps": [{
        "name": "Add JWT middleware",
        "description": "Validate tokens in request pipeline",
        "summary": "JWT middleware",
        "file_paths": "pkg/auth/jwt.go",
        "criteria": [{ "description": "Tests pass", "type": "automated", "command": "go test ./pkg/auth/..." }]
      }]
    }]
  })

## Example — the sectioned shape
  create_plan({
    "name": "Auth redesign prefill",
    "goal": "the implementer's preloaded context",
    "summary": "touch points, reuse, seams, what to test",
    "no_patterns_reason": "no new architectural shape",
    "sections": [
      { "name": "Touch points", "body": "...", "summary": "every site the change reaches" },
      { "name": "What to test", "body": "...", "summary": "the list tests are written from" }
    ]
  })
`

const helpCreateResearch = `# create_research — Batch-create research with nested questions

## Required fields
  name, goal, summary, questions

## Key fields
  name                   — research project name
  goal                   — what this research aims to answer
  summary                — short search-optimized summary
  ticket_id              — optional parent ticket node ID
  format                 — "text" (default) or "json" ({id, name, question_ids})
  questions[].question   — question text
  questions[].context    — background/why this question matters
  questions[].summary    — REQUIRED per-question search-optimized summary. It is
                           yours to write and nothing composes one from question
                           + context; an omitted or empty one is refused under
                           questions[i].summary, and an over-cap one is clamped
                           at a word boundary with a warning.

## Example
  create_research({
    "name": "Cache system analysis",
    "goal": "Understand current cache invalidation and hot paths",
    "summary": "Cache architecture audit",
    "questions": [
      { "question": "What is the current cache invalidation strategy?", "summary": "cache invalidation strategy is undocumented", "context": "Baseline before changes" },
      { "question": "Where are the hot paths?", "summary": "the hot paths are unidentified", "context": "Guide optimization work" }
    ]
  })
`

const helpCreateTestPlan = `# create_test_plan — Create a structured test plan with steps

## Required fields
  name, goal, summary, steps

## Key fields
  name                    — test plan name
  goal                    — what this test plan verifies
  summary                 — search-optimized one-line summary, max 500 chars
  format                  — "text" (default) or "json" ({id, name, step_ids})
  steps[].name            — step name
  steps[].description     — what to test and expected result
  steps[].summary         — short summary of the step
  steps[].criteria[].description — criterion text
  steps[].criteria[].type        — "automated" or "manual"
  steps[].criteria[].command     — shell command for automated criteria

## Example
  create_test_plan({
    "name": "Auth integration tests",
    "goal": "Verify JWT auth on public endpoints",
    "summary": "Auth smoke suite",
    "steps": [
      { "name": "Valid token accepted", "description": "POST /api with valid JWT returns 200", "summary": "valid token" },
      { "name": "Expired token rejected", "description": "POST /api with expired JWT returns 401", "summary": "expired token" }
    ]
  })
`

const helpRecordDecision = `# record_decision — Record a design decision with rationale

## Parameters
  name         — searchable title (required)
  choice       — what was decided (required)
  rationale    — why (required)
  summary      — author-supplied one-line searchable summary, max 500 chars (required;
                 over-cap values are clamped at a word boundary with a warning)
  alternatives — what else was considered and why rejected
  informed_by  — comma-separated node IDs of findings/research that informed this
  ticket_id    — born-link the decision under its work item (ticket--contains-->decision)
  session      — born-link it under a working session (creates the session if new)
  links        — node IDs to relate it to (decision--relates-to-->target); code/cloud
                 IDs are linked post-create via the cross-graph linkage
  format       — "text" (default) or "json" ({id, name, warnings})

  An unresolvable ticket_id / link target is dropped with a warning and never
  blocks the write.

## Example
  record_decision({
    "name": "Cache-serve reflect modes from the propagation loop",
    "choice": "Compute per tick, serve O(1) from cache",
    "rationale": "Recomputing over the full corpus on every call blocks the tool past its timeout; serving the loop's cached result makes the call O(1).",
    "alternatives": "Recompute per call: rejected — full-corpus recompute times out under load",
    "informed_by": "finding_id1,finding_id2"
  })
`

const helpSearchCode = `# search — Unified search across code, knowledge, practice, cloud, linkage, and log graphs

## Graph routing
  graph          — "code" (default) | "knowledge" | "practice" | "cloud" | "cicd" | "linkage" | "logs"

## Parameters (all graphs)
  query          — single search query
  queries        — batch array of queries (deduped, merged)
  limit          — max results per query (default 10, max 50)
  query_vector   — optional base64-encoded binary embedding (32 bytes / 256-bit
                   decoded). When set, the server skips its local embedder and
                   uses the supplied vector for hybrid search. Wired by the
                   client-side LLM pipeline's InterceptSearch so the server
                   stays unencumbered by Voyage API keys post-Phase-5.
                   Decoded length mismatches return a structured validation
                   error and no search is performed.

## Result shaping (all graphs)
  format         — "text" (default, markdown) or "json" ({results:[{id,name,type,score,...}]})
  fields         — format=json only: per-result field projection, to shrink
                   high-volume responses. An unsupported key is REFUSED naming it.
  types          — filter results by node type (knowledge + registered custom graphs)
  rerank         — apply the post-fusion rerank when configured (default true)
  RETIRED        — search no longer accepts include_tombstones or explain. Both were
                   declared and read by nothing, so they were accepted and dropped;
                   they are now refused by name. Tombstoned nodes: use query.

## Code graph parameters
  repo           — repository name. REQUIRED for graph=code — it is NEVER inferred
                   from cwd and there is no active-repo default. "all" spans every
                   indexed repo.
  repos          — specific repos array (alternative to repo:"all")
  path_prefix    — filter to files under this path
  include_source — include full source code (default true)
  group_by_file  — group results by file
  branch         — search branch overlay (auto-detected on feature branch)
  staleness      — show index staleness info
  NOTE           — mode is not honored on the code arm: code search always fuses
                   BM25 and vector whenever an embedder is available.

## Knowledge graph parameters
  mode           — "hybrid" (default) fuses the BM25 and vector arms.
                   "text" — BM25 only, with no query embedding and no rerank.
                   "vector" — the vector arm alone; needs a configured embedder.
                   "recent"/"temporal" — one recency boost, both spellings equal.
                   "similar" — see node_id below.
                   The same vocabulary is honored on registered custom graphs.
  node_id        — with mode:"similar", the node whose nearest corpus neighbors to
                   return. mode:"similar" searches the node's OWN STORED vector (its
                   on-disk embedding, NOT a fresh embedding of any query text) and
                   EXCLUDES the node itself. Results are ranked by the client
                   engine's reciprocal-rank fusion over the stored-vector (HNSW)
                   arm — pure stored-vector proximity, NOT a raw cosine score.

## Practice graph parameters
  language       — language slug (e.g. "go", "python"). Required for search, omit to list graphs.

## Cloud graph parameters
  account        — selects a collected external-provider account/org within your own graph (e.g. an AWS/GCP account, or a CI provider org). Required for search, omit to list cloud graphs.
  resource_type  — resource type prefix filter (e.g. "ec2", "ec2:instance")

## Linkage graph
  Searches cross-graph proxy nodes (code-to-cloud relationships).
  No additional parameters required beyond query.

## Log graph parameters
  name           — query_id of the log graph (required). List active graphs with manage list_logs.

  BM25-only: log graphs are excluded from LLM summarization/embedding, so HNSW
  is never populated for them. Results are filtered to log-template nodes —
  chunks hold compressed payloads and streams are label buckets.

## Examples
  search({ "query": "cache invalidation" })
  search({ "queries": ["JWT middleware", "auth handler", "token validation"], "limit": 8 })
  search({ "query": "handleRequest", "repo": "all" })
  search({ "query": "database connection", "path_prefix": "pkg/db/" })
  search({ "query": "auth", "graph": "knowledge" })
  search({ "graph": "knowledge", "mode": "similar", "node_id": "<node_id>" })  — nearest stored-vector neighbors of a node
  search({ "query": "concurrency", "graph": "practice" })
  search({ "query": "web server", "graph": "cloud", "account": "aws-prod" })
  search({ "query": "bucket", "graph": "cloud", "account": "aws-prod", "resource_type": "s3" })
  search({ "graph": "cloud" })  — list available cloud graphs (no account)
  search({ "query": "deploy", "graph": "linkage" })  — search cross-graph linkage proxies
  search({ "graph": "logs", "name": "<query_id>", "query": "connection refused" })  — BM25 over log templates
`

const helpFileSymbols = `# file_symbols — List all symbols in a file

## Parameters
  file_path      — file path, partial paths work (required)
  file_paths     — several files in one call (combined with file_path)
  repo           — repository name. REQUIRED — it is NEVER inferred from cwd and
                   there is no all-repos default. "all" spans every indexed repo.
  branch         — branch overlay to read instead of the base graph. Auto-filled
                   from the machine-local repo manifest when omitted and repo is
                   not "all"; supply it to pin a specific overlay.
  include_source — include source code (default true)
  include_tombstones — include tombstoned symbols (default false)
  format         — "text" (default, markdown) or "json" (rows: {id, symbol_name,
                   type, file_path, start_line, end_line, signature, summary})

## Examples
  file_symbols({ "file_path": "tools/tools_dispatch.go", "repo": "myrepo" })
  file_symbols({ "file_path": "tools_help.go", "repo": "myrepo", "include_source": false })
`

const helpHelp = `# help — Get documentation about tools, node types, edge types, and workflows

## Parameters
  topic — topic name (optional, default: "overview")

## Available topics

  Reference: overview, node_types, edge_types, statuses, workflows,
  logs, patterns, recipes, topology

  Per-tool: query, traverse, mutate, delete, manage, manage_checks, ast,
  thoughts, create_project, create_ticket, create_plan, create_research,
  create_test_plan, record_decision, search, file_symbols,
  help, assemble, sync

  That is the complete set. An unrecognized topic is refused naming the topic
  and pointing at help() with no args for the list. There is no per-operation
  topic — the thoughts operations (think / charge / recall / trace / propagate /
  adjacency / charges_for / similarity_report) are all documented under the one
  "thoughts" topic.
`

const helpAssemble = `# assemble — Walk a plan/test-plan/agent tree and render it

The node type determines the traversal pattern:
  plan       — phases → steps → criteria + decisions + findings; for a CHUNKED
               plan, a "## Sections" index naming every section with its size and
               annotation state, plus the section bodies section_start/section_end
               selects
  plan_section — one section: its position, its body in full, and its annotations
               as SUMMARIES with their ids, kinds, tiers and lanes. A full
               annotation body is fetched by its id
  test_plan  — steps + criteria; with new_run:true creates test_run nodes
  agent/skill — tool_guides (via uses) + rules (via constrains)
  research   — questions + findings per question + resulting decisions
  decision   — follow informed-by to findings/research
  fallback   — generic 2-hop traversal

## Parameters
  id          — node ID to assemble (use this or name)
  name        — name-based lookup (requires type)
  type        — node type for name lookup
  new_run     — if true and test_plan, creates test_run nodes (default false)
  run_session — filter or group test runs by session UUID
  format      — "text" (default) or "json" (structured)
  section_start / section_end — for a CHUNKED plan: the inclusive zero-based
                range of section BODIES to return, so a whole plan is read in
                bounded pages. Omit start to begin at the first section, end to
                run to the last, BOTH to get the index and tree alone. An
                out-of-bounds, negative or inverted range ERRORS naming the
                bound — it is never clamped, because a clamp hands a reader a
                page they did not ask for with nothing in the result to say so.
                THE SAME RULES HOLD IN BOTH FORMATS, and where the two differ they
                differ only in how they say it: with no range, a text read shows
                each section's first 120 characters in the tree and a json read
                omits the body entirely, marking it "body_omitted":true beside a
                "body_bytes" count so an absent body is never mistaken for an
                empty section. A range on a node that is NOT a plan is refused in
                both formats, with the same message, and so is a range on a plan
                that HAS no sections: a phase-and-step plan has nothing to page,
                and answering a page request with the whole plan would be the
                clamp above wearing a different name

  ANNOTATIONS REACH EVERY FORMAT of every read here. A text render carries the
  per-section line "annotations: N (kind n, ...)"; a json render carries an
  "annotations" object with the same count and kinds. Both OMIT it entirely for a
  section that has none, so a plan written before annotations existed renders the
  bytes it always did. When the annotation read itself FAILS, both disclose it in
  their own words and name the error — never as a row-ceiling truncation, which is
  a different cause with a remedy that would not help

## Examples
  assemble({ "id": "plan_id" })
  assemble({ "id": "plan_id", "section_start": 0, "section_end": 2 })
  assemble({ "id": "section_id" })
  assemble({ "name": "Auth integration tests", "type": "test_plan" })
  assemble({ "id": "test_plan_id", "new_run": true })
  assemble({ "id": "agent_id" })
`

const helpSync = `# sync — knowledge graph sync with Fulminate Cloud

Requires the "sync" license scope. Three operations:

  push — upload the LOCAL graph to your cloud account (local → cloud). The
         server serializes the local graph and the client uploads it; the cloud
         side merges it new-wins into your authoritative copy.
  pull — overwrite the LOCAL graph from your cloud account (cloud → local). A
         FULL OVERWRITE from the authoritative cloud copy: the client fetches the
         serialized cloud bytes and fully replaces the local graph (all
         sync-eligible types). Requires a local server (the destination is the
         local graph), so a cloud-only install cannot pull.
  list — print a table of sync-eligible local graphs with their cloud sync
         status and last-synced time.

## Parameters
  operation — push | pull | list (required)
  graph     — graph type (knowledge, code, cloud, ...); defaults to 'knowledge'
  name      — graph name; defaults to 'default'

## Examples
  sync({ "operation": "push" })
  sync({ "operation": "pull", "graph": "knowledge", "name": "default" })
  sync({ "operation": "list" })

## Result shape
  push → "pushed <graph>/<name> (N bytes; ...)"
  pull → "pulled <graph>/<name> (N bytes; N nodes, E edges)"
  list → a table of sync-eligible graphs + cloud status
`
