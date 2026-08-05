# Capabilities

What Knowledge actually does, in depth. The [README](../../README.md) carries
the short version; this guide keeps the full narrative for each of the four
capability pillars.

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
top of it. The full model — the recall → think → charge → reflect
cycle, the evidence semantics, and the propagation math — is covered
in the [Reasoning guide](reasoning.md).

## 2. Search across everything you have

Knowledge runs hybrid BM25 + vector search across every graph it knows
about: code, decisions, findings, cloud resources, log streams, and
docs. One query surface, every source — the LLM doesn't pick a
backend, it asks for what it wants.

Code search covers 30+ languages with tree-sitter chunking and a binary
HNSW index, plus structural AST search for the shapes regex can't
express — every `defer x.Close()`, every goroutine without a recover,
every public function returning an error, every framework-specific
call site. The AST DSL is one grammar for the languages it supports — a
few config/markup grammars (and PHP, whose `$` variables collide with the
placeholder sigil) are out of scope; elsewhere patterns port across
languages without rewriting.

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
