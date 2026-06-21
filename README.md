# Knowledge

*An engineering operating system for LLMs.*

LLMs have great short-term memory and zero long-term memory. They grep
when they should search a graph. They guess at infrastructure they've
never seen. The reasoning that produced last week's decision lives in
someone's scrollback. Knowledge is the layer that fixes this — the
persistent runtime your coding LLM lives inside, across sessions,
machines, and teammates.

Knowledge runs as a local MCP server. Collectors pull your code, cloud
infrastructure, logs, docs, and patterns into a queryable graph. The
LLM searches that graph instead of grepping. It externalizes hypotheses
and charges them with evidence as it learns. It runs your tickets and
plans and reviews against state that survives every context boundary.
The four kernel layers are simple: collectors are the drivers, search
and traverse are the syscalls, thoughts and decisions and plans are the
persistent state, and brainstorm → ticket → plan → revise → implement
is the process model.

---

## How it works

```mermaid
---
title: "Brainstorm = WHY · Ticket = WHAT · Plan = HOW · Implement = THE WORK"
---
flowchart TD

    Start([Human starts work])

    B["<b>Brainstorm — the WHY</b>
    Purpose — Research the full architectural surface that the principle touches
    Deliverables — Ticket (the WHAT) with thorough In Scope, Out of Scope, success criteria, attached patterns
    Human touch — States goal and guiding principles, confirms surface classification, signs off the ticket
    Human role peaks here by design"]

    P["<b>Plan — the HOW</b>
    Purpose — Lock in specifics, file paths, function names, phase ordering, criterion text
    Deliverables — Structured phased plan with reuse_check classifications
    Human touch — None unless context gap surfaces
    Planner open_questions are accusations of a bad prompt or bad ticket. Orchestrator self-test, can I answer honestly from my context? Yes, fix the brief, re-spawn. No, re-enter Brainstorm with the user to cover the gap together"]

    R["<b>Review — does the HOW match the WHAT?</b>
    Purpose — Adversarial four-tier audit of the plan against the ticket
    Deliverables — Findings report; next action determined automatically by severity thresholds
    Human touch — None, verdict and finding counts drive routing"]

    I["<b>Implement — the WORK</b>
    Purpose — Execute the plan one phase at a time
    Deliverables — Working code, tests green, lint clean, closure rolled up plan to ticket to project
    Human touch — None, agents work through phases autonomously"]

    Done([Status closure rolled up])

    Start --> B
    B -->|ticket signed off| P
    P -->|plan created| R
    R -->|"no T1/T2 and at most 2 T3, ship-as-is"| I
    I --> Done

    P -.->|"TICKET-GAP or open_question with no honest answer in orchestrator context — re-enter brainstorm mode with the user"| B
    R -.->|"T1 or T2 or 3 plus T3, auto revise plan"| P
    R -.->|"Tier 0 finding, ticket missed the principle"| B

    classDef phase fill:#FFFCEC,stroke:#B89030,stroke-width:2px,color:#222,text-align:left
    classDef terminator fill:#E8F4FF,stroke:#3A6FA0,stroke-width:2px,color:#222

    class B,P,R,I phase
    class Start,Done terminator

    linkStyle 0,1,2,3,4 stroke:#2A7A2A,stroke-width:2px
    linkStyle 5,6,7 stroke:#C04040,stroke-width:2px,stroke-dasharray:6 4
```

---

## 1. Reasoning that survives sessions

Most "LLM memory" tools are RAG: chunk a document, embed it, retrieve
the top-k by cosine similarity. That works for facts. It doesn't work
for reasoning under uncertainty.

Knowledge has a thought graph. Hypotheses are first-class nodes. As
evidence comes in, you attach charges — positive or negative, with a
weight and a reason. DeGroot-style propagation lets contradictory
beliefs find equilibrium across the graph; consistency scores let you
filter to the contested ones precisely because the system tracks where
the disagreements live. The LLM externalizes its working beliefs in
one session and recalls the contested threads next session, with the
full charge history intact.

```jsonc
thoughts({ "operation": "think",
           "content": "Auth retry storms correlate with deploys",
           "session": "auth-retry-rca" })
thoughts({ "operation": "charge", "thought": "<id>",
           "polarity": "positive", "weight": 5,
           "reasoning": "Grafana shows storm onset within 90s of every deploy" })
thoughts({ "operation": "recall", "query": "retry", "consistency_max": 0.6 })
```

The reasoning loop is the differentiator. Every other capability in
Knowledge — search, planning, decisions — feeds the loop or runs on
top of it.

## 2. Search across everything you have

