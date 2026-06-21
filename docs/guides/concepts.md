# Concepts

Read this first. The other guides tell you what each [tool](index.md#tools),
[agent](agents.md), and [skill](skills.md) does; this page gives you the mental
model they all assume — what knowledge *is*, the shape of its data, and how the
pieces fit together. Once the model below is in your head, the individual guides
read as details rather than surprises.

## The kernel / OS mental model

Knowledge is best understood as an operating system for a coding LLM, with four
kernel layers:

- **Drivers — the collectors.** Collectors pull your code, cloud infrastructure,
  CI/CD, logs, docs, and patterns into a queryable graph. They are the device
  drivers that turn external sources into something the system can read.
- **Syscalls — `search` and `traverse`.** The LLM does not grep. It calls
  `search` to find things semantically and by keyword, and `traverse` to walk the
  edges between them (the call graph, provenance chains, plan hierarchies). These
  two are the primitives every higher-level workflow is built on.
- **Persistent state — thoughts, decisions, and plans.** Hypotheses (and the
  charges that weigh evidence for and against them), recorded decisions, findings,
  rules, and the project/ticket/plan tree are all first-class nodes that survive
  across sessions, machines, and teammates. This is the long-term memory the model
  otherwise lacks.
- **The process model — `brainstorm → ticket → plan → revise → implement`.**
  Work flows through stages that each leave durable state behind: brainstorm
  decides *why*, the ticket captures *what*, the plan sequences *how*, a revise
  pass tightens it, and implement does the work — each reading the graph the
  previous stage wrote.

This framing mirrors the project overview in
[`README.md`](../../README.md): collectors are the drivers, `search` and
`traverse` are the syscalls, thoughts and decisions and plans are the persistent
state, and `brainstorm → ticket → plan → revise → implement` is the process
model.

## The 10 graph families

Everything in knowledge lives in a graph, and every graph belongs to one of ten
*families*. A family is the *kind* of graph; an individual graph is one instance
of a family (one repo's code graph, one account's cloud graph, one language's
practice graph). The families and their roles:

| Family | Role |
| --- | --- |
| `knowledge` | The default graph — memory: thoughts, decisions, plans, findings, rules. |
| `code` | Tree-sitter symbols and `CALLS` edges for a repository (one graph per repo). |
| `cloud` | Cloud infrastructure inventory (one graph per account). |
| `practice` | Architecture and language patterns (one graph per language). |
| `linkage` | Cross-graph proxy edges that connect the other families into one whole. |
| `cicd` | CI/CD pipelines and runs (one graph per account). |
| `logs` | Ephemeral, per-query graphs built from structured log entries; never sent to the summarizer or embedder. |
| `transformers` | Recipe DSL bodies — the executable transformer recipes, stored in the graph. |
| `web` | Raw per-source graphs emitted by the web collector; the raw text is never embedded directly. |
| `pdf` | Raw per-source graphs emitted by the PDF collector; likewise never embedded directly. |

These ten — and their canonical ordering — are defined in
[`cmd/knowledge/internal/kgtypes/graph_types.go`](../../cmd/knowledge/internal/kgtypes/graph_types.go).
Two sub-groupings within them are worth knowing, because they govern what a graph
can *do*:

- **Sync-eligible (can be pushed to Fulminate Cloud): the first seven —
  `knowledge`, `code`, `cloud`, `cicd`, `practice`, `linkage`, `transformers`.**
  `transformers` *is* sync-eligible. The only families excluded are the raw,
  LLM-skipped graphs `logs`, `web`, and `pdf`.
- **Embeddable / rebuildable-segments (carry search segments that can be
  regenerated from embedded nodes): `knowledge`, `code`, `cloud`, `cicd`,
  `practice`.** This is the embeddable subset — `linkage` and `transformers` are
  sync-eligible but carry no embedded vectors, so they have no rebuildable search
  segments, and the raw `logs` / `web` / `pdf` graphs are excluded entirely.

## The selector vocabulary

When you address a graph, the `graph` parameter selects the *family*, not the
instance. The instance identity — *which* repo, *which* account, *which*
language — lives in its own typed field:

| Family | Instance field |
| --- | --- |
| `code` | `repo` |
| `cloud`, `cicd` | `account` |
| `practice` | `language` |
| everything else | `name` |

So you reach a repository's code graph with `graph:"code", repo:"myrepo"`, an
account's infra graph with `graph:"cloud", account:"prod"`, a language's pattern
graph with `graph:"practice", language:"go"`, and a log query's graph with
`graph:"logs", name:"<query_id>"`.

**The gotcha:** `graph` is the family, never the instance name. The code graph
for a repo that happens to be *named* `knowledge` is addressed
`graph:"code", repo:"knowledge"` — **not** `graph:"knowledge"`. `graph:"knowledge"`
is always the memory/thought/decision graph, regardless of what your repo is
called. Keep instance identifiers in `repo` / `account` / `language` / `name` and
this never bites you.

## The topology — three runtimes

Knowledge runs as three cooperating processes:

- **The `knowledge` client.** The CLI plus the background machinery: it runs the
  client-side LLM pipeline (summarization and embedding), the propagation runtime
  that maintains the thought graph, and the worker runtime for background jobs. It
  talks to the graph server over a local TCP port.
- **The `knowledge serve` MCP daemon.** A long-lived daemon that exposes the
  streamable-HTTP MCP endpoint (`/mcp`) on a loopback port. This is the **sole MCP
  path** your editor or assistant connects to — it connects via a *registered URL*,
  not a spawned stdio child process.
- **`knowledge-server`.** The graph server that owns the on-disk graph and serves
  it. It holds **no model API keys** — all summarization and embedding happen in
  the client, which writes the results back. That keeps the storage layer free of
  credentials.

The exact flags and ports for each binary are documented in the
[binaries guide](binaries.md); this page does not restate them.

## Logged-out → local, logged-in → cloud

The same client talks to different backends depending on whether you are logged
in. Every routed call passes through a single chokepoint
([`Router.pick`](../../cmd/knowledge/internal/graphclient/router.go)):

- **Logged out → the local graph server.** With no live login (and no machine
  auth token), calls go to your local `knowledge-server`.
- **Logged in → Fulminate Cloud.** A live `knowledge login` (or a machine bearer
  token) routes calls to the cloud backend instead.

The choice is re-evaluated **per RPC**, not cached for the session. So a
mid-session `knowledge login` or `knowledge logout` re-routes the *next* call
automatically — no restart needed. (With neither a backend nor a login, a routed
call returns an error rather than guessing.)

## The triple-graph — code + cloud + knowledge as one whole

The most important thing to internalize is that the families are not isolated
silos. Code, cloud, and knowledge form one connected whole: a decision can point
at the function it constrains, a finding can point at the cloud resource it
describes, a thought can point at the code it reasons about. The cross-graph
connections are what make graph-walk retrieval powerful.

Those connections are carried by the `linkage` family as **linkage proxies**, and
they resolve transparently. As a caller you never deal with the proxy machinery:
you pass a `knowledge` node id straight into a cross-graph traversal —
`traverse(graph:"code"|"cloud"|"practice", start:<knowledge node id>)` — and the
traversal auto-resolves through the linkage proxies to reach the code, cloud, or
practice node on the other side. You never construct or supply a proxy id
yourself; it is resolved for you.

---

With the model in hand, dip into the [tool guides](index.md#tools) for the
syscalls (`search`, `traverse`, `mutate`, and the rest), the [agents](agents.md)
and [skills](skills.md) for the workflows built on top of them, and the
[binaries guide](binaries.md) for running the system itself.
