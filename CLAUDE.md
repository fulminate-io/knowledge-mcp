# Knowledge

A persistent memory and reasoning graph for LLMs. Runs as a local MCP server. Primitives: `think` / `charge` / `recall` for hypothesis + evidence, `informed-by` edges linking code → decision → plan, triple-graph architecture (code + cloud + knowledge). Free OSS; Fulminate Cloud hosts the paid tiers.

## Build

```bash
CGO_ENABLED=1 go build -o bin/knowledge .   # MCP stdio client
CGO_ENABLED=1 go test -p 4 ./...                          # all tests
```

CGO is required for tree-sitter C bindings (31 language grammars).

## Setup

The `knowledge` binary is the MCP stdio client. It needs a graph server to talk to. Two options:

**Local server** (free, file-backed):
```bash
knowledge install   # downloads the server binary for your platform
knowledge start     # spawns the server on 127.0.0.1:15022
knowledge status    # confirm running
```

**Fulminate Cloud** (paid, team-shared):
```bash
knowledge login     # browser-PKCE OAuth flow
```

Once a server is available, point your MCP host at the client:

```json
{
  "mcpServers": {
    "knowledge": {
      "command": "knowledge",
      "args": ["--root", "."]
    }
  }
}
```

The stdio client auto-connects to `127.0.0.1:15022` when unauthenticated, or routes to Fulminate Cloud when logged in. Run `knowledge login` for paid cloud features (team sync, always-on agents). Browser-only by design — headless environments are not supported.

## Code Exploration: knowledge tools FIRST, shell tools LAST

**Default to `search`, `file_symbols`, and `traverse` for every code-exploration task. Grep / Glob / Read / Bash-tail are the fallback, not the starting point.** The knowledge graph is indexed, semantically aware, and knows the call graph — shell tools aren't. Reaching for grep first is almost always slower and less accurate.

