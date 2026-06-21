# Recipes

## Overview

A recipe is a declarative transformation that reads one graph and writes another —
turning a raw [web crawl](web-collection.md) or [PDF](pdf-collection.md) into
structured domain nodes (patterns, idioms, sections, whatever your corpus
warrants). Recipes are written in a small DSL and run by a pure-Go interpreter
with **zero LLM at runtime**: the translation is mechanical (regex, heading, and
edge-walk driven), so it is cheap, deterministic, and endlessly repeatable.

Two things make recipes distinctive:

- **Recipes are graph-resident, not files.** A recipe is a node in the
  `transformers` graph, authored and loaded with [`mutate`](tools/mutate.md). There
  is no `.recipe` file on disk to check in — the recipe lives in the graph.
- **A recipe is corpus-specific.** What makes sense for your web crawl of one
  pattern catalog is not what makes sense for someone else's PDF. For that reason
  you **never check a recipe into the open-source repo** — you build it in the
  graph against your own collected source.

This guide narrates the workflow and the best practices. For the full DSL grammar
and the rule-by-rule semantics, run `help('recipes')` (see the
[help tool](tools/help.md)) — this guide deliberately does not reproduce the
grammar.

## When & how to use

Reach for a recipe when you have already collected a raw source graph and want to
extract structured nodes from it repeatably — and when the extraction is
mechanical rather than open-ended semantic summarization. If you need
LLM-judgment-driven translation, recipes are the wrong tool.

### Author and load the recipe

A recipe is created like any other node, into the `transformers` graph with type
`recipe`. Its body is the DSL; its metadata declares what it reads and what it
writes:

```jsonc
mutate({
  "operation": "create",
  "graph": "transformers",
  "type": "recipe",
  "name": "eip-to-design-patterns",
  "content": "select page where page.url ~= /patterns/\nemit pattern { type := \"pattern\", name := page.name, summary := page.name } as $pat",
  "description": "Translate hohpe-eip web pages into practice/design-patterns nodes",
  "metadata": {
    "source_graph_type": "web",
    "target_graph_type": "practice",
    "target_name": "design-patterns"
  }
})
```

All three metadata keys are **required**. `source_graph_type` is the graph family
the recipe reads (`web`, `pdf`, ...); `target_graph_type` and `target_name` name
the graph it writes. A recipe missing any of the three fails at run time with an
explicit error — they are not optional. All recipes live in a single bucket named
`recipes` in the `transformers` graph, and a recipe is looked up by its `name`, so
keep recipe names unique.

### Inspect the source before you write rules

A recipe is only as good as your understanding of the source graph's shape. Before
drafting rules, inspect what you actually collected — node types, the edges that
exist, the metadata keys that are populated, the heading shapes that appear:

```jsonc
query({ "graph": "web", "name": "<source-slug>", "mode": "stats" })
```

Then `query` and `traverse` a few sample nodes. Recipe rules select by node type
and filter by field and metadata expressions, so you need to know those keys
before you can write a `select ... where`.

### Run it: dry-run first, then for real

A recipe runs through [`collect`](tools/collect.md), with the source `type`
matching the recipe's `source_graph_type`, the `transformer` set to `recipe`, and
the recipe named:

```jsonc
// Dry-run: compute what would be emitted, write nothing
collect({
  "type": "web",
  "id": "<source-slug>",
  "transformer": "recipe",
  "recipe": "eip-to-design-patterns",
  "dry_run": true
})
```

Two things matter here. First, the collect `type` **must** match the recipe's
`source_graph_type` metadata — the metadata is what selects the source graph
family, so keep the `type` you pass aligned with it (the tool contract requires the
match). Second, because
you pass `transformer: "recipe"` with no `seed_urls`, the crawl is **skipped** —
the recipe runs against the already-cached raw graph, so iterating costs nothing
beyond the transform itself. The `recipe`, `dry_run`, and `transformer` fields only
apply when `transformer` is `"recipe"`; an unknown recipe name returns an error
listing the recipes that are registered.

Unlike the crawl and the PDF parse — which run client-side — the recipe
**transform runs server-side**, where both the source raw graph and the target
graph live. The client simply forwards the call.

Read the dry-run stats, check the emissions are what you intended, then run for
real by dropping `dry_run` (or setting it to `false`). A real run lands the
emitted nodes in the target graph, each carrying a `translated-from` edge back to
the source node it came from for lineage.

### Re-run with force after editing the recipe

Emissions are idempotent: a recipe computes a stable identity for each node it
emits, so the same source under the same recipe produces the same target IDs every
time, and a re-run overwrites rather than duplicating. When you **edit** a recipe,
re-run with `force: true` — it first deletes the prior emissions for this source
(matched via the `translated-from` lineage edge), then rebuilds from the new rules,
so you get a clean state instead of a mix of old and new nodes:

```jsonc
// After editing the recipe body, rebuild cleanly
collect({
  "type": "web",
  "id": "<source-slug>",
  "transformer": "recipe",
  "recipe": "eip-to-design-patterns",
  "force": true
})
```

### Watch the miss counters

Every run reports stats. Two of them are your primary recipe-bug signals:
`lookup_misses` (a `lookup` rule whose target node was not found) and `link_misses`
(a `link` rule skipped because an endpoint was empty or absent). A nonzero
`lookup_misses` or a high `link_misses` almost always means the recipe is wrong —
a mistaken identity expression or rules in the wrong order — not that the source is
incomplete. Check the recipe against a sample source node before accepting the run.
The `help('recipes')` reference documents the full stats block and the rule
semantics behind these counters.

### The iteration loop

Putting it together, the practical loop is: inspect the source graph → draft the
recipe and load it with `mutate` → `dry_run` → inspect the emissions and the miss
counters → real run → `force` to rebuild after each edit. Because the source graph
is already cached, every turn of this loop is cheap.
