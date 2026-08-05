# ast

## Overview

`ast` is structural code search-and-replace, driven by a tree-sitter pattern DSL
that works across most indexed languages — a deny set (config/markup grammars,
plus PHP for a placeholder-sigil collision) is refused by match/count/replace and
surfaced at runtime by `list_node_kinds`/`explain`. Where `search` answers "is
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
- `explain` — parse one snippet into its node-kind tree, each node marked named
  or anonymous (a token with no named wrapper above it is the per-token replace
  hazard); a debugging aid. For a deny-listed language it still parses but marks
  match/replace unsupported.
- `list_node_kinds` — enumerate a language's tree-sitter node-kind vocabulary;
  for a deny-listed language it answers but marks match/replace unsupported.

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
| `context` | string |  | decl, stmt, expr, member | Pin the parse context a pattern compiles under, for match/count/replace. WITHOUT it (the default) a pattern compiles to EVERY context that can express it and matches their union — java '$T $N = $V;' finds class fields AND local variables, and every result is stamped with the contexts that produced its tree. Pin one to narrow: 'member' keeps only the class-member reading, 'stmt' only the in-body statement one. A pin naming a context this language does not register, or one no wrapper hosts this pattern under, fails loud and names the contexts that would have worked. The pin scopes the outer pattern only: a where-leaf sub-pattern (inside_pattern / contains_pattern) always compiles to its full union, because a leaf asks whether the match CONTAINS a given shape and the contained thing sits wherever the target puts it — pinning it would turn 'members containing a return statement' into a silent zero. There is consequently no way to pin a leaf's context. |
| `dry_run` | boolean |  |  | operation=replace only. Defaults TRUE: preview unified diffs + a blast-radius summary (files_matched and files_changed, matches_replaced and matches_changed) WITHOUT writing to disk. Set false to APPLY — each file is rewritten with an atomic per-file write gated by a post-edit re-parse (a rewrite that no longer parses is rejected, never written). Overlapping/nested matches in a file refuse that file whole. |
| `include_tests` | boolean |  |  | Include TEST source in the walk. Defaults to false, which drops the paths the LANGUAGE'S OWN test-file convention claims — Go's _test.go suffix, Ruby's spec conventions, and so on — so the flag means the same thing in every language instead of meaning Go everywhere. Some languages register no convention because they have no unambiguous FILENAME one (Rust marks tests with an in-file `mod tests`; C has none at all). Passing include_tests explicitly for one of those is a HARD ERROR that names the language and lists the languages which do support it, rather than a control that is accepted and then silently does nothing. Omitting it is never an error: a language with a convention gets the default-false filter, and one without has nothing to filter by, so every file is walked — narrow with package_prefixes instead. This flag governs test files ONLY. It neither applies nor lifts the walk's own exclusion rules; those are disclosed in stats.excluded_by_rule and lifted with lift_exclusions. |
| `language` | string |  |  | Source language (e.g. 'go', 'python', 'typescript', 'rust'). Required for match/count/replace/explain/list_node_kinds. |
| `lift_exclusions` | boolean |  |  | Walk the files discovery would otherwise decline, instead of declining them. Discovery drops eight classes of file before the walk ever sees them — markdown, lockfiles, .d.ts declarations, generated Go, vendored/third-party/generated path components, unsupported languages, files over 500KB, and (only on the non-git fallback walk) pruned directories like .git and node_modules — and every match/count result reports them under stats.excluded_by_rule with a bounded name sample per rule. Set this true to search them anyway. It does NOT lift .gitignore, which is git's own filtering and your repo's configuration rather than a rule ast chose, and it does NOT lift your own narrowing: language, package_prefixes and include_tests still apply to whatever discovery returns. A lifted run is reported as such in stats.discovery_path, so a run that was not allowed to exclude anything stays distinguishable from a tree that had nothing to exclude. |
| `limit` | number |  |  | Cap on how many matches operation=match RENDERS — a response-size bound only, default 100. It NEVER bounds the walk: count and replace always traverse the full scope and ignore limit entirely, and match's `total` field reports the FULL-walk match count even when fewer results are rendered. |
| `operation` | string | yes | match, count, replace, explain, list_node_kinds | Which ast op to run. |
| `package_prefixes` | array of string |  |  | Restrict the walk to files at or under any of these repo-relative prefixes, matched at PATH-SEGMENT boundaries: 'a/b' admits a/b and everything under a/b/, and never the sibling a/bc. A prefix may name a single FILE as well as a directory. Empty means no restriction. This is a narrowing you asked for rather than a rule discovery applied, so paths outside the prefixes are absent from the exclusion report entirely rather than counted under a rule — a scoped run's per-rule counts are a slice of an unscoped run's, not the same numbers. |
| `package_prefixes[]` | string |  |  |  |
| `pattern` | string |  |  | DSL pattern with placeholders ($X capture, $_ wildcard, $$$X seq capture, $$$_ seq wildcard). Example: 'defer $X.Close()'. Required for match/count/replace when 'patterns' is unset. $$ escapes a literal $ (e.g. write $${expr} to match a ${expr} template literal). |
| `patterns` | array of string |  |  | Sibling-form pattern alternation: list of DSL patterns whose match results are unioned. Use when one logical rule has multiple syntactic shapes (e.g., ['log.Print($$$_)', 'log.Println($$$_)', 'log.Printf($$$_)']). The same 'where' filter applies to each pattern's matches. Mutually exclusive with 'pattern'. Each pattern triggers an independent repo walk — acceptable for small N; for very large alternation lists, prefer authoring separate annotations. |
| `patterns[]` | string |  |  |  |
| `replacement` | string |  |  | operation=replace only. Replacement template in the SAME $X DSL grammar: $NAME interpolates capture NAME's matched text verbatim, $$$NAME interpolates a sequence capture's verbatim span, $$ emits a literal '$'. Wildcards $_ / $$$_ are NOT referenceable (usage error). Example: pattern 'defer $X.Close()' + replacement 'safeClose($X)' rewrites every match to safeClose(<the receiver>). Pass an EMPTY STRING ("") to DELETE the matched ranges (the template interpolates to nothing — useful for stripping a call/decl). Required when operation=replace and may be empty; an OMITTED replacement errors. |
| `repo` | string |  |  | Directory to walk — ast is FILESYSTEM-based (it parses files on disk), not a graph query. Omit to walk the current tree; pass an ABSOLUTE PATH to target any local checkout; or pass a bare repo NAME, which resolves to where that repo was last collected on THIS machine (via the local ~/.knowledge manifest), falling back to the current tree when the name is the current repo. A name that is neither in the manifest nor the current repo fails loud. Match hydration uses the walked directory's basename as the graph name. |
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
