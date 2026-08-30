# Corpus checks

## Overview

A corpus check is a deterministic assertion about source code that lives in the
graph rather than in a linter's configuration. It is an ordinary `finding` node
that carries `check_type` metadata — that key is what makes it executable. One
node holds both halves: the prose an engineer reads in the body fields, and the
machine-readable pattern in metadata.

Checks answer a different question from a practice graph. A practice graph holds
prose guidance and model entries an assistant reads when it wants advice; a checks
graph holds assertions something can *run*, plus the fixture code that proves each
one works. The separation is structural and load-bearing: fixtures are written
deliberately to be wrong so a check has something to fire on, and that code must
never leak into the ranked corpus that answers "what does good code look like".
Check and fixture nodes are neither summarized nor embedded, so they never appear
in ranked or semantic search.

**Checks live in one graph.** Address it as `graph:"checks"` with no `language`
and no `name` — it is a singleton. Language is a *label* on each node, not a graph
selector, so a scan for one language narrows within the single graph.

The rule that governs everything below:

> An admitted check must FIRE on its linked bad example and stay SILENT on the
> good one. No fixture, no admission.

That gate runs on every write. A check that has never been executed cannot enter
the corpus, which is the whole point — an unrun check reads exactly like a passing
one to every later consumer.

## When & how to use

Reach for a check when a rule is mechanically decidable and you want it enforced
rather than remembered. Reach for the `llm_only` lane (below) when the rule is
real but has no deterministic expression. Reach for a practice graph when what you
want is guidance to read, not an assertion to run.

### The contract keys

| Key | Meaning |
| --- | --- |
| `check_type` | The execution kind: `ast_pattern`, `graph_assertion`, `topology_threshold`, or `flow_model`. Its presence is what makes the node a check. |
| `severity` | What the check emits at: `info`, `notice`, `warning`, or `critical`. |
| `language` | The tree-sitter language slug the check is written against. Required — every corpus read is language-scoped. |
| `dsl_pattern` | The `ast` pattern body. Required for `ast_pattern`. |
| `check_where` | Optional `ast` where-tree, as JSON text. |
| `check_fixture_bad` | Node id of the example the check MUST match. |
| `check_fixture_good` | Node id of the example the check MUST NOT match. |
| `llm_only` | `"true"` on prose with no deterministic expression. Exclusive with every check-body key above, but still requires `language`. |

Fixtures are bound by **metadata**, and they live in the same checks graph as the
check that names them, so the binding never crosses a graph boundary. Edges
between a check and its fixtures are display-only — no executor consults them.

A node with no `check_type` is not a check, and this contract constrains nothing
else about it. Catalog data an executor reads — symbol tables, threshold tables —
carries no `check_type` and is never gated. There is no fixture-exempt check type:
a shape that cannot be silent on a good example is data, not a check.

### Authoring a check

**Validate the pattern before you write anything to the graph.** The fixture gate
will refuse a bad check, but iterating against `ast` directly is far faster than
iterating against write refusals.

Put every fixture in one scratch directory and run the pattern across all of them
at once. Each file is then a control for the others — the result tells you both
that the check fires and *where*:

```bash
mkdir -p /tmp/checkfix
# write bad.go and good.go here
```

```jsonc
ast({
  "operation": "count",
  "language": "go",
  "repo": "/tmp/checkfix",
  "pattern": "defer $X.Close()",
  "where": { "inside_pattern": { "of": "$match", "pattern": "for $$$_ { $$$_ }" } }
})
// → {"by_file": {"bad.go": 1}, "total": 1}
```

`total: 1` in the bad file only is the gate's condition, confirmed before any
write. Pass an absolute `repo` path: without it, `ast` walks the current
repository.

Then create the two fixtures as `example` nodes carrying the code in `content`
and `language` in metadata, and finally the check itself, naming the returned
fixture ids:

```jsonc
mutate({ "operation": "create", "graph": "checks", "type": "example",
         "name": "defer-close-in-loop-bad",
         "summary": "BAD fixture: defer inside a loop holds every handle until the function returns.",
         "content": "package fixture\n\n…",
         "metadata": { "language": "go", "fixture_role": "bad" } })

mutate({ "operation": "create", "graph": "checks", "type": "finding",
         "name": "defer-close-inside-a-loop-holds-every-handle",
         "summary": "A deferred Close inside a loop runs at function exit, not iteration end.",
         "description": "…the prose an engineer reads…",
         "metadata": {
           "check_type": "ast_pattern", "severity": "warning", "language": "go",
           "dsl_pattern": "defer $X.Close()",
           "check_where": "{\"inside_pattern\":{\"of\":\"$match\",\"pattern\":\"for $$$_ { $$$_ }\"}}",
           "check_fixture_bad": "<bad fixture id>",
           "check_fixture_good": "<good fixture id>"
         } })
```

`check_where` is JSON *as a string*, so its quotes are escaped.

Validation materializes each fixture into a temporary directory and runs the real
`ast` walk over it, because there is no in-memory matcher. Budget roughly 30–50ms
per check and expect one discovery warning line per fixture.

### The good fixture must be a near-miss

This is the part that decides whether a check is worth having.

A good fixture that shares nothing with the bad one proves almost nothing: a check
matching a bare call shape stays silent on unrelated code no matter how broad it
is. The good fixture should contain **the same construct the check keys on**,
placed where it is legitimate, so the only thing separating the pair is the
discriminator the check claims to apply.

For the defer-in-loop check, the good fixture is a `defer f.Close()` at function
scope. A check keying on the defer alone fires on both files and has narrowed
nothing; the loop ancestor is the entire discriminator, and the good fixture exists
to prove the check consults it.

