# Knowledge

*An engineering operating system for LLMs.*

The metaphor is literal. Collectors are the drivers: they pull your
code, cloud infrastructure, logs, and docs into one cross-linked,
queryable graph. `search` and `traverse` are the syscalls. Thoughts,
decisions, tickets, and plans are the persistent state. Brainstorm →
ticket → plan → implement is the process model. It all runs as a local
MCP server, and any LLM that speaks MCP works from the graph instead
of rediscovering your system every session.

Long-term memory falls out of this design — state persists across
sessions, machines, and teammates — but memory is the floor, not the
product. The graph knows your call graph, your cloud, your incidents,
and the reasoning behind your decisions, and one query surface reaches
all of it.

## See it work

Index a repo, then ask questions grep can't answer. Against this
repository:

```jsonc
search({ "queries": ["bisect embedding batch on token overflow"],
         "repo": "knowledge-mcp" })

// internal/embed/voyage.go — Voyage embedder: batches texts under item
//   and token budgets, classifies errors, bisects token-overflow batches
// internal/embed/voyage.go:180 isBatchTokenOverflow — detects batch token
//   overflow by unwrapping LLMError causes
```

Results are graph nodes, not text matches. Walk the call graph from
any hit:

```jsonc
traverse({ "start": "internal/embed/voyage.go:voyageEmbedder.EmbedBinaryBatch",
           "graph": "code", "repo": "knowledge-mcp",
           "edge_types": ["CALLS"], "direction": "in" })

// EmbedBinary                                     internal/embed/voyage.go
// TestVoyageEmbedder_BisectsOnBatchTokenOverflow  internal/embed/voyage_test.go
// TestVoyageEmbedder_PacksByTokenBudget           internal/embed/voyage_test.go
// ...
```

Shape questions get structural answers. This is tree-sitter pattern
matching against the parsed syntax tree, not regex:

```jsonc
ast({ "operation": "match", "language": "go", "pattern": "defer $X.Close()" })

// 65 matches across 1,560 files in 185ms: every deferred Close,
// through whitespace, comments, and receiver renames
```

The same engine rewrites. Give `replace` a capture template
(`"defer safeClose($X)"`) and it previews the unified diff without
touching disk (dry-run is the default), then applies atomically; a
rewrite that no longer parses is rejected, never written. Mechanical
multi-file refactors are one tool call, not a sed script.

Reasoning persists the same way. The hypothesis recorded while that
overflow was being debugged comes back in a later session with its
evidence attached:

```jsonc
thoughts({ "operation": "recall", "query": "voyage batch overflow" })

// 1. Voyage rejects the whole batch on token overflow, not the one long
//    text — bound batches by estimated tokens and bisect on overflow
//    [validated] charges: +2 (bisection test green; overflow retries gone)
```

And from any node you can keep walking — to the decision that shaped
the code, the ticket that shipped it, or the log stream where it
failed.

## What's in the graph

**Code intelligence.** Hybrid BM25 + semantic search over 31
tree-sitter-chunked languages, an indexed call graph, and structural
AST search *and replace*: match the shapes regex can't express, then
rewrite every site from a capture template, gated by a dry-run diff
and a per-file re-parse. "Is there code that *does* this" gets a real
answer, so agents extend what exists instead of re-implementing it.

**Reasoning with evidence.** Hypotheses are first-class nodes; evidence
attaches as weighted positive or negative charges, and propagation lets
contradictory beliefs find equilibrium. "Why did we do it this way" has
an answer months later. See [Reasoning](./docs/guides/reasoning.md).

**Workflow.** Brainstorm → ticket → plan → implement, with every
artifact in the graph and tickets synced to Linear in real time. One
coordinator dispatches researchers, planners, reviewers, and
implementers against shared state, so no single context has to hold
everything and a compaction or restart is a non-event. Jira, GitHub
Issues, and Asana are on the roadmap. The full process model, with its
routing and re-entry paths, is in
[Concepts](./docs/guides/concepts.md).

