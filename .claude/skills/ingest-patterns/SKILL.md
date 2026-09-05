---
name: ingest-patterns
description: Ingest practices from an authoritative source (book, public catalog, reference site) into a language's practice graph, each with a sister structural check where the practice has a shape a checker can see. Collect the source once into a raw graph, search it for the passages worth reading, read those passages with inline recipe bodies, draft and polish the candidates by hand, land each one, then drop the raw graph.
argument-hint: <source-slug-or-pdf-path-or-ticket-id>
---

# Ingest patterns: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline reference /orchestrate.
This skill is pattern-ingestion-specific.
</precedence>

The flow is: collect, find, read, draft, polish, land, drop. Step 0 orients, and
steps 1 through 7 run in that order.

Reading a collected source is cheap and repeatable — the raw graph is a cached
structured document, and every read after the collect replays it and fetches
nothing over the network. So the deciding happens while reading. The writing at
the end is hand work on the small set you already chose.

## Step 0: Orient

Check the server is up and see what is already collected.

```jsonc
manage({ "operation": "status" })
```

```jsonc
query({ "graph": "web", "mode": "modules" })
```

```jsonc
query({ "graph": "pdf", "mode": "modules" })
```

`mode:"modules"` lists the raw graphs already collected, with their node counts.
Run it for whichever family your source belongs to, or both if you are not sure
which one holds it.

Pick a SCRATCH NAME for this run — something you will not mind losing, keyed to
the source and this session. Step 7 drops the graph.

## Step 1: Collect the source into a raw graph

Web:

```jsonc
collect({
  "type": "web",
  "id": "<scratch-slug>",
  "seed_urls": ["<entry-url>"],
  "follow_patterns": ["^<host-and-path-prefix-regex>"],
  "max_pages": 100,
  "politeness_ms": 500
})
```

PDF:

```jsonc
collect({ "type": "pdf", "id": "<absolute-path-to-pdf>" })
```

`max_pages` is a HARD cap on the pages the crawl fetches, not a target it
approaches — the crawl stops there even with work still queued, and zero means
unbounded. `follow_patterns` is a regex allowlist for internal links; anchor it
at the host and path prefix you actually want, or a site-wide crawl will bring
back mostly navigation.

Pass no `force` on either call. A scratch name has nothing to overwrite, and
`force` is refused outright on a recipe run later in this flow.

Then read the shape of what you collected, before writing any body:

```jsonc
query({ "graph": "<web|pdf>", "name": "<slug>", "mode": "stats" })
```

That read is not a formality. It gives the node-type breakdown and the edge
vocabulary, and the DSL compares edge types EXACTLY, including case. A PDF raw
graph carries `CONTAINS` only. A web raw graph can carry both `CONTAINS` and a
lowercase `contains` at the same time, because a crawl that materializes a code
host anchors its files under a lowercase edge while the document structure stays
uppercase. A body that names the wrong spelling is refused before the walk, with
the graph's real vocabulary in the message — but reading it here is cheaper than
meeting the refusal.

## Step 2: Find the passages worth reading

```jsonc
search({
  "graph": "<web|pdf>",
  "name": "<slug>",
  "query": "<concept>",
  "mode": "hybrid",
  "limit": 10
})
```

Each hit renders a locality line reading `under: <heading> | p. N`. That is how
you find the passages worth reading instead of walking the whole document, and
the heading it names is what you key the section-body read on in Step 3.

A freshly collected raw graph carries no vectors until it has been enrolled and
embedded, so a just-collected document answers BM25-only. The response footer
says which mode it actually ran in.

## Step 3: Read the passages

Two reads through the extract path. Nothing is written, and nothing is saved.

An OUTLINE first — what sections the document has, and where each one sits:

```jsonc
collect({
  "type": "<web|pdf>",
  "id": "<slug-or-absolute-pdf-path>",
  "transformer": "recipe",
  "extract": true,
  "max_rows": 50,
  "recipe_body": "select section\nemit outline {\n    name := section.symbol_name\n    path := heading_path(\"CONTAINS\", \"symbol_name\", \" > \")\n}"
})
```

