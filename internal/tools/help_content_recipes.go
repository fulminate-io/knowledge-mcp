// SPDX-License-Identifier: Apache-2.0

package tools

const helpRecipes = "# Recipe DSL\n" +
	"\n" +
	"The recipe DSL EXTRACTS structured rows out of a source graph via a\n" +
	"declarative pipeline of rules. Zero LLM at runtime — the interpreter\n" +
	"is pure Go. A recipe is an ephemeral body an agent writes inline for\n" +
	"one extraction and discards; nothing is stored, and a run writes\n" +
	"nothing. Invoked via the `collect` tool.\n" +
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
	"A recipe body is EPHEMERAL. You draft one for a single extraction,\n" +
	"run it inline, read the rows and discard it. Nothing is stored, and\n" +
	"there is no name to look a body up by.\n" +
	"\n" +
	"1. **Inspect the source graph.** Use `query(graph:\"web\", name:\"<slug>\", mode:\"stats\")`\n" +
	"   to see node types, then `query`/`traverse` on sample nodes to\n" +
	"   understand the structure (what edges exist, what metadata keys\n" +
	"   are populated, what heading shapes appear). Every where-tree leaf\n" +
	"   is censused against that vocabulary before the run starts, so a\n" +
	"   key the graph does not stamp is REFUSED naming it rather than\n" +
	"   read as empty.\n" +
	"2. **Draft the body** as a multi-line string following the grammar\n" +
	"   below, or copy one of the worked reading-loop bodies further down\n" +
	"   and adapt it.\n" +
	"3. **Run it inline**, with `recipe_body` carrying the text and\n" +
	"   `extract` set to true:\n" +
	"\n" +
	"       collect({\n" +
	"         type: \"web\", id: \"<source-slug>\",\n" +
	"         transformer: \"recipe\", extract: true,\n" +
	"         recipe_body: \"<DSL body>\",\n" +
	"         max_rows: 50\n" +
	"       })\n" +
	"\n" +
	"   No seed_urls + transformer=\"recipe\" → the crawl is skipped and\n" +
	"   the body runs against the already-cached raw graph. Iterate\n" +
	"   without paying fetch cost. `force` is refused on a recipe run:\n" +
	"   an extract writes nothing, so there is nothing to bypass.\n" +
	"4. **Read the rows, edit the body, run it again.** The header\n" +
	"   reports rows returned over rows matched, and any truncation\n" +
	"   prints a line beginning `TRUNCATED by` naming the cap that fired\n" +
	"   and the offset to resume from.\n" +
	"5. **Drop the raw graph** once the reading is done:\n" +
	"   `manage(operation:\"drop_graph\", graph:\"web\", name:\"<slug>\")`,\n" +
	"   with `dry_run: true` first for a preview. Raw graphs are\n" +
	"   scratch state; the rows you took are the deliverable.\n" +
	"\n" +
	helpRecipesGrammar +
	helpRecipesReading +
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
	"    select page where {\"not\": {\"matches\": {\"of\": \"page.name\",\n" +
	"                                           \"regex\": \"^(TOC|Preface)\"}}}\n" +
	"    emit pattern { type := \"pattern\", name := page.name } as $pat\n" +
	"    traverse references out as $related\n" +
	"    filter {\"not\": {\"matches\": {\"of\": \"page.name\",\n" +
	"                                \"regex\": \"^(TOC|Preface)\"}}}\n" +
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
	"- `recipe_body: \"<DSL body>\"` — the body to execute (required when\n" +
	"  `transformer:\"recipe\"`).\n" +
	"- `extract: true` — required. A run returns rows and writes nothing,\n" +
	"  so there is no other mode for it to be in.\n" +
	"- `max_rows`, `max_bytes`, `offset` — the output ceilings and the\n" +
	"  page cursor, described under \"Extract mode\" below.\n" +
	"\n" +
	"THREE PARAMS ARE REFUSED BY NAME rather than accepted and dropped:\n" +
	"`recipe` (naming a saved recipe, which no longer exists), `dry_run`\n" +
	"(there is no write to skip) and `force` (there is nothing to\n" +
	"overwrite). Each refusal names the param and says why.\n" +
	"\n" +
	"## Stats reported after a run\n" +
	"\n" +
	"    nodes_emitted      — rows the emit rules produced\n" +
	"    skipped_chunks     — emit-skipped rows (empty name + empty identity)\n" +
	"    lookups_resolved   — lookup rules that found their same-run target\n" +
	"    lookup_misses      — lookup rules whose target was not emitted this run\n" +
	"    link_misses        — link rules skipped due to empty or missing endpoint\n" +
	"    elapsed_ms         — wall-clock time of the run\n" +
	"\n" +
	"THE EXTRACT PATH DISCLOSES THE SAME COUNTERS, in its header line, so\n" +
	"an extract that MATCHED NOTHING is distinguishable from one whose\n" +
	"every row was SKIPPED for an empty identity. Both used to render\n" +
	"`rows=0/0 bytes=0` and nothing else.\n" +
	"\n" +
	"A nonzero `lookup_misses` or high `link_misses` usually indicates a\n" +
	"recipe bug (wrong identity expression, wrong rule ordering). Check\n" +
	"the recipe against a sample source node before accepting a run.\n" +
	"\n" +
	"## Common patterns\n" +
	"\n" +
	"### Flat emit (no cross-refs)\n" +
	"\n" +
	"    select section where {\"exists\": {\"of\": \"section.name\"}}\n" +
	"    emit idiom { type := \"idiom\", name := section.name } as $id\n" +
	"\n" +
	"### Hierarchical emit with a parent-child link\n" +
	"\n" +
	"    select page\n" +
	"    emit document { type := \"document\", name := page.name } as $doc\n" +
	"    traverse CONTAINS out\n" +
	"    filter {\"kind\": {\"of\": \"node\", \"is\": \"section\"}}\n" +
	"    emit section { type := \"section\", name := page.name } as $sec\n" +
	"    link $doc --[contains]--> $sec\n" +
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
	"  Add a `{\"exists\": {\"of\": \"section.symbol_name\"}}` where-tree to be\n" +
	"  louder about it, and read `skipped=` in the extract header.\n" +
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
	"- **Re-running an edited body is free.** A run writes nothing, so\n" +
	"  there is no prior state to collide with — edit the body and run it\n" +
	"  again as many times as you like.\n" +
	"\n" +
	"## How a recipe runs (client-side dispatch)\n" +
	"\n" +
	"A recipe runs entirely CLIENT-SIDE through a single function,\n" +
	"`recipe.RunRecipe`. `collect type=web|pdf transformer=recipe`\n" +
	"dispatches to it directly — there is no transformer interface, no\n" +
	"registry, and no server round-trip. RunRecipe: parses the supplied\n" +
	"DSL body (AST cached in-process on a hash of the body text),\n" +
	"materializes the source graph once into an in-memory view (two reads\n" +
	"— all nodes, then all edges — never a per-row query), interprets the\n" +
	"rules into an in-memory result, and returns the extracted rows.\n" +
	"NOTHING IS SHIPPED: the run reaches no target graph and no sink.\n" +
	"StableID and translated-from lineage are owned by the recipe package\n" +
	"itself — recipes never see those mechanics.\n" +
	"\n" +
	"The collect `type` (web vs pdf) is the source graph type the body is\n" +
	"read against; an inline body carries none of its own, so the param is\n" +
	"required and an empty one is refused rather than guessed.\n" +
	"\n" +
	"A recipe run REPLAYS THE ALREADY-COLLECTED raw graph. The recipe\n" +
	"dispatch returns before any crawl options are applied, so nothing is\n" +
	"fetched and no page is re-requested: iterating a recipe against a\n" +
	"document you already collected is zero network cost. Collect the\n" +
	"document once, then iterate the extraction as many times as you like.\n" +
	"\n" +
	"## Extract mode — see what a recipe pulls out, without writing\n" +
	"\n" +
	"`extract: true` runs the recipe and returns the emitted ROWS instead\n" +
	"of writing anything. Nothing reaches a target graph, so it is safe to\n" +
	"iterate on a recipe until it says what you meant.\n" +
	"\n" +
	"`recipe_body:` carries the body, and `extract: true` is required —\n" +
	"a run returns rows and writes nothing, so extract is the only mode\n" +
	"there is.\n" +
	"\n" +
	"    collect({ type: \"web\", id: \"hohpe-eip\", transformer: \"recipe\",\n" +
	"              extract: true, recipe_body: \"select section\\nemit ...\" })\n" +
	"\n" +
	"A body is INLINE AND EPHEMERAL and there is nothing to freeze: write\n" +
	"one for a single extraction, edit it until it says what you meant, and\n" +
	"discard it — the worked bodies under \"The reading loop\" above are\n" +
	"there to be copied and edited.\n" +
	"\n" +
	"### Output shape and the two ceilings\n" +
	"\n" +
	"    extract: recipe=inline source=<type>/<slug> rows=<n>/<m> bytes=<b>\n" +
	"    --- row 0 type=<T> src=<source node>\n" +
	"    <field>: <value>\n" +
	"\n" +
	"Fields render in sorted key order, so two runs over one document are\n" +
	"comparable. `rows=` reports rows RETURNED over rows MATCHED, so a\n" +
	"bounded extract is never mistaken for a short one.\n" +
	"\n" +
	"### Paging a document with `offset`\n" +
	"\n" +
	"`offset` is the ZERO-BASED index of the first MATCHED row returned,\n" +
	"so a document larger than one response is read a page at a time:\n" +
	"\n" +
	"    collect({ type: \"pdf\", id: \"<slug>\", transformer: \"recipe\",\n" +
	"              extract: true, recipe_body: \"...\",\n" +
	"              offset: 100, max_rows: 50 })\n" +
	"\n" +
	"EVERY MATCHED ROW IS STILL COUNTED, whether or not it is returned, so\n" +
	"the header's `rows=50/208` names the whole population behind the page\n" +
	"rather than the page. The truncation line names the offset to resume\n" +
	"from — `Next offset=150.` — counted from the rows the RENDERER\n" +
	"emitted, so resuming never skips a row the byte cap cut. A page that\n" +
	"starts past the end says so, on a line beginning `OFFSET PAST END:`,\n" +
	"naming the offset, the matched population and where the last real\n" +
	"page starts — an overshot cursor is never left looking like a recipe\n" +
	"that matched nothing. A NEGATIVE offset is refused rather than\n" +
	"clamped.\n" +
	"\n" +
	"### The two ceilings\n" +
	"\n" +
	"`max_rows` caps rows (default 200) and `max_bytes` caps the rendered\n" +
	"response (default 65536). NEITHER CAP IS SILENT: when one fires the\n" +
	"output carries a line beginning `TRUNCATED by`, naming the cap, its\n" +
	"value, the returned and matched counts, and what to do about it. The\n" +
	"byte cap cuts at ROW BOUNDARIES only, so a value is never clipped\n" +
	"mid-string; when even the first row does not fit, the disclosure says\n" +
	"so and names that row's size so you can pick a cap that works.\n" +
	"\n" +
	"## Rowsets come back in document reading order\n" +
	"\n" +
	"A `select` or `traverse` rowset is in DOCUMENT READING ORDER, and a\n" +
	"node for which no position is determinable follows EVERY ordered\n" +
	"node, by node id. So a `select section` over a collected book returns\n" +
	"its sections front to back rather than in node-id order, and pages\n" +
	"never go backwards down an extract. `walk` orders the same way.\n" +
	"\n" +
	"The order comes from the child's `position` — read from the NODE\n" +
	"first and from the containment EDGE as a fallback — walked once per\n" +
	"run into a reading-order index. It is a whole-document order, not a\n" +
	"per-parent one: two paragraphs sitting at index 1 under different\n" +
	"sections are strictly ordered against each other.\n" +
	"\n" +
	"A SOURCE GRAPH IN WHICH ONE NODE IS CLAIMED BY TWO POSITIONED\n" +
	"CONTAINMENT EDGES IS REFUSED. Such a node has two document positions\n" +
	"and no way to choose between them, so the run stops and names the\n" +
	"node and both parents rather than picking one — the repair is to\n" +
	"remove the extra edge or re-collect the source graph.\n" +
	"\n" +
	"## Bare field-path heads are checked when the recipe is parsed\n" +
	"\n" +
	"A bare head — the part before the first dot, with no `$` — must name\n" +
	"something the current row can be. Legal heads are the type named by\n" +
	"the most recent `select`, plus any `traverse ... as` alias declared\n" +
	"since it, plus group and node. Each `select` RESETS the set, so an\n" +
	"alias from before a second select stops being legal at it.\n" +
	"\n" +
	"`node` is the universal alias for the current row and names no type\n" +
	"at all, which is why no select ever removes it. `group` is the\n" +
	"group-by rule's namespace, as in `group.keys`, and `walk` is the\n" +
	"walk rule's, as in `walk.depth` — a pseudo-variable namespace is a\n" +
	"value the rule stamps on the row, not a key any node carries, so it\n" +
	"is legal only after the rule that stamps it.\n" +
	"\n" +
	"A head outside that set is a PARSE ERROR naming the head and listing\n" +
	"the legal set, so a recipe can be repaired from the message alone.\n" +
	"There are two repairs: rename the head to the selected type, or — if\n" +
	"the access happens after a traverse — to that traverse alias.\n" +
	"\n" +
	"## What the DSL refuses, and when\n" +
	"\n" +
	"Nothing in this list degrades to an empty result. Every one of them is\n" +
	"a message naming the offending value, a near-miss where one exists,\n" +
	"and the vocabulary it was checked against.\n" +
	"\n" +
	"Refused at parse time, decidable from the recipe text alone:\n" +
	"\n" +
	"- a bare field-path head outside the legal set, as above;\n" +
	"- the retired string-expression predicate after `where` or `filter`;\n" +
	"- an emit carrying neither `name` nor `identity`;\n" +
	"- an unknown key anywhere in a where-tree, at the leaf level as well\n" +
	"  as the top.\n" +
	"\n" +
	"Refused before the walk, needing the loaded source graph or the whole\n" +
	"rule list:\n" +
	"\n" +
	"- a select type the source graph does not carry;\n" +
	"- an edge type it does not carry, ON ANY OF ITS FOUR CARRIERS — the\n" +
	"  `traverse` rule, the `walk` rule, a where-tree `ancestor` or `descendant`\n" +
	"  leaf, and a literal edge-type argument to one of the six edge-taking\n" +
	"  builtins.\n" +
	"  The comparison is EXACT, including case;\n" +
	"- a non-literal edge-type argument, which could only be checked per\n" +
	"  row;\n" +
	"- a metadata key the graph does not stamp, on any field path — a\n" +
	"  where-tree `of` value or an expression alike;\n" +
	"- an unknown builtin, or one called with the wrong argument count;\n" +
	"- a literal regex that does not compile;\n" +
	"- a `compare` leaf whose operator is not one of the admitted set, or whose\n" +
	"  literal operand is not a finite number.\n" +
	"\n" +
	"THESE ARE REPORTED TOGETHER, not one per run. The validator collects\n" +
	"every violation across the whole recipe and reports them in one error\n" +
	"ordered by position, so an author repairing a recipe sees every site\n" +
	"at once instead of one site per re-run.\n" +
	"\n" +
	"## Where the body text lives: web-collect vs pdf-collect\n" +
	"\n" +
	"Both raw collectors carry a node's text in the same field, and\n" +
	"`node.source` tells you which collector you are reading:\n" +
	"\n" +
	"- web-collect — paragraphs, list items, blockquotes and code blocks\n" +
	"  carry their text in `content`; a page carries a FLATTENED body in\n" +
	"  `description` that deliberately EXCLUDES code blocks, tables, images\n" +
	"  and quotes; a section carries its heading in `content` as well as\n" +
	"  in SymbolName.\n" +
	"- pdf-collect — leaf chunks carry their text in `content`; the document\n" +
	"  root carries a metadata summary in `description`; a section carries\n" +
	"  its heading in `content` as well as in SymbolName.\n" +
	"\n" +
	"`body` is a virtual field that resolves this: it returns `content`\n" +
	"when set, otherwise `description`. One field name therefore reaches\n" +
	"the text on either source, and every field-taking builtin gets it too.\n" +
	"\n" +
	"BOTH SOURCES AGREE ABOUT SECTIONS: a section carries its heading in\n" +
	"SymbolName and ALSO in `content`, so `section.body` returns that\n" +
	"heading on either source rather than empty. Section BODIES on both\n" +
	"sources come from `subtree_concat` over the section's children, never\n" +
	"from `body` on the section itself.\n" +
	"\n" +
	"## Canned section renders\n" +
	"\n" +
	"A heading path plus the section subtree, which together are what turns\n" +
	"a document graph into readable patterns. THE FIELD DIFFERENCE IS\n" +
	"ABSORBED BY `body`; THE EDGE TYPE IS NOT. A pdf raw graph carries\n" +
	"CONTAINS only, while a web raw graph carries BOTH `CONTAINS` (content\n" +
	"containment) and `contains` (the github root) at the same time, and\n" +
	"the comparison is exact. Check your source graph's edge vocabulary and\n" +
	"write the casing it actually has — the run is refused if you do not,\n" +
	"which is how you find out. The example below is written for a pdf\n" +
	"source:\n" +
	"\n" +
	"    select section\n" +
	"    filter {\"matches\": {\"of\": \"section.symbol_name\",\n" +
	"                        \"regex\": \"^[A-Z]\"}}\n" +
	"    emit pattern {\n" +
	"        name := section.symbol_name\n" +
	"        path := heading_path(\"CONTAINS\", \"symbol_name\", \" > \")\n" +
	"        body := subtree_concat(\"CONTAINS\", \"body\", \"\\n\\n\", \"4\")\n" +
	"    }\n" +
	"\n" +
	"The section subtree render walks EVERY child node type, so it includes\n" +
	"the code blocks, tables and quotes a page's own flattened description\n" +
	"leaves out. Use max_depth \"1\" instead for just the ordered immediate\n" +
	"children.\n" +
	""