**Infrastructure and runtime.** Collectors for cloud (AWS, GCP, Azure,
Kubernetes), CI/CD, logs (CloudWatch, Loki, Elasticsearch, Stackdriver,
K8s Events), web pages, and PDFs — each a graph, all cross-linked to
code. An incident traces from log line to deploy to commit to the
design decision behind it. And the built-in families are not a closed
set: `custom_collector` registers your own collector binary, and the
graph it emits gets the same treatment as the rest — summarized,
embedded, searchable, syncable.

Practice graphs — best-practice patterns collected from books,
references, and websites — sit beside your code, so agents reach for
the established idiom instead of the first thing that compiles. The
full write-up for each pillar:
[Capabilities](./docs/guides/capabilities.md).

## Install

One line, macOS (Apple Silicon) or Linux (x86_64 / arm64):

```bash
curl -fsSL https://raw.githubusercontent.com/fulminate-io/knowledge-mcp/main/install.sh | sh
```

The script downloads the latest release of both binaries
(checksum-verified) into `~/.knowledge/bin`, then hands off to
`knowledge setup`. Setup writes your first-run config (auto-detecting
an LLM provider), installs the agents and skills for Claude Code
and/or Codex if those CLIs are present, and registers the MCP daemon
with them. It also installs user-level services (launchd on macOS,
`systemd --user` on Linux) so the graph server (127.0.0.1:15022) and
MCP daemon (127.0.0.1:15023) start at login. Everything runs as
**your user** — no `sudo` anywhere.

Re-running the same line upgrades in place; your config is never
touched. To configure interactively (pick a provider, paste optional
API keys), run `knowledge setup` in a terminal any time. Headless
provisioning: append flags after `sh -s --` (e.g. `--headless`,
`--no-service`); credentials come from the environment
(`ANTHROPIC_API_KEY`, `VOYAGE_API_KEY`, `LINEAR_API_KEY`, ...) or
`~/.knowledge/config` — never from flags. On Windows, follow the
[manual install guide](./docs/guides/install-windows.md).

<details>
<summary><b>Homebrew</b></summary>

```bash
brew tap fulminate-io/knowledge
brew install knowledge
brew services start knowledge-server   # local graph server  (127.0.0.1:15022)
brew services start knowledge          # shared MCP daemon    (127.0.0.1:15023)
knowledge install-claude-assets        # wire Claude Code (or: install-codex-assets)
```

Run the services as **your user**, never with `sudo`: a root
LaunchDaemon can't read your login keychain.

</details>

<details>
<summary><b>From source</b></summary>

Requirements: Go 1.26+, CGO enabled (tree-sitter C bindings). Building
from source produces the `knowledge` binary only; run `knowledge
install` afterwards to fetch the matching prebuilt `knowledge-server`
from GitHub releases (checksum-verified).

```bash
git clone https://github.com/fulminate-io/knowledge-mcp.git
cd knowledge-mcp
CGO_ENABLED=1 go build -o bin/knowledge .
```

Source-built users (no `brew services`) run the processes by hand:

```bash
knowledge serve                    # MCP daemon on 127.0.0.1:15023
knowledge start / status / stop    # knowledge-server lifecycle (15022)
```

</details>

### First index

Restart your editor so it picks up the new MCP server, then trigger the
first index from inside the LLM:

```jsonc
collect({ "type": "code", "id": "/absolute/path/to/repo" })
```

The first pass takes 30s–2min for a typical repo: tree-sitter chunks
the files, the LLM summarizes each node. Subsequent indexes are
incremental: only changed files re-summarize.

No credentials are required to get here. On first run the server
auto-detects an LLM provider: it prefers a logged-in Claude or Codex
CLI on `$PATH`, then falls back to `ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`, or `GEMINI_API_KEY` from the environment.

> [!WARNING]
> A large first index is thousands of LLM calls — one summary per node.
> If your summarizer is a logged-in `claude` or `codex` CLI, every call
> draws on that subscription's session quota. For a big repo, point the
> summarizer at an API provider first: add a `[summarizer]` section to
> `~/.knowledge/config` with `provider = "anthropic"`, `"openai"`, or
> `"gemini"` and the matching key, then restart the daemon. See
> [Configuration](./docs/guides/config.md). Subsequent indexes are
> incremental and cheap either way.

