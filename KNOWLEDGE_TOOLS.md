# Knowledge Graph MCP Tools

> Copy this into your project's `CLAUDE.md` to give Claude full guidance on the knowledge graph tools.

**Tool design: primitives over shortcuts.** `query`, `traverse`, `mutate` are lean generic primitives — graph selection via the `graph` param. A small set of composite shortcuts (`query(mode: "plan_tree")`, `query(mode: "lineage")`, `query(mode: "evidence")`) are kept as exceptions, justified by frequent historical use. Default for a new pattern: compose with the primitive and use `query(mode: "stats")` to discover the vocabulary.

## Search

**Use `search` instead of Grep/Glob for code questions.** It's a unified search tool that routes to the right graph based on the `graph` parameter. Combines BM25 text search with semantic vector search and returns results inline.

### Code Search (default)

```jsonc
// Single query (graph defaults to "code")
search({ "query": "authentication middleware" })

// Batch queries (preferred — covers more ground in one call)
search({ "queries": ["auth middleware", "JWT validation", "session handler"], "limit": 10 })

// Cross-repo search
search({ "query": "database migration", "repo": "all" })

// Search with branch overlay (or auto-detected from current branch)
search({ "query": "my function", "branch": "feature-x" })
```

Results include a staleness indicator (e.g., "Indexed 2h ago, 3 commits behind HEAD"). If stale, consider reindexing.

### Knowledge Graph Search

```jsonc
// Search across all knowledge nodes (decisions, findings, rules, etc.)
search({ "query": "authentication", "graph": "knowledge" })
```

### Practice Graph Search

```jsonc
// Search best practices for a specific language
search({ "query": "concurrency patterns", "graph": "practice", "language": "go" })
```

**Use `file_symbols`** to see all symbols in a file before diving into specific functions:

```jsonc
file_symbols({ "file_path": "path/to/file.go" })
```

## Code Graph Traversal

**Use `traverse` to understand code relationships.** This is often more useful than reading files directly.

**Discover before traversing:** Don't memorize edge types — discover them:
```jsonc
query({ "mode": "stats", "graph": "code" })   // shows all node types and edge types
```

```jsonc
// See all callers and callees of a function (most common)
traverse({ "start": "path/to/file.go:FunctionName", "graph": "code",
           "edge_types": ["calls"], "direction": "both", "include_source": true })

// Just callers (who calls this?)
traverse({ "start": "path/to/file.go:FunctionName", "graph": "code",
           "edge_types": ["calls"], "direction": "in" })

// Just callees (what does this call?)
traverse({ "start": "path/to/file.go:FunctionName", "graph": "code",
           "edge_types": ["calls"], "direction": "out" })

// Walk a plan tree (plan → phases → steps) — use query mode shortcut
query({ "mode": "plan_tree", "id": "plan_id" })

// View version history of a node (changeset chain)
traverse({ "start": "node_id", "graph": "versions",
           "edge_types": ["changed_to"], "direction": "out" })
```

### Composite shortcuts

| Shortcut | Equivalent | When |
|---|---|---|
| `query({"mode": "plan_tree", "id": id})` | traverse `contains` + depends-on sort | Walk plan/project/ticket hierarchy |
| `query({"mode": "lineage", "id": id})` | multi-edge sequential probe | Trace provenance chains |
| `query({"mode": "evidence", "id": decision_id})` | follow `informed-by` from decision | Surface what shaped a decision |

## Thought System

The knowledge graph has a persistent reasoning system. **Use it to externalize your thinking** — not just conclusions, but the reasoning that led to them.

### Core Cycle: think → charge → recall

1. **`recall`** — **Always start here.** Before beginning any work, search past thoughts. They contain debugging notes, design rationale, and gotchas that save re-investigation.

   ```jsonc
   recall({ "text": "edge resolution callers" })
   ```

2. **`think`** — Record hypotheses, trade-offs, observations. Group related thoughts into sessions.

   ```jsonc
   think({
     "content": "The edge IDs don't match node IDs — tree-sitter uses pkg.Symbol but graph uses filepath:Symbol. Need a resolution layer.",
     "session": "fix-traversal"
   })
   ```

