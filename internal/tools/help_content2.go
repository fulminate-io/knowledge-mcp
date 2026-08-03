// SPDX-License-Identifier: Apache-2.0

package tools

const helpThoughts = `# thoughts — Persistent reasoning graph (think / charge / recall / trace / propagate)

The thought graph externalizes reasoning so it survives sessions, restarts,
and compactions. Hypotheses become first-class nodes; charges add evidence;
DeGroot propagation derives valence (consensus direction), magnitude
(significance), consistency, and self-trust from charge topology.

## Operations

  - think     : record a thought (hypothesis / observation / plan)
  - charge    : attach evidence that supports/contradicts a thought's claim
  - recall    : search thoughts with composable filters
  - trace     : follow reasoning chains forward/backward from a thought
  - propagate : manually run DeGroot propagation across all thoughts;
                with similarity:true, ASYNC-trigger the topic-similarity lever
  - similarity_report : fetch the result of the async topic-similarity pass

Common cycle:  recall → think → (do work) → charge → recall again to confirm
the hypothesis landed. Examine a single thought via query(mode: "examine",
id: thought_id). Link a thought to another node via mutate(operation:
"link", from: thought_id, to: node_id, relationship: "informed-by"|
"supports"|"contradicts"|"relates-to"|"produced").

## operation: "think" — Record a unit of reasoning

  content       — the thought (required)
  summary       — search-optimized one-line summary, max 500 chars (REQUIRED).
                  Authored deliberately — this is what makes the thought findable
                  via recall. NOT auto-derived from content.
  session       — group name for related thoughts (e.g. "backend-auth-design")
  branches_from — thought ID this replaces (after invalidating the original)
  links         — node IDs to connect to (decisions, steps, code)
  status        — hypothesized (default) | validated | invalidated

  Examples:
    thoughts({ "operation": "think", "content": "Cache invalidation bug caused by X",
               "summary": "Cache bug root cause: stale key X never invalidated on write",
               "session": "debug-cache" })
    thoughts({ "operation": "think", "content": "Approach: use Y instead of Z",
               "summary": "Decided to use Y over Z for cache layer",
               "links": ["decision_id"] })

  When to use:
    - Before implementing: record your planned approach
    - When debugging: what's broken, your hypothesis
    - After fixing: what was wrong and how you fixed it

## operation: "charge" — Add evidence to a thought

  thought   — thought node ID (required)
  polarity  — "positive" (evidence SUPPORTS the thought's claim) or "negative"
              (evidence CONTRADICTS it). Required. This is about the claim's
              truth, NOT good-vs-bad news; sentiment goes in reasoning.
  weight    — significance 1-10 (required)
  reasoning — why this charge applies (required)
  evidence  — node IDs of supporting evidence: cite the SPECIFIC thought,
              finding, decision, or charge IDs the charge actually drew on
              (not a vague hand-wave). Citing a related thought records a
              charge→thought evidenced-by edge that feeds cross-cluster trust
              differentiation, so a well-cited charge strengthens exactly the
              thoughts it relied on — leave it empty and that signal is lost.

  Examples:
    // claim CONFIRMED → positive (the evidence supports the thought)
    thoughts({ "operation": "charge", "thought": "t_abc",
               "polarity": "positive", "weight": 8,
               "reasoning": "Tests pass, behavior confirmed in prod — supports the claim",
               "evidence": ["t_root_cause", "finding_bench_id"] })
    // claim REFUTED → negative (the evidence contradicts the thought)
    thoughts({ "operation": "charge", "thought": "t_abc",
               "polarity": "negative", "weight": 5,
               "reasoning": "Benchmark shows no regression — contradicts the claim" })
    // polarity tracks the CLAIM, not the sentiment. Thought claims "competitor X
    // poses a significant threat"; confirming the threat is POSITIVE because the
    // evidence supports the claim — even though it is bad news for us.
    thoughts({ "operation": "charge", "thought": "t_threat",
               "polarity": "positive", "weight": 7,
               "reasoning": "Confirmed X ships feature Y at lower price — supports the claim that the threat is real (this is bad news for us, but the charge is positive because it supports the thought)." })

## operation: "recall" — Search thoughts with composable filters

  query           — semantic search text (omit to browse)
  session         — filter by session name
  status          — hypothesized | validated | invalidated
  valence_min/max — computed valence range (-1.0 to 1.0)
  magnitude_min   — minimum magnitude threshold
  consistency_max — max consistency (low = contested thoughts)
  connected_to    — node ID thoughts must be connected to
  time_start/end  — date range (ISO 8601)
  limit           — max results (default 20, max 50)
  mode            — search | timeline | charges | graph | clusters
  format          — text (default) | json

  Examples:
    thoughts({ "operation": "recall", "query": "cache invalidation" })
    thoughts({ "operation": "recall", "session": "debug-cache",
               "status": "hypothesized" })
    thoughts({ "operation": "recall", "mode": "timeline", "limit": 10 })
    thoughts({ "operation": "recall", "valence_min": 0.5, "magnitude_min": 2.0 })

## operation: "trace" — Follow reasoning chains

  thought           — starting thought node ID (required)
  direction         — forward | backward | both (default: both)
  depth             — max hops (default: 5)
  include_charges   — include charge nodes in the trace
  include_artifacts — include linked artifacts (code, decisions, PRs)
  format            — text (default) | json

  Examples:
    thoughts({ "operation": "trace", "thought": "t_abc" })
    thoughts({ "operation": "trace", "thought": "t_abc",
               "direction": "backward", "depth": 3, "include_artifacts": true })

  When to use:
    - Walking the reasoning that led to a decision
    - Auditing whether a hypothesis was actually charged with evidence
    - Finding all the artifacts a thought informed

  A thought recorded with an origin role (the think origin param) carries an
  agent--produced-->thought hub edge, so its trace surfaces the originating
  agent node (e.g. the planner agent) as a provenance step — intentional
  lineage, not noise.

## operation: "propagate" — Manually run DeGroot propagation

  No required parameters.

  The background loop normally fires propagation on its own — invoke this
  when you want immediate convergence after a batch of charges, or in tests
  that need to observe the post-propagation state without a timer wait.

  Optional:
    force_full (bool) — Run the full-corpus backstop pass now: bypasses the
      quiet-tick skip and incremental scoping, recomputes every component, and
      resets the backstop cadence. Use for an on-demand full reflection (ops /
      debug) instead of waiting for the periodic backstop tick. Errors if the
      reflection loop is not running in this process.

    similarity (bool) — ASYNC-trigger the topic-similarity lever (drain →
      centroids → reconcile → merge cascade → summaries → drift → links →
      densify). The pass can run many minutes — LONGER than the tool-call
      timeout — so the trigger does NOT wait: it acquires the single-flight
      guard, starts the pass in the background, and returns IMMEDIATELY with a
      copy-pasteable thoughts({"operation":"similarity_report"}) fetch call and
      a duration estimate. Only ONE pass runs at a time; a second trigger while
      one is in flight coalesces (returns "already running" + the same fetch
      contract, no second pass). Optional link_threshold / merge_threshold /
      densify_threshold / densify_k / densify_edge_budget override the HIGH
      defaults.

  Returns: thoughts_processed, components, iterations, converged (plain
  propagate); or the async-trigger contract (similarity:true).

  Example:
    thoughts({ "operation": "propagate" })
    thoughts({ "operation": "propagate", "force_full": true })
    thoughts({ "operation": "propagate", "similarity": true })

## operation: "similarity_report" — Fetch the async similarity pass result

  Optional:
    id (string) — a specific past pass to fetch. Omit for the LATEST pass.

  Renders by status:
    running   — in-progress + elapsed + the duration estimate + may-take-longer
    completed — the FULL rendered report (links, merge cascade, summaries,
                reconciliation, densification, the threshold-tuning histogram)
    failed    — the failure, surfaced loudly
    no pass yet — a clear empty-state message naming how to trigger one

  The report is persisted as an event node and fetched by id/marker — it is NOT
  vector-searchable (event nodes do not embed); this op is the way to read it.

  Example:
    thoughts({ "operation": "similarity_report" })
    thoughts({ "operation": "similarity_report", "id": "<pass-id>" })
`

