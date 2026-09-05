// SPDX-License-Identifier: Apache-2.0

package tools

// helpRecipesGrammar is the grammar half of help("recipes"): the productions,
// the per-rule semantics and the expression sub-language.
//
// It lives in its own file because the topic body outgrew the repo's 500-line
// per-file ceiling once the where-tree grammar was added. helpRecipes composes
// it back in, so the rendered topic is one document and a reader never sees the
// seam.
const helpRecipesGrammar = "## Grammar (EBNF-ish)\n" +
	"\n" +
	"    recipe       = { rule NEWLINE } .\n" +
	"    rule         = select | traverse | walk | filter | bind | group_by\n" +
	"                 | emit | lookup | link | source_ref .\n" +
	"\n" +
	"    select       = \"select\" IDENT [ \"where\" WHERE_TREE ] .\n" +
	"    traverse     = \"traverse\" IDENT direction [ \"as\" VAR ] .\n" +
	"    walk         = \"walk\" IDENT [ \"as\" VAR ] .\n" +
	"    filter       = \"filter\" WHERE_TREE .\n" +
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
	"    WHERE_TREE   = a JSON object, brace-delimited, spanning as many\n" +
	"                   lines as it needs. Composers: all, any, not.\n" +
	"                   Leaves: kind{of,is}, matches{of,regex},\n" +
	"                   equals{of,value}, exists{of}, compare{of,op,value},\n" +
	"                   ancestor{edge,where}, descendant{edge,where}.\n" +
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
	"## The where-tree\n" +
	"\n" +
	"`select ... where` and `filter` both take a WHERE-TREE: a JSON boolean\n" +
	"tree, in the same shape the `ast` tool's where-tree uses. String\n" +
	"expressions are NOT accepted there; they still work everywhere else,\n" +
	"in emit field values and in `bind`.\n" +
	"\n" +
	"    filter {\"all\": [\n" +
	"        {\"kind\": {\"of\": \"node\", \"is\": [\"section\", \"block\"]}},\n" +
	"        {\"not\": {\"matches\": {\"of\": \"node.symbol_name\",\n" +
	"                              \"regex\": \"^Part [IVX]+\"}}}\n" +
	"    ]}\n" +
	"\n" +
	"- Composers `all` / `any` / `not` nest to any depth. Several set on\n" +
	"  one object are AND-ed.\n" +
	"- `of` is a field path in the SAME dotted vocabulary the expression\n" +
	"  language uses — `section.symbol_name`, `node.metadata.page_first`,\n" +
	"  `$var`.\n" +
	"- A `kind` leaf's `of` names a ROW (the select type, a traverse alias,\n" +
	"  or `node`); every other leaf's `of` names a FIELD.\n" +
	"- `kind`'s `is` takes one type or a list; both spellings mean the same.\n" +
	"- `matches` compiles Go regexp syntax. Every literal regex is compiled\n" +
	"  ONCE per run, before the first row, not per row.\n" +
	"- `exists` is true when the path resolves non-empty. The DSL has no\n" +
	"  null, so `exists` and an `equals` against `\"\"` are exact\n" +
	"  complements.\n" +
	"- `ancestor` walks INCOMING edges of its `edge` type transitively and\n" +
	"  `descendant` walks OUTGOING ones; each is true when any reached node\n" +
	"  satisfies its nested `where`. Both walks are depth-bounded.\n" +
	"- INSIDE an `ancestor` or `descendant` sub-tree the only legal bare\n" +
	"  head is `node`, because the row being tested is a walked neighbor\n" +
	"  rather than the selected row. A `$var` bound before the walk still\n" +
	"  resolves there.\n" +
	"- An UNKNOWN KEY anywhere in the tree is refused, naming the key and\n" +
	"  the accepted set — at the leaf level as well as the top, so a typo\n" +
	"  inside `kind` cannot vanish.\n" +
	"- `compare` tests a field NUMERICALLY against a literal:\n" +
	"  `{\"compare\": {\"of\": \"node.font_ratio_to_body\", \"op\": \"gte\",\n" +
	"  \"value\": \"1.15\"}}`.\n" +
	"- The operators are `eq`, `ne`, `lt`, `lte`, `gt`, `gte` — lower case,\n" +
	"  matched exactly. Any other spelling is refused before the walk,\n" +
	"  naming the admitted set.\n" +
	"- BOTH OPERANDS ARE NUMBERS. A non-numeric `value` is refused before\n" +
	"  the walk; a row holding non-numeric TEXT where the compare expects a\n" +
	"  magnitude is an error naming the node and the value. Nothing is\n" +
	"  coerced or trimmed. For a string comparison use `equals`, which\n" +
	"  answers differently on purpose: a stored `\"1\"` equals the number\n" +
	"  `\"1.0\"` under `compare` and does not under `equals`.\n" +
	"- A row whose compared field is ABSENT does not match in EITHER\n" +
	"  direction, and it is not an error — the recipe named something real\n" +
	"  and this row does not have it. A key the SOURCE GRAPH never carries\n" +
	"  is a different thing and is refused before the walk. Compose\n" +
	"  `exists` beside the compare to tell an unstamped node from an\n" +
	"  unmatched one.\n" +
	"- After a `traverse`, `edge` names the edge the row was traversed\n" +
	"  ALONG. `edge.type` is its type, `edge.evidence.<key>` reads its\n" +
	"  Evidence, and `edge.<key>` is sugar for the same key — so the\n" +
	"  position stamped on a contains edge is `edge.position`.\n" +
	"- INSIDE an `ancestor` or `descendant` sub-tree, `edge` is the WALKED\n" +
	"  edge rather than the outer traverse's.\n" +
	"- `edge` under a bare `select` is a PARSE error: a selected row walked\n" +
	"  no edge. An attribute no edge carries is refused before the walk,\n" +
	"  naming the Evidence keys the graph does carry and the edge's own\n" +
	"  fields.\n" +
	"\n" +
	"A worked body using both, over a document's ordered children:\n" +
	"\n" +
	"    select document\n" +
	"    traverse CONTAINS out as $child\n" +
	"    filter {\"all\": [\n" +
	"        {\"compare\": {\"of\": \"node.font_ratio_to_body\", \"op\": \"gte\", \"value\": \"1.15\"}},\n" +
	"        {\"compare\": {\"of\": \"edge.position\", \"op\": \"lte\", \"value\": \"3\"}}\n" +
	"    ]}\n" +
	"    emit heading {\n" +
	"        name := node.symbol_name\n" +
	"        position := edge.position\n" +
	"    }\n" +
	"\n" +
	"## Rule semantics\n" +
	"\n" +
	"### select\n" +
	"`select page where {\"matches\": {\"of\": \"page.url\", \"regex\": \"hohpe\"}}` —\n" +
	"load all nodes of the given type from the source graph, optionally\n" +
	"filtered by a where-tree. Replaces the working rowset. Each row is one\n" +
	"source node with its hydrated Node (for field access) and a fresh Vars\n" +
	"map.\n" +
	"\n" +
	"### traverse\n" +
	"`traverse references out as $related` — walk edges from each current\n" +
	"row in the given direction (in|out|both), filtered by edge type.\n" +
	"Replaces the rowset: one row per traversed target, each carrying the\n" +
	"target's hydrated Node. `as $var` stamps the target's ID on the new\n" +
	"row's Vars. CRITICAL: Vars from the pre-traverse row ARE CLONED into\n" +
	"every post-traverse row, so any prior emit/lookup binding survives.\n" +
	"THE EDGE TYPE IS MATCHED EXACTLY, including case, and one the source\n" +
	"graph does not carry is refused before the run rather than traversed\n" +
	"to nothing.\n" +
	"\n" +
	"### walk\n" +
	"`walk CONTAINS as $child` — replace the rowset with each row's whole\n" +
	"SUBTREE along that edge type, in document reading order. A traverse\n" +
	"expands ONE level and returns it as a block, so an outline needs one\n" +
	"rule per level and comes back level by level; a walk returns every\n" +
	"level interleaved as the document reads, which is one rule and one\n" +
	"extract for a whole nested outline.\n" +
	"\n" +
	"The starting row is NOT emitted: depth 1 is a direct child. Two\n" +
	"row-scoped pseudo-variables are stamped on every walked row and read\n" +
	"as bare dotted heads exactly as `group.keys` is:\n" +
	"\n" +
	"- `walk.depth` — 1 for a direct child, 2 for a grandchild, and so on.\n" +
	"- `walk.position` — the child's own order key under its parent, empty\n" +
	"  when none is determinable.\n" +
	"\n" +
	"There is NO depth clause. Narrowing by level is a filter on\n" +
	"`walk.depth`. THE EDGE TYPE IS MATCHED EXACTLY, including case, and\n" +
	"is CENSUSED like traverse's: a type the source graph does not carry\n" +
	"is refused before the walk, not answered with zero rows.\n" +
	"\n" +
	"    select document\n" +
	"    walk CONTAINS\n" +
	"    emit outline {\n" +
	"        name := node.symbol_name\n" +
	"        level := walk.depth\n" +
	"        page := node.page_first\n" +
	"    }\n" +
	"\n" +
	"### filter\n" +
	"`filter {\"matches\": {\"of\": \"section.name\", \"regex\": \"Problem|Solution\"}}`\n" +
	"— drop rows the where-tree does not match. A filter always carries a\n" +
	"tree; there is no bare `filter`.\n" +
	"\n" +
	"### bind\n" +
	"`bind $slug := lower(concat(page.name, \"-ext\"))` — store the\n" +
	"evaluated value under $slug. Writes to both every row's Vars and\n" +
	"env.Vars so downstream rules can reference it via $slug.\n" +
	"\n" +
	"### group_by\n" +
	"`group_by paragraph.metadata.page_first` — collapse rows with equal\n" +
	"key into one row. The row carries a synthetic `group.keys` pseudo-\n" +
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
	"- AN EMIT MUST SET `name` OR `identity`. One carrying neither is a\n" +
	"  PARSE ERROR — it has no way to identify the node it writes, so every\n" +
	"  row would be silently skipped.\n" +
	"- The `identity` field (if present) feeds StableID; otherwise `name`\n" +
	"  is used. If the chosen expression EVALUATES to empty on a row, that\n" +
	"  row is SKIPPED (not emitted with a hex-ID fallback) —\n" +
	"  Stats.SkippedChunks increments, and extract mode discloses the count.\n" +
	"- StableID is computed from (target graph, sourceSlug, NodeType,\n" +
	"  identity). Same identity + same sourceSlug → same target ID across\n" +
	"  runs (idempotent). Same identity + different sourceSlug → distinct\n" +
	"  IDs (source-scoped emissions don't collide).\n" +
	"- A `translated-from` edge is stamped from the emitted node back to\n" +
	"  the source row's NodeID (or `source_ref` override), carrying the\n" +
	"  sourceSlug in Evidence so lineage stays attributable per source.\n" +
	"- `as $var` binds the emitted node's ID into the current row's Vars\n" +
	"  AND the env-wide EmitMap. The binding survives into later rules\n" +
	"  and through traverse (via cloneRowVars).\n" +
	"\n" +
	"### lookup\n" +
	"`lookup pattern by page.name as $rel` — compute the StableID that a\n" +
	"prior emit WOULD have produced for the same (target, sourceSlug,\n" +
	"NodeType, identity) tuple, verify that node was EMITTED EARLIER IN\n" +
	"THIS RUN (an in-run emitted-set check, never a target-graph read),\n" +
	"and bind the resulting ID to $rel.\n" +
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
	"- Endpoint not emitted earlier in THIS run → silent skip,\n" +
	"  Stats.LinkMisses++. The check is the same in-run emitted set\n" +
	"  `lookup` consults; the interpreter never reads the target graph.\n" +
	"- No unique-edge enforcement at the DSL level — the underlying\n" +
	"  target DB dedups equal (from, to, type) edges.\n" +
	"- The relationship names a TARGET-graph edge type, so it is not\n" +
	"  checked against the source graph's edge vocabulary.\n" +
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
	"Expressions are what emit field values, `bind`, `group_by`, `lookup`\n" +
	"identities, `link` endpoints and `source_ref` are written in. The\n" +
	"where-tree above is a separate grammar and takes none of them.\n" +
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
	"  well-known field). A key the SOURCE GRAPH does not carry is refused\n" +
	"  before the run, naming the keys it does carry.\n" +
	"- `page.url` — sugar for `page.metadata.url` (same fall-through).\n" +
	"- `$var` — the bound value, which for an emit binding is the emitted\n" +
	"  node's ID.\n" +
	"- `edge.type` — the type of the edge the row was traversed along.\n" +
	"  Legal after a `traverse` or a `walk`, and inside an `ancestor` /\n" +
	"  `descendant` sub-tree where it names the walked edge.\n" +
	"- `edge.position` — an Evidence key on that edge; `edge.<key>` is\n" +
	"  sugar for `edge.evidence.<key>`, the same fall-through `page.url`\n" +
	"  gets. `edge.type` is the edge's OWN type, and every other name is an\n" +
	"  Evidence key — so on a web graph `edge.rel` reads the collector's\n" +
	"  `rel` key (`internal` or `external`).\n" +
	"- `edge.evidence.<key>` — the explicit spelling of the same read.\n" +
	"- `$var.anything` — returns the SAME bound ID regardless of the\n" +
	"  trailing path: bindings hold scalars, not node handles, so a dotted\n" +
	"  var path does NOT read a field off the bound node. To read a field\n" +
	"  after a traverse, use the bare traverse alias — after\n" +
	"  `traverse CONTAINS out as $file`, write `file.symbol_name`.\n" +
	"\n" +
	"### Operators\n" +
	"- `lhs ~= /pattern/` — regex match. Returns the matched substring\n" +
	"  (truthy) or empty (falsy).\n" +
	"- `lhs !~ /pattern/` — regex NOT-match. Inverts: returns a sentinel\n" +
	"  truthy value when the LHS does NOT match, empty when it matches.\n" +
	"  These are EXPRESSION operators. To drop rows, write a where-tree on\n" +
	"  `where` / `filter` instead.\n" +
	"\n" +
	"### Built-in functions\n" +
	"\n" +
	"String:\n" +
	"- `concat(a, b, c, ...)` — string concatenation.\n" +
	"- `trim(s)` — strip leading/trailing whitespace.\n" +
	"- `lower(s)` / `upper(s)` — case conversion.\n" +
	"- `length(s)` — byte length, returned as a string.\n" +
	"- `slice(s, start, end)` — byte-offset substring, bounds clamped.\n" +
	"- `match_group(s, pattern, n)` — the nth capture group, 1-indexed.\n" +
	"\n" +
	"Graph:\n" +
	"- `has_edge(edge_type, direction)` — true (sentinel non-empty) if\n" +
	"  the current row's node has at least one matching edge. Direction\n" +
	"  is `in` | `out` | `both`.\n" +
	"- `children_concat(edge_type, field, sep)` — read a field off every\n" +
	"  neighbor across an outgoing edge, joined.\n" +
	"- `ancestors_concat(edge_type, field, sep)` — the same over incoming\n" +
	"  edges.\n" +
	"- `has_ancestor(edge_type, field, pattern)` — walk upward and test a\n" +
	"  field against a regex.\n" +
	"\n" +
	"EVERY EDGE TYPE ABOVE MUST BE A STRING LITERAL, and it is checked\n" +
	"against the source graph's edge types — exactly, including case —\n" +
	"before the run. A computed edge type is refused, because it could only\n" +
	"be checked per row, where a miss is silent.\n" +
	"\n" +
	"Boolean:\n" +
	"- `and(a, b)` / `or(a, b)` / `not(a)` — truthiness composition over\n" +
	"  the empty-is-false rule.\n" +
	"\n" +
	"Render — these two walk document SHAPE rather than reading one field:\n" +
	"- `heading_path(edge_type, field, sep)` — walk UPWARD and join each\n" +
	"  ancestor's field root-first. The row's own field is not included and\n" +
	"  empty values are skipped.\n" +
	"  SINGLE-PARENT ASSUMPTION: the edge type comes from you, and the walk\n" +
	"  follows the FIRST parent at each hop, so it is meaningful only on an\n" +
	"  edge type that forms a tree. The builtin cannot check that for you.\n" +
	"- `subtree_concat(edge_type, field, sep, max_depth)` — walk DOWNWARD,\n" +
	"  visiting each level's children in document order, and join their\n" +
	"  fields. max_depth \"1\" is the ordered immediate children; a larger\n" +
	"  value renders the whole subtree. A non-integer max_depth errors and\n" +
	"  names the offending value.\n" +
	"\n" +
	"No arithmetic and no ternaries. If a predicate needs more logic,\n" +
	"compose alternations in the regex (`/^A|^B|^C/`), nest the where-tree's\n" +
	"`all` / `any` / `not`, or split into multiple `filter` rules.\n" +
	"\n"
