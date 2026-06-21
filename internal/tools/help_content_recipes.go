// SPDX-License-Identifier: Apache-2.0

package tools

const helpRecipes = "# Recipe DSL\n" +
	"\n" +
	"The recipe DSL transforms a source graph into a target graph via a\n" +
	"declarative pipeline of rules. Zero LLM at runtime — the interpreter\n" +
	"is pure Go. Recipes are authored once (by an agent inspecting the\n" +
	"source graph, or by hand), stored in the `transformers` graph, and\n" +
	"invoked via the `collect` tool.\n" +
	"\n" +
	"## When to use recipes\n" +
	"\n" +
	"- A raw graph (typically `web` but can be any graph) has been\n" +
	"  collected and you want to extract structured domain nodes from it.\n" +
	"- You want repeatable, cheap re-translation when the recipe changes\n" +
	"  (no re-fetching the source).\n" +
	"- The translation is mechanical (regex / heading / edge-walk driven)\n" +
	"  — not open-ended semantic summarization. Use LLM-driven translation\n" +
	"  paths for that.\n" +
	"\n" +
	"## Lifecycle\n" +
	"\n" +
	"1. **Inspect the source graph.** Use `query(graph:\"web\", name:\"<slug>\", mode:\"stats\")`\n" +
	"   to see node types, then `query`/`traverse` on sample nodes to\n" +
	"   understand the structure (what edges exist, what metadata keys\n" +
	"   are populated, what heading shapes appear).\n" +
	"2. **Draft the recipe body** as a multi-line string following the\n" +
	"   grammar below.\n" +
	"3. **Store it** as a node in the transformers graph:\n" +
	"\n" +
	"       mutate({\n" +
	"         operation: \"create\", graph: \"transformers\", type: \"recipe\",\n" +
	"         name: \"my-source-to-target\",\n" +
	"         content: \"<DSL body>\",\n" +
	"         description: \"One-line summary of what the recipe extracts\",\n" +
	"         metadata: {\n" +
	"           source_graph_type: \"web\",\n" +
	"           target_graph_type: \"practice\",\n" +
	"           target_name: \"my-target-language\"\n" +
	"         }\n" +
	"       })\n" +
	"\n" +
	"4. **Dry-run** to see what would be emitted:\n" +
	"\n" +
	"       collect({\n" +
	"         type: \"web\", id: \"<source-slug>\",\n" +
	"         transformer: \"recipe\", recipe: \"my-source-to-target\",\n" +
	"         dry_run: true\n" +
	"       })\n" +
	"\n" +
	"   No seed_urls + transformer=\"recipe\" → the crawl is skipped and\n" +
	"   the recipe runs against the already-cached raw graph. Iterate\n" +
	"   without paying fetch cost.\n" +
	"\n" +
	"5. **Real run** (omit `dry_run` or set to false). Emits land in the\n" +
	"   target graph with `translated-from` edges back to the source\n" +
	"   nodes for lineage.\n" +
	"\n" +
	"6. **Re-run with `force:true`** to wipe and rebuild emissions for\n" +
	"   this source slug (via `translated-from` edge Evidence match). Use\n" +
	"   after editing the recipe.\n" +
	"\n" +
	"## Grammar (EBNF-ish)\n" +
	"\n" +
	"    recipe       = { rule NEWLINE } .\n" +
	"    rule         = select | traverse | filter | bind | group_by\n" +
	"                 | emit | lookup | link | source_ref .\n" +
	"\n" +
	"    select       = \"select\" IDENT [ \"where\" expr ] .\n" +
	"    traverse     = \"traverse\" IDENT direction [ \"as\" VAR ] .\n" +
	"    filter       = \"filter\" expr .\n" +
	"    bind         = \"bind\" VAR \":=\" expr .\n" +
	"    group_by     = \"group_by\" expr .\n" +
	"    emit         = \"emit\" IDENT \"{\" field_map \"}\" [ \"as\" VAR ] .\n" +
	"    lookup       = \"lookup\" IDENT \"by\" expr \"as\" VAR .\n" +
	"    link         = \"link\" expr \"--[\" IDENT \"]-->\" expr .\n" +
	"    source_ref   = \"source_ref\" expr .\n" +
	"\n" +
	"    field_map    = { IDENT \":=\" expr [ NEWLINE ] } .\n" +
	"    direction    = \"in\" | \"out\" | \"both\" .\n" +
	"\n" +
	"    expr         = primary [ (\"~=\" | \"!~\") REGEX ] .\n" +
	"    primary      = VAR { \".\" IDENT }\n" +
	"                 | IDENT { \".\" IDENT }\n" +
	"                 | IDENT \"(\" [ expr { \",\" expr } ] \")\"\n" +
	"                 | STRING\n" +
	"                 | \"(\" expr \")\" .\n" +
	"\n" +
	"    VAR          = \"$\" IDENT\n" +
	"    REGEX        = \"/\" pattern \"/\"   (Go regexp syntax; `\\/` escapes /)\n" +
	"    STRING       = \"...\"              (double-quoted, standard escapes)\n" +
	"    IDENT        = [a-zA-Z_][a-zA-Z0-9_-]*\n" +
	"    comments     = # to end-of-line, anywhere whitespace is legal\n" +
	"\n" +
	"Rules run sequentially top-to-bottom. Each rule operates on and\n" +
	"potentially transforms the working rowset.\n" +
	"\n" +
	"## Rule semantics\n" +
	"\n" +
	"### select\n" +
	"`select page where page.url ~= /hohpe/` — load all nodes of the given\n" +
	"type from the source graph, optionally filtered by expression.\n" +
	"Replaces the working rowset. Each row is one source node with its\n" +
	"hydrated Node (for field access) and a fresh Vars map.\n" +
	"\n" +
	"### traverse\n" +
	"`traverse references out as $related` — walk edges from each current\n" +
	"row in the given direction (in|out|both), filtered by edge type.\n" +
	"Replaces the rowset: one row per traversed target, each carrying the\n" +
	"target's hydrated Node. `as $var` stamps the target's ID on the new\n" +
	"row's Vars. CRITICAL: Vars from the pre-traverse row ARE CLONED into\n" +
	"every post-traverse row, so any prior emit/lookup binding survives.\n" +
	"\n" +
	"### filter\n" +
	"`filter section.heading ~= /Problem|Solution/` — drop rows whose\n" +
	"predicate evaluates to empty string. Non-empty = truthy. The regex\n" +
	"operator returns the match text (truthy) or empty (falsy).\n" +
	"\n" +
	"### bind\n" +
	"`bind $slug := lower(concat(page.name, \"-ext\"))` — store the\n" +
	"evaluated value under $slug. Writes to both every row's Vars and\n" +
	"env.Vars so downstream rules can reference it via $slug.\n" +
	"\n" +
	"### group_by\n" +
	"`group_by section.parent_page` — collapse rows with equal key into\n" +
	"one row. The resulting row carries a synthetic `group.keys` pseudo-\n" +
	"variable (comma-joined distinct key values) for downstream emits\n" +
	"that need to enumerate the group.\n" +
	"\n" +
	"### emit\n" +
	"`emit pattern { type := \"pattern\", name := page.name, summary :=\n" +
	"page.name } as $pat` — write a target-graph node per row.\n" +
	"\n" +
	"- Well-known field names land on Node struct fields: `type`, `name`\n" +
	"  (SymbolName), `summary`, `description`, `content`, `source`,\n" +
	"  `status`. Any other key lands in Metadata.\n" +
	"- The `identity` field (if present) feeds StableID; otherwise `name`\n" +
	"  is used. If both are empty the row is SKIPPED (not emitted with a\n" +
	"  hex-ID fallback) — Stats.SkippedChunks increments.\n" +
	"- StableID is computed from (target graph, sourceSlug, NodeType,\n" +
	"  identity). Same identity + same sourceSlug → same target ID across\n" +
	"  runs (idempotent). Same identity + different sourceSlug → distinct\n" +
	"  IDs (source-scoped emissions don't collide).\n" +
	"- A `translated-from` edge is stamped from the emitted node back to\n" +
	"  the source row's NodeID (or `source_ref` override), carrying the\n" +
	"  sourceSlug in Evidence for Force-cleanup scoping.\n" +
	"- `as $var` binds the emitted node's ID into the current row's Vars\n" +
	"  AND the env-wide EmitMap. The binding survives into later rules\n" +
	"  and through traverse (via cloneRowVars).\n" +
	"\n" +
	"### lookup\n" +
	"`lookup pattern by page.name as $rel` — compute the StableID that a\n" +
	"prior emit WOULD have produced for the same (target, sourceSlug,\n" +
	"NodeType, identity) tuple, verify the node exists in the target\n" +
	"graph (in-memory ByID check), and bind the resulting ID to $rel.\n" +
	"\n" +
	"- No write. No translated-from edge. No NodesEmitted increment.\n" +
	"- If the identity expression resolves to empty OR the node is\n" +
	"  absent: no binding, Stats.LookupMisses increments, downstream\n" +
	"  `link` rules for that row silently skip.\n" +
	"- Use this instead of re-`emit` when a later rule needs to reference\n" +
	"  a target that an earlier rule already emitted. Pays only\n" +
	"  StableID + map-read cost.\n" +
	"\n" +
	"### link\n" +
	"`link $pat --[relates-to]--> $rel` — create a target-graph edge\n" +
	"between two resolved IDs. Both endpoints can be bound vars, field\n" +
	"refs, literal strings, or function calls — any expression that\n" +
	"returns a string.\n" +
	"\n" +
	"- Empty endpoint (unbound $var) → silent skip, Stats.LinkMisses++.\n" +
	"- Non-empty endpoint that doesn't exist in the target graph → silent\n" +
	"  skip, Stats.LinkMisses++ (ByID verification is automatic).\n" +
	"- No unique-edge enforcement at the DSL level — the underlying\n" +
	"  target DB dedups equal (from, to, type) edges.\n" +
	"\n" +
	"### source_ref\n" +
	"`source_ref $explicit_origin` — override the default translated-from\n" +
	"target for subsequent emits in this recipe. Evaluated once against\n" +
	"the first row (recipe-scope, not row-scope). Rarely needed; default\n" +
	"(current row's NodeID) is usually right.\n" +
	"\n" +
	"## Expression sub-language\n" +
	"\n" +
	"Types are strings only. Empty string = false. Any non-empty = true.\n" +
	"\n" +
	"### Literals\n" +
	"- `\"Hello world\"` — double-quoted string literal. Standard escapes.\n" +
	"- `/pattern/` — regex literal (only valid on the right of `~=` /\n" +
	"  `!~`). Go regexp syntax.\n" +
	"\n" +
	"### Field access\n" +
	"- `page.name` — SymbolName of the current row's hydrated Node.\n" +
	"- `page.type` / `page.summary` / `page.content` / `page.description` / `page.source` / `page.status` — well-known Node struct fields.\n" +
	"- `page.metadata.http_status` — arbitrary metadata key (the chain\n" +
	"  `page.<key>` also falls through to metadata when `<key>` isn't a\n" +
	"  well-known field).\n" +
	"- `page.url` — sugar for `page.metadata.url` (same fall-through).\n" +
	"- `$var.name` — read the hydrated-target node's name via a\n" +
	"  previously-bound var. `$var` alone returns the ID.\n" +
	"\n" +
	"### Operators\n" +
	"- `lhs ~= /pattern/` — regex match. Returns the matched substring\n" +
	"  (truthy) or empty (falsy).\n" +
	"- `lhs !~ /pattern/` — regex NOT-match. Inverts: returns a sentinel\n" +
	"  truthy value when the LHS does NOT match, empty when it matches.\n" +
	"  Use this on `where` / `filter` predicates to drop rows.\n" +
	"\n" +
	"### Built-in functions\n" +
	"- `concat(a, b, c, ...)` — string concatenation.\n" +
	"- `trim(s)` — strip leading/trailing whitespace.\n" +
	"- `lower(s)` / `upper(s)` — case conversion.\n" +
	"- `has_edge(edge_type, direction)` — true (sentinel non-empty) if\n" +
	"  the current row's node has at least one matching edge. Direction\n" +
	"  is `in` | `out` | `both`.\n" +
	"\n" +
	"No arithmetic, no length, no boolean AND/OR, no ternaries. If the\n" +
	"predicate needs more logic, compose alternations in the regex\n" +
	"(`/^A|^B|^C/`) or split into multiple `filter` rules.\n" +
	"\n" +
	"## Cross-emit bindings — how `$var` works through traverse\n" +
	"\n" +
	"When `emit ... as $pat` fires, the emitted target ID is stamped onto\n" +
	"the CURRENT row's Vars (and env.EmitMap as an audit trail).\n" +
	"\n" +
	"When `traverse ... as $related` fires, each new row's Vars are\n" +
	"CLONED from its pre-traverse row — so $pat, bound before the\n" +
	"traverse, lands on every post-traverse row automatically.\n" +
	"\n" +
	"This means the canonical cross-ref recipe shape is one pipeline:\n" +
	"\n" +
	"    select page where page.name !~ /^(TOC|Preface|...)/\n" +
	"    emit pattern { type := \"pattern\", name := page.name } as $pat\n" +
	"    traverse references out as $related\n" +
	"    filter page.name !~ /^(TOC|Preface|...)/\n" +
	"    lookup pattern by page.name as $rel\n" +
	"    link $pat --[relates-to]--> $rel\n" +
	"\n" +
	"- `select` establishes source rows.\n" +
	"- `emit` stamps $pat on each source row.\n" +
	"- `traverse` replaces rows with reference-targets; each inherits\n" +
	"  $pat via cloneRowVars.\n" +
	"- `filter` drops nav-page targets.\n" +
	"- `lookup` binds $rel to the target's already-emitted pattern.\n" +
	"- `link` connects $pat (source) to $rel (target).\n" +
	"\n" +
	"Do NOT repeat `select` in the middle of the pipeline. A second\n" +
	"`select` resets the rowset — $pat bindings are dropped.\n" +
	"\n" +
	"## Options that reach the recipe\n" +
	"\n" +
	"The `collect` tool passes through:\n" +
	"- `recipe: \"<name>\"` — the recipe node to execute (required when\n" +
	"  `transformer:\"recipe\"`).\n" +
	"- `dry_run: true` — populate Result.Nodes/Edges/Lineage with what\n" +
	"  would be emitted, skip all DB writes. Stats still advance.\n" +
	"- `force: true` — before evaluating, delete every target-graph node\n" +
	"  whose `translated-from` edge Evidence names the current source\n" +
	"  slug. Cleans previous emissions so the re-run produces a fresh\n" +
	"  state. Stats.ForceDeleted shows the count removed.\n" +
	"\n" +
	"## Stats reported after a run\n" +
	"\n" +
	"    nodes_emitted      — target-graph nodes written (or would be in DryRun)\n" +
	"    skipped_chunks     — emit-skipped rows (empty name + empty identity)\n" +
	"    lookups_resolved   — lookup rules that found their same-run target\n" +
	"    lookup_misses      — lookup rules whose target was not emitted this run\n" +
	"    link_misses        — link rules skipped due to empty or missing endpoint\n" +
	"    force_deleted      — nodes removed by Force cleanup\n" +
	"    elapsed_ms         — wall-clock time of the run\n" +
	"\n" +
	"A nonzero `lookup_misses` or high `link_misses` usually indicates a\n" +
	"recipe bug (wrong identity expression, wrong rule ordering). Check\n" +
	"the recipe against a sample source node before accepting a run.\n" +
	"\n" +
	"## Common patterns\n" +
	"\n" +
	"### Flat emit (no cross-refs)\n" +
	"\n" +
	"    select section where section.heading ~= /.+/\n" +
	"    emit idiom { type := \"idiom\", name := section.heading } as $id\n" +
	"\n" +
	"### Hierarchical emit with parent link\n" +
	"\n" +
	"    select page\n" +
	"    emit document { type := \"document\", name := page.name } as $doc\n" +
	"    traverse contains out\n" +
	"    filter page.type ~= /^section$/\n" +
	"    emit section { type := \"section\", name := page.name } as $sec\n" +
	"    link $sec --[contained-by]--> $doc\n" +
	"\n" +
	"### Cross-reference via lookup (see canonical shape above)\n" +
	"\n" +
	"Use `lookup` (not `emit`) when the later rule only needs a reference\n" +
	"to an earlier emit's ID. Cuts 10-100× the per-row work for recipes\n" +
	"that walk many edges per source row.\n" +
	"\n" +
	"## Pitfalls\n" +
	"\n" +
	"- **Two separate selects lose earlier bindings.** The rowset is\n" +
	"  rebuilt each select; Vars on the old rows are discarded. Use one\n" +
	"  pipeline if you need cross-emit references.\n" +
	"- **`name := \"\"` on some rows produces skipped emits.** Intended —\n" +
	"  avoids hex-ID target nodes from sections/pages without headings.\n" +
	"  Add an explicit `where name ~= /.+/` filter to be louder about it.\n" +
	"- **Regex is partial-match, not full-match.** `name ~= /Pattern/`\n" +
	"  matches anywhere in the string. Use anchors (`/^Pattern$/`) for\n" +
	"  exact matches.\n" +
	"- **Navigation pages emit as patterns.** Filter by URL or title at\n" +
	"  the `select where` clause; every raw web graph has sidebar nav\n" +
	"  chrome that looks identical to real pages at the graph level.\n" +
	"- **`link $x --[type]--> $y` on unbound $x dedups silently.** With\n" +
	"  link-endpoint verification this now reports Stats.LinkMisses. If\n" +
	"  the count is surprisingly high, one of your var bindings isn't\n" +
	"  landing on the rowset the link is iterating over.\n" +
	"- **Force=true scope is per-sourceSlug.** Running Force on\n" +
	"  sourceSlug=A doesn't touch emissions from sourceSlug=B, even when\n" +
	"  they target the same practice graph.\n" +
	"\n" +
	"## How a recipe runs (client-side dispatch)\n" +
	"\n" +
	"A recipe runs entirely CLIENT-SIDE through a single function,\n" +
	"`recipe.RunRecipe`. `collect type=web|pdf transformer=recipe`\n" +
	"dispatches to it directly — there is no transformer interface, no\n" +
	"registry, and no server round-trip. RunRecipe: loads the named recipe\n" +
	"from the GraphTransformers/recipes bucket over the wire, parses the\n" +
	"DSL body (AST cached in-process), materializes the source graph once\n" +
	"into an in-memory view (two reads — all nodes, then all edges — never\n" +
	"a per-row query), interprets the rules into an in-memory result, and\n" +
	"ships the projected practice-graph nodes + lineage edges back through\n" +
	"the collector Sink. StableID / translated-from lineage / Force cleanup\n" +
	"are owned by the recipe package itself — recipes never see those\n" +
	"mechanics.\n" +
	"\n" +
	"The collect `type` (web vs pdf) is validated against the recipe's\n" +
	"`source_graph_type` metadata: a mismatch is rejected before any read.\n" +
	"`dry_run:true` computes and returns the projection's stats without\n" +
	"writing; `force:true` deletes prior emissions from the same source\n" +
	"slug before re-emitting.\n" +
	""
