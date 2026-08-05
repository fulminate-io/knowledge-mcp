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
				"pattern":     {Type: "string", Description: "DSL pattern with placeholders ($X capture, $_ wildcard, $$$X seq capture, $$$_ seq wildcard). Example: 'defer $X.Close()'. Required for match/count/replace when 'patterns' is unset. $$ escapes a literal $ (e.g. write $${expr} to match a ${expr} template literal)."},
				"replacement": {Type: "string", Description: "operation=replace only. Replacement template in the SAME $X DSL grammar: $NAME interpolates capture NAME's matched text verbatim, $$$NAME interpolates a sequence capture's verbatim span, $$ emits a literal '$'. Wildcards $_ / $$$_ are NOT referenceable (usage error). Example: pattern 'defer $X.Close()' + replacement 'safeClose($X)' rewrites every match to safeClose(<the receiver>). Pass an EMPTY STRING (\"\") to DELETE the matched ranges (the template interpolates to nothing — useful for stripping a call/decl). Required when operation=replace and may be empty; an OMITTED replacement errors."},
				"dry_run":     {Type: "boolean", Description: "operation=replace only. Defaults TRUE: preview unified diffs + a blast-radius summary (files_matched and files_changed, matches_replaced and matches_changed) WITHOUT writing to disk. Set false to APPLY — each file is rewritten with an atomic per-file write gated by a post-edit re-parse (a rewrite that no longer parses is rejected, never written). Overlapping/nested matches in a file refuse that file whole."},
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
				"context":          {Type: "string", Description: "Pin the parse context a pattern compiles under, for match/count/replace. WITHOUT it (the default) a pattern compiles to EVERY context that can express it and matches their union — java '$T $N = $V;' finds class fields AND local variables, and every result is stamped with the contexts that produced its tree. Pin one to narrow: 'member' keeps only the class-member reading, 'stmt' only the in-body statement one. A pin naming a context this language does not register, or one no wrapper hosts this pattern under, fails loud and names the contexts that would have worked. The pin scopes the outer pattern only: a where-leaf sub-pattern (inside_pattern / contains_pattern) always compiles to its full union, because a leaf asks whether the match CONTAINS a given shape and the contained thing sits wherever the target puts it — pinning it would turn 'members containing a return statement' into a silent zero. There is consequently no way to pin a leaf's context.", Enum: []string{"decl", "stmt", "expr", "member"}},
				"snippet":          {Type: "string", Description: "Source snippet to parse for operation=explain. Returns an indented node-kind tree. Required when operation=explain."},
				"repo":             {Type: "string", Description: "Directory to walk — ast is FILESYSTEM-based (it parses files on disk), not a graph query. Omit to walk the current tree; pass an ABSOLUTE PATH to target any local checkout; or pass a bare repo NAME, which resolves to where that repo was last collected on THIS machine (via the local ~/.knowledge manifest), falling back to the current tree when the name is the current repo. A name that is neither in the manifest nor the current repo fails loud. Match hydration uses the walked directory's basename as the graph name."},
				"package_prefixes": {Type: "array", Description: "Restrict the walk to files at or under any of these repo-relative prefixes, matched at PATH-SEGMENT boundaries: 'a/b' admits a/b and everything under a/b/, and never the sibling a/bc. A prefix may name a single FILE as well as a directory. Empty means no restriction. This is a narrowing you asked for rather than a rule discovery applied, so paths outside the prefixes are absent from the exclusion report entirely rather than counted under a rule — a scoped run's per-rule counts are a slice of an unscoped run's, not the same numbers.", Items: &kgtools.Property{Type: "string"}},
				"include_tests":    {Type: "boolean", Description: "Include TEST source in the walk. Defaults to false, which drops the paths the LANGUAGE'S OWN test-file convention claims — Go's _test.go suffix, Ruby's spec conventions, and so on — so the flag means the same thing in every language instead of meaning Go everywhere. Some languages register no convention because they have no unambiguous FILENAME one (Rust marks tests with an in-file `mod tests`; C has none at all). Passing include_tests explicitly for one of those is a HARD ERROR that names the language and lists the languages which do support it, rather than a control that is accepted and then silently does nothing. Omitting it is never an error: a language with a convention gets the default-false filter, and one without has nothing to filter by, so every file is walked — narrow with package_prefixes instead. This flag governs test files ONLY. It neither applies nor lifts the walk's own exclusion rules; those are disclosed in stats.excluded_by_rule and lifted with lift_exclusions."},
				"lift_exclusions":  {Type: "boolean", Description: "Walk the files discovery would otherwise decline, instead of declining them. Discovery drops eight classes of file before the walk ever sees them — markdown, lockfiles, .d.ts declarations, generated Go, vendored/third-party/generated path components, unsupported languages, files over 500KB, and (only on the non-git fallback walk) pruned directories like .git and node_modules — and every match/count result reports them under stats.excluded_by_rule with a bounded name sample per rule. Set this true to search them anyway. It does NOT lift .gitignore, which is git's own filtering and your repo's configuration rather than a rule ast chose, and it does NOT lift your own narrowing: language, package_prefixes and include_tests still apply to whatever discovery returns. A lifted run is reported as such in stats.discovery_path, so a run that was not allowed to exclude anything stays distinguishable from a tree that had nothing to exclude."},
				"limit":            {Type: "number", Description: "Cap on how many matches operation=match RENDERS — a response-size bound only, default 100. It NEVER bounds the walk: count and replace always traverse the full scope and ignore limit entirely, and match's `total` field reports the FULL-walk match count even when fewer results are rendered."},
			},
			Required: []string{"operation"},
		},
	}
}

