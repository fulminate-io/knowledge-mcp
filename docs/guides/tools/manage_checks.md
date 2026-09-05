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

By default the walk skips the paths the language's own test-file convention
claims, so a check whose defect class lives only in test code can never fire on a
real instance. Two controls change that, and they are deliberately different
shapes: `include_tests` on the run widens every check in that run, while
`applies_to_tests` on a check node widens THAT CHECK ALONE on every run, with no
knob at any call site. `test_files_scanned` on the verdict line is how a reader
tells a run that read the test code from one that did not — zero is a different
answer from "found nothing there".

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
| `applies_to_tests` | boolean |  |  | create only: declare that this check's defect class lives in TEST files, so a run widens the walk for this check alone with no run-wide include_tests. Omitted or false writes no declaration; true is refused for a language ast carries no test-file convention for, where it would widen nothing. |
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
| `include_tests` | boolean |  |  | run only: walk this language's TEST files too. Omitted (the default) walks non-test files only, and is legal for every language; an explicit true or false for a language ast carries no test-file convention for is REFUSED naming the language, because there the flag would decide nothing. A check may instead declare applies_to_tests on its own node, which widens that check alone and needs no knob here. |
| `language` | string |  |  | Tree-sitter language slug (e.g. 'go', 'python'). REQUIRED for run — it selects the checks corpus. Optional narrowing for list; omit it to list every language. |
| `name` | string |  |  | create only: the check node's name. |
| `operation` | string | yes | create, list, run | What to do: create \| list \| run |
| `path_prefix` | string |  |  | run only: repo-relative subtree the walk is narrowed to. Prefixes match whole path SEGMENTS, so 'pkg' is the pkg directory and never pkgextra. A prefix that reached NO FILE of the corpus language is REFUSED naming the prefix — a mistyped or over-specific scope is never reported as a clean corpus, because a scan that opened no file is not a clean scan. |
| `repo` | string |  |  | Code-graph name, or an absolute checkout path. REQUIRED for run — it names both the graph and the tree the checks walk. |
| `severity` | string |  |  | create only: the severity its findings are emitted at (info \| notice \| warning \| critical). |
| `summary` | string |  |  | create only: required search-optimized one-line summary of the check, max 500 chars. (max length: 500) |
| `top_k` | integer |  |  | run only: cap on how many findings are rendered (0 = no cap, the analyzer's own ceilings apply). It bounds ONLY the rendered body and never reaches the classification: the verdict line and its counts are folded over every finding the run produced. A render that was clipped states the total, as 'returning X of Y findings'. The admitted range is 0 or a positive count; a NEGATIVE value is REFUSED naming it, rather than coerced into a second spelling of no cap. |
<!-- END GENERATED: params -->
