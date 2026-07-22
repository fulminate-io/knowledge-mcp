// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// AstToolDef returns the unified ast tool definition. The ast tool is a
// generic primitive (alongside query/mutate/manage) — it gets a single
// schema with an `operation` enum dispatching to 4 ops: match, count,
// explain, list_node_kinds.
//
// Description string is example-heavy on purpose: the LLM client reads
// this text to learn the placeholder DSL and the JSON where-tree shape
// it composes constraints in.
//
// Relocated client-side: source files live on the client's filesystem,
// and the actual handler is InterceptAst in ast.go. The schema lives
// next to the handler so loadSchemas augmentation in
// cmd/knowledge/internal/bootstrap/client.go advertises the tool to the
// LLM without the server ever seeing a `case "ast"` dispatch arm.
func AstToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name:        "ast",
		Description: astToolDescription,
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"operation":   {Type: "string", Description: "Which ast op to run.", Enum: []string{"match", "count", "replace", "explain", "list_node_kinds"}},
				"language":    {Type: "string", Description: "Source language (e.g. 'go', 'python', 'typescript', 'rust'). Required for match/count/replace/explain/list_node_kinds."},
				"pattern":     {Type: "string", Description: "DSL pattern with placeholders ($X capture, $_ wildcard, $$$X seq capture, $$$_ seq wildcard). Example: 'defer $X.Close()'. Required for match/count/replace when 'patterns' is unset."},
				"replacement": {Type: "string", Description: "operation=replace only. Replacement template in the SAME $X DSL grammar: $NAME interpolates capture NAME's matched text verbatim, $$$NAME interpolates a sequence capture's verbatim span, $$ emits a literal '$'. Wildcards $_ / $$$_ are NOT referenceable (usage error). Example: pattern 'defer $X.Close()' + replacement 'safeClose($X)' rewrites every match to safeClose(<the receiver>). Pass an EMPTY STRING (\"\") to DELETE the matched ranges (the template interpolates to nothing — useful for stripping a call/decl). Required when operation=replace and may be empty; an OMITTED replacement errors."},
				"dry_run":     {Type: "boolean", Description: "operation=replace only. Defaults TRUE: preview unified diffs + a blast-radius summary (files touched, matches replaced) WITHOUT writing to disk. Set false to APPLY — each file is rewritten with an atomic per-file write gated by a post-edit re-parse (a rewrite that no longer parses is rejected, never written). Overlapping/nested matches in a file refuse that file whole."},
				"patterns":    {Type: "array", Description: "Sibling-form pattern alternation: list of DSL patterns whose match results are unioned. Use when one logical rule has multiple syntactic shapes (e.g., ['log.Print($$$_)', 'log.Println($$$_)', 'log.Printf($$$_)']). The same 'where' filter applies to each pattern's matches. Mutually exclusive with 'pattern'. Each pattern triggers an independent repo walk — acceptable for small N; for very large alternation lists, prefer authoring separate annotations.", Items: &kgtools.Property{Type: "string"}},
				"where": {Type: "object", Description: "Optional JSON where-tree filter. Composers: all/any/not. Leaves: kind (node-kind constraint), matches (regex), equals (literal text), same_node (capture identity), inside_pattern + contains_pattern (recursive sub-patterns over ancestors / descendants; each accepts an optional 'as' field that names the matched ancestor/descendant for downstream sibling leaves to reference — useful for 'find ancestor function then check its body' shapes). Capture references: bare names (\"X\") for local user-named captures, \"$match\" for the outermost matched node (built-in, no explicit placeholder needed), \"$outer.X\" / \"$outer.outer.X\" walk parent scopes. See description block for examples.", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
					"all":              {Type: "array", Description: "Composer: every child where-tree must match (AND).", Items: &kgtools.Property{Type: "object"}},
					"any":              {Type: "array", Description: "Composer: at least one child where-tree must match (OR).", Items: &kgtools.Property{Type: "object"}},
					"not":              {Type: "object", Description: "Composer: the child where-tree must NOT match (negation)."},
					"kind":             {Type: "object", Description: "Leaf: node-kind constraint, e.g. { of: X, is: function_declaration } (is may be a string or array)."},
					"matches":          {Type: "object", Description: "Leaf: regex over capture text, e.g. { of: X, regex: \"^err[A-Z]\" }."},
					"equals":           {Type: "object", Description: "Leaf: literal text equality, e.g. { of: X, value: Close }."},
					"same_node":        {Type: "object", Description: "Leaf: NODE-identity match across captures, e.g. { captures: [X, $outer.Y] }."},
					"same_text":        {Type: "object", Description: "Leaf: TEXT-equality match across captures, e.g. { captures: [X, $outer.Y] }."},
					"inside_pattern":   {Type: "object", Description: "Leaf: capture has an ancestor matching a sub-pattern; accepts of/pattern/where/as."},
					"contains_pattern": {Type: "object", Description: "Leaf: capture has a descendant matching a sub-pattern; accepts of/pattern/where/as."},
				}},
				"snippet":          {Type: "string", Description: "Source snippet to parse for operation=explain. Returns an indented node-kind tree. Required when operation=explain."},
				"repo":             {Type: "string", Description: "Directory to walk — ast is FILESYSTEM-based (it parses files on disk), not a graph query. Omit to walk the current tree; pass an ABSOLUTE PATH to target any local checkout; or pass a bare repo NAME, which resolves to where that repo was last collected on THIS machine (via the local ~/.knowledge manifest), falling back to the current tree when the name is the current repo. A name that is neither in the manifest nor the current repo fails loud. Match hydration uses the walked directory's basename as the graph name."},
				"package_prefixes": {Type: "array", Description: "Restrict the walk to files whose repo-relative path starts with any of these prefixes. Empty means no restriction.", Items: &kgtools.Property{Type: "string"}},
				"include_tests":    {Type: "boolean", Description: "When true, _test.go-suffix files are included in the walk. Defaults to false. NOTE: testdata/, vendor/, node_modules/, .git/, dist/, build/, etc. are hard-skipped by parser.DiscoverFiles regardless of this flag."},
				"limit":            {Type: "number", Description: "Cap on RawMatch results. Default 100."},
			},
			Required: []string{"operation"},
		},
	}
}