3. **`charge`** — When evidence arrives, charge the thought. Positive if confirmed, negative if disproven.
   ```jsonc
   charge({
     "thought": "thought_node_id",
     "polarity": "positive",
     "weight": 8,
     "reasoning": "Tests pass — 71 callers found for store.Open after adding resolveEdges"
   })
   ```

### When to Think

- **Starting a task**: what you expect, your approach
- **Debugging**: what's broken, your hypothesis — **always record the broken→fixed transition**
- **Design trade-offs**: why you chose one approach over another
- **Surprising behavior**: what was unexpected and what it implies
- **After testing**: charge with pass/fail results

Think early, charge often. Thoughts without charges are hypotheses. Thoughts with charges are evidence.

**Use thoughts instead of Claude Code memories.** Thoughts are searchable via `recall`, linkable to other nodes, and chargeable with evidence. Memories are isolated files with no graph connectivity.

### Reflection

Periodically examine your reasoning patterns:

```jsonc
query({ "mode": "personality" })    // how you tend to reason
query({ "mode": "tensions" })       // conflicting thoughts
query({ "mode": "blind_spots" })    // areas you haven't charged
query({ "mode": "summary" })        // overview of thought activity
```

## Node Versioning

Every node mutation is automatically tracked in a version overlay (`knowledge@versions`). The main graph stores the current state — reads are zero-cost. History is query-optional.

### Viewing History

```jsonc
// Full changeset chain for a node (base + patches in chronological order)
traverse({ "start": "node_id", "graph": "versions",
           "edge_types": ["changed_to"], "direction": "out" })

// Node details with version history appended
query({ "id": "node_id", "show_history": true })
```

### Entropy Signal

The version system gives the graph a sense of time through accumulated change. Use entropy to understand which nodes are stable vs in flux:

```jsonc
// Most volatile nodes in the graph (ranked by patch count)
query({ "mode": "entropy" })

// Entropy stats for a specific node (patch count, change velocity, most-changed fields)
query({ "id": "node_id", "mode": "entropy" })
```

### Version Reindex

Changeset summaries and embeddings are generated during reindex, not on every write. The version overlay has its own BM25/HNSW indexes for temporal search:

```jsonc
// Reindex includes version overlay when present
manage({ "operation": "reindex" })

// Or reindex just the version overlay
manage({ "operation": "reindex_versions" })
```

## Knowledge Nodes

The graph stores structured knowledge alongside code. Use these to build institutional memory.

### Searching Knowledge

```jsonc
// Text search across all knowledge
query({ "text": "authentication", "limit": 10 })

// Get a specific node
query({ "id": "node_id" })

// Browse by type
query({ "type": "decision" })     // architectural decisions
query({ "type": "finding" })      // research findings
query({ "type": "rule" })         // codebase rules/constraints
query({ "type": "plan" })         // implementation plans
query({ "type": "project" })     // projects
query({ "type": "ticket" })      // tickets
query({ "type": "test_plan" })   // test plans
query({ "type": "agent" })       // agent definitions
query({ "type": "skill" })       // skill definitions

// Graph statistics
query({ "mode": "stats" })

// Most volatile nodes (entropy signal)
query({ "mode": "entropy" })

// Deep node inspection — ancestry chain, edges, version history
// Use when debugging why a node has unexpected status or isn't appearing in what_next
query({ "mode": "examine", "id": "node_id" })
```

### Recording Decisions

After making a significant architectural or design choice:

```jsonc
record_decision({
  "name": "Use two-pass edge resolution",
  "choice": "Resolve edges in a second pass after all nodes are added",
  "rationale": "Edges reference symbols that may be defined in other files processed later",
  "alternatives": "Single-pass with deferred edge resolution, or post-hoc edge fixup"
})
```

### Creating Findings

When you discover something during research or implementation:

