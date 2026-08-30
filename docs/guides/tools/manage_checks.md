# manage_checks

## Overview

`manage_checks` is the working surface for the deterministic corpus checks: the
executable assertions that live in the checks graph, each bound to two example
nodes — one it MUST fire on, one it must stay SILENT on. The tool authors them,
inventories them, and runs them against a repository's working tree.

A check is a finding node carrying its assertion in metadata; the contract itself
(the eight metadata keys, the four-row parse table) lives in `help("patterns")`.
This tool is the surface over that contract, and `help("manage_checks")` is its
full reference, including the seven where-tree scoping traps that cost a
debugging round each when the first defect-class check was authored.

## When & how to use

**`create`** authors a check and both fixtures in ONE call. Everything is
validated in memory first — the pattern parses and compiles, the where-tree names
kinds the grammar has, the check fires on the bad example, stays silent on the
good one, and fires again on the good one with the where-tree dropped — and
nothing is written unless all of it passes. Before this existed an author had to
create two example nodes, copy their ids into the check's metadata, and create
the check, with the admission gate running only on that third call: a fixture
problem was discovered after two nodes were already in the graph.

**`list`** renders what the checks graph holds, under all four contract rows, plus
the lane no other surface produces: example nodes bound by no check. An orphaned
fixture is reachable by no executor and by no ranked search, so nothing else names
it.

**`run`** executes checks over a repo's working tree and leads with one
machine-readable verdict line. `repo` names both the code graph and the tree that
is walked; `ids` narrows to named checks, and an id matching no check is an error
naming it rather than a silent widening back to the whole corpus.

```jsonc
manage_checks({ "operation": "list", "language": "go" })

manage_checks({ "operation": "run", "repo": "knowledge", "language": "go",
                "path_prefix": "cmd/knowledge/internal/tools" })
```

The same classification is available from the shell, which is what lets a check
back a plan criterion — `knowledge check run --repo <name> --language go
<check-id>`, exiting 0 for clean, 3 for flagged and 4 for inconclusive. The exit
status and the verdict line are computed by one fold over one findings slice, so
the two faces cannot disagree.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `check_type` | string |  |  | create only: the check's execution kind (ast_pattern \| graph_assertion \| topology_threshold \| flow_model). |
| `check_where` | string |  |  | create only: an optional ast where-tree as JSON text. |
| `content` | string |  |  | create only: the check node's full content body. |
| `description` | string |  |  | create only: the check's prose guidance — what the rule is and why. |
| `dsl_pattern` | string |  |  | create only: the check body — for ast_pattern, an ast DSL pattern. |
| `fixture_bad` | object |  |  | The bad fixture example node to author alongside the check. Its content is the snippet the admission gate runs the check against. |
| `fixture_bad.content` | string |  |  | The fixture source text the check is run over. |
| `fixture_bad.description` | string |  |  | Why this snippet is the bad example. |
| `fixture_bad.name` | string |  |  | Fixture node name. |
| `fixture_bad.summary` | string |  |  | Required search-optimized one-line summary, max 500 chars. (max length: 500) |
| `fixture_good` | object |  |  | The good fixture example node to author alongside the check. Its content is the snippet the admission gate runs the check against. |
| `fixture_good.content` | string |  |  | The fixture source text the check is run over. |
| `fixture_good.description` | string |  |  | Why this snippet is the good example. |
| `fixture_good.name` | string |  |  | Fixture node name. |
| `fixture_good.summary` | string |  |  | Required search-optimized one-line summary, max 500 chars. (max length: 500) |
| `format` | string |  |  | Output format: 'text' (default) or 'json'. |
| `ids` | array of string |  |  | run only: check node ids to execute. Omit to run every check in the corpus. An id matching no check is an error naming it, never a silent widening. |
| `ids[]` | string |  |  |  |
| `language` | string |  |  | Tree-sitter language slug (e.g. 'go', 'python'). REQUIRED for run — it selects the checks corpus. Optional narrowing for list; omit it to list every language. |
| `name` | string |  |  | create only: the check node's name. |
| `operation` | string | yes | create, list, run | What to do: create \| list \| run |
| `path_prefix` | string |  |  | run only: repo-relative subtree the walk is narrowed to. |
| `repo` | string |  |  | Code-graph name, or an absolute checkout path. REQUIRED for run — it names both the graph and the tree the checks walk. |
| `severity` | string |  |  | create only: the severity its findings are emitted at (info \| notice \| warning \| critical). |
| `summary` | string |  |  | create only: required search-optimized one-line summary of the check, max 500 chars. (max length: 500) |
| `top_k` | integer |  |  | run only: cap on how many findings are rendered (0 = the analyzer's own ceilings apply). |
<!-- END GENERATED: params -->