const helpCreateProject = `# create_project — Create a project container node

Projects are top-level containers for related work. A project holds tickets,
and tickets hold plans. Hierarchy: project → ticket → plan → phase → step → criterion.

## Key fields
  name        — project name (required)
  description — project description (optional)
  summary     — short search-optimized summary (optional)

## Example
  create_project({ "name": "Auth overhaul", "description": "Refactor the auth system" })
`

const helpCreateTicket = `# create_ticket — Create a ticket node inside a project

Tickets are units of work within a project. A ticket holds plans.
Hierarchy: project → ticket → plan → phase → step → criterion.

## Key fields
  name        — ticket name (required)
  project_id  — parent project node ID (optional; links ticket to project via contains edge)
  description — ticket description (optional)
  summary     — short search-optimized summary (optional)

## Example
  create_ticket({ "name": "Add OAuth2 login", "project_id": "proj_abc" })
`

const helpCreatePlan = `# create_plan — Batch-create plan → phases → steps → criteria

Creates a full plan tree in one call.

## Required fields
  name, goal, summary, phases

## Key fields
  name                            — plan name
  goal                            — what the plan aims to achieve
  summary                         — short search-optimized summary
  ticket_id                       — optional parent ticket node ID (links via contains edge)
  research_id                     — optional research project ID (creates informed-by edge)
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

## Example
  create_plan({
    "name": "Add auth",
    "goal": "Ship JWT-based auth for the public API",
    "summary": "JWT middleware + handler plumbing + integration tests",
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
`