The same logic drives a security check. `exec.Command("sh", "-c", x)` is dangerous
when `x` is caller-controlled and fine when it is a source literal — the same call
shape occurs on safe and unsafe arguments alike. So the good fixture is that same
shell invocation with a literal argument, and the discriminator is the argument's
node kind.

### A fixture pair proves only the axes it varies

The corollary, and the failure mode worth internalizing: **a pair is silent about
every axis it holds constant.**

A check for shell interpolation claims two things — that the program is a shell,
and that the argument is caller-controlled. If both fixtures use `sh -c`, the pair
varies only the argument and can never detect that the check's handling of the
*program* is broken. The check passes a real gate and is still overbroad in
production.

The remedy is to give the good fixture one function per axis the check claims to
discriminate on:

```go
package fixture

import "os/exec"

// Axis 1: same shell, literal script — the argument kind differs.
func runGoodLiteral() error {
	return exec.Command("sh", "-c", "ls -l /tmp").Run()
}

// Axis 2: caller-controlled argument, but not a shell — the program differs.
func runGoodNotAShell(userInput string) error {
	return exec.Command("git", "show", userInput).Run()
}
```

This is the same defect class as a unit-test fixture that constructs the input
shape the code already expects: the test is green, the gate is real, and neither
is exercising the thing that breaks.

### Control every load-bearing element of a pattern

An inlined literal constrains the match: a grammar declares which of its node
kinds carry text in a span the children do not cover, and the matcher compares
those whole rather than descending into them. So `exec.Command("sh", "-c", $ARG)`
matches only a genuine `sh -c` call, and a placeholder written *inside* a literal
— `zz("$X")` — is a compile error naming the placeholder rather than a pattern
that silently matches everything.

You can also constrain a **captured** node with an `equals` leaf, which is the
form to reach for when the same value is needed elsewhere in the where-tree:

```jsonc
{
  "pattern": "exec.Command($PROG, $FLAG, $ARG)",
  "where": { "all": [
    { "equals": { "of": "PROG", "value": "\"sh\"" } },
    { "equals": { "of": "FLAG", "value": "\"-c\"" } },
    { "not": { "kind": { "of": "ARG", "is": "interpreted_string_literal" } } }
  ]}
}
```

The durable habit, which outlives any particular matcher behavior: **run a
known-positive control on each element you are relying on.** Swap it for a value
that cannot possibly exist and re-run. If the match count does not move, that
element was never constraining anything, and the check is broader than its name
claims. This costs one extra call and is the only thing that distinguishes "my
pattern is precise" from "my pattern happens to be the only thing I tried".

### Revising, superseding, and deleting

A check created through `create` is given a store-generated id, which is unique by
construction. When you later name a node — with `upsert`, or with an `update` that
introduces a new id — the id must be namespaced by its language as
`<language>:<name>`, so two languages' checks cannot collide in the single graph.
Editing a node that already exists mints nothing and is not subject to that rule.

Choosing a qualified id up front makes a check addressable by name later:

```jsonc
mutate({ "operation": "upsert", "graph": "checks", "id": "go:shell-interpolated-exec",
         "type": "finding", "name": "…", "summary": "…", "metadata": { /* … */ } })
```

The gate re-runs on every update-shaped write, and it validates the **merged**
node, not the payload alone — so an update that changes only `dsl_pattern` is
still checked against the `check_type` and fixtures already stored. A write that
mentions no contract key skips the gate entirely, which is why a status-only edit
is cheap:

```jsonc
mutate({ "operation": "update", "graph": "checks", "id": "<id>", "status": "superseded" })
mutate({ "operation": "delete", "graph": "checks", "ids": ["<id>"] })
```

Deletes are soft by default — tombstoned and recoverable. Prefer `superseded` when
the check documents a lesson worth keeping.

### The `llm_only` lane

Some rules are real and have no deterministic expression. Record those with
`llm_only: "true"` and no check-body keys. They still require `language`: every
corpus read is language-scoped, so an unlabeled node is returned to nobody and the
needs-judgment lane silently empties.

A consumer must test for `llm_only` **before** its skip branch. Parsing an
`llm_only` node reports "not an executable check", so a consumer that skips on
that alone makes the whole judgment lane invisible and cannot produce an honest
machine-verified / needs-judgment split.

### Running checks against real code

An `ast_pattern` check's `dsl_pattern` and `check_where` are exactly what `ast`
takes, so running one across a repository is the same call you used to validate
it, pointed at real source:

```jsonc
ast({ "operation": "count", "language": "go", "repo": "/abs/path/to/repo",
      "pattern": "<dsl_pattern>", "where": { /* check_where, parsed */ },
      "package_prefixes": ["cmd"] })
```

Use `count` to size the result set before asking for matches. **Read the hits
before reporting them** — a non-zero count is a claim about real code, and the
fastest way to discover an overbroad pattern is to look at what it caught.

Nothing about a passed validation is persisted: there is no `validated_at`, no
digest, no marker to read. A consumer that must not execute an unvalidated check
re-validates immediately before executing.

## Gotchas

- **`graph:"checks"` takes no `language` and no `name`.** It is a singleton;
  language is a metadata label. Passing either is refused.
- **A zero from `ast` is not proof of absence** when the corpus did not fully
  parse. The response reports files with parse errors — check it before concluding
  the construct is not there.
- **`check_where` is a JSON string**, not a nested object.
- **Fixture ids must resolve in the checks graph.** An id that only resolves
  elsewhere is a dangling reference, not a cross-graph lookup.
- **Severity is a judgment about the reader**, not about the pattern. Reserve
  `critical` for what a reviewer must not merge past.