Knowledge runs hybrid BM25 + vector search across every graph it knows
about: code, decisions, findings, cloud resources, log streams, and
docs. One query surface, every source — the LLM doesn't pick a
backend, it asks for what it wants.

Code search covers 30+ languages with tree-sitter chunking and a binary
HNSW index, plus structural AST search for the shapes regex can't
express — every `defer x.Close()`, every goroutine without a recover,
every public function returning an error, every framework-specific
call site. The AST DSL works the same way across every language it
parses; patterns in one language port without rewriting.

```jsonc
search({ "queries": ["retry backoff", "circuit breaker"], "repo": "all" })
ast({ "language": "go", "pattern": "defer $X.Close()" })
```

Results from any graph link to nodes you can traverse. Walk from a
search hit to its callers, to the decision that introduced it, to the
cloud resource that consumes it, to the log stream that tracks it. The
graphs are connected; the search is, too.

## 3. Real workflow integration

Knowledge tickets are real tickets. The flow:

**Brainstorm.** The LLM externalizes hypotheses, searches prior
decisions for context, and pulls in architecture patterns from the
practice graph. The output is a research project with charges and a
clear next step — not a chat transcript that evaporates when the
window closes.

**Ticket.** Filed against a project, synced to Linear in real time.
Create a ticket in Knowledge and it shows up in Linear with the right
team workflow state; status updates flow both ways. Tickets carry
pattern endorsements and an explicit out-of-scope section so the
planner stays in its lane.

**Plan.** Phased decomposition with success criteria per step. Every
step links back to the ticket it implements and the patterns it's
building toward. Plans live in the graph; they can be assembled into
full context with one call.

**Revise.** A reviewer agent walks the plan before implementation —
checks reuse against the existing codebase, flags scope creep, audits
language anti-patterns the ticket flagged, gates implementation on
high-severity findings. Cheap to run; catches expensive mistakes.

**Implement.** Steps execute one at a time. Status flows back through
the ticket into Linear. Failures land as findings linked to the step,
not as lost work — the next session picks up where the last one
stopped.

Linear sync runs today. Jira, GitHub Issues, and Asana are on the
roadmap.

## 4. Persistent context the LLM trusts

The graph is only useful if it reflects reality. Knowledge ships with
collectors for the surfaces a coding LLM cares about, each populating
its own graph type:

**Code.** Tree-sitter chunkers across 30+ languages produce per-file
AST nodes — functions, types, calls, imports — indexed for hybrid
search and walked as a static call graph. Branch overlays index
non-default branches as thin diffs over main; only changed nodes
re-summarize and re-embed.

**Cloud.** AWS, GCP, Azure, and Kubernetes resources land as nodes
with their topology preserved. The Helm chart that deploys a service,
the IAM role that grants its access, the secret it mounts — all
queryable, all linkable to the code that defines them.

**Logs.** Ephemeral per-query graphs from CloudWatch, Loki,
Elasticsearch, Stackdriver, and Kubernetes Events. Templates cluster
by message shape; streams correlate to cloud resources automatically.
Pull logs for an incident, walk to the resource that emitted them,
walk to the code that runs on that resource — one query.

**Web.** URLs ingest into a structured graph: titles, sections, links,
extracted entities. The LLM reads documentation as a graph it can
traverse, not as a string it has to summarize.

**PDF.** Page-aware chunks with structural reading order, font
fingerprints, and bounding boxes. Specs and papers come in as nodes
the LLM can search and cite, not as raw text.

Cross-graph traversal auto-resolves proxies. Walk from a failing log
line back to the code that emitted it, the cloud resource it ran on,
and the decision behind that code — in one call. The graph is the
context layer; the LLM trusts it because it's traceable end-to-end.

---

## Install

### Homebrew

The recommended path on macOS and Linux. `brew install knowledge` pulls
in two formulae — the `knowledge` binary (CLI + the shared MCP daemon)
and the `knowledge-server` graph server — and registers a launchd
service for each.

```bash
brew tap fulminate-io/knowledge
brew install knowledge
```

Start both background services (run as **your user** — do not `sudo`; a
root LaunchDaemon can't read your login keychain, which breaks Fulminate
Cloud auth):

```bash
brew services start knowledge-server   # local graph server  (127.0.0.1:15022)
brew services start knowledge          # shared MCP daemon    (127.0.0.1:15023)
```

The daemon is the MCP endpoint your editor connects to; the graph server
backs it locally. Wire your editor with `knowledge install-claude-assets`
or `knowledge install-codex-assets` (below) — they point the editor at
the daemon for you.

### From source

