// SPDX-License-Identifier: Apache-2.0

package tools

// helpRecipesReading is the reading-loop half of help("recipes"): how an LLM
// holding nothing but this topic reads a document it just collected, and five
// worked bodies it copies to do it.
//
// It lives in its own file for the same reason the grammar half does — the
// topic body outgrew the repo's 500-line per-file ceiling. helpRecipes composes
// it back in right after the grammar, so the rendered topic is one document.
//
// EVERY WORKED BODY IS SEPARATED FROM THE NEXT BY NON-INDENTED PROSE, and that
// is load-bearing rather than stylistic. extractRecipeBlocks treats a blank
// line inside an indented run as part of that run, so two bodies separated only
// by a blank line MERGE into one block: the merged block still parses, as a
// single recipe carrying both pipelines, and the only detector is the
// population count the fixture gate pins.
const helpRecipesReading = "## The reading loop\n" +
	"\n" +
	"To READ a document you have collected — an outline of it, then the\n" +
	"body of a section — takes FOUR calls and NO SAVED RECIPE. A body is\n" +
	"inline and ephemeral: write one for a single extraction, edit it\n" +
	"until it says what you meant, and discard it.\n" +
	"\n" +
	"1. `query(graph:\"pdf\"|\"web\", mode:\"modules\")` — list the graphs\n" +
	"   already collected, with their node counts. SKIPPABLE FOR A PDF you\n" +
	"   hold the file path for: the collect id accepts the absolute path\n" +
	"   the collect took as well as the graph's own slug, so a path you\n" +
	"   already have makes this call unnecessary.\n" +
	"2. `search(graph:\"pdf\"|\"web\", name:\"<slug>\", query:\"...\")` — find the\n" +
	"   passages worth reading. Each hit names its heading and its page, so\n" +
	"   the heading you feed to call 4 comes from here. The response footer\n" +
	"   reports the mode it ran in: a freshly collected raw graph carries no\n" +
	"   vectors until it is enrolled and embedded, so a just-collected\n" +
	"   document answers BM25-only and says so.\n" +
	"3. An OUTLINE extract — one `walk`, one extract, the whole nested\n" +
	"   structure in document reading order. Examples 1 and 2 below.\n" +
	"4. A SECTION-BODY extract — the prose under one heading, keyed either\n" +
	"   by the heading text or by the `ref` the outline returned. Example 3.\n" +
	"\n" +
	"Calls 3 and 4 are `collect(..., transformer:\"recipe\", extract:true,\n" +
	"recipe_body:\"<one of the bodies below>\")`. A recipe run REPLAYS the\n" +
	"already-collected graph and fetches nothing, so iterating a body costs\n" +
	"no network.\n" +
	"\n" +
	"### 1. A pdf's ordered nested outline\n" +
	"\n" +
	"`walk` returns every level interleaved as the document reads, so one\n" +
	"rule and one extract give a whole nested outline. `walk.depth` is the\n" +
	"nesting level and `walk.position` the child's own order key; scope the\n" +
	"depth to keep the outline readable, and the page range to skip front\n" +
	"matter.\n" +
	"\n" +
	"    select document\n" +
	"    walk CONTAINS\n" +
	"    filter {\"all\": [\n" +
	"        {\"kind\": {\"of\": \"node\", \"is\": \"section\"}},\n" +
	"        {\"compare\": {\"of\": \"walk.depth\", \"op\": \"lte\", \"value\": \"2\"}},\n" +
	"        {\"compare\": {\"of\": \"node.page_first\", \"op\": \"gte\", \"value\": \"10\"}}\n" +
	"    ]}\n" +
	"    emit outline {\n" +
	"        name := node.symbol_name\n" +
	"        level := walk.depth\n" +
	"        pos := walk.position\n" +
	"        page := node.page_first\n" +
	"        ref := node.id\n" +
	"    }\n" +
	"\n" +
	"`ref := node.id` is what makes call 4 cheap: the outline hands you the\n" +
	"source node id of every heading, so the section-body extract can key on\n" +
	"an exact id instead of guessing a regex. `compare` is numeric — the\n" +
	"operators are `eq`, `ne`, `lt`, `lte`, `gt`, `gte`, lower case — so a\n" +
	"page range needs no digit-alternation regex.\n" +
	"\n" +
	"### 2. A web crawl's ordered nested outline\n" +
	"\n" +
	"The same shape over a web graph, with the pages scoped at the select.\n" +
	"SCOPE THEM: an unscoped outline over a crawl that followed a site-wide\n" +
	"pattern is mostly translation roots and index pages, and they outnumber\n" +
	"the content. Swap the regex for the site you collected.\n" +
	"\n" +
	"    select page where {\"matches\": {\"of\": \"page.uri\",\n" +
	"                                   \"regex\": \"^https://example\\\\.com/\"}}\n" +
	"    walk CONTAINS\n" +
	"    filter {\"all\": [\n" +
	"        {\"kind\": {\"of\": \"node\", \"is\": \"section\"}},\n" +
	"        {\"compare\": {\"of\": \"walk.depth\", \"op\": \"lte\", \"value\": \"2\"}}\n" +
	"    ]}\n" +
	"    emit outline {\n" +
	"        name := node.symbol_name\n" +
	"        level := walk.depth\n" +
	"        pos := walk.position\n" +
	"        uri := node.uri\n" +
	"        ref := node.id\n" +
	"    }\n" +
	"\n" +
	"A web section's `uri` carries the page URL plus the heading's own\n" +
	"anchor when it has one, so the outline doubles as a link list.\n" +
	"\n" +
	"### 3. One section's body\n" +
	"\n" +
	"The second half of the loop: the prose under a single heading.\n" +
	"\n" +
	"    select section where {\"matches\": {\"of\": \"section.symbol_name\",\n" +
	"                                      \"regex\": \"^Event\"}}\n" +
	"    emit section_body {\n" +
	"        name := section.symbol_name\n" +
	"        path := heading_path(\"CONTAINS\", \"symbol_name\", \" > \")\n" +
	"        page_first := section.page_first\n" +
	"        page_last := section.page_last\n" +
	"        body := subtree_concat(\"CONTAINS\", \"body\", \"\\n\\n\", \"6\")\n" +
	"    }\n" +
	"\n" +
	"To key on the outline's `ref` instead of a heading regex, swap the\n" +
	"where-tree for `{\"equals\": {\"of\": \"section.id\", \"value\": \"<ref>\"}}` —\n" +
	"exact, and immune to two sections sharing a heading. `subtree_concat`\n" +
	"visits EVERY child type, so code blocks and tables arrive with the\n" +
	"prose rather than being dropped the way a page's flattened description\n" +
	"drops them. `page_first` and `page_last` are the HEADING BLOCK's own\n" +
	"page range, not the extent of the body beneath it. Do not reach for\n" +
	"`section.page_span`: it is stamped only on cross-page-MERGED body\n" +
	"records, headings are skipped on both sides of that merge, so a section\n" +
	"never carries it and a document with no merged blocks does not carry\n" +
	"the key at all — which refuses the run.\n" +
	"\n" +
	"### 4. Dropping a pdf's furniture\n" +
	"\n" +
	"A real book's outline carries table-of-contents dot-leaders, bare page\n" +
	"numerals and running headers. Filter them out rather than reading them.\n" +
	"\n" +
	"    select document\n" +
	"    walk CONTAINS\n" +
	"    filter {\"kind\": {\"of\": \"node\", \"is\": \"section\"}}\n" +
	"    filter {\"not\": {\"matches\": {\"of\": \"node.symbol_name\",\n" +
	"                                \"regex\": \"\\\\.{4,}\"}}}\n" +
	"    filter {\"not\": {\"matches\": {\"of\": \"node.symbol_name\",\n" +
	"                                \"regex\": \"^[0-9IVXivx]+$\"}}}\n" +
	"    filter {\"not\": {\"all\": [\n" +
	"        {\"exists\": {\"of\": \"node.page_repeat_count\"}},\n" +
	"        {\"compare\": {\"of\": \"node.page_repeat_count\", \"op\": \"gte\", \"value\": \"2\"}}\n" +
	"    ]}}\n" +
	"    emit outline {\n" +
	"        name := node.symbol_name\n" +
	"        page := node.page_first\n" +
	"    }\n" +
	"\n" +
	"THE `exists` LEAF BESIDE THE `compare` IS REQUIRED. A row whose\n" +
	"compared field is ABSENT does not match in either direction, so without\n" +
	"the `exists` an unstamped node is indistinguishable from an unmatched\n" +
	"one and the `not` would keep both. The key is `page_repeat_count`; only\n" +
	"`chrome_shape` and `chrome_repeat_shaped` carry the `chrome_` prefix.\n" +
	"That key rides only the blocks a repeated fingerprint applies to, so a\n" +
	"document with no repeated header does not carry it anywhere and the\n" +
	"whole body is refused naming it — drop the last filter for such a\n" +
	"document.\n" +
	"\n" +
	"### 5. Dropping a web page's navigation chrome\n" +
	"\n" +
	"The web equivalent: sidebars, breadcrumbs and link strips look exactly\n" +
	"like content at the graph level.\n" +
	"\n" +
	"    select page\n" +
	"    walk CONTAINS\n" +
	"    filter {\"all\": [\n" +
	"        {\"kind\": {\"of\": \"node\", \"is\": [\"section\", \"paragraph\"]}},\n" +
	"        {\"not\": {\"matches\": {\"of\": \"node.tag\",\n" +
	"                              \"regex\": \"^(nav|header|footer|aside)$\"}}},\n" +
	"        {\"not\": {\"equals\": {\"of\": \"node.links_only\", \"value\": \"true\"}}}\n" +
	"    ]}\n" +
	"    emit content {\n" +
	"        name := node.body\n" +
	"        kind := node.type\n" +
	"        tag := node.tag\n" +
	"        uri := node.uri\n" +
	"    }\n" +
	"\n" +
	"THE KIND LEAF ADMITS PARAGRAPHS AS WELL AS SECTIONS, and that is a\n" +
	"correctness requirement rather than a widening. `links_only` is written\n" +
	"by the paragraph emitter alone, on paragraph records, when the run is\n" +
	"links-only; no section emitter writes it. A section-only form therefore\n" +
	"negates a key sections never carry, so the clause excludes nothing.\n" +
	"`name := node.body` for the same reason: an emit whose name expression\n" +
	"is empty SKIPS the row, and a paragraph has no SymbolName, so naming\n" +
	"`node.symbol_name` here would silently drop every paragraph. `body`\n" +
	"resolves `content` then `description`, which is the heading on a\n" +
	"section and the text on a paragraph. `links_only` is stamped only on a\n" +
	"links-only run, so a document containing none does not carry the key\n" +
	"and the filter is refused naming it — drop that line for such a\n" +
	"document.\n" +
	"\n" +
	"### What each example needs from the collector\n" +
	"\n" +
	"    1  pdf   CONTAINS, section, page_first          always stamped\n" +
	"    2  web   CONTAINS, section, uri                 always stamped\n" +
	"    3  pdf   CONTAINS, section, page_first/last     always stamped\n" +
	"    4  pdf   + page_repeat_count                    repeated headers only\n" +
	"    5  web   + tag, links_only                      links-only runs only\n" +
	"\n" +
	"A metadata key, edge type or node type THE SOURCE GRAPH DOES NOT CARRY\n" +
	"is refused BEFORE THE WALK, in a message naming the key and listing\n" +
	"what the graph does carry — never answered with an empty result. So an\n" +
	"example whose last two rows do not apply to your document tells you so\n" +
	"by name, and the repair is to drop that filter.\n" +
	"\n"