Full walkthroughs: **[Set up with Claude Code](./docs/guides/setup-claude.md)**
· **[Set up with Codex](./docs/guides/setup-codex.md)**. `knowledge doctor`
diagnoses install and daemon/server health. Connecting another MCP
client by hand? Point it at the daemon's streamable-HTTP endpoint:
`http://127.0.0.1:15023/mcp`.

## Two keys worth setting

Both are optional; both change what you get.

| Key | With it | Without it |
| --- | --- | --- |
| `VOYAGE_API_KEY` | Hybrid semantic + keyword search — the LLM finds code and knowledge by meaning | Keyword (BM25) search only |
| `LINEAR_API_KEY` | Projects and tickets sync to Linear in real time; status flows both ways | Tickets stay local to the graph |

Get a Voyage key at [voyageai.com](https://voyageai.com); the Linear
key is a personal API key from Linear's settings (Settings → API).
Both go in `~/.knowledge/config` (TOML, auto-created on first run;
config wins over the environment):

```toml
[credentials]
voyage_api_key = "..."
linear_api_key = "..."
```

To pin LLM providers and models explicitly, the same file takes
`[default]` and per-consumer sections; see
[Configuration](./docs/guides/config.md) for the full reference.

## Documentation

Step-by-step guides ship in [`docs/guides/`](./docs/guides/index.md):
setup ([Claude Code](./docs/guides/setup-claude.md),
[Codex](./docs/guides/setup-codex.md),
[Configuration](./docs/guides/config.md)), the mental model
([Concepts](./docs/guides/concepts.md),
[Capabilities](./docs/guides/capabilities.md),
[Reasoning](./docs/guides/reasoning.md)), collection
([Web](./docs/guides/web-collection.md) ·
[PDF](./docs/guides/pdf-collection.md) ·
[Recipes](./docs/guides/recipes.md)), and reference
([Binaries & CLI](./docs/guides/binaries.md) ·
[Agents](./docs/guides/agents.md) · [Skills](./docs/guides/skills.md)).

## Tools

23 MCP tools across ten graph families. The full reference is
[KNOWLEDGE_TOOLS.md](./KNOWLEDGE_TOOLS.md). The ones you'll touch
daily: `search`, `ast`, `traverse`, `thoughts`, `record_decision`,
`create_project` / `create_ticket` / `create_plan`, `assemble`,
`collect`. Generic primitives — `query`, `mutate`, `delete`,
`manage` — route by graph and operation.

## Fulminate Cloud (optional)

Knowledge OSS runs entirely local: bring your own LLM, zero
credentials, full feature set. [Fulminate Cloud](https://fulminate.io)
is the agent development environment on top of it: cloud machines your
coding agents run in, one graph your whole team cites, and the
automation that ties the two together.

A dev environment is a full remote workspace on its own VM; your
agents keep working after you close your laptop, and the knowledge
daemon runs inside it, serving the team's shared graph over MCP.
Workflows codify recurring work, built step by step in a visual
editor or conversationally in chat, delegating heavy steps to
subagents. Inbound events (webhooks, cron ticks, anything your
systems emit) pass through routing rules whose condition trees decide
where each one lands: a workflow run, a Slack channel, or forwarded
straight into one of your dev environments. Dashboards are drag-assembled UIs over a dev
environment, published as pages your team can open — no build step,
nothing to deploy. Chat and Slack bots answer from the same graph.
Underneath it all: live tracking of every run and every agent, usage
analytics down to token counts and tool latency, audit logs of what
agents read and wrote, hooks that gate what agents may run, and BYOC
when everything must stay in your own cloud account. All tiers are
BYOK: bring your own LLM key; Fulminate never resells tokens.

```bash
knowledge login    # browser-PKCE OAuth flow; token stored in your keychain
knowledge logout   # revoke + clear keychain
```

Logged in, the daemon serves tool calls from the hosted graph server;
logged out, it runs fully local. A subscription with
`mcp:knowledge:write` permission unlocks the `sync` tool: push local
graph state to cloud, pull team-visible state down, promote a working
copy as the team head.

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
