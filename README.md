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

Code search covers 31 languages with tree-sitter chunking and a binary
HNSW index, plus structural AST search for the shapes regex can't
express — every `defer x.Close()`, every goroutine without a recover,
every public function returning an error, every framework-specific
call site. The AST DSL works the same way across all 31 languages;
patterns in one language port without rewriting.

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

**Code.** Tree-sitter chunkers across 31 languages produce per-file
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

The recommended path on macOS and Linux. The formula installs both the
`knowledge` MCP client and the `knowledge-server` graph server side by
side, so lifecycle and auto-spawn work out of the box.

```bash
brew tap fulminate-io/knowledge
brew install knowledge
```

Run the server as a background service (optional — the client also
auto-spawns it on demand):

```bash
brew services start knowledge   # launchd-managed knowledge-server
```

### From source

Requirements: Go 1.26+, CGO enabled (tree-sitter C bindings). Optional:
[Voyage AI key](https://voyageai.com) for vector search;
[Fulminate Cloud account](https://fulminate.io) for paid cloud features.

Building from source produces the `knowledge` client only; the
`knowledge-server` binary ships via the Homebrew formula or a release
download.

```bash
CGO_ENABLED=1 go install github.com/fulminate-io/knowledge-mcp@latest
```

Or build from source:

```bash
git clone https://github.com/fulminate-io/knowledge-mcp.git
cd knowledge-mcp
CGO_ENABLED=1 go build -o bin/knowledge .
```

That produces the `knowledge` MCP stdio client. (`make build`
does the same and also refreshes the embedded Claude Code agents and
skills.)

### Server setup

`knowledge` is the MCP stdio client. It proxies tool calls to a
`knowledge-server` graph server over loopback TCP (`127.0.0.1:15022`
by default). Two options:

**Local server** (free, file-backed). The client looks for a
`knowledge-server` binary next to itself or on `$PATH` and spawns it
automatically the first time a tool call needs it, so the common case
needs no manual lifecycle. To drive it by hand:

```bash
knowledge start      # spawn knowledge-server on 127.0.0.1:15022
knowledge status     # confirm running (pid + node/edge counts)
knowledge stop       # graceful drain + shutdown
```

The server stores graphs in `~/.knowledge/`. If `knowledge start`
reports that `knowledge-server` was not found, install it via Homebrew
(below), which ships both binaries side by side.

**Fulminate Cloud** (paid, team-shared):
```bash
knowledge login      # browser-PKCE OAuth flow — no local server needed
```

Logged-in users route tool calls through Fulminate Cloud's hosted
graph server. No local server installation required.

### Claude Code integration

Install the curated agents and skills that ship with Knowledge:

```bash
knowledge install-claude-assets
```

That writes the project's curated agents (`~/.claude/agents/*.md`) and
skills (`~/.claude/skills/*/SKILL.md`) so Claude Code picks them up.

**Keeping them in sync:** the catalog is embedded in the `knowledge`
binary, so re-run `install-claude-assets` after every upgrade to refresh
your installed copies — a startup hint warns when they drift. Preview
before writing with `--dry-run`, see exactly what would change with
`--diff`, and list each file with `--verbose`.

Wiring the MCP server itself into `.mcp.json` is covered under
[Connect](#connect) below.

### Codex CLI integration

Codex consumes the same curated catalog. Install the Codex twin of the
agents and skills:

```bash
knowledge install-codex-assets
```

That writes, using Codex's native layout (split roots):

- skills → `~/.agents/skills/<name>/SKILL.md` (verbatim copies of the
  Claude skills — Codex interprets the same constructs)
- agents → `~/.codex/agents/<name>.toml` (the Claude agents converted to
  Codex subagent TOML: `name`, `description`, `developer_instructions`)
- a clobber-safe knowledge-priming block in `~/.codex/AGENTS.md`,
  bounded by managed markers so any prose you keep there is preserved

**Keeping them in sync:** the catalog is embedded in the binary, so
re-run `install-codex-assets` after every upgrade to refresh your
installed copies — a startup hint warns when they drift. Preview with
`--dry-run`, see exactly what would change with `--diff`, list each file
with `--verbose`. Skills you've added yourself and any non-managed prose
in `~/.codex/AGENTS.md` are left untouched.

Register the MCP server itself in `~/.codex/config.toml`:

```toml
[mcp_servers.knowledge]
command = "knowledge"
args = ["mcp"]
```

`knowledge` resolves through `$PATH` after brew install; source-built
users use the absolute path to `bin/knowledge`.

## Connect

Add to `.mcp.json` at your project root:

```json
{
  "mcpServers": {
    "knowledge": {
      "command": "knowledge"
    }
  }
}
```

That's the whole config — `knowledge` resolves through `$PATH` after
brew install. Source-built users replace `knowledge` with the absolute
path to `bin/knowledge`.

The stdio client (`knowledge`) auto-spawns the graph server on first
use if no server is running, so brew users without `brew services
start knowledge` get a working setup anyway. To drive lifecycle
manually:

```bash
knowledge start    # spawn the server
knowledge status   # show pid + node/edge counts
knowledge stop     # graceful shutdown
```

After installing, point your MCP client at the binary, restart the
client, and trigger an initial index from inside the LLM:

```jsonc
collect({ "type": "code", "id": "/absolute/path/to/repo" })
```

First pass takes 30s–2min for typical repos: tree-sitter chunks the
files, the LLM summarizes each node, Voyage embeds them. Subsequent
indexes are incremental — only changed files re-summarize. Branch
overlays and auto-compaction recovery run in the background.

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

## Tools

21 MCP tools across the five graph types. The full reference is
[KNOWLEDGE_TOOLS.md](./KNOWLEDGE_TOOLS.md). The ones you'll touch
daily: `search`, `ast`, `traverse`, `thoughts`, `record_decision`,
`create_project` / `create_ticket` / `create_plan`, `assemble`,
`what_next`. Generic primitives — `query`, `mutate`, `delete`,
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
knowledge login    # WorkOS browser-PKCE flow
knowledge logout   # revoke + clear keychain
```

A subscription with `mcp:knowledge:write` permission unlocks the
`sync` MCP tool: push local graph state to cloud, pull team-visible
state down, promote a working copy as the team head.

## Status

Pre-1.0. Active development toward Apache 2.0 OSS launch.

**Shipping today**: MCP server with five-graph architecture, thought
reasoning with DeGroot propagation, 25 topology analyzers, branch
overlays, auto-compaction recovery, tokenless OSS boot, browser-PKCE
OAuth login with keychain-backed credentials.

**In flight toward OSS launch**: Apache 2.0 LICENSE + release
automation, repo rename, bidirectional sync to Fulminate Cloud,
post-incident learning loop, code-aware RCA demo.

## Contributing

Contribution guide, build rules, test conventions, and architectural
constraints: [CLAUDE.md](./CLAUDE.md).

## License

Apache 2.0 on OSS launch. See [LICENSE](./LICENSE) once committed.
Fulminate Cloud commercial use is separately licensed; see
[fulminate.io/legal](https://fulminate.io/legal).