Requirements: Go 1.26+, CGO enabled (tree-sitter C bindings). Optional:
[Voyage AI key](https://voyageai.com) for vector search;
[Fulminate Cloud account](https://fulminate.io) for paid cloud features.

Building from source produces the `knowledge` binary only (CLI + the
shared MCP daemon). The `knowledge-server` graph server is a prebuilt
download — it ships via the Homebrew formula or a release tarball and is
not built from this repo. After building, run `knowledge install` to
fetch the matching `knowledge-server` from the GitHub releases — it
verifies the checksum and installs it next to `knowledge`.

```bash
CGO_ENABLED=1 go install github.com/fulminate-io/knowledge-mcp@latest
```

Or build from source:

```bash
git clone https://github.com/fulminate-io/knowledge-mcp.git
cd knowledge-mcp
CGO_ENABLED=1 go build -o bin/knowledge .
```

That produces the `knowledge` binary. (`make build` does the same and
also refreshes the embedded Claude Code agents and skills.)

### Running it

Two long-lived processes:

- **`knowledge serve`** — the shared MCP daemon. A streamable-HTTP MCP
  server on `127.0.0.1:15023` (path `/mcp`); this is what your editor
  connects to. `brew services start knowledge` runs it.
- **`knowledge-server`** — the local graph server on `127.0.0.1:15022`,
  file-backed under `~/.knowledge/`. The daemon talks to it for local
  work and sync. `brew services start knowledge-server` runs it.

Source-built users (no `brew services`) run them by hand:

```bash
knowledge serve                    # MCP daemon on 127.0.0.1:15023
knowledge start / status / stop    # knowledge-server lifecycle (15022)
knowledge doctor                   # diagnose install + daemon/server health
```

**Fulminate Cloud** (paid, team-shared) — log in once and the daemon
routes your tool calls to the hosted graph server:

```bash
knowledge login      # browser-PKCE OAuth flow; token stored in your keychain
```

When you're logged in, the daemon serves every tool call from cloud; the
local `knowledge-server` is then needed only for `sync`. Logged out, the
daemon runs fully local against `knowledge-server`.

### Claude Code integration

One command wires Claude Code to the daemon and installs the curated
catalog:

```bash
knowledge install-claude-assets
```

→ Full walkthrough: **[Set up with Claude Code](./docs/guides/setup-claude.md)**.

That does two things:

- **Registers the MCP daemon** with Claude Code (`claude mcp add-json -s
  user knowledge '{"type":"http","url":"http://127.0.0.1:15023/mcp","timeout":180000}'`)
  — no manual `.mcp.json` editing.
- **Writes the curated agents** (`~/.claude/agents/*.md`) and skills
  (`~/.claude/skills/*/SKILL.md`) so Claude Code picks them up.

**Keeping them in sync:** the catalog is embedded in the `knowledge`
binary, so re-run `install-claude-assets` after every upgrade to refresh
your installed copies — a startup hint warns when they drift. Preview
before writing with `--dry-run`, see exactly what would change with
`--diff`, and list each file with `--verbose`.

### Codex CLI integration

Codex consumes the same curated catalog. One command wires Codex to the
daemon and installs the agents and skills:

```bash
knowledge install-codex-assets
```

→ Full walkthrough: **[Set up with Codex](./docs/guides/setup-codex.md)**.

That does two things:

- **Registers the MCP daemon** with Codex (`codex mcp add knowledge
  --url http://127.0.0.1:15023/mcp`; Codex names the streamable-HTTP
  target with `--url`) — no manual `config.toml` editing.
- **Writes the catalog** using Codex's native layout (split roots):
  - skills → `~/.agents/skills/<name>/SKILL.md` (verbatim copies of the
    Claude skills — Codex interprets the same constructs)
  - agents → `~/.codex/agents/<name>.toml` (the Claude agents converted
    to Codex subagent TOML: `name`, `description`, `developer_instructions`)
  - a clobber-safe knowledge-priming block in `~/.codex/AGENTS.md`,
    bounded by managed markers so any prose you keep there is preserved

**Keeping them in sync:** the catalog is embedded in the binary, so
re-run `install-codex-assets` after every upgrade to refresh your
installed copies — a startup hint warns when they drift. Preview with
`--dry-run`, see exactly what would change with `--diff`, list each file
with `--verbose`. Skills you've added yourself and any non-managed prose
in `~/.codex/AGENTS.md` are left untouched.

## First index

`install-claude-assets` / `install-codex-assets` already registered the
daemon with your editor, so there's no `.mcp.json` to hand-edit. Restart
your editor so it picks up the new MCP server, then trigger an initial
index from inside the LLM:

```jsonc
collect({ "type": "code", "id": "/absolute/path/to/repo" })
```

First pass takes 30s–2min for typical repos: tree-sitter chunks the
files, the LLM summarizes each node, Voyage embeds them. Subsequent
indexes are incremental — only changed files re-summarize. Branch
overlays and auto-compaction recovery run in the background.

> Connecting another MCP client by hand? Point it at the daemon's
> streamable-HTTP endpoint: `http://127.0.0.1:15023/mcp`.

### LLM and embedding providers

Bring your own key. On first run the server auto-detects a provider:
it prefers a logged-in Claude or Codex CLI when one is on `$PATH`,
then falls back to an API key from the environment —
`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `GEMINI_API_KEY`. Set
`VOYAGE_API_KEY` to enable vector search; without it, search runs on
BM25 alone.

To pin providers and models explicitly, edit `~/.knowledge/config`
(TOML, auto-created on first run):

```toml
[default]
provider = "anthropic"        # anthropic | openai | gemini | claude-cli | codex-cli
model    = "claude-haiku-4-5"

[summarizer]                   # optional — overrides [default] for code summarization
provider = "openai"
model    = "gpt-4o-mini"

[credentials]                  # optional — overrides the matching env vars
voyage_api_key = "..."
```

## Documentation

Full step-by-step guides ship in [`docs/guides/`](./docs/guides/index.md):

**Setup** — first run, end to end:

- [Set up with Claude Code](./docs/guides/setup-claude.md)
- [Set up with Codex](./docs/guides/setup-codex.md)
- [Configuration](./docs/guides/config.md) — the `~/.knowledge/config` file: providers, models, credentials, and fallback chains

**Concepts** — the mental model:

- [Concepts](./docs/guides/concepts.md) — the graph families, the selector vocabulary, and the client / daemon / graph-server topology
- [Reasoning](./docs/guides/reasoning.md) — the thought graph: recall → think → charge → reflect, and DeGroot propagation

**Collection** — getting data in:

- [Web](./docs/guides/web-collection.md) · [PDF](./docs/guides/pdf-collection.md) · [Transformer recipes](./docs/guides/recipes.md)

**Reference**:

- [Binaries & CLI flags](./docs/guides/binaries.md) · [Agents](./docs/guides/agents.md) · [Skills](./docs/guides/skills.md)

## Tools

22 MCP tools across the ten graph families. The full reference is
[KNOWLEDGE_TOOLS.md](./KNOWLEDGE_TOOLS.md). The ones you'll touch
daily: `search`, `ast`, `traverse`, `thoughts`, `record_decision`,
`create_project` / `create_ticket` / `create_plan`, `assemble`,
`collect`. Generic primitives — `query`, `mutate`, `delete`,
`manage` — route by graph and operation.

## Fulminate Cloud (optional)

Knowledge OSS runs entirely local — bring your own LLM, zero
credentials, full feature set. For teams that want a hosted shared
graph, always-on investigation agents, and enterprise governance,
[Fulminate Cloud](https://fulminate.io) offers capabilities that
structurally can't run on a laptop:

- Multi-user shared graph with bidirectional sync
- Always-on autonomous investigation against your synced graph
- Webhook reception (GitHub, PagerDuty, Alertmanager, Grafana,
  Datadog, …)
- Scheduled and event-triggered workflows
- SSO/SCIM, RBAC, audit, BYOC for enterprise

All Fulminate Cloud tiers are BYOK — bring your LLM key, Fulminate
never resells tokens. Connect via `knowledge login`. Browser-only by
design — headless environments are not supported. Sign up at
[fulminate.io](https://fulminate.io).

```bash
knowledge login    # browser-PKCE OAuth flow
knowledge logout   # revoke + clear keychain
```

A subscription with `mcp:knowledge:write` permission unlocks the
`sync` MCP tool: push local graph state to cloud, pull team-visible
state down, promote a working copy as the team head.

## Status

Pre-1.0. Active development toward Apache 2.0 OSS launch.

**Shipping today**: MCP server with ten-graph architecture, thought
reasoning with DeGroot propagation, 30+ topology analyzers, branch
overlays, auto-compaction recovery, tokenless OSS boot, browser-PKCE
OAuth login with keychain-backed credentials.

## Contributing

Contribution guide, build rules, test conventions, and architectural
constraints: [CLAUDE.md](./CLAUDE.md).

## License

Apache 2.0 on OSS launch. See [LICENSE](./LICENSE).
Fulminate Cloud commercial use is separately licensed; see
[fulminate.io/legal](https://fulminate.io/legal).