```jsonc
mutate({
  "operation": "create",
  "type": "finding",
  "name": "Large functions lose identity when split",
  "description": "Functions exceeding maxChunkTokens are split into anonymous Block chunks, losing their name and symbol registration",
  "evidence": "indexer/reindex.go — buildGraphWithOptions splits at 500 tokens"
})
```

### Linking Nodes

Connect related knowledge:

```jsonc
mutate({ "operation": "link", "from": "finding_id", "to": "decision_id", "relationship": "informed-by" })
```

## Planning and Implementation

### Project and Ticket Hierarchy

Projects are top-level containers. The full hierarchy is: **project → ticket → plan → phase → step → criterion**.

**Status values:**
- Projects: `active`, `completed`, `archived`
- Tickets: `open`, `in_progress`, `closed`

**Ticket metadata fields:** `external_id` (e.g. GitHub issue number), `priority` (`low`, `medium`, `high`, `critical`), `labels` (comma-separated string)

**Full create workflow:**

```jsonc
// 1. Create a project
create_project({ "name": "Auth Refactor", "description": "Modernize the auth system" })

// 2. Create a ticket inside the project
create_ticket({
  "name": "Migrate to OAuth2",
  "project_id": "proj_abc",
  "priority": "high",
  "labels": "auth,security",
  "external_id": "GH-42"
})

// 3. Create a plan linked to the ticket (ticket_id is optional — omit for standalone plans)
create_plan({ "name": "OAuth2 impl", "ticket_id": "ticket_abc", "phases": [...] })

// 4. Create research linked to the ticket (ticket_id is optional)
create_research({ "name": "OAuth provider options", "ticket_id": "ticket_abc", "questions": [...] })

// 5. Find next actionable steps scoped to a project
what_next({ "project_id": "proj_abc" })

// 6. Assemble a project (shows all tickets with progress)
assemble({ "id": "proj_abc" })

// 7. Assemble a ticket (shows linked plans, research, decisions)
assemble({ "id": "ticket_abc" })
```

**Browsing projects and tickets:**

```jsonc
// List all projects
query({ "type": "project" })

// List all tickets
query({ "type": "ticket" })

// Update ticket status
mutate({ "operation": "update", "id": "ticket_abc", "status": "in_progress" })
```

### Creating Plans

For multi-step tasks, create structured plans:

```jsonc
create_plan({
  "name": "Fix edge resolution",
  "goal": "Callers/callees traversal returns accurate results",
  "summary": "Add edge ID resolution layer between tree-sitter output and graph storage",
  "phases": [
    {
      "name": "Phase 1: Edge Resolution",
      "overview": "Build resolveEdges function and integrate into graph building",
      "steps": [
        {
          "name": "Create resolveEdges function",
          "description": "Map pkg.Symbol → filepath:Symbol using symbol index",
          "file_paths": "indexer/edges.go",
          "criteria": [
            { "description": "Tests pass", "command": "go test ./indexer/ -run TestResolveEdges", "type": "automated" }
          ]
        }
      ]
    }
  ]
})
```

### Finding Next Work

```jsonc
what_next({ "project_id": "plan_node_id" })
```

### Creating Research

For investigating unknowns before planning:

```jsonc
create_research({
  "name": "How edge IDs are generated",
  "goal": "Understand the full pipeline from tree-sitter to graph",
  "summary": "Trace how edge FromID/ToID values are created and where they diverge from node IDs",
  "questions": [
    { "question": "What format does tree-sitter use for edge IDs?", "context": "chunker.go emitDeclarationEdges" },
    { "question": "What format do graph node IDs use?", "context": "ChunkNodeID in indexer/chunk.go" }
  ]
})
```

## Test Plans

Test plans are reusable templates — they define what to test, not execution results. Each run creates separate `test_run` nodes.

### Creating Test Plans

