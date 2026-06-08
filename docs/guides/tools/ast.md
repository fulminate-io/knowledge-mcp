# ast

## Overview

`ast` is structural code search-and-replace, driven by a tree-sitter pattern DSL
that works uniformly across every indexed language. Where `search` answers "is
there code *named* this", `ast` answers "is there code *shaped* like this" — every
`defer x.Close()`, every error-returning function declaration, every goroutine
spawned inside a loop. It sees through whitespace, comments, and incidental token
order, which line-based tools cannot.

It has five operations:

- `match` — find structural matches and return them with named captures and the
  enclosing function/method.
- `count` — the same walk without hydration; returns totals by file. Run this
  first to size a result set.
- `replace` — the write counterpart: match, interpolate captures into a
  replacement template, and splice, gated by a re-parse safety check.
- `explain` — parse one snippet into its node-kind tree (a debugging aid).
- `list_node_kinds` — enumerate a language's tree-sitter node-kind vocabulary.

## When & how to use

Reach for `ast` for structural questions and for uniform multi-file refactors:
auditing an anti-pattern at scale, finding every call site whose shape changed in
a migration, or rewriting the same structural pattern across many files. For
"what is the call graph" use `traverse`; for "where is X named" use `search`.

The placeholder DSL has four forms: `$X` captures a single node, `$_` is a
single-node wildcard, `$$$X` captures a sequence, and `$$$_` is a sequence
wildcard. An optional `where`-tree filters matches by node kind, regex, literal,
and ancestor/descendant sub-patterns.

```jsonc
// Find every deferred Close()
ast({ "operation": "match", "language": "go", "pattern": "defer $X.Close()" })

// Uniform rewrite — preview first (dry_run defaults true), then apply
ast({ "operation": "replace", "language": "go",
      "pattern": "defer $X.Close()", "replacement": "safeClose($X)",
      "dry_run": true })
```

`replace` defaults to `dry_run: true`: it returns a unified diff and a
blast-radius summary and writes nothing. Set `dry_run: false` to apply — each file
is written atomically and re-parsed, and any rewrite that no longer parses is
rejected rather than written. For the full DSL and where-tree grammar, run
`help("ast")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `dry_run` | boolean |  |  | operation=replace only. Defaults TRUE: preview unified diffs + a blast-radius summary (files touched, matches replaced) WITHOUT writing to disk. Set false to APPLY — each file is rewritten with an atomic per-file write gated by a post-edit re-parse (a rewrite that no longer parses is rejected, never written). Overlapping/nested matches in a file refuse that file whole. |
| `include_tests` | boolean |  |  | When true, _test.go-suffix files are included in the walk. Defaults to false. NOTE: testdata/, vendor/, node_modules/, .git/, dist/, build/, etc. are hard-skipped by parser.DiscoverFiles regardless of this flag. |
| `language` | string |  |  | Source language (e.g. 'go', 'python', 'typescript', 'rust'). Required for match/count/replace/explain/list_node_kinds. |
| `limit` | number |  |  | Cap on RawMatch results. Default 100. |
| `operation` | string | yes | match, count, replace, explain, list_node_kinds | Which ast op to run. |
| `package_prefixes` | array of string |  |  | Restrict the walk to files whose repo-relative path starts with any of these prefixes. Empty means no restriction. |
| `package_prefixes[]` | string |  |  |  |
| `pattern` | string |  |  | DSL pattern with placeholders ($X capture, $_ wildcard, $$$X seq capture, $$$_ seq wildcard). Example: 'defer $X.Close()'. Required for match/count/replace when 'patterns' is unset. |
| `patterns` | array of string |  |  | Sibling-form pattern alternation: list of DSL patterns whose match results are unioned. Use when one logical rule has multiple syntactic shapes (e.g., ['log.Print($$$_)', 'log.Println($$$_)', 'log.Printf($$$_)']). The same 'where' filter applies to each pattern's matches. Mutually exclusive with 'pattern'. Each pattern triggers an independent repo walk — acceptable for small N; for very large alternation lists, prefer authoring separate annotations. |
| `patterns[]` | string |  |  |  |
| `replacement` | string |  |  | operation=replace only. Replacement template in the SAME $X DSL grammar: $NAME interpolates capture NAME's matched text verbatim, $$$NAME interpolates a sequence capture's verbatim span, $$ emits a literal '$'. Wildcards $_ / $$$_ are NOT referenceable (usage error). Example: pattern 'defer $X.Close()' + replacement 'safeClose($X)' rewrites every match to safeClose(<the receiver>). Required when operation=replace. |
| `repo` | string |  |  | Code graph name (defaults to the active code graph when exactly one is loaded; required when multiple are loaded). |
| `snippet` | string |  |  | Source snippet to parse for operation=explain. Returns an indented node-kind tree. Required when operation=explain. |
| `where` | object |  |  | Optional JSON where-tree filter. Composers: all/any/not. Leaves: kind (node-kind constraint), matches (regex), equals (literal text), same_node (capture identity), inside_pattern + contains_pattern (recursive sub-patterns over ancestors / descendants; each accepts an optional 'as' field that names the matched ancestor/descendant for downstream sibling leaves to reference — useful for 'find ancestor function then check its body' shapes). Capture references: bare names ("X") for local user-named captures, "$match" for the outermost matched node (built-in, no explicit placeholder needed), "$outer.X" / "$outer.outer.X" walk parent scopes. See description block for examples. |
| `where.all` | array of object |  |  | Composer: every child where-tree must match (AND). |
| `where.all[]` | object |  |  |  |
| `where.any` | array of object |  |  | Composer: at least one child where-tree must match (OR). |
| `where.any[]` | object |  |  |  |
| `where.contains_pattern` | object |  |  | Leaf: capture has a descendant matching a sub-pattern; accepts of/pattern/where/as. |
| `where.equals` | object |  |  | Leaf: literal text equality, e.g. { of: X, value: Close }. |
| `where.inside_pattern` | object |  |  | Leaf: capture has an ancestor matching a sub-pattern; accepts of/pattern/where/as. |
| `where.kind` | object |  |  | Leaf: node-kind constraint, e.g. { of: X, is: function_declaration } (is may be a string or array). |
| `where.matches` | object |  |  | Leaf: regex over capture text, e.g. { of: X, regex: "^err[A-Z]" }. |
| `where.not` | object |  |  | Composer: the child where-tree must NOT match (negation). |
| `where.same_node` | object |  |  | Leaf: NODE-identity match across captures, e.g. { captures: [X, $outer.Y] }. |
| `where.same_text` | object |  |  | Leaf: TEXT-equality match across captures, e.g. { captures: [X, $outer.Y] }. |
<!-- END GENERATED: params -->
