// SPDX-License-Identifier: Apache-2.0

package tools

const helpThoughts = `# thoughts — Persistent reasoning graph (think / charge / recall / trace / propagate)

The thought graph externalizes reasoning so it survives sessions, restarts,
and compactions. Hypotheses become first-class nodes; charges add evidence;
DeGroot propagation derives valence (consensus direction), magnitude
(significance), consistency, and self-trust from charge topology.

## Operations

  - think     : record a thought (hypothesis / observation / plan)
  - charge    : add positive/negative evidence to a thought
  - recall    : search thoughts with composable filters
  - trace     : follow reasoning chains forward/backward from a thought
  - propagate : manually run DeGroot propagation across all thoughts

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
  polarity  — "positive" or "negative" (required)
  weight    — significance 1-10 (required)
  reasoning — why this charge applies (required)
  evidence  — node IDs of supporting evidence

  Examples:
    thoughts({ "operation": "charge", "thought": "t_abc",
               "polarity": "positive", "weight": 8,
               "reasoning": "Tests pass, behavior confirmed in prod" })
    thoughts({ "operation": "charge", "thought": "t_abc",
               "polarity": "negative", "weight": 5,
               "reasoning": "Performance regression in benchmark" })

## operation: "recall" — Search thoughts with composable filters

  query           — semantic search text (omit to browse)
  session         — filter by session name
  status          — hypothesized | validated | invalidated
  valence_min/max — computed valence range (-1.0 to 1.0)
  magnitude_min   — minimum magnitude threshold
  consistency_max — max consistency (low = contested thoughts)
  connected_to    — node ID thoughts must be connected to
  time_start/end  — date range (ISO 8601)
  limit           — max results (default 20)
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

## operation: "propagate" — Manually run DeGroot propagation

  No required parameters.

  The background loop normally fires propagation on its own — invoke this
  when you want immediate convergence after a batch of charges, or in tests
  that need to observe the post-propagation state without a timer wait.

  Returns: thoughts_processed, components, iterations, converged.

  Example:
    thoughts({ "operation": "propagate" })
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

const helpWhatNext = `# what_next — Find the next actionable steps

Returns pending steps whose depends-on dependencies are all completed.

## Parameters
  project_id — filter to a specific project (optional)

## Examples
  what_next()
  what_next({ "project_id": "b9c77f4e30f7e448ea3b63f626a0a6fa" })
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
    "name": "Use Badger v4 for graph storage",
    "choice": "Badger v4 with custom serialization",
    "rationale": "Concurrent read performance 3x better than SQLite. No file locks.",
    "alternatives": "SQLite: rejected due to file locking incompatible with MCP server",
    "informed_by": "finding_id1,finding_id2"
  })
`

const helpSearchCode = `# search — Unified search across code, knowledge, practice, cloud, linkage, and log graphs

## Graph routing
  graph          — "code" (default) | "knowledge" | "practice" | "cloud" | "linkage" | "logs"

## Parameters (all graphs)
  query          — single search query
  queries        — batch array of queries (deduped, merged)
  mode           — "hybrid" (default) | "text" (BM25 only) | "vector" (semantic only)
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

## Knowledge graph parameters
  mode           — also supports "ppr"/"graph_reach" (PPR reranking) and "recent"/"temporal" (recency boost)

## Practice graph parameters
  language       — language slug (e.g. "go", "python"). Required for search, omit to list graphs.

## Cloud graph parameters
  account        — cloud account key. Required for search, omit to list cloud graphs.
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
  search({ "query": "concurrency", "graph": "practice", "language": "go" })
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
  what_next, record_decision, search, file_symbols,
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

const helpSync = `# sync — Bidirectional knowledge graph sync with Fulminate Cloud

Requires the "sync" license scope. Three operations:

  push    — upload local version-overlay changesets newer than the persisted
            cursor. Idempotent: re-running a push with no new changes is a no-op.
  pull    — download every server bundle newer than the persisted cursor and
            apply with current-state-wins semantics. Empty local cursor → server
            ships a full snapshot (first-run bootstrap).
  promote — collapse a local overlay into the cloud base graph. Optional
            local=true also merges the overlay into the local base.

## Parameters
  operation — push | pull | promote (required)
  overlay   — overlay name (promote only; defaults to active overlay)
  local     — boolean (promote only); also merge locally when true (default false)

## Cursor state
  Stored at <graph-storage>/sync_cursor as a single timestamp line. Server-
  authoritative — safe to delete to force a full re-pull on next sync.

## First-run bootstrap
  pull with no cursor on disk → server returns the full upstream history as a
  snapshot bundle. The client applies it and writes the resulting cursor.
  No special argument needed; the empty-cursor pull IS the bootstrap.

## Examples
  sync({ "operation": "push" })
  sync({ "operation": "pull" })
  sync({ "operation": "promote", "overlay": "session-abc" })
  sync({ "operation": "promote", "overlay": "session-abc", "local": true })

## Result shape
  push    → { "pushed": N, "cursor": "...", "bootstrap": bool, "changesets": N }
  pull    → { "pulled": N, "applied": N, "cursor": "...", "bootstrap": bool }
  promote → { "promoted": "...", "local": bool, "local_nodes_merged": N,
              "local_edges_merged": N }   // last two only when local=true
`
