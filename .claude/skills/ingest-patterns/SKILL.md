---
name: ingest-patterns
description: Ingest practices from an authoritative source (book, public catalog, reference site) into the combined practice graph under a source hub. Collect the source once into a raw graph, search it for the passages worth reading, read those passages with inline recipe bodies, land the emitted nodes as basic practice nodes, polish them by hand into golden state, then drop the raw graph.
argument-hint: <source-slug-or-pdf-path-or-ticket-id>
---

# Ingest patterns: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline reference /orchestrate.
This skill is pattern-ingestion-specific.
</precedence>

The flow is: collect, find, read, land, polish, drop. Step 0 orients, and
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
  "recipe_body": "select section\nemit reference {\n    identity := section.id\n    name := section.symbol_name\n    path := heading_path(\"CONTAINS\", \"symbol_name\", \" > \")\n}"
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
  "recipe_body": "select section\nfilter {\"matches\": {\"of\": \"section.symbol_name\", \"regex\": \"^Idempotent\"}}\nemit example {\n    identity := section.id\n    name := section.symbol_name\n    path := heading_path(\"CONTAINS\", \"symbol_name\", \" > \")\n    body := subtree_concat(\"CONTAINS\", \"body\", \"\\n\\n\", \"4\")\n}"
})
```

Swap the regex for the heading Step 2 handed you. `help("recipes")` carries the
grammar and worked bodies for both collectors — copy one of those and adapt it
rather than writing a body from scratch.

`identity` is what the emitted node's stable id is hashed on, and it must be
unique per row: two rows resolving to one identity refuse the whole run at the
first collision. It defaults to `name`, which is a heading on most documents and
is therefore not unique — key it on the row's own `id`, as both bodies above do,
unless you have a better unique field.

What `heading_path` renders, so an empty path does not read as a broken read:
it walks the row's ancestors and joins their headings, skipping any ancestor
that has none. A PDF document's heading is the file's embedded Title; when the
file carries none the collector derives one and stamps `metadata.title_source`
on the document node so a derived title is never mistaken for the document's
own. The `under:` line on a search hit comes from a different mechanism and is
not evidence that `heading_path` will populate for that row.

COST SHAPE OF ONE PASS. The section-body read dominates whatever a pass costs:
a heading regex that matches many sections instead of one is the difference
between a small read and a large one. Key the read to a single section.

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

## Step 4: Land the basic nodes

Once the extract says what you meant, run it again with `land: true` instead
of `extract: true`, with the same body plus a `summary` field. That WRITES the
emitted nodes into the combined practice graph as BASIC nodes, grouped under a
`source` hub named from the raw graph's slug:

```jsonc
collect({
  "type": "<web|pdf>",
  "id": "<slug-or-absolute-pdf-path>",
  "transformer": "recipe",
  "land": true,
  "recipe_body": "select section\nfilter {\"matches\": {\"of\": \"section.symbol_name\", \"regex\": \"^Idempotent\"}}\nemit example {\n    identity := section.id\n    name := section.symbol_name\n    summary := section.symbol_name\n    path := heading_path(\"CONTAINS\", \"symbol_name\", \" > \")\n    body := subtree_concat(\"CONTAINS\", \"body\", \"\\n\\n\", \"4\")\n}"
})
```

The response names the hub, the count landed, and any versioned twins. Keep
the hub id: it is how you delete the whole collection if the run was wrong.

What it writes: each node with the emit body's fields, the hub's id in its
`source_hub` metadata, and one `sourced-from` edge to the hub. Nothing points
back into the raw graph. A node of a known practice type with NO `summary` is
refused by the server, so put a summary in the emit block.

Re-running: an emitted id that already exists lands a VERSIONED TWIN beside
it — a new node, an incremented `version`, and one `next-version` edge old to
new. The existing row is never modified, so a landing over a hub you have
already hand-edited is safe. Iterate with `extract`, then land once.

`max_rows` and `offset` are REFUSED on a landing: they window what an extract
SHOWS, and a landing writes the whole emitted set.

If the run was wrong, remove the whole collection by its hub:

```jsonc
delete({ "graph": "practice", "source": "<hub id>", "dry_run": true })
```

then the same call without `dry_run`. It is soft by default and recoverable;
`hard: true` is permanent. Nodes under any other hub are untouched.

## Step 5: Polish

You are now editing LANDED nodes rather than drafting them from scratch. The
human edits each one: what the practice is, when it applies, when it does not,
and what the source's example actually demonstrates.

Code arrives as `code_block` nodes whose text is in `Content`. A web collect
stamps `metadata.language` on them; a PDF collect stamps no language at all, so
for a PDF source you supply the language yourself.

Keep the source's own words in the excerpt. Your prose goes in the description.

The practice graph is hand-massaged golden state. Nothing in this flow overwrites
a node you have edited — a re-landing lands a twin beside it, and every edit
below is one deliberate update you authored.

## Step 6: Massage into golden state

**(a) The practice node.** It is already in the graph; what a human does is
edit it into its final shape:

```jsonc
mutate({
  "operation": "update",
  "id": "<landed node id>",
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
the edges that link them. If the flow ever needs a hand create, the practice
graph is one combined graph and a write names the hub, never a language:

```jsonc
mutate({
  "operation": "create",
  "graph": "practice",
  "source_hub": "<hub id>",
  "type": "pattern",
  "name": "<short-kebab-name>",
  "summary": "<one line, search-optimized>"
})
```

**(b) A sister structural check is a separate, later act.** No landing, import
or migration creates a check. A practice whose rule has a shape a checker can
see MAY later get a sister check, authored deliberately with `manage_checks`
after a reader has gone over the practice node and confirmed it is correct,
with both fixtures drawn from the source's own code blocks. A practice with no
structural shape never gets one: advice about naming, sequencing or judgement
has nothing for an ast pattern to match, and a check written to match it
anyway will be wrong in both directions. The same holds when the source's only
code is in a language with no registered grammar
(`ast({"operation": "list_node_kinds", "language": "<lang>"})` answers in one
call): the practice node stands alone, fixtures are never invented in another
language, and the description says no check exists and why.

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
landed nodes carry no edge into the raw graph, so dropping it loses nothing.
The practice nodes are the deliverable; the raw graph is scaffolding.

## Closure

1. Confirm each landed practice reads correctly: `assemble({ "id": "<pattern_id>" })`.
2. Close the associated work item if there is one:
   `mutate({ "operation": "update", "id": "<ticket_id>", "status": "closed" })`.
3. No commit is needed — practice writes are graph-resident.

<constraint id="ingest-patterns-anti-patterns" severity="hard">

  <anti-patterns>
    <pattern>Skipping the search and reading the whole document instead — the ranked read is what finds the passages worth your attention, and reading everything costs far more than the search that would have narrowed it</pattern>
    <pattern>Writing a body from scratch before reading the source graph's stats — the DSL compares edge types exactly, so a guessed edge spelling is refused and the repair is the read you skipped</pattern>
    <pattern>Authoring a check as part of the landing — a check is a deliberate, separate act after a reader has confirmed the practice node, never a side effect of an ingest</pattern>
    <pattern>Manufacturing a check for a practice with no structural shape — a check that cannot express the rule fires on the wrong code and stays silent on the right code</pattern>
    <pattern>Keying an emit's identity on a heading-derived name — two sections with one heading refuse the whole run; the row's own id is unique by construction</pattern>
    <pattern>Leaving the raw graph behind after the practice node lands — it is scratch state that will look like a curated source to the next reader</pattern>
    <pattern>Discarding the source's own words when writing the description — keep the cited excerpt with the node so the claim stays checkable</pattern>
  </anti-patterns>

</constraint>