```jsonc
create_test_plan({
  "name": "Auth smoke tests",
  "goal": "Verify authentication endpoints handle valid and invalid credentials",
  "summary": "Authentication endpoint smoke tests covering login, logout, and token refresh",
  "steps": [
    {
      "name": "Login with valid credentials",
      "description": "POST /auth/login returns 200 with valid JWT",
      "criteria": [
        { "description": "Returns 200 status", "command": "curl -s -o /dev/null -w '%{http_code}' localhost:8080/auth/login -d '{...}'", "type": "automated" },
        { "description": "Response contains valid JWT", "type": "manual" }
      ]
    },
    {
      "name": "Login with invalid credentials",
      "description": "POST /auth/login returns 401 with wrong password"
    }
  ]
})
```

### Running Test Plans

```jsonc
// Start a run session (creates test_run nodes for each step)
assemble({ "id": "test_plan_id", "new_run": true })

// Record results
mutate({ "operation": "update", "id": "test_run_id", "status": "pass" })
mutate({ "operation": "update", "id": "test_run_id", "status": "fail" })

// Review the full run
assemble({ "id": "test_plan_id", "run_session": "uuid-from-above" })
```

## Assembling Context

Use `assemble` to get a fully assembled view of any structured node — it pulls in related context automatically.

```jsonc
// Plan with linked decisions and research
assemble({ "id": "plan_id" })

// Test plan with run results
assemble({ "id": "test_plan_id", "run_session": "uuid" })

// Agent/skill with phases, tool guides, and constraining rules
assemble({ "id": "agent_id" })

// Auto-recovery after compaction (no args)
assemble()
```

## Getting Help

Use `help` for reference documentation on any tool, node type, edge type, or workflow:

```jsonc
help()                              // overview of all tools
help({ "topic": "assemble" })       // assemble tool docs
help({ "topic": "node_types" })     // all node types
help({ "topic": "edge_types" })     // all edge types
help({ "topic": "statuses" })       // valid status values
help({ "topic": "workflows" })      // common workflow patterns
```

## Graph Management

```jsonc
// Server status
manage({ "operation": "status" })

// Reindex codebase (auto-detects branch)
manage({ "operation": "reindex" })

// List branch overlays
manage({ "operation": "list_branches", "name": "myrepo" })

// Reindex version overlay (changeset summaries + embeddings)
manage({ "operation": "reindex_versions" })
```

### Reindexing

After significant code changes, **ask the user** if they'd like to reindex. Don't auto-reindex — it takes 30s-2min depending on repo size. Incremental reindex carries forward summaries and vectors for unchanged nodes.

### Branch Overlays

Non-default branches are automatically detected and indexed as thin overlays on the main branch store. Search auto-detects the current branch.

```jsonc
// Search auto-detects branch overlay
search({ "query": "my function" })

// Explicit branch overlay
search({ "query": "my function", "branch": "feature-x" })

// List indexed branches
manage({ "operation": "list_branches", "name": "myrepo" })

// Delete a branch index
manage({ "operation": "delete_branch", "name": "myrepo", "branch": "feature-x" })
```

## Practice Graphs

Per-language graphs storing best practices, patterns, and conventions. Separate from the main knowledge graph and code graphs.

### Searching

```jsonc
// Search best practices for a language
search({ "query": "error handling", "graph": "practice", "language": "go" })

// List all practice graphs
query({ "graph": "practice" })

// Browse a specific language's graph
query({ "graph": "practice", "language": "go" })

// Text search within a practice graph
query({ "graph": "practice", "language": "go", "text": "concurrency" })
```

### Creating and Updating

```jsonc
// Create a practice node
mutate({ "operation": "create", "type": "finding", "name": "Use errgroup for concurrent goroutines",
         "description": "...", "graph": "practice", "language": "go" })

// Update a practice node
mutate({ "operation": "update", "id": "node_id", "description": "...",
         "graph": "practice", "language": "go" })

// Link two practice nodes
mutate({ "operation": "link", "from": "a", "to": "b", "relationship": "relates-to",
         "graph": "practice", "language": "go" })
```

### Cross-Graph Linking

Link a knowledge graph node to a practice graph node. Creates a proxy node in the knowledge graph:

```jsonc
mutate({ "operation": "link", "from": "agent_id", "to": "practice_node_id",
         "relationship": "uses", "graph": "practice", "language": "go" })
```

## License

