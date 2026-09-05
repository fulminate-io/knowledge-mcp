---
name: knowledge-tools
description: Action rulebook for tool choice — the knowledge tools are the universal first choice and the shell is the fallback. The question-to-call table, the recall protocol, the shell allowlist, and the tool census every report carries. Read by every agent before its first tool call. Not user-invocable.
user-invocable: false
---

# KNOWLEDGE-TOOLS — the graph answers first; the shell is the fallback

<!-- version: 2 -->
<!-- Read at: every agent, before the first tool call of the session, and again
     at the moment you notice you are about to grep, cat, sed or find inside
     indexed source. -->

## Why this rulebook exists

Lanes that live in the shell produce artifacts one step removed from the
truth: a citation typed from memory, a caller list from a grep that cannot
see interface dispatch, a "does not exist" from one miss, a mechanism asserted
without recalling that the graph already recorded it. The graph is indexed,
call-graph-aware, and carries every decision, finding and thought from every
prior session. Measured lanes that answered code questions with two hundred
shell calls and four graph calls delivered the plans that failed audit. The
order below is prescriptive, not advisory.

## The question-to-call table

Ask the question; make the call named for it; go to the shell only when the
table sends you there.

| The question | The call |
|---|---|
| What did prior sessions learn, decide or rule about this? | `thoughts({operation:"recall", query:"<topic>"})` then `search({graph:"knowledge", queries:[...]})`; hydrate hits with `query({ids:[...], fields:["summary","content"]})` |
| What was decided, and why? | `query({type:"decision", text:"<topic>"})`; `query({mode:"evidence", id:"<decision>"})` |
| Where is X, what is X named, does anything like X exist? | `search({graph:"code", repo:"<repo>", queries:[3 to 5 phrasings]})` |
| Does code SHAPED like this exist, and where? | `ast({operation:"match", language:"<lang>", pattern:"<dsl>", where:{...}})`; size first with `operation:"count"` |
| Who calls X; what does X call? | `traverse({start:"<id copied from a search or file_symbols hit>", graph:"code", repo:"<repo>", edge_types:["CALLS"], direction:"in"})` plus an `ast` shape match with `include_tests:true`. The start id is COPIED from a hit, never composed from a remembered file name; a `not found` names your id, not the graph, so search the symbol and copy the id it returns |
| What is in this file? | `query({mode:"file_symbols", ...})` or `file_symbols`, then `Read` the specific range |
| Every site that does X across the tree (a census) | `ast({operation:"count"})` then `match`, `package_prefixes` to scope, output pasted as the census |
| Every occurrence of an exact literal: a string, a status word, a metadata key, a name | `ast({operation:"count", language:"<lang>", pattern:"\"<literal>\""})` then `match`; a quoted literal is a pattern and matches the literal node wherever it appears, with a by-file count |
| Which files carry a build constraint, or any question about comment text | The shell: `grep` for the constraint or the comment text, recorded in the census as such. A comment pattern compiles in the DSL but matches no comment node, so this is an allowlisted shell class, not a fallback |
| Which checks cover the shapes I touch? | `manage_checks({operation:"run", language:"<lang>", repo:"<abs path>", ids:[...]})`; read the hits |
| A structural requirement: a shape that must, or must never, appear | `manage_checks({operation:"create", language, name, summary, description, check_type:"ast_pattern", dsl_pattern, check_where, severity, fixture_bad, fixture_good})`; admission fires on the bad fixture and stays silent on the good one; then `run` it over the tree |
| How much did a lane spend, and on which tools? | `analyze_usage({operation:"run-detectors", scope:"single", agent:"<lane id>"})` (orchestrator) |
| The idiom or pattern for this mechanism | `search({graph:"practice", language:"all", queries:[...]})` |
| The node types and edge types a graph carries | `query({mode:"stats", graph:"<family>"})` |
| A plan, ticket or project's shape | `query({mode:"plan_tree", id:"..."})` then `query({ids:[...], fields:[...]})` for bodies and metadata |
| Why a node is in an unexpected state | `query({mode:"examine", id:"..."})` |
| An external API, library or provider behavior | `WebSearch` / `WebFetch` the pinned version's documentation, never a remembered shape |
| A uniform structural rewrite across files | `ast({operation:"replace", ..., dry_run:true})`, read the diff, then `dry_run:false` |

## The recall protocol

Before you state a mechanism, a premise, a prior ruling or a project fact, in
a report, a brief, a ticket or a finding: recall, search the knowledge graph,
hydrate the top hits, and cite the node ids you used. A recalled node is a
signpost: verify it against the current artifact before building on it, and
charge it when your evidence confirms or contradicts it. A claim made without
this step is a guess, and guesses about this project are wrong more often
than they are right.

## The shell allowlist

The shell is correct, and only correct, for:

- Building, testing, linting and running the project's own targets.
- Git reads: `git log`, `git show`, `git diff`, `git rev-parse`, `git
  worktree list`; and the lane's own scratch-copy operations.
- Log files, build output, process and port state, timing.
- Dotfiles, editor and CLI configuration, generated or binary artifacts, and
  files the indexer does not chunk.
- `Read` of a specific file or range you already located through the table.
  `sed -n`, `cat`, `head` or `tail` over a source file is a `Read` with the
  wrong tool and counts as a shell call in the census.
- Interface-dispatch caller counts after a `traverse`, where a grep for
  `.Method(` is the one text search that adds what the graph cannot see.
- Build constraints and comment text, which the pattern DSL does not match.

Everything else that lives in indexed source goes through the table first. A
`search` or `ast` miss is not proof of absence: an absence claim needs two
search phrasings, an ast shape match, and a look in the other package, flavor
or build tag, each recorded.

## Staleness is never a grep license

Every `search` result carries a staleness line. When it is behind the tree
you work at, report the staleness to the orchestrator, who collects; you keep
using the graph on what is indexed and `Read` the specific files that changed.
The moment you notice the thought "the index is behind, I'll grep", that
thought is the failure this rulebook exists to stop.

## A spilled result is never a grep license

When a graph result exceeds the harness cap and is written to a file, the
next call NARROWS: one query at a time, a smaller `limit`, `path_prefix` or
`package_prefixes`, `count` before `match`, a `fields` projection. Reading the
spilled file with a script is a shell read of graph output and is counted as
such in the census. Report every spill to the orchestrator as a tool
observation, with the call that produced it, so the tool gets fixed rather
than routed around. A spill teaches nothing about the next question: the
table still answers it.

## The tool census

Every report ends with a tool census: the count of `recall`, `search`,
`query`, `traverse`, `ast`, `file_symbols` and `manage_checks` calls, the
count of shell calls, and one line naming what the shell calls were for. The
orchestrator reads it on every return. A research, prefill or audit lane whose
shell calls outnumber its knowledge-tool calls is drift and is re-spawned with
this rulebook named; an implementer's shell calls are its builds and tests, so
its census is read for the absence of graph calls before decisions, not for
the ratio.

## Mode-switch tripwire

Log forensics, builds and test runs are shell work. When the next question is
about code, decisions or history, the answer comes from the table again. Tool
inertia, "I am already in the shell", is how a lane drifts into the failure
mode above one call at a time.
