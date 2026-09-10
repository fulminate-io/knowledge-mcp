// SPDX-License-Identifier: Apache-2.0

package tools

// helpAst is assembled from two parts held in two files, because the topic
// outgrew the repository's 500-line file threshold. The SPLIT POINT IS THE
// TOPIC'S OWN SEAM, not an arbitrary line: everything up to the where-tree
// vocabulary is what a caller reads to WRITE a call, and everything from the
// union-compile section on is what a caller reads to UNDERSTAND A RESULT.
// helpAstResults holds the second half; the two are concatenated here so the
// rendered topic is byte-identical to the single constant it replaced.
const helpAst = helpAstCalling + helpAstResults

const helpAstCalling = `# ast — Structural code search-and-replace via tree-sitter

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

Four forms appear as values for the 'of' / 'captures' fields:

  "X"              — local-scope capture (bare name; the match's own
                     bindings from $X / $$$X placeholders).
  "$match"         — built-in: the outermost matched node of the local
                     match. Available without an explicit named
                     placeholder, so a wildcard pattern $_ paired with
                     a kind / matches / equals leaf on $match gates the
                     outer node directly.
  "$outer.X"       — capture from one parent scope (used inside a
                     sub-pattern's where-tree to reach back to the
                     enclosing match). Chain the WHOLE token for deeper
                     nesting: "$outer.$outer.X" walks two levels, one
                     "$outer." per level. ("$outer.outer.X" is NOT two
                     levels — it walks one and then looks for a capture
                     literally named "outer.X".)
  "<as>.<capture>" — a sub-pattern leaf's OWN capture, exported under the
                     name that leaf declared in its 'as' field.

WORKED EXAMPLE — the namespaced sub-pattern capture. A contains_pattern
leaf that declares 'as' exports every capture ITS pattern binds, under
that name, so a SIBLING leaf can reference the sub-pattern's capture
directly instead of nesting inside it:

  { "all": [
      { "contains_pattern": { "of": "FN", "pattern": "&$MP.Transport{Command: $C}",
                              "as": "T" } },
      { "flows_to": { "from": "$match", "to": "T.C", "within": "FN" } }
  ] }

'T' is the matched literal, as it always was; 'T.C' is the value that
literal's Command field holds. Two things the form does NOT do, because
both are worth knowing before you write one:

  · A leaf with NO 'as' declares no namespace and exports nothing. Its
    captures are unreachable by any spelling, and a reference to one is
    refused by name.
  · The export binds the FIRST matching candidate and does not backtrack,
    exactly as the 'as' handle always has. For "does ANY candidate satisfy
    this", put the constraint inside the sub-pattern's own where with
    "$outer." refs — see the sub-pattern section below.

A reference naming something no placeholder and no 'as' declaration binds
is refused BEFORE the walk, with the vocabulary it would have accepted, so
an authoring typo cannot return the clean zero a correct search returns.

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

## flows_to destinations are VALUE-precise

flows_to's 'to' names a VALUE, and it can name a value INSIDE a composite
literal rather than the literal itself. There is no new syntax for this —
which is exactly why it needs an example, because the change is in what a
destination capture reaches rather than in how you spell one.

WORKED EXAMPLE — field versus field. A record supplies two values to one
transport literal, one to Command and one to Dir:

  dir := spec.GetDir()
  cmd := spec.GetCommand()
  return &mcp.CommandTransport{Command: cmd, Dir: dir}

Asked with 'to' naming the WHOLE literal, both values reach it and the two
are indistinguishable. Asked with 'to' naming the FIELD's value:

  { "contains_pattern": { "of": "FN", "pattern": "&$MP.CommandTransport{Command: $C, $$$REST}",
                          "where": { "flows_to": { "from": "$outer.$match",
                                                   "to": "C",
                                                   "within": "$outer.FN" } } } }

...only the value that actually occupies Command matches. The same
precision separates two spawns in one function: the one whose result
reaches the destination matches and the one whose result is read locally
does not.

The 'from' side is value-scoped the same way. When 'from' is a composite
expression — a call such as "spec.GetCommand()" rather than a bare
identifier — the walk follows the DECLARED steps that consumed it, and does
not close over every other use of the operands inside it. Without that,
"spec.GetCommand()" and "spec.GetEnv()" would be interchangeable, because
they share the receiver.

THE LIMIT: the walk is intra-declaration. A value handed to another
function is outside every flows_to, which is why 'within' is required and
never defaulted.

## Importing a package: the import-spec form (Go)

An import SPEC is a first-class pattern target, so "which files import
os/exec" is a structural question rather than a text search:

  "$$$P \"os/exec\""   — every import of that path, in every spec form Go
                         writes: ungrouped, grouped, aliased, plain, and a
                         group holding a single spec. P binds the local
                         name and is EMPTY when the spec has none.
  "$P \"os/exec\""     — the ALIASED forms only, because a single-node
                         placeholder requires a name to bind.

The sequence placeholder is what covers the absent local name: it sits in
an optional child slot with no separator, where it matches zero siblings.

WHAT THIS IS NOT. A bare "\"os/exec\"" fragment is a STRING LITERAL
pattern, and it matches the path wherever it appears — as a const value,
as a return value, as an import path. That difference is the whole point
of the spec form. The compile disclosure tells the two apart: the import
form reports root_kind "import_spec" under the "importspec" wrapper, and
the bare literal reports a string-literal root under every wrapper that
hosts it.

THE RESIDUAL, stated rather than left to be discovered: a bare capture
with a kind leaf — "$X" plus { "kind": { "of": "X", "is": "import_spec" } }
— still binds only the specs that carry a LOCAL NAME. An unnamed spec
shares its byte span with its path literal, and the engine's same-span
descent binds the literal instead. Use the form above rather than the kind
leaf for imports.
`