Then a SECTION BODY — the prose under one heading, with its subtree in document
order:

```jsonc
collect({
  "type": "<web|pdf>",
  "id": "<slug-or-absolute-pdf-path>",
  "transformer": "recipe",
  "extract": true,
  "max_rows": 20,
  "recipe_body": "select section\nfilter {\"matches\": {\"of\": \"section.symbol_name\", \"regex\": \"^Idempotent\"}}\nemit passage {\n    name := section.symbol_name\n    path := heading_path(\"CONTAINS\", \"symbol_name\", \" > \")\n    body := subtree_concat(\"CONTAINS\", \"body\", \"\\n\\n\", \"4\")\n}"
})
```

Swap the regex for the heading Step 2 handed you. `help("recipes")` carries the
grammar and worked bodies for both collectors — copy one of those and adapt it
rather than writing a body from scratch.

What `heading_path` renders, so an empty path does not read as a broken read:
it walks the row's ancestors and joins their headings, skipping any ancestor
that has none. A PDF document's heading is the file's embedded Title; when the
file carries none the collector derives one and stamps `metadata.title_source`
on the document node so a derived title is never mistaken for the document's
own. The `under:` line on a search hit comes from a different mechanism and is
not evidence that `heading_path` will populate for that row.

MEASURED COST OF ONE PASS. This loop was run end to end on two collected
sources. On a book: three calls returning 88,204 response bytes, of which a
single section-body read accounted for 63,574 because its heading regex matched
fifteen sections instead of one — keyed to a single section, the same loop is
two calls and 24,630 bytes. On a site: three successful calls returning 32,880
bytes; a fourth call was refused against a stale graph and is not counted. Both
are single runs on one document each, so read them as the order of magnitude of
a pass, and expect the section-body read to dominate whatever you pay.

What the tool enforces, so you meet none of it by surprise:

- `recipe_body` requires `extract: true`.
- An inline body is the only form this flow uses. A body is written for one
  extraction and discarded; there is no save step and nothing to name later.
- `force` is refused on a recipe run.
- `max_rows` defaults to 200 and `max_bytes` to 65536.
- `offset` is the zero-based index of the first matched row returned, for paging
  a document larger than one response.
- Any truncation prints a line beginning `TRUNCATED by`, naming the cap that
  fired and the offset to resume from. Rows returned and rows matched are both
  reported, so a truncated read never looks like a short one.

Iterate here. A mistake costs one read.

## Step 4: Draft the candidates

List the best-practice and anti-pattern statements the source actually makes.
Each candidate carries the cited passage and any code the source gave for it.

Code arrives as `code_block` nodes whose text is in `Content`. A web collect
stamps `metadata.language` on them; a PDF collect stamps no language at all, so
for a PDF source you supply the language yourself.

Keep the source's own words in the excerpt. Your prose goes in the description.

## Step 5: Polish

The human edits the candidates: what the practice is, when it applies, when it
does not, and what the source's example actually demonstrates.

Practice graphs are hand-massaged golden state. Nothing in this flow overwrites
them — every write below is one deliberate create you authored.

## Step 6: Land

TWO separate hand operations, in this order. No tool changes between them.

**(a) The practice node.** This is where the prose lives.

```jsonc
mutate({
  "operation": "create",
  "graph": "practice",
  "language": "<lang>",
  "type": "pattern",
  "name": "<short-kebab-name>",
  "summary": "<one line, search-optimized>",
  "description": "<the practice in your words>",
  "metadata": {
    "source": "<url or file>",
    "source_locator": "<page or anchor>"
  }
})
```

Carry the provenance and the polished excerpt with it. `help("patterns")` has
the full authoring sequence — the use_case, example and reference children and
the edges that link them.