// astToolDescription is split out as a const so the AstToolDef body stays
// scannable and the (intentionally long) markdown lives at file scope.
const astToolDescription = `Structural AST search-and-replace across code graphs via a placeholder-template DSL paired with a recursive JSON boolean where-tree, working uniformly across every indexed language. Five ops:

  - match: Run a pattern (with optional where-tree filter) and return hydrated MatchResults with named captures and the enclosing function/method node from the code graph.
  - count: Same walk as match but skip Hydrate. Returns {total, by_file (repo-relative path keys), files_scanned, files_skipped, duration_ms}.
  - replace: The WRITE counterpart to match — match → interpolate captures into a 'replacement' template → splice → re-parse gate. dry_run defaults TRUE (preview unified diffs + blast-radius, no write); set dry_run:false to apply atomically. An EMPTY replacement ("") DELETES the matched ranges. Overlapping/nested matches in a file refuse that file whole; a rewrite that fails to re-parse is rejected (never written). Example: pattern 'defer $X.Close()' + replacement 'safeClose($X)'.
  - explain: Parse a single snippet and emit the indented node-kind tree. Pure debug aid; does not touch the code graph.
  - list_node_kinds: Enumerate the tree-sitter node-kind vocabulary for a language. Useful when authoring a 'kind' leaf.

Required params by operation (in addition to the always-required operation): match / count require language + pattern (or patterns for sibling-form alternation); replace requires language + replacement + pattern (or patterns); explain requires language + snippet; list_node_kinds requires language.

Placeholder DSL — four forms:

  - $X     — capture a single node, bind to capture name X
  - $_     — wildcard single node (no capture)
  - $$$X   — capture a sequence (zero-or-more siblings), bind to X
  - $$$_   — wildcard sequence (no capture)

Capture references inside where-tree leaves come in three forms:
  - "X"              — local user-named capture (bare name, no leading $)
  - "$match"         — built-in: the outermost matched node of the local match. No explicit named placeholder needed; pair with $_ + a kind/matches/equals leaf on $match to gate the outer node directly.
  - "$outer.X"       — capture from one parent scope (used inside sub-pattern wheres). Chain "$outer." for deeper nesting: "$outer.outer.X" walks two levels.

User identifiers cannot start with $. The leading $ is reserved for the two built-in prefixes above.

JSON where-tree — composers + leaves:

Three composers (any node may set exactly one; multiple at once are AND'd implicitly):
  - all: [...]   — every child must match (AND)
  - any: [...]   — at least one child must match (OR)
  - not: {...}   — child must NOT match (negation)

Six leaves:
  - kind            — { "of": "X", "is": "function_declaration" } or { "of": "X", "is": ["function_declaration", "method_declaration"] }
  - matches         — { "of": "X", "regex": "^err[A-Z]" }   (regex over capture text)
  - equals          — { "of": "X", "value": "Close" }       (literal text equality)
  - same_node       — { "captures": ["X", "$outer.Y"] }     (NODE identity — different occurrences of the same identifier won't match)
  - same_text       — { "captures": ["X", "$outer.Y"] }     (TEXT equality — variable-name match across siblings; use when same_node is too strict)
  - inside_pattern  — { "of": "X", "pattern": "func $F() error { $$$BODY }", "where": {...}, "as": "FN" }   (X has an ancestor matching this sub-pattern; "as" optionally names the matched ancestor for downstream sibling leaves)
  - contains_pattern — { "of": "X", "pattern": "$F.Close()", "where": {...}, "as": "TGT" }                   (X has a descendant matching this sub-pattern; "as" works the same)

Sibling-form alternation: pass 'patterns: [string, ...]' instead of 'pattern' when one logical rule has multiple syntactic shapes. Results unioned; same where applies to each.

Walk root: ast is FILESYSTEM-based — it walks a directory on disk, chosen from the 'repo' arg. Omit repo to walk the current tree (the session cwd, or the daemon's --root); pass an ABSOLUTE PATH to walk that checkout directly; pass a bare repo NAME to walk where that repo was last collected on THIS machine (the ~/.knowledge manifest). When repo is omitted AND --root was left at its default AND no session cwd is known, the call FAILS LOUD rather than silently walking the daemon's process cwd — pass repo:<name|/abs/path> or start the daemon with --root <dir>. Every match/count result echoes 'walked_root' (the directory actually walked); when files_scanned is 0 the result carries a wrong-root hint.

Examples:

  // Every defer X.Close() inside a for-loop, NOT also inside a closure (so the goroutine pattern wins):
  ast({ "operation": "match", "language": "go",
        "pattern": "defer $X.Close()",
        "where": { "all": [
          { "inside_pattern":  { "of": "X", "pattern": "for $$$INIT { $$$BODY }" } },
          { "not":             { "inside_pattern": { "of": "X", "pattern": "func() { $$$BODY }" } } }
        ] } })

  // Function decls returning error whose name starts with "load":
  ast({ "operation": "match", "language": "go",
        "pattern": "func $NAME($$$ARGS) error { $$$BODY }",
        "where": { "matches": { "of": "NAME", "regex": "^load" } } })

  // Two captures bind to the same identifier (e.g. defer F.Close where F was already opened in a parent ctor):
  ast({ "operation": "match", "language": "go",
        "pattern": "defer $X.Close()",
        "where": { "inside_pattern": { "of": "X",
                                       "pattern": "$Y, _ := os.Open($_)",
                                       "where": { "same_node": { "captures": ["Y", "$outer.X"] } } } } })

  // Just gate the outer node by kind: wildcard pattern + $match kind leaf.
  ast({ "operation": "match", "language": "go",
        "pattern": "$_",
        "where": { "kind": { "of": "$match", "is": "method_declaration" } } })

Note on scope filters: parser.DiscoverFiles (the underlying file walker) hard-skips testdata/, vendor/, node_modules/, .git/, dist/, build/, and similar generated/dependency directories regardless of the include_tests flag. The flag governs whether _test.go-style test files in regular package directories are included; it does NOT bypass skipPathComponents directory filtering. Source of truth: skipDirs map at cmd/knowledge/internal/collector/parser/indexer_discover.go:111 and skipPathComponents at indexer_discover.go:70.`