The knowledge binary boots with no credentials for all free OSS functionality. Paid Fulminate Cloud features (`workflow`, `execution`, `sync`) require an OAuth login:

```bash
knowledge login  # OAuth device flow
```

Sign up at [fulminate.io/signup](https://fulminate.io) first if you don't already have an account. Check server status:

```jsonc
manage({ "operation": "status" })
```

## Agents

Use specialized agents for complex tasks:

| Agent          | Purpose                                                                          | When to use                                                        |
| -------------- | -------------------------------------------------------------------------------- | ------------------------------------------------------------------ |
| `researcher`   | Investigate topics using graph search + code analysis                            | Before planning — explore unknowns, gather context                 |
| `planner`      | Create structured phased plans with success criteria                             | After research — break work into steps                             |
| `implementer`  | Execute plans step-by-step with status tracking                                  | After plan is approved — follows steps, verifies criteria          |
| `test-planner` | Design test plans collaboratively with research and criteria discussion          | Before testing — define what to test and how                       |
| `tester`       | Execute test plans, record pass/fail/skip results                                | After test plan is created — run and verify                        |

**Workflow:** researcher → planner → implementer. Each builds on the knowledge graph state left by the previous.

## Skills

| Skill              | Purpose                                                    |
| ------------------ | ---------------------------------------------------------- |
| `/research`        | Investigate a topic — searches code, decisions, patterns   |
| `/plan`            | Create an implementation plan with phases, steps, criteria |
| `/implement`       | Execute a plan step-by-step with verification              |
| `/brainstorm`      | Interactive exploration with probing questions             |
| `/record-decision` | Record an architectural decision with rationale            |
| `/reflect`         | Examine thought patterns, tensions, blind spots            |
| `/reindex`         | Full reindex pipeline (prompts for confirmation)           |
| `/test-plan`       | Design a test plan with steps and pass/fail criteria       |
| `/test`            | Execute a test plan and record results                     |

## Quick Reference

| I want to...          | Use                                                                                    |
| --------------------- | -------------------------------------------------------------------------------------- |
| Find code             | `search({ "queries": [...] })`                                                         |
| Understand a function | `traverse({ "start": "file:Func", "graph": "code", "edge_types": ["calls"], "direction": "both" })` |
| See file structure    | `file_symbols({ "file_path": "..." })`                                                 |
| Check past reasoning  | `recall({ "text": "topic" })`                                                          |
| Record my thinking    | `think({ "content": "...", "session": "..." })`                                        |
| Record evidence       | `charge({ "thought": "id", "polarity": "positive", "weight": 7, "reasoning": "..." })` |
| Search knowledge      | `query({ "text": "topic" })`                                                           |
| Check past decisions  | `query({ "type": "decision" })`                                                        |
| Record a decision     | `record_decision({ "name": "...", "choice": "...", "rationale": "..." })`              |
| Create a project      | `create_project({ "name": "...", "description": "..." })`                              |
| Create a ticket       | `create_ticket({ "name": "...", "project_id": "..." })`                                |
| Plan work             | `create_plan({ "name": "...", "phases": [...] })`                                      |
| Find next step        | `what_next({ "project_id": "..." })`                                                   |
| Reindex code          | `manage({ "operation": "reindex" })`                                                   |
| Assemble context      | `assemble({ "id": "node_id" })`                                                        |
| Create test plan      | `create_test_plan({ "name": "...", "steps": [...] })`                                  |
| Run tests             | `assemble({ "id": "plan_id", "new_run": true })`                                       |
| Inspect a node        | `query({ "mode": "examine", "id": "node_id" })`                                        |
| View node history     | `traverse({ "start": "id", "graph": "versions", "edge_types": ["changed_to"], "direction": "out" })` |
| Check node volatility | `query({ "id": "node_id", "mode": "entropy" })`                                        |
| Find volatile nodes   | `query({ "mode": "entropy" })`                                                          |
| Search practices      | `search({ "query": "...", "graph": "practice", "language": "go" })`                    |
| Get help              | `help({ "topic": "..." })`                                                             |