| Want to...                                                   | Use this first                                                                                                | Not this                    |
| ------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------- | --------------------------- |
| Find functions, types, patterns, implementations             | `search({ "queries": [...] })` (batch 3-5 terms)                                                              | Grep / rg                   |
| List symbols in a file before editing                        | `file_symbols({ "file_path": "..." })`                                                                        | Read the whole file         |
| Find callers of a function                                   | `traverse({ "start": "path/to/file.go:Func", "graph": "code", "repo": "...", "edge_types": ["CALLS"], "direction": "in" })`  | `grep -rn 'FuncName('`      |
| Find callees of a function                                   | `traverse({ "start": "path/to/file.go:Func", "graph": "code", "repo": "...", "edge_types": ["CALLS"], "direction": "out" })` | `grep -rn 'FuncName('`      |
| Find structural patterns (every `defer X.Close()`, every `sync.Once.Do`, every error-returning func decl) | `ast({ "operation": "match", "language": "go", "pattern": "defer $X.Close()" })`                              | `grep` (misses through whitespace, comments, token reorder) |
| Apply a UNIFORM structural edit across many files (rename a call pattern, wrap/unwrap a call, swap a deprecated API) | `ast({ "operation": "replace", "language": "go", "pattern": "defer $X.Close()", "replacement": "defer safeClose($X)" })` — dry-run previews the diff (default), `dry_run:false` applies | `sed` / `perl -pi` / `grep`+script (mangle multi-line + slice literals, can't scope by enclosing type, no re-parse safety) |
| Search knowledge (decisions, findings, rules, past thoughts) | `search({ "queries": [...], "graph": "knowledge" })` or `recall({ "text": "..." })`                           | Reading docs manually       |
| Inspect a node's full context (ancestry, edges, history)     | `query({ "mode": "examine", "id": "..." })`                                                                   | Multiple individual queries |

**When shell tools _are_ the right call:**

- Reading/editing a specific file whose path you already know → `Read` / `Edit` (after optionally calling `file_symbols` first)
- Counting callers of an **interface method** (Go's static analysis can't resolve interface dispatch) → fall back to `grep -rn '\.MethodName('`
- Checking log files, build output, runtime state, running processes → `Bash` (logs and live state aren't in the graph)
- Non-source files the indexer doesn't chunk (binary blobs, generated files, config not under git)

**Discover before traversing:** Don't memorize edge types — discover them:

```jsonc
query({ "mode": "stats", "graph": "code", "repo": "knowledge" })  // shows all node types and edge types
```

Use the returned edge types to pick `edge_types` for `traverse`. `edge_types` is case-insensitive (the tool case-folds to the canonical casing per graph: code/cloud/cicd/linkage/logs are uppercase, knowledge/practice are lowercase). Method IDs in the code graph are receiver-qualified — e.g. `internal/graphclient/client.go:GraphClient`, NOT `client.go:GraphClient`.

**Cross-graph traversal auto-resolves.** `traverse(graph: "code"|"cloud"|"practice", start: "<knowledge_proxy_id>")` resolves via linkage proxies. You don't need to find the proxy ID yourself.

**Red flag (reads):** if you're about to run `grep` or `rg` over `*.go` files, stop and ask whether `search`, `file_symbols`, or `traverse` would give you the answer faster. Usually yes.

**Red flag (structural writes):** about to write a `sed -i` / `perl -pi` one-liner or a bash loop to make the SAME structural edit across files? Stop — use `ast({operation:"replace"})`. It matches through whitespace/comments/token-reorder (regex can't), scopes by enclosing structure via the `where`-tree, dry-runs a unified diff before touching disk, re-parses every rewrite and REJECTS any that no longer parses (sed/perl happily write garbage), and writes atomically. One file you're already editing → `Edit`; the same structural change across many files → `ast replace`.

## Tools

**Tool design: primitives over shortcuts.** `query`, `traverse`, `mutate` are lean generic primitives — graph selection via the `graph` param. A small set of composite shortcuts (`query(mode: "plan_tree" | "lineage" | "evidence")`) are kept as exceptions, justified by frequent historical use. Default for a new pattern: compose with the primitive and use `query(mode: "stats")` to discover the vocabulary.

**Most-used tools:** `search`, `file_symbols`, `query`, `traverse`, `mutate`, `assemble`, `thoughts`, `what_next`, `record_decision`, `manage`. Structural search + replace: `ast` — a tree-sitter pattern DSL across all 31 indexed languages: `operation:"match"` finds structural shapes, `operation:"replace"` mass-rewrites them with a dry-run diff preview + per-file re-parse gate (the go-to for mechanical multi-file refactors, over grep/sed/perl). See `help("ast")`. Reasoning graph: `thoughts` (operation-dispatched: `think | charge | recall | trace | propagate` — see `help("thoughts")`). Batch creators: `create_project`, `create_ticket`, `create_plan`, `create_research`, `create_test_plan`. Conditional (paid scopes): `workflow`, `execution`, `sync`.

**For full reference call `help` directly:**

- `help()` — full tool index
- `help("node_types")` / `help("edge_types")` / `help("statuses")` — vocabulary
- `help("workflows")` — common multi-tool patterns
- `help("query")` / `help("traverse")` / `help("mutate")` / `help("manage")` / `help("ast")` / `help("thoughts")` — generic-tool deep dives
- `help("topology")` — analyzers and `query(mode:"topology")` dispatch
- `help("logs")` — ephemeral log graph workflow
- `help("patterns")` — pattern catalog (project + library practice graphs)
- `help("recipes")` — recipe DSL grammar, semantics, authoring workflow (graph→graph transformer)
- `help("sync")` — Fulminate Cloud sync (push / pull / promote)

**Workers:** install `code-smell-scanner` via `worker(operation:"create", ...)` from `examples/workers/code-smell-scanner.json`; trigger with a payload narrowing the scan (e.g. `{code_graph:"knowledge", language:"go", package_prefixes:["cmd/"], smell_ids:[...]}`).

## Agents

The `.claude/agents/` directory provides specialized agents for Claude Code. **All agents must use `model: opus`.**

| Agent          | Purpose                                                    | When to use                                               |
| -------------- | ---------------------------------------------------------- | --------------------------------------------------------- |
| `researcher`   | Investigate topics via graph search + code analysis        | Before planning — explore unknowns, gather context        |
| `planner`      | Create structured phased plans with success criteria       | After research — breaking work into steps                 |
| `implementer`  | Execute plans step-by-step with status tracking            | After plan is approved — follows steps, verifies criteria |
| `test-planner` | Design test plans collaboratively with criteria discussion | Before testing — defining scope and criteria              |
| `tester`       | Execute test plans, record pass/fail/skip results          | After test plan created — runs and reports                |

**Workflow:** researcher → planner → implementer. For testing: test-planner → tester. Each builds on the knowledge graph state left by the previous.

## Skills

The `.claude/skills/` directory provides slash commands for Claude Code.

| Skill              | Purpose                                                                                                  |
| ------------------ | -------------------------------------------------------------------------------------------------------- |
| `/research`        | Document what exists — code, decisions, patterns                                                         |
| `/plan`            | Create implementation plans with phases, steps, criteria                                                 |
| `/implement`       | Execute a plan step-by-step with verification                                                            |
| `/brainstorm`      | Interactive exploration with probing questions                                                           |
| `/record-decision` | Record architectural decisions with rationale                                                            |
| `/reflect`         | Examine thought patterns, tensions, blind spots                                                          |
| `/test-plan`       | Design a test plan with steps and pass/fail criteria                                                     |
| `/test`            | Execute a test plan and record results                                                                   |

## Thought Graph

The knowledge graph includes a persistent reasoning system. Use it to externalize thinking — not just conclusions, but the reasoning that led to them.

**Cycle:** `thoughts(operation:"think")` → `thoughts(operation:"charge")` → `thoughts(operation:"recall")` → reflect (`query(mode: "personality"|"tensions"|"blind_spots"|"summary")`). The `thoughts` tool is operation-dispatched: `think | charge | recall | trace | propagate`. Examine a single thought via `query(mode:"examine", id: thought_id)`. Link thoughts to other nodes via `mutate(operation:"link", from: thought_id, to: node_id, relationship:"informed-by"|"supports"|"contradicts"|"relates-to"|"produced")`.

**Always start with `thoughts(operation:"recall")`** before beginning work. Past thoughts contain debugging notes, design rationale, and gotchas that save re-investigation. Think early, charge often — thoughts without charges are hypotheses; with charges, evidence.

**Always use thoughts instead of Claude Code memories.** Thoughts are searchable via `thoughts(operation:"recall")`, linkable to other nodes, and chargeable with evidence. File-based memory is not connected to the graph.

**When to think:** during research (forming hypotheses), implementation (approach + trade-offs + unexpected behavior), debugging (always record the broken→fixed transition), brainstorming (exploring options), and after testing (charge thoughts with pass/fail results).

See `help("thoughts")` for the full op vocabulary, parameters, and examples.

## Recipes

Recipes (DSL transformer bodies) are **graph-resident, not file-resident**. The canonical store is `graph=transformers, name=recipes` (persisted to `~/.knowledge/transformers/recipes.bin`). Author and load via the graph; never check `.recipe` files into the repo.

```jsonc
mutate({ "operation": "create", "graph": "transformers", "type": "recipe",
         "name": "my-source-to-target",
         "content": "<DSL body>",
         "metadata": { "source_graph_type": "web", "target_graph_type": "practice", "target_name": "<lang>" } })
```

Recipes are user-specific data — what makes sense for your corpus is not what makes sense for someone else's. See `help("recipes")` for the DSL grammar.

## Reindexing

After a successful git commit, **ask the user** if they'd like to run `collect(type:"code", id:"<absolute-path-to-repo>")`. The `id` MUST be an absolute path; the code collector rejects relative paths (`"."`, `"./foo"`, etc.) because the repo name is derived from `filepath.Base(id)` and would key a fresh graph under `"."` rather than the real repo, triggering a full re-summarization. The chunk upload returns quickly; the client-side LLM pipeline drains summarization/embedding in the background. The staleness indicator in `search` results shows how far along the index is. Incremental: unchanged nodes carry summaries/vectors forward, only changed files and their ancestor packages are re-summarized.

## Other reference

- **Topology analyzers** (centrality, cycles, bridges, exposure) — `help("topology")`
- **Sync** (Fulminate Cloud bidirectional sync) — `help("sync")`
- **Log graphs** (ephemeral graphs from CloudWatch, Loki, ES, Stackdriver, K8s Events) — `help("logs")`
- **Practice graphs + pattern catalog** — `help("patterns")`, plus `help("query")` / `help("mutate")` for the practice-graph CRUD shape
- **Test plans + agent/skill instruction nodes** — `help("workflows")` covers both