A language with no practice graph yet gets one from this first create: the
graph is created on first write. The `language` value must already be its
canonical slug (lowercase, hyphen-separated); a name that is not its own slug
is refused naming the value, never coerced, so pick the slug before you write.

**(b) The sister structural check**, authored exactly as any other ast check:

```jsonc
manage_checks({
  "operation": "create",
  "language": "<lang>",
  "name": "<check-name>",
  "summary": "<one line, search-optimized>",
  "description": "<what the rule is and why, naming the practice node>",
  "check_type": "ast_pattern",
  "dsl_pattern": "<pattern>",
  "check_where": "<optional where-tree as JSON text>",
  "severity": "<info|notice|warning|critical>",
  "fixture_bad": {
    "name": "<name>",
    "summary": "<one line>",
    "description": "Why this snippet is the bad example.",
    "content": "<snippet the check must fire on>"
  },
  "fixture_good": {
    "name": "<name>",
    "summary": "<one line>",
    "description": "Why this snippet is the good example.",
    "content": "<snippet the check must stay silent on>"
  }
})
```

Draw both fixtures from the source's own code blocks where it gave them.
Nothing is written unless the check fires on the bad fixture and stays silent on
the good one — that admission run is the gate, and it happens inside the create.

THE EXCEPTION, stated so you do not manufacture a check to satisfy the pattern:
a practice with no structural shape gets a practice node and NO check. Advice
about naming, sequencing or judgement has nothing for an ast pattern to match,
and a check written to match it anyway will be wrong in both directions.

THE OTHER EXCEPTION, a different case: the practice has a shape but the
source's code is in a language the checker cannot parse. Every check type
requires a registered tree-sitter grammar for its language, and an
`ast_pattern` check additionally requires one the ast engine does not deny;
`ast({"operation": "list_node_kinds", "language": "<lang>"})` answers in one
call. When the source's only code blocks are in a language with no grammar,
the practice node lands alone and no check of any kind is authored for it,
`llm_only` included. Fixtures are never invented in another language to get a
check through — that breaks the rule above that fixtures come from the
source's own code blocks. Say in the practice node's description that no check
exists and why, so the next reader does not go looking for one.

## Step 7: Drop the raw graph

Preview first:

```jsonc
manage({
  "operation": "drop_graph",
  "graph": "<web|pdf>",
  "name": "<slug>",
  "dry_run": true
})
```

Then the same call without `dry_run`.

This step is mandatory, not optional. Raw graphs are temporary: they exist to be
read once, and they are cleaned up as soon as the polished content exists. The
practice node and its check are the deliverable; the raw graph is scaffolding.

## Closure

1. Confirm each landed practice reads correctly: `assemble({ "id": "<pattern_id>" })`.
2. Close the associated work item if there is one:
   `mutate({ "operation": "update", "id": "<ticket_id>", "status": "closed" })`.
3. No commit is needed — practice and checks writes are graph-resident.

<constraint id="ingest-patterns-anti-patterns" severity="hard">

  <anti-patterns>
    <pattern>Skipping the search and reading the whole document instead — the ranked read is what finds the passages worth your attention, and reading everything costs far more than the search that would have narrowed it</pattern>
    <pattern>Writing a body from scratch before reading the source graph's stats — the DSL compares edge types exactly, so a guessed edge spelling is refused and the repair is the read you skipped</pattern>
    <pattern>Landing a practice node without its structural check when the practice HAS a shape — the prose is then unenforced, and the corpus reads as covered when it is not</pattern>
    <pattern>Manufacturing a check for a practice with no structural shape — a check that cannot express the rule fires on the wrong code and stays silent on the right code</pattern>
    <pattern>Leaving the raw graph behind after the practice node lands — it is scratch state that will look like a curated source to the next reader</pattern>
    <pattern>Discarding the source's own words when writing the description — keep the cited excerpt with the node so the claim stays checkable</pattern>
  </anti-patterns>

</constraint>
