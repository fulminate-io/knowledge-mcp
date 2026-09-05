# Recipes

## Overview

A recipe is a declarative transformation that reads a graph and returns
structured rows from it — turning a raw [web crawl](web-collection.md) or
[PDF](pdf-collection.md) into the sections, passages and code blocks you
actually wanted. Recipes are written in a small DSL and run by a pure-Go
interpreter with **zero LLM at runtime**: the translation is mechanical (regex,
heading, and edge-walk driven), so it is cheap, deterministic, and endlessly
repeatable.

Two things make recipes distinctive:

- **A recipe body is ephemeral.** You write one for a single extraction, edit it
  until it says what you meant, and discard it. It is a string you pass on the
  call, not an artifact with a lifecycle.
- **A recipe is corpus-specific.** What makes sense for your web crawl of one
  pattern catalog is not what makes sense for someone else's PDF. There is
  nothing to check into a repository and nothing to share: you write the body
  against the source you collected.

This guide narrates the workflow and the best practices. For the full DSL grammar
and the rule-by-rule semantics, run `help('recipes')` (see the
[help tool](tools/help.md)) — this guide deliberately does not reproduce the
grammar.

## When & how to use

Reach for a recipe when you have already collected a raw source graph and want to
read structured rows out of it — and when the extraction is mechanical rather
than open-ended semantic summarization. If you need LLM-judgment-driven
translation, recipes are the wrong tool.

### Inspect the source before you write rules

A recipe is only as good as your understanding of the source graph's shape. Before
drafting rules, inspect what you actually collected — node types, the edges that
exist, the metadata keys that are populated, the heading shapes that appear:

```jsonc
query({ "graph": "web", "name": "<source-slug>", "mode": "stats" })
```

Then `query` and `traverse` a few sample nodes. This step is not optional
diligence: a recipe's `select ... where` and `filter` clauses take a **where-tree**
whose leaves name node types, edge types and metadata keys, and every one of them
is checked against the graph you actually collected **before the run starts**. A
key the graph does not stamp, or an edge type in the wrong case, is now a refusal
naming the offending value and listing what the graph does carry — not a run that
reads it as empty and emits blank fields. Learn the vocabulary first and the
refusals never fire.

See `help("recipes")` for the where-tree grammar and the full rule reference.

### Run the body

A body runs through [`collect`](tools/collect.md), with the source `type` naming
the graph family you collected, `transformer` set to `recipe`, `extract` set to
true, and the body itself passed as `recipe_body`:

```jsonc
collect({
  "type": "web",
  "id": "<source-slug>",
  "transformer": "recipe",
  "extract": true,
  "max_rows": 50,
  "recipe_body": "select page\nemit passage {\n    name := page.name\n    body := page.description\n}"
})
```

Three things matter here. `extract` is required with `recipe_body` — the pair is
what makes the run a read. Because you pass `transformer: "recipe"` with no
`seed_urls`, the crawl is **skipped**: the body runs against the already-cached
raw graph, so iterating costs nothing beyond the transform itself. And `force`
is refused on a recipe run — there is nothing for it to bypass, since the run
writes nothing.

The rows come back in the response. Read them, adjust the body, run it again.

### Keeping a body while you iterate

A body is ephemeral, but re-escaping it by hand every run is tedious. Keep the
escaped JSON string in whatever scratch file you like while you work on it:

```jsonc
{
  "name": "sections-outline",
  "content": "select section\nemit outline {\n    name := section.symbol_name\n    path := heading_path(\"CONTAINS\", \"symbol_name\", \" > \")\n}"
}
```

Nothing but you reads that file, and this flow never looks a body up by name —
every run passes the text.

### Watch the miss counters

Every run reports stats. Two of them are your primary recipe-bug signals:
`lookup_misses` (a `lookup` rule whose target node was not found) and `link_misses`
(a `link` rule skipped because an endpoint was empty or absent). A nonzero
`lookup_misses` or a high `link_misses` almost always means the recipe is wrong —
a mistaken identity expression or rules in the wrong order — not that the source is
incomplete. Check the recipe against a sample source node before accepting the run.
The `help('recipes')` reference documents the full stats block and the rule
semantics behind these counters.

### Reading order, `walk`, and paging a large extract

Three properties matter once you point a recipe at a real document rather than a
handful of sections.

**Rowsets come back in document reading order.** A `select` or `traverse` rowset
is ordered as the document reads, and a node for which no position is
determinable follows every ordered node, by node id. A `select section` over a
collected book therefore returns its sections front to back, and page numbers
never go backwards down an extract. The order is derived once per run from each
child's `position` — read from the node first, and from the containment edge as a
fallback — so it is a whole-document order rather than a per-parent one: two
paragraphs sitting at index 1 under different sections are still strictly ordered
against each other.

That ordering has one refusal attached to it, and it is worth knowing before you
meet it. If a node in the source graph is claimed by **two** positioned
containment edges it has two document positions and no way to choose between
them, so the run stops and names the node and both parents instead of picking
one. The repair is to remove the extra edge or re-collect the source.

**`walk` reads a whole subtree in one rule.** A `traverse` expands one level and
replaces the rowset with that level, so a nested outline needs one rule per level
and comes back level by level. `walk EDGE [as $var]` descends the whole subtree
instead and returns it interleaved as the document reads, stamping `walk.depth`
(1 for a direct child) and `walk.position` on every row. Those are read as bare
dotted heads exactly as `group.keys` is, and the walk's edge type is censused
against the source graph just as traverse's is, so a mis-cased edge type is
refused before the walk rather than answered with zero rows.

**`offset` pages an extract.** `offset` is the zero-based index of the first
matched row returned, so a document larger than one response is read a page at a
time. Every matched row is still counted whether or not it is returned, so the
header's matched total names the whole population behind the page; the truncation
line names the offset to resume from, counted from what the renderer actually
emitted so a resume never skips a row the byte cap cut; and a page that starts
past the end says so rather than looking like a recipe that matched nothing. A
negative offset is refused rather than clamped.

```jsonc
// One rule for a whole nested outline, fifty rows at a time
collect({
  "type": "pdf",
  "id": "<source-slug>",
  "transformer": "recipe",
  "extract": true,
  "recipe_body": "select document\nwalk CONTAINS\nemit outline {\n    name := node.symbol_name\n    level := walk.depth\n    page := node.page_first\n}",
  "offset": 100,
  "max_rows": 50
})
```

### The iteration loop

Putting it together, the practical loop is: inspect the source graph → draft the
body → run it inline → read the rows and the miss counters → edit the body → run
it again. Because the source graph is already cached, every turn of this loop is
cheap. When the rows say what you wanted, take them and throw the body away.