const helpCreateResearch = `# create_research — Batch-create research with nested questions

## Required fields
  name, goal, summary, questions

## Key fields
  name                   — research project name
  goal                   — what this research aims to answer
  summary                — short search-optimized summary
  ticket_id              — optional parent ticket node ID
  questions[].question   — question text
  questions[].context    — background/why this question matters

## Example
  create_research({
    "name": "Cache system analysis",
    "goal": "Understand current cache invalidation and hot paths",
    "summary": "Cache architecture audit",
    "questions": [
      { "question": "What is the current cache invalidation strategy?", "context": "Baseline before changes" },
      { "question": "Where are the hot paths?", "context": "Guide optimization work" }
    ]
  })
`

const helpCreateTestPlan = `# create_test_plan — Create a structured test plan with steps

## Required fields
  name, goal, summary, steps

## Key fields
  name                    — test plan name
  goal                    — what this test plan verifies
  summary                 — short search-optimized summary
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
  alternatives — what else was considered and why rejected
  informed_by  — comma-separated node IDs of findings/research that informed this

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
  graph          — "code" (default) | "knowledge" | "practice" | "cloud" | "linkage" | "logs"

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

## Code graph parameters
  repo           — repository name (default: active repo, "all" for cross-repo)
  repos          — specific repos array
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
  repo           — repository name (default: all repos)
  include_source — include source code (default true)

## Examples
  file_symbols({ "file_path": "tools/tools_dispatch.go" })
  file_symbols({ "file_path": "tools_help.go", "include_source": false })
`

const helpHelp = `# help — Get documentation about tools, node types, edge types, and workflows

## Parameters
  topic — topic name (optional, default: "overview")

## Available topics
  overview, node_types, edge_types, statuses, workflows,
  logs, patterns, recipes, topology, dreaming

  Per-tool: query, traverse, mutate, delete, manage,
  think, charge, recall, create_project, create_ticket,
  create_plan, create_research, create_test_plan,
  record_decision, search, file_symbols,
  help, assemble, sync
`

const helpAssemble = `# assemble — Walk a plan/test-plan/agent tree and render it

The node type determines the traversal pattern:
  plan       — phases → steps → criteria + decisions + findings
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

## Examples
  assemble({ "id": "plan_id" })
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
