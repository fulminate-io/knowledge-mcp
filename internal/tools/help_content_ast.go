// SPDX-License-Identifier: Apache-2.0

package tools

const helpAst = `# ast — Structural code search-and-replace via tree-sitter

Pattern-match (and optionally REWRITE) against parsed syntax trees in most
indexed languages — a deny set (config/markup grammars, plus PHP for a
placeholder-sigil collision) is refused by match/count/replace and surfaced at
runtime by list_node_kinds/explain. Use ast when the question is about CODE SHAPE rather
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
                     tree, each node marked NAMED or ANONYMOUS — a token with
                     no named wrapper above it is the per-token replace-hazard
                     tell. Pure debug aid; does not touch the code graph. For a
                     deny-listed language it still parses but annotates
                     match/replace as unsupported.
  list_node_kinds  — Enumerate the tree-sitter node-kind vocabulary for a
                     language. Use when authoring a 'kind' leaf. For a
                     deny-listed language it answers but annotates match/replace
                     as unsupported.

## Placeholder DSL — four forms

  $X     — capture a single node, bind to capture name X
  $_     — wildcard single node (no capture)
  $$$X   — capture a sequence of zero-or-more siblings, bind to X
  $$$_   — wildcard sequence (no capture)

  $$     — escape: a literal '$' in the PATTERN (not a placeholder). Lets a
           pattern match a JS/TS template-literal interpolation like ${expr} —
           write $${expr}. Mirrors the replacement-template $$ escape.

Identifier rule: ASCII letters, digits, underscore; must not start with a
digit. Bare '$' and '$$$' (without a following identifier or '_') are parser
errors; '$$' is the literal-'$' escape above, not an error.

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
boolean tree. Three composers and eight leaves cover the surface.

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

  flows_to         — does the value bound to 'from' REACH the position bound
                     to 'to', by dataflow, inside one declaration? The walk
                     is intra-declaration: 'within' names the declaration it
                     is scoped to and is REQUIRED — there is no default,
                     because deriving the scope would mean guessing which
                     ancestor kind is a declaration in each language. Name it
                     with "$match" when the pattern matches the declaration
                     itself, or with an inside_pattern 'as' binding otherwise.
                     Available only on flow-armed languages; a leaf on any
                     other errors and lists the ones that work, rather than
                     quietly matching nothing.
                     { "from": "P", "to": "ARG", "within": "FN" }

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

## Union compile, the compiled disclosure, and the 'context' pin

A pattern is a source FRAGMENT, and the same fragment is often grammatical in
more than one place. Compilation therefore enumerates EVERY parse context that
can host it and matches the union of the distinct trees — java '$T $N = $V;' is
both a class field and a local variable, and both are returned. That is what
stops a fragment silently compiling to a construct you did not mean: c#
'Debug.Assert($X);' reads as a class-body field declaration as readily as a
statement, and answering with only the first reading returns zero against
thousands of real call sites.

Every result discloses what it compiled to, so a surprising zero is diagnosable
from the output rather than by experiment: match and count echo a 'compiled'
array (one entry per variant, each with root_kind, the wrappers tried and the
contexts that produced it), and every individual match carries compiled_kind
plus compiled_contexts. The contexts are a SET, not a single value — a fragment
legal in several usually compiles identically under each, and naming only the
first would report the registry's ordering as a property of your pattern.

Pass 'context' to narrow the union to ONE reading — 'decl', 'stmt', 'expr' or
'member'. A pin naming a context the language does not register, or one no
wrapper hosts this pattern under, fails loud and names the contexts that would
have worked.

THE PIN SCOPES THE OUTER PATTERN ONLY. A where-leaf sub-pattern
(inside_pattern / contains_pattern) always compiles to its full union whatever
the outer pattern pinned, because a leaf asks whether the match CONTAINS a given
shape and the contained thing sits wherever the target puts it. Inheriting the
pin would break the most natural mixed query there is: "class members containing
a return statement" is context:"member" with a 'return $X;' leaf, and java
compiles 'return $X;' under the member context to a field declaration whose type
is the literal word "return" — matching nothing, so the query would answer a
silent zero. The cost of that choice, stated plainly: there is no way to pin a
leaf's context.

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

An EMPTY replacement ("") DELETES the matched ranges — the template interpolates
to nothing, which is how a call or decl is stripped. replacement is required on
replace and may be empty; an OMITTED replacement errors.

Safety model (the replacement is never a blind text edit):

  - dry_run defaults TRUE. A dry run returns unified diffs + a blast-radius
    summary (files_matched, files_changed, matches_replaced, matches_changed)
    and writes NOTHING. Set dry_run:false to apply.
  - Apply mode writes each file atomically (temp + rename).
  - Matched is not changed: files_matched / matches_replaced count what the
    pattern hit and spliced, files_changed / matches_changed count what
    actually moved bytes. An identity template matches everything and changes
    nothing, and only the second of each pair can say so.
  - Pre-edit parse baseline: a file whose ORIGINAL source already carried a
    grammar error is reported in preexisting_parse_failures as
    {path, line, column}, never spliced and never written. It is deliberately
    NOT in rejected_files — nothing can be concluded about an edit to a file
    that did not parse before the edit.
  - Re-parse gate: after splicing, the rewritten file is re-parsed. If it no
    longer parses cleanly the file is REJECTED (listed in rejected_files) and
    never written — a broken edit cannot land. rejected_files therefore means
    only that YOUR edit broke a file which parsed clean before it.
  - Overlap refuse-and-report: if two matches in one file have intersecting or
    nested byte ranges, that whole file is REFUSED (listed in refused_files)
    and left untouched — the tool does not guess which edit wins.
  - Single pass: each match is replaced once; there is no iterate-to-fixpoint.
  - Source-anchored splice: bytes inside the matched span the template did NOT
    rewrite are copied from the file's own source, so inter-token whitespace,
    line structure and indentation survive and an identity template is a
    byte-identical no-op.

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
'log.Println', 'log.Printf' all sharing the "no ad-hoc stdout logging"
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
  context          — pin the parse context to one of decl | stmt | expr | member
                     (match/count/replace). See "## Union compile" below
  replacement      — replacement template in the $X grammar (replace only,
                     required — and MAY be empty, which deletes the matched
                     ranges; an OMITTED replacement errors)
  dry_run          — replace only; default TRUE (preview diffs, no write); false applies
  snippet          — source text (explain only, required)
  repo             — the DIRECTORY to walk. ast is FILESYSTEM-based (it parses
                     files on disk), not a graph query. Omit it to walk the
                     current tree (the session cwd, or the daemon's --root);
                     pass an ABSOLUTE PATH to target any local checkout; or pass
                     a bare repo NAME, which resolves through the machine-local
                     ~/.knowledge manifest to where that repo was last collected,
                     falling back to the current tree when the name IS the
                     current repo. A name in neither fails loud. With repo
                     omitted AND --root at its default AND no session cwd known,
                     the call FAILS LOUD rather than walking the daemon's process
                     cwd. Every match/count result echoes walked_root, and a
                     zero-files_scanned result carries a wrong-root hint. Match
                     hydration uses the walked directory's basename as the graph
                     name.
  package_prefixes — restrict the walk to repo-relative path prefixes, matched
                     at PATH-SEGMENT boundaries ("a/b" admits a/b and anything
                     under a/b/, never the sibling a/bc); a prefix may name a
                     single file
  include_tests    — include the language's own test files (default false)
  lift_exclusions  — walk the files discovery would otherwise decline
  limit            — response-size bound on what match RENDERS (default 100);
                     count and replace traverse the full scope and ignore it

Note on include_tests: the filter consults the LANGUAGE'S OWN test-file
convention, so it means the same thing in Ruby as in Go rather than meaning
_test.go everywhere. A language with no unambiguous filename convention
(Rust marks tests with an in-file "mod tests"; C has none) registers no
predicate, and passing include_tests explicitly for one of those is a HARD
ERROR naming the language and listing the ones that do support it — better
than accepting a blast-radius control that would silently do nothing.
Omitting the flag is never an error.

Note on walk exclusions: discovery declines files by eight named rules before
the walk sees them — unsupported extension, lockfiles, .d.ts declarations,
generated Go, vendored/third-party/generated path components, files in no
language ast parses, files over the 500KB size cap, and pruned directory
names. Two are discovery-path-dependent: the pruned-directory rule fires only
on the non-git fallback walk, and on that walk the path-component rule reads
zero because pruning pre-empts it. A rule reading zero is therefore not
evidence it never ran, and stats.discovery_path names the path that executed.
The authority for what a run actually excluded is the response's own report —
stats.excluded_by_rule (exact counts), stats.excluded_samples (bounded name
samples) and stats.excluded_truncated (per-rule, sample list was capped) —
not any in-tree list. include_tests does not lift these rules;
lift_exclusions does.

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

  match → { matches: [...], compiled: [...], walked_root, stats: {...},
            hint: "..." (only when matches is empty) }
  count → { total, by_file, compiled, walked_root, stats fields rendered inline }
  replace → { applied, dry_run, files_matched, files_changed,
              matches_replaced, matches_changed, refused_files,
              rejected_files, preexisting_parse_failures,
              diffs (relPath → unified diff) }
  explain → indented node-kind tree (text)
  list_node_kinds → array of node-kind strings

The walk stats on match and count carry more than a scanned/skipped pair:
files_skipped is split by cause into skipped_read (unreadable),
skipped_parse_error (the parser returned an error) and skipped_parse_limit
(a parser bound was exceeded), because one conflated counter cannot tell a
permissions problem from a grammar one. files_with_parse_errors and
matches_from_degraded_trees exist because tree-sitter answers a file it
cannot fully parse with a PARTIAL tree rather than an error, so a match can
come off a damaged tree and you should be able to see that it did. The
exclusion report (excluded_by_rule, excluded_samples, excluded_truncated,
discovery_path) rides along on both ops.

pattern_errors appears on match, count and replace whenever a patterns[]
alternation member could not be used: {index, pattern, error} per failed
member, carrying the index YOU wrote, so a partial result reads as partial
rather than as a silently narrower one. It is omitted entirely when every
member was usable, and the singular 'pattern' form still fails hard on a
bad pattern.

Compile-time loudness: an unknown where-tree 'kind' is refused BEFORE the
walk with a near-miss suggestion from that language's own node-kind
vocabulary (the vocabulary list_node_kinds prints); an anonymous token is
told it is one rather than offered a spelling. A where-tree that could never
have matched says so instead of answering a confident zero.

When matches is empty, the hint suggests broadening scope, simplifying the
pattern, or dropping to operation:"count" to confirm the walk produced no
candidates.
`
