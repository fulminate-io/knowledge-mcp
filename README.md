# Knowledge

knowledge is a set of MCP tools and skills for producing better code
with fewer tokens. The tools index your code, cloud, logs, and docs into
cross-linked graphs and answer questions sized to the question: hybrid
code search, call-graph traversal, structural AST search and replace
across 31 languages, best-practice patterns with runnable checks, and a
graph that keeps thoughts, decisions, tickets, and plans across
sessions. The skills run the engineering workflow over those tools,
from research through plan, implement, and review. It runs as a local
MCP server, so any LLM that speaks MCP can use it.

The retrieval argument is simple. A whole-file read puts text in the
context that is not the answer; a fragment read leaves gaps. The index
returns the pieces the question asked for, which is where the accuracy
and the token savings come from.

## What it does

| Pillar | What runs | Guide |
| --- | --- | --- |
| Code intelligence | BM25 + semantic search over tree-sitter chunks, an indexed call graph, AST match and replace with a dry-run diff and a re-parse gate | [Capabilities](./docs/guides/capabilities.md) |
| Reasoning with evidence | Hypotheses are nodes; evidence attaches as weighted charges; propagation settles contradictions. `tensions` lists thoughts whose evidence disagrees | [Reasoning](./docs/guides/reasoning.md) |
| Practices and checks | `/ingest-patterns` pulls best practices and style rules from a book, reference, or site into a practice graph the agent reads before it writes. Where a rule has a shape, `manage_checks` stores it as a runnable assertion with a bad and a good fixture, and runs it over your tree | [Corpus checks](./docs/guides/corpus-checks.md) · [manage_checks](./docs/guides/tools/manage_checks.md) |
| Workflow | Brainstorm → ticket → plan → implement over the same graphs, with tickets synced to Linear. Researchers, planners, reviewers, and implementers share state, so a restart loses nothing the graph holds | [Concepts](./docs/guides/concepts.md) |
| Infrastructure and runtime | Web pages and PDFs collect built in. Cloud, CI, and log collectors install from [knowledge-contrib](https://github.com/fulminate-io/knowledge-contrib) (see below), each a graph cross-linked to code. An incident traces from log line to deploy to commit to the decision behind it | [Web](./docs/guides/web-collection.md) · [PDF](./docs/guides/pdf-collection.md) · [Recipes](./docs/guides/recipes.md) |

Jira, GitHub Issues, and Asana sync are on the roadmap.

## See it work

Against this repository. Each result is a graph node you can keep
walking from.

```jsonc
search({ "queries": ["bisect embedding batch on token overflow"],
         "repo": "knowledge-mcp" })

// internal/embed/voyage.go — Voyage embedder: batches texts under item
//   and token budgets, classifies errors, bisects token-overflow batches
// internal/embed/voyage.go:180 isBatchTokenOverflow — detects batch token
//   overflow by unwrapping LLMError causes
```

```jsonc
ast({ "operation": "match", "language": "go", "pattern": "defer $X.Close()" })

// 65 matches across 1,560 files in 185ms
```

Give `replace` a capture template (`"defer safeClose($X)"`) and it
previews the unified diff, then applies atomically. A rewrite that no
longer parses is rejected, never written.

```jsonc
thoughts({ "operation": "recall", "query": "voyage batch overflow" })

// 1. Voyage rejects the whole batch on token overflow, not the one long
//    text — bound batches by estimated tokens and bisect on overflow
//    [validated] charges: +2 (bisection test green; overflow retries gone)
```

That hypothesis was recorded while the bug was being debugged. It comes
back in a later session with its evidence attached.

## Practices and checks

An agent writes the first thing that compiles unless something in its
context says otherwise. A style guide in a wiki is not in its context. A
practice graph is.

`/ingest-patterns` takes a source (a book, a public catalog, a reference
site, a PDF) and lands its practices as nodes under a source hub: the
pattern, when to use it, a worked example, the reference. The agent
searches those beside your code, so "how do we do retries here" returns
the established idiom and not a guess. Practices from your own team go in
the same way, from a doc or a ticket.

Guidance the agent reads is one half. The other half is rules something
can run. Where a practice has a shape, the skill also writes a sister
check, and `manage_checks` is the tool for those:

```jsonc
manage_checks({ "operation": "create", "language": "go",
                "name": "defer-close-inside-a-loop",
                "check_type": "ast_pattern", "severity": "warning",
                "dsl_pattern": "defer $X.Close()",
                "check_where": "{\"inside_pattern\":{\"of\":\"$match\",\"pattern\":\"for $$$_ { $$$_ }\"}}",
                "fixture_bad":  { "name": "…", "summary": "…", "content": "…" },
                "fixture_good": { "name": "…", "summary": "…", "content": "…" } })
```

One call authors the check and both fixtures, and nothing is written
unless the check fires on the bad example and stays silent on the good
one. A check that has never been run reads exactly like a passing one to
everything downstream, so the gate runs at admission.

```jsonc
manage_checks({ "operation": "run", "repo": "knowledge-mcp", "language": "go",
                "path_prefix": "internal" })
```

`run` walks the working tree and leads with one verdict line. The same
run is available from the shell as `knowledge check run`, exiting 0 for
clean, 3 for flagged, and 4 for inconclusive, so a check can back a plan
criterion or a CI step. Rules with no deterministic expression are
stored as `llm_only` and go to a judgment lane instead of a scan.

The good fixture has to be a near-miss: the same construct as the bad
one, placed where it is legitimate. A pair that shares nothing proves
nothing. [Corpus checks](./docs/guides/corpus-checks.md) covers that and
the other ways a check passes its gate and is still wrong.

## Install

macOS (Apple Silicon) or Linux (x86_64 / arm64):

```bash
curl -fsSL https://raw.githubusercontent.com/fulminate-io/knowledge-mcp/main/install.sh | sh
```

The script downloads both binaries (checksum-verified) into
`~/.knowledge/bin`, then runs `knowledge setup`. Setup detects an LLM
provider, installs the agents and skills for Claude Code and Codex if
those CLIs are present, registers the MCP daemon with them, and installs
user-level services (launchd or `systemd --user`) so the graph server
(127.0.0.1:15022) and MCP daemon (127.0.0.1:15023) start at login.
Everything runs as your user. No `sudo` anywhere.

Re-running the same line upgrades in place and never touches your
config. Windows: [manual install](./docs/guides/install-windows.md).
Docker: [docker.md](./docs/guides/docker.md).

<details>
<summary><b>Homebrew</b></summary>

```bash
brew tap fulminate-io/knowledge
brew install knowledge
brew services start knowledge-server   # graph server  127.0.0.1:15022
brew services start knowledge          # MCP daemon    127.0.0.1:15023
knowledge install-claude-assets        # or: install-codex-assets
```

Run the services as your user, never with `sudo`. A root LaunchDaemon
can't read your login keychain.

</details>

<details>
<summary><b>From source</b></summary>

Go 1.26+ with CGO enabled (tree-sitter C bindings). This builds the
`knowledge` binary only; `knowledge install` fetches the matching
prebuilt `knowledge-server` from GitHub releases.

```bash
git clone https://github.com/fulminate-io/knowledge-mcp.git
cd knowledge-mcp
CGO_ENABLED=1 go build -o bin/knowledge .
knowledge serve                    # MCP daemon on 127.0.0.1:15023
knowledge start / status / stop    # knowledge-server lifecycle (15022)
```

</details>

## First index

Restart your editor so it picks up the MCP server, then from inside the
LLM:

```jsonc
collect({ "type": "code", "id": "/absolute/path/to/repo" })
```

The first pass takes 30s–2min on a typical repo. Tree-sitter chunks the
files and the LLM summarizes each node. Later indexes are incremental;
only changed files re-summarize.

No credentials are needed to get here. The server prefers a logged-in
Claude or Codex CLI on `$PATH`, then falls back to `ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`, or `GEMINI_API_KEY`.

> [!WARNING]
> A large first index is thousands of LLM calls, one summary per node.
> If the summarizer is a logged-in `claude` or `codex` CLI, every call
> draws on that subscription's quota. For a big repo, point the
> summarizer at an API provider first: a `[summarizer]` section in
> `~/.knowledge/config` with `provider = "anthropic"`, `"openai"`, or
> `"gemini"` and the key, then restart the daemon. See
> [Configuration](./docs/guides/config.md).

`knowledge doctor` diagnoses install and daemon health. Any other MCP
client connects at `http://127.0.0.1:15023/mcp`.

## Two optional keys

| Key | With it | Without it |
| --- | --- | --- |
| `VOYAGE_API_KEY` | Hybrid semantic + keyword search | Keyword (BM25) search only |
| `LINEAR_API_KEY` | Projects and tickets sync to Linear; status flows both ways | Tickets stay local to the graph |

Both go in `~/.knowledge/config` (TOML, auto-created on first run;
config wins over the environment):

```toml
[credentials]
voyage_api_key = "..."
linear_api_key = "..."
```

## Collectors

A collector is a process that speaks MCP and answers with nodes and
edges. The daemon spawns it, calls one tool, and writes the result into
a graph of its own, which then gets the same summarize, embed, search,
and sync treatment as code. The pre-built ones live in
[knowledge-contrib](https://github.com/fulminate-io/knowledge-contrib),
one static binary each:

| Cloud | CI | Logs |
| --- | --- | --- |
| `aws` · `gcp` · `azure` · `k8s` | `github-actions` · `gitlab-ci` · `bitbucket-pipelines` | `cloudwatch` · `loki` · `stackdriver` · `k8s-logs` |

```bash
curl -fsSL https://raw.githubusercontent.com/fulminate-io/knowledge-contrib/main/install.sh | sh -s -- <collector>
```

The script verifies the archive against the release checksums, places
the binary under `~/.knowledge/bin`, and registers it with
`knowledge collector add`. Provider credentials go in the daemon's
environment, never in the config entry. Each module's README has what
it reads, what it produces, and the config entry, so you can skip the
script and write the entry yourself.

To write your own, the same repo ships the framework the built-in ones
use, in Go, Python, Rust, and TypeScript. Your collector is dialed and
registered the same way as these.

## Guides

| | |
| --- | --- |
| Setup | [Claude Code](./docs/guides/setup-claude.md) · [Codex](./docs/guides/setup-codex.md) · [Configuration](./docs/guides/config.md) · [Docker](./docs/guides/docker.md) · [Windows](./docs/guides/install-windows.md) |
| Mental model | [Concepts](./docs/guides/concepts.md) · [Capabilities](./docs/guides/capabilities.md) · [Reasoning](./docs/guides/reasoning.md) · [Corpus checks](./docs/guides/corpus-checks.md) |
| Collection | [Web](./docs/guides/web-collection.md) · [PDF](./docs/guides/pdf-collection.md) · [Recipes](./docs/guides/recipes.md) · [Collectors](https://github.com/fulminate-io/knowledge-contrib) |
| Reference | [Tools](./KNOWLEDGE_TOOLS.md) · [Per-tool guides](./docs/guides/tools/) · [Binaries & CLI](./docs/guides/binaries.md) · [Agents](./docs/guides/agents.md) · [Skills](./docs/guides/skills.md) |

23 MCP tools across ten graph families. The ones you'll touch daily:
`search`, `ast`, `traverse`, `thoughts`, `manage_checks`,
`record_decision`, `create_ticket`, `assemble`, `collect`. Skills you'll
type: `/research`, `/plan`, `/implement`, `/ingest-patterns`, `/retro`.

## Fulminate Cloud

Knowledge OSS runs entirely local: bring your own LLM, zero
credentials, full feature set. If one machine and one developer is your
whole setup, the local server is the product, not a trial of the paid
one.

[Fulminate Cloud](https://fulminate.io) is the same graph as a shared
team environment. Cloud machines your coding agents run in, one graph
the whole team reads and writes, workflows that turn webhooks, cron
ticks, and Slack messages into runs, and dashboards published as pages.
It tracks every run and agent, keeps usage analytics and audit logs,
gates what agents may run with hooks, and supports BYOC when everything
has to stay in your own cloud account. All tiers are BYOK. Fulminate
never resells tokens.

```bash
knowledge login    # browser-PKCE OAuth; token stored in your keychain
knowledge logout   # revoke + clear keychain
```

Logged in, the daemon serves tool calls from the hosted graph server.
Logged out, it runs fully local. A subscription with
`mcp:knowledge:write` unlocks `sync`: push local graph state up, pull
team-visible state down, promote a working copy as the team head.

## Status

Pre-1.0, in active development toward the Apache 2.0 OSS launch.
Shipping today: the ten-graph MCP server, thought reasoning with DeGroot
propagation, 30+ topology analyzers, branch overlays, auto-compaction
recovery, tokenless OSS boot, browser-PKCE login with keychain-backed
credentials.

## Contributing and license

[CONTRIBUTING.md](./CONTRIBUTING.md) has the build rules, test
conventions, and architectural constraints. [SECURITY.md](./SECURITY.md)
for reporting vulnerabilities.

Apache 2.0 on OSS launch; see [LICENSE](./LICENSE). Fulminate Cloud
commercial use is separately licensed at
[fulminate.io/legal](https://fulminate.io/legal).
