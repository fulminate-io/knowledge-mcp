// SPDX-License-Identifier: Apache-2.0

package tools

const helpAst = `# ast — Structural code search-and-replace via tree-sitter

Pattern-match (and optionally REWRITE) against parsed syntax trees in any of
the 31 indexed languages. Use ast when the question is about CODE SHAPE rather
than text — "every defer that calls Close()", "all goroutines spawned inside
loops", "function decls returning error", "calls to sync.Once.Do with a closure
body". Tree-sitter sees through whitespace, comments, and incidental token
order; grep doesn't. operation:"replace" is the WRITE counterpart — match,
interpolate captures into a replacement template, splice, and (with a re-parse
safety gate) write the result.

The handler runs CLIENT-SIDE — cmd/knowledge intercepts ast tool calls and
parses files locally. The server has the code graph; the client has the
source files. Hydration (enclosing function/method node IDs) round-trips
through one bulk query(file_symbols) call rather than N+1.

## Operations

  match            — Run a pattern (with optional where-tree filter) and
                     return hydrated MatchResults: file_path,
                     start_line/end_line, named captures, enclosing_node_id
                     + enclosing_signature resolved against the code graph.
  count            — Same walk as match but skip Hydrate. Returns
                     {total, by_file, files_scanned, files_skipped, duration_ms}.
                     Use this first to size the result set before hydrating.
  replace          — The WRITE counterpart to match: interpolate captures
                     into a 'replacement' template, splice, and (gated by a
                     re-parse safety check) write. dry_run defaults TRUE —
                     preview unified diffs without touching disk. See the
                     '## operation:replace' section below.
  explain          — Parse a single snippet and emit the indented node-kind
                     tree. Pure debug aid; does not touch the code graph.
  list_node_kinds  — Enumerate the tree-sitter node-kind vocabulary for a
                     language. Use when authoring a 'kind' leaf.

## Placeholder DSL — four forms

  $X     — capture a single node, bind to capture name X
  $_     — wildcard single node (no capture)
  $$$X   — capture a sequence of zero-or-more siblings, bind to X
  $$$_   — wildcard sequence (no capture)

Identifier rule: ASCII letters, digits, underscore; must not start with a
digit. Bare '$', '$$', and '$$$' (without a following identifier or '_')
are parser errors.

## Capture references in where-tree leaves

Three forms appear as values for the 'of' / 'captures' fields:

  "X"              — local-scope capture (bare name; the match's own
                     bindings from $X / $$$X placeholders).
  "$match"         — built-in: the outermost matched node of the local
                     match. Available without an explicit named
                     placeholder, so a wildcard pattern $_ paired with
                     a kind / matches / equals leaf on $match gates the
                     outer node directly.
  "$outer.X"       — capture from one parent scope (used inside a
                     sub-pattern's where-tree to reach back to the
                     enclosing match). Chain "$outer." for deeper nesting:
                     "$outer.outer.X" walks two levels.

Plain bare names ("X") reference user-named captures. The leading $ is
reserved for the two built-in prefixes above; user identifiers cannot
start with $.

## JSON where-tree — composers + leaves

Constraints attach via the optional 'where' argument: a recursive JSON
boolean tree. Three composers and six leaves cover the surface.

Composers (a node may set exactly one; multiple set at once are AND'd):

  all: [<node>, <node>, ...]    — every child must match (AND)
  any: [<node>, <node>, ...]    — at least one child must match (OR)
  not: <node>                   — child must NOT match (negation)

Leaves:

  kind             — node-kind constraint. Single string or array of strings.
                     { "of": "X", "is": "function_declaration" }
                     { "of": "X", "is": ["function_declaration", "method_declaration"] }

  matches          — regex over the capture's source text.
                     { "of": "X", "regex": "^err[A-Z]" }

  equals           — literal text equality.
                     { "of": "X", "value": "Close" }

  same_node        — two-or-more captures bind to the same AST node. Each
                     entry is a capture reference; cross-scope references
                     (see below) walk parent scopes.
                     { "captures": ["X", "$outer.Y"] }
                     Note: this is NODE IDENTITY. Different occurrences of
                     the same identifier (e.g., two ` + "`" + `parser` + "`" + ` references on
                     different lines) are different AST nodes and won't
                     match. For "same variable NAME across occurrences",
                     use same_text below.

  same_text        — two-or-more captures share the same source text. Use
                     for variable-name matching across siblings — e.g., a
                     deferred close on the same identifier as the receiver
                     short_var_decl, where same_node fails because the
                     occurrences are distinct AST nodes.
                     { "captures": ["X", "$outer.RESP"] }

  inside_pattern   — capture has an ancestor matching a sub-pattern. The
                     sub-pattern is itself a DSL pattern with its own
                     optional where-tree (recursive).
                     { "of": "X", "pattern": "func $F() error { $$$BODY }",
                       "where": { ... } }

  contains_pattern — capture has a descendant matching a sub-pattern.
                     { "of": "X", "pattern": "$F.Close()" }

Both inside_pattern and contains_pattern accept an optional 'as' field
that names the matched ancestor/descendant for downstream sibling leaves
in the SAME composer scope. Use it for "find ancestor/descendant, then
check ITS subtree" shapes — the named binding stays available to every
following leaf in the parent 'all' / 'any' block.

  { "inside_pattern": { "of": "$match", "pattern": "func $_($$$_) $$$_ { $$$BODY }",
                        "as": "FN" } }   ← FN now references the matched ancestor
  { "not": { "contains_pattern": { "of": "FN", "pattern": "..." } } }   ← uses FN

The binding only fires when the leaf returns true. Wrapping in 'not'
flips the parent verdict but the binding still happens on inner-match;
referencing 'as' from a sibling-of-'not' is a usage smell because the
binding may not be set if the inner failed.

A reference that can't be resolved in the scope chain is an error, not a
silent miss — the walker surfaces "capture not found" so authoring bugs
stay visible.

## Sub-pattern recursion

inside_pattern and contains_pattern recursively re-enter the engine: the
sub-pattern is parsed, compiled, and walked over the search root (ancestors
for inside_pattern, descendants for contains_pattern). Sub-pattern wheres
nest the same way. Recursion is hard-capped at 8 levels to keep pathological
nesting from looping forever.

## operation:replace — structural search-and-replace

replace matches exactly like match, then rewrites each match by interpolating
its captures into a 'replacement' template written in the SAME $X grammar:

  $NAME    — interpolate capture NAME's matched source text, VERBATIM.
  $$$NAME  — interpolate a sequence capture's verbatim span (the bytes the
             $$$NAME placeholder matched).
  $$       — a literal '$' (escape). Note: $$$NAME is a sequence reference,
             not an escape followed by a name — the escape only fires when
             the two dollars are NOT followed by a third '$'.
  $_ / $$$_ — wildcards are NOT referenceable in a replacement (usage error).

Safety model (the replacement is never a blind text edit):

  - dry_run defaults TRUE. A dry run returns unified diffs + a blast-radius
    summary (files_touched, matches_replaced) and writes NOTHING. Set
    dry_run:false to apply.
  - Apply mode writes each file atomically (temp + rename).
  - Re-parse gate: after splicing, the rewritten file is re-parsed. If it no
    longer parses cleanly the file is REJECTED (listed in rejected_files) and
    never written — a broken edit cannot land.
  - Overlap refuse-and-report: if two matches in one file have intersecting or
    nested byte ranges, that whole file is REFUSED (listed in refused_files)
    and left untouched — the tool does not guess which edit wins.
  - Single pass: each match is replaced once; there is no iterate-to-fixpoint.
  - The replacement is a verbatim textual splice of the matched byte range
    (no re-indentation of multi-line replacements).

Worked example — preview rewriting every defer Close() to safeClose():
  ast({ "operation": "replace", "language": "go",
        "pattern": "defer $X.Close()",
        "replacement": "safeClose($X)",
        "dry_run": true })            ← diffs only, no write

Then apply once the diff looks right:
  ast({ "operation": "replace", "language": "go",
        "pattern": "defer $X.Close()",
        "replacement": "safeClose($X)",
        "dry_run": false })           ← atomic write, re-parse gated

## Examples

Go defer-Close pairing — pure structure, no where:
  ast({ "operation": "match", "language": "go",
        "pattern": "defer $X.Close()" })

Every method declaration — wildcard pattern + $match kind gate:
  ast({ "operation": "match", "language": "go",
        "pattern": "$_",
        "where": { "kind": { "of": "$match", "is": "method_declaration" } } })

Function decls returning error whose name starts with "load":
  ast({ "operation": "match", "language": "go",
        "pattern": "func $NAME($$$ARGS) error { $$$BODY }",
        "where": { "matches": { "of": "NAME", "regex": "^load" } } })

Defer Close inside a for-loop but NOT bounded by an inner closure
(the closure pattern would actually own the lifetime):
  ast({ "operation": "match", "language": "go",
        "pattern": "defer $X.Close()",
        "where": { "all": [
          { "inside_pattern": { "of": "X",
                                "pattern": "for $$$INIT { $$$BODY }" } },
          { "not": { "inside_pattern": { "of": "X",
                                         "pattern": "func() { $$$BODY }" } } }
        ] } })

Cross-scope same_node — defer X.Close() where X was opened by a parent
constructor:
  ast({ "operation": "match", "language": "go",
        "pattern": "defer $X.Close()",
        "where": { "inside_pattern": {
          "of": "X",
          "pattern": "$Y, _ := os.Open($_)",
          "where": { "same_node": { "captures": ["Y", "$outer.X"] } }
        } } })

Count call sites before hydrating:
  ast({ "operation": "count", "language": "go",
        "pattern": "$ONCE.Do(func() { $$$BODY })" })

Discover the node-kind vocabulary:
  ast({ "operation": "list_node_kinds", "language": "go" })

Debug a snippet to see how tree-sitter parses it:
  ast({ "operation": "explain", "language": "go",
        "snippet": "defer x.Close()" })

## Sibling-form alternation — patterns: [...]

When one logical rule has multiple syntactic shapes (e.g., 'log.Print',
'log.Println', 'log.Printf' all sharing the "no stdout in stdio mode"
rule), pass an array of patterns instead of a single pattern. Results are
unioned; the same where-tree applies to each pattern's matches.

  ast({ "operation": "count", "language": "go",
        "patterns": [
          "log.Print($$$_)",
          "log.Println($$$_)",
          "log.Printf($$$_)"
        ],
        "where": { ... } })

Mutually exclusive with the singular 'pattern' field. Each pattern
triggers an independent repo walk — fine for small N (3-5 sibling
forms); for very large alternation lists prefer authoring separate
annotations.

## Parameters

  operation        — match | count | replace | explain | list_node_kinds (required)
  language         — go | python | typescript | rust | ... (required)
  pattern          — single DSL pattern (match/count/replace) — exclusive with 'patterns'
  patterns         — array of DSL patterns for sibling-form alternation (match/count/replace)
  where            — JSON where-tree (optional, match/count/replace)
  replacement      — replacement template in the $X grammar (replace only, required)
  dry_run          — replace only; default TRUE (preview diffs, no write); false applies
  snippet          — source text (explain only, required)
  repo             — code graph name (defaults to active when one is loaded)
  package_prefixes — restrict the walk to repo-relative path prefixes
  include_tests    — include _test.go-style files (default false)
  limit            — cap on RawMatch results (default 100)

Note on scope filters: parser.DiscoverFiles hard-skips testdata/, vendor/,
node_modules/, .git/, dist/, build/, etc. regardless of include_tests. The
flag governs only whether _test.go-style files in normal package directories
are walked.

## When ast beats search/grep

  - Counting "every place that does X structurally" — defer X.Close,
    sync.Once, error-returning func decls, channel sends inside select.
  - Auditing anti-patterns at scale — empty error checks, naked goroutines,
    unbounded loops, panic in library code, fmt.Errorf without %w.
  - Migration prep — find every callsite shape that changed across a refactor
    (e.g. every place that used to take ctx as arg 2).
  - Boolean composition over structure (X inside A but not inside inner B)
    — express precisely with all + not + inside_pattern.
  - Cross-language structural surveys when you don't want to write 31 regexes.

For "what is the call graph?" questions, traverse(graph: "code",
edge_types: ["calls"]) is still the right tool — that's edge data, not shape.

## Output

  match → { matches: [...], stats: {files_scanned, files_skipped, duration_ms},
            hint: "..." (only when matches is empty) }
  count → { total, by_file, files_scanned, files_skipped, duration_ms }
  replace → { applied, dry_run, files_touched, matches_replaced,
              refused_files, rejected_files, diffs (relPath → unified diff) }
  explain → indented node-kind tree (text)
  list_node_kinds → array of node-kind strings

When matches is empty, the hint suggests broadening scope, simplifying the
pattern, or dropping to operation:"count" to confirm the walk produced no
candidates.
`