// astToolDescription is split out as a const so the AstToolDef body stays
// scannable and the (intentionally long) markdown lives at file scope.
const astToolDescription = `Structural AST search-and-replace across code graphs via a placeholder-template DSL paired with a recursive JSON boolean where-tree, across most indexed languages — a deny set (config/markup grammars, plus PHP for a placeholder-sigil collision) is refused by match/count/replace and surfaced at runtime by list_node_kinds/explain. Five ops:

  - match: Run a pattern (with optional where-tree filter) and return hydrated MatchResults with named captures and the enclosing function/method node from the code graph.
  - count: Same walk as match but skip Hydrate. Returns {total, by_file (repo-relative path keys), files_scanned, files_skipped, duration_ms}.
  - replace: The WRITE counterpart to match — match → interpolate captures into a 'replacement' template → splice → re-parse gate. dry_run defaults TRUE (preview unified diffs + blast-radius, no write); set dry_run:false to apply atomically. An EMPTY replacement ("") DELETES the matched ranges. Overlapping/nested matches in a file refuse that file whole; a rewrite that fails to re-parse is rejected (never written). Example: pattern 'defer $X.Close()' + replacement 'safeClose($X)'.
  - explain: Parse a single snippet and emit the indented node-kind tree, each node marked NAMED or ANONYMOUS — a token with no named wrapper above it is the per-token replace-hazard tell. Pure debug aid; does not touch the code graph. For a deny-listed language it still parses but annotates match/replace as unsupported.
  - list_node_kinds: Enumerate the tree-sitter node-kind vocabulary for a language. Useful when authoring a 'kind' leaf. For a deny-listed language it answers but annotates match/replace as unsupported.

Required params by operation (in addition to the always-required operation): match / count require language + pattern (or patterns for sibling-form alternation); replace requires language + replacement + pattern (or patterns); explain requires language + snippet; list_node_kinds requires language.

Placeholder DSL — four forms:

  - $X     — capture a single node, bind to capture name X
  - $_     — wildcard single node (no capture)
  - $$$X   — capture a sequence (zero-or-more siblings), bind to X
  - $$$_   — wildcard sequence (no capture)

$$ escapes a literal '$' in a PATTERN (not a placeholder) — e.g. match a ${...} template literal by writing $${...}. This mirrors the replacement-template $$ escape; three dollars ($$$NAME / $$$_) stays a sequence reference.

Capture references inside where-tree leaves come in three forms:
  - "X"              — local user-named capture (bare name, no leading $)
  - "$match"         — built-in: the outermost matched node of the local match. No explicit named placeholder needed; pair with $_ + a kind/matches/equals leaf on $match to gate the outer node directly.
  - "$outer.X"       — capture from one parent scope (used inside sub-pattern wheres). Chain "$outer." for deeper nesting: "$outer.outer.X" walks two levels.

User identifiers cannot start with $. The leading $ is reserved for the two built-in prefixes above; write $$ to escape a literal '$' (e.g. $${...} matches a ${...} template literal).

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

Union compile, the compiled-kind disclosure, and the 'context' pin:

A pattern is a source FRAGMENT, and the same fragment is often grammatical in more than one place. Compilation therefore enumerates EVERY parse context that can host it and matches the union of the distinct trees — java '$T $N = $V;' is both a class field and a local variable, and both are returned. This is what stops a fragment silently compiling to a construct you did not mean: c# 'Debug.Assert($X);' reads as a class-body field declaration as readily as a statement, and answering with only the first reading returns zero against thousands of real call sites.

Every result discloses what it compiled to, so a surprising zero is diagnosable from the output rather than by experiment. match and count echo a 'compiled' array — one entry per variant, each carrying root_kind, the wrappers tried and the contexts that produced it — and every individual match carries compiled_kind plus compiled_contexts. The contexts are a SET rather than a single value: a fragment legal in several of them usually compiles identically under each, and naming only the first would report the registry's ordering as if it were a property of your pattern.

Pass 'context' to narrow the union to one reading — 'decl', 'stmt', 'expr' or 'member'. The pin scopes the outer pattern only. A where-leaf sub-pattern (inside_pattern / contains_pattern) compiles to its full union no matter what the outer pattern pinned, because a leaf asks whether the match CONTAINS a given shape and the contained thing sits wherever the TARGET puts it. Inheriting the pin would break the most natural mixed query there is: 'class members containing a return statement' is context:"member" with a 'return $X;' leaf, and java compiles 'return $X;' under the member context to a field declaration whose type is the literal word "return" — matching nothing, so the query would answer a silent zero. The cost of that choice, stated plainly: there is no way to pin a leaf's context.

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

Walk exclusions, and why a zero is readable: discovery declines files by EIGHT named rules before the walk ever sees them — unsupported file extension, lockfiles, .d.ts declaration files, generated Go, vendored/third-party/generated PATH COMPONENTS, files in no language ast parses, files over the 500KB size cap, and pruned directory names. Two of those depend on WHICH discovery path ran: the pruned-directory rule fires only on the non-git fallback walk, because git ls-files never offers a path under one, and on that same walk the path-component rule reads zero because directory pruning pre-empts it. So a rule reading zero is not evidence the rule never ran, and stats.discovery_path names the path that executed. Read no in-tree list as the contract: the RESPONSE is the authority for what a given run excluded — an exact count per rule in stats.excluded_by_rule, up to five sample names per rule in stats.excluded_samples, and a per-rule stats.excluded_truncated flag saying the sample list was capped. include_tests is a separate control governing test files only; lift_exclusions is what walks the rule-declined set.

What the response reports, beyond the matches themselves:

  - stats.files_skipped is split by cause into stats.skipped_read (the file could not be read), stats.skipped_parse_error (the parser returned an error) and stats.skipped_parse_limit (the file exceeded a parser bound). One conflated counter cannot tell a permissions problem from a grammar one.
  - stats.files_with_parse_errors and stats.matches_from_degraded_trees: tree-sitter answers a file it cannot fully parse with a PARTIAL tree rather than an error, so matches can come from a damaged one. These say how many files were damaged and how many results came off a damaged tree.
  - replace reports preexisting_parse_failures — the files whose ORIGINAL source already carried a grammar error, each as {path, line, column}. They are never spliced and never written, and they are deliberately NOT in rejected_files: rejected_files means only that YOUR edit broke a file which parsed clean before it.
  - replace reports files_matched alongside files_changed, and matches_replaced alongside matches_changed. Matched/replaced count what the pattern hit and spliced; changed counts what actually moved bytes. An identity template matches everything and changes nothing, and only the second of each pair can say so.
  - pattern_errors lists the patterns[] alternation members that could not be used, each as {index, pattern, error} carrying the index YOU wrote. It is omitted entirely when every member was usable. Present on match, count and replace.

Two compile-time refusals, both so that a call which could never have matched says so instead of answering a confident zero. An unknown where-tree 'kind' is rejected BEFORE the walk, with a near-miss suggestion drawn from that language's own node-kind vocabulary — the same vocabulary list_node_kinds prints — and an anonymous token gets told it is one rather than offered a spelling. And in a patterns[] alternation, a member that fails to compile is reported in pattern_errors while the members that did compile still run; the singular 'pattern' form is unchanged and still fails hard.`
