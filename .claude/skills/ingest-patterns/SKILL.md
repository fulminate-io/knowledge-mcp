---
name: ingest-patterns
description: Ingest design patterns from an authoritative source (book, public catalog, reference site) into the practice/design-patterns.bin library graph. Four stages — collect the source once into a raw graph, extract from it interactively with zero LLM spend, freeze a good extraction as a saved recipe when it is worth re-running, then hydrate the chosen slice into full pattern nodes via the pattern-ingester agent. Extract is where you decide what is worth having; hydrate is where the token spend lands, so it runs on a chosen slice rather than everything.
argument-hint: <source-slug-or-pdf-path-or-ticket-id>
---

# Ingest patterns: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline reference /orchestrate.
This skill is pattern-ingestion-specific.
</precedence>

Four stages, in order:

1. **Collect** — pull the source into a raw graph once. The raw graph is a cached
   structured document: every later stage replays it at zero network cost, so
   iterating costs nothing but your own time.
2. **Extract** — ZERO LLM SPEND, and that is the whole point of this stage. Run
   inline recipe bodies against the raw graph and read the rows back; use the
   ranked text read to find which sections even mention what you are after.
   Nothing is written and nothing is summarized by a model.
3. **Freeze** — when an extraction is worth re-running (a new edition, a second
   source of the same shape), SAVE the same body as a recipe. Freezing is a
   save, not a rewrite: an inline extract and a saved recipe are one mechanism
   with two ways to invoke it.
4. **Hydrate** — the only stage that spends tokens. The `pattern-ingester` agent
   turns a CHOSEN SLICE of the extraction into full pattern nodes: original
   prose, applies-when and avoid-when use cases, working examples, primary
   references. Roughly 50k output tokens per pattern.

The shape of the cost is why the order matters. Collect and extract are cheap and
repeatable, so do your deciding there; hydrate is expensive and therefore runs on
what you decided, not on everything the source contains.

<constraint id="phase-2-default-off" severity="hard">

  <rule>
    Default is extract only. Don't auto-hydrate every pattern.
  </rule>

  <override-default>
    Trained instinct: produce maximum-quality output, hydrate everything.
    Wrong here — hydration budget should track downstream design impact,
    not abstract interestingness.
  </override-default>

  <when-to-hydrate>
    Pattern actually shapes design decisions in downstream /brainstorm or /plan sessions.
    Not: "pattern looks interesting in the abstract."
  </when-to-hydrate>

</constraint>

## Step 0: Identify Source + Mode + Verify Server

```jsonc
manage({ "operation": "status" })
```

Resolve source identity from `$ARGUMENTS`:
- **Web slug** (e.g. `sre-workbook`) → `~/.knowledge/web/<slug>.bin`
- **Absolute PDF path** → resolves to `~/.knowledge/pdf/<hashed-slug>.bin`
- **Ticket ID** (40-hex) → `assemble({id:...})` to read embedded source spec

If neither bin exists → **new-source mode** (Step 1a). If exists → skip the crawl.

```jsonc
query({ "graph": "transformers", "type": "recipe", "limit": 30 })
```

Find recipe named `<slug>-to-<target>` (target usually `design-patterns`). Exists → **fix mode**. Missing → **new-recipe mode**.

## Stages 1-3: Collect, Extract, Freeze

### Step 1a (new-source mode): Collect

Web:
```jsonc
collect({
  "type": "web", "id": "<slug>",
  "seed_urls": ["<entry-url>"],
  "follow_patterns": ["^<host-and-path-prefix-regex>"],
  "max_pages": 100, "politeness_ms": 500, "force": true
})
```

PDF:
```jsonc
collect({ "type": "pdf", "id": "<absolute-path-to-pdf>", "force": true })
```

Verify body capture on a sample node — body should appear under title.

### Step 1b: Extract interactively — no writes, no model spend

Before authoring anything permanent, ASK THE RAW GRAPH WHAT IS IN IT. Both reads
below replay the cached raw graph and fetch nothing over the network.

Find the sections that mention what you care about:

```jsonc
query({ "graph": "<web|pdf>", "name": "<slug>", "text": "connection pooling" })
```

That is a ranked text read computed on the client with no embedding step, so it
costs nothing and works on a raw graph that was never summarized. `mode:"stats"`
on the same selector gives the node-type breakdown; `mode:"modules"` lists the
raw graphs you have collected.

Then run a body against it and read the rows back, with NOTHING written:

```jsonc
collect({
  "type": "<web|pdf>", "id": "<slug>",
  "transformer": "recipe", "extract": true, "max_rows": 20,
  "recipe_body": "select section\nemit pattern {\n    name := section.symbol_name\n    path := heading_path(\"CONTAINS\", \"symbol_name\", \" > \")\n    body := subtree_concat(\"CONTAINS\", \"body\", \"\\n\\n\", \"3\")\n}"
})
```

`recipe_body` is an INLINE body — no saved recipe node is created, and `extract`
is required with it. Iterate the filters here, where a mistake costs one read.

Three DSL pieces do most of the work on a document graph:
- `body` — one field name that reaches the text on either collector, because the
  web side puts it in `content` and the pdf side in `description`.
- `heading_path(edge, field, sep)` — the section's path, walking up.
- `subtree_concat(edge, field, sep, max_depth)` — the section's body, walking
  down in document order. Unlike a page's own flattened description this
  includes code blocks, tables and quotes.

Output is bounded and says so: the header reports rows returned over rows
MATCHED, and any truncation prints a line beginning `TRUNCATED by` naming the cap
that fired. Raise `max_rows` / `max_bytes` or narrow the body.

### Step 1c: Freeze the extraction — Author or Update Recipe

FREEZE ONLY WHAT IS WORTH RE-RUNNING: a source that gets new editions, or a shape
you will point at a second source. A one-off extraction does not need a saved
recipe at all — the rows you already read were the deliverable.

Freezing is a SAVE of the body you just iterated on, not a rewrite of it.

**New-recipe mode.** Canonical PDF template:

```
select section
filter page.metadata.heading_level ~= /^2$/
filter page.name !~ /\.\s+\.\s+\./           # drop TOC dot-leaders
filter page.name !~ /^Part [IVX]+/           # drop "Part I" headers
filter page.name !~ /^PART [IVX]*$/          # all-caps variant
filter page.name !~ /\| \d+$/                # drop "<title> | <pagenum>"
filter not(has_ancestor("CONTAINS", "symbol_name", "^(Foreword|Preface|Acknowledgments|How to Read This Book|About the Author|Revision History|Index|Bibliography)$"))
filter page.name !~ /^(Foreword|Preface|Acknowledgments|How to Read|About the Author|Revision History|Introduction|Setting the Stage|Conclusion|Index|Bibliography)/
filter page.name ~= /^.{4,80}$/              # name length sanity
emit pattern {
    type := "pattern"
    identity := page.name
    name := page.name
    summary := slice(children_concat("CONTAINS", "description", " "), "0", "200")
    description := children_concat("CONTAINS", "description", "\n\n")
    source := "<source-slug>"
    source_path := "<absolute-path-or-url>"
    source_page_first := page.metadata.page_first
    source_page_last := page.metadata.page_last
} as $pat
```

Web template uses `select page` and `description := page.description` directly. See existing recipes in `graph: "transformers"` for live web-source examples.

Store recipe:
```jsonc
mutate({
  "operation": "create", "graph": "transformers", "type": "recipe",
  "name": "<slug>-to-design-patterns",
  "content": "<DSL body>",
  "metadata": {
    "source_graph_type": "<web|pdf>",
    "target_graph_type": "practice",
    "target_name": "design-patterns"
  }
})
```

**Fix mode.** Pull existing recipe, edit content, `mutate(operation:"update")`. Common edits: tighter front-matter filters, and swapping `description := page.name` for `description := children_concat(...)` once you confirm the source's child nodes actually carry the body.

### Step 1d: Dry-Run

```jsonc
collect({
  "type": "<source-type>", "id": "<slug-or-pdf-path>",
  "transformer": "recipe", "recipe": "<recipe-name>",
  "dry_run": true
})
```

The run prints one line, and these are the tokens it actually uses:

`Ran recipe "<name>" over <type>/<slug> — emitted N nodes (skipped S, force-deleted D, lookups L/T, link misses M) in Xms.`

Healthy shape: `emitted` roughly one pattern per real article (title-only stubs filtered), `skipped` zero, `lookups` fully resolved — L equal to T — when the recipe has `lookup` rules, and `link misses` low. Emitted too high → chapter scaffolding is leaking; too low → the filters are too aggressive. Iterate the recipe.

### Step 1e: Real Run

```jsonc
collect({
  "type": "<source-type>", "id": "<slug-or-pdf-path>",
  "transformer": "recipe", "recipe": "<recipe-name>",
  "force": true
})
```

`force:true` deletes prior practice nodes whose `translated-from` edge Evidence names this source slug. Confirm `force-deleted` matches the prior pattern count and `emitted` matches the dry run.

#### force-rerun-same-target discipline

A force re-run REPLACES the previous run's emissions in the same target graph. So re-run a changed recipe against the SAME target it wrote before, rather than pointing it at a new target name: a new name leaves the old emissions in place, and the graph then carries two generations of the same source with nothing marking which is current. If you genuinely want a parallel copy, that is a new source slug, not a new target for the same slug.

### Step 1f: Reindex

```jsonc
manage({ "operation": "rebuild_segments", "graph": "practice", "name": "design-patterns" })
```

The background pipeline rebuilds search segments on its own cadence; run
`rebuild_segments` only when the new patterns must be searchable immediately.

Spot-check via `assemble({id: <pattern_id>})` — body should be coherent multi-paragraph extract, not title-only. If title-only, recipe didn't pick up children — verify edge type matches source graph storage (PDF uses `CONTAINS`).

**Extract and freeze done.** Surface results and pause.

## Stage 4: Hydrate (Interactive)

After the extract and freeze stages, ask the user:

> Extraction complete: N patterns. Ready to hydrate a slice of them?
> Reply: `none` (stop after extract) / `all` (hydrate every pattern — expensive) / `<comma-separated names>` / `--top K` (longest descriptions).

<constraint id="phase-2-token-cost-confirmation" severity="hard">

  <rule>
    Don't proceed silently into hydrate — ~50k output tokens per pattern.
    Wait for user's explicit choice. "all" requires double-confirmation.
  </rule>

  <reason>
    No-op "all" is the costly mistake to avoid. The user touch point here is
    legitimate — token-budget decisions belong to the user.
  </reason>

</constraint>

### Step 4a: Pick Hydration Targets

- `none` → done. Exit; the extraction stands on its own.
- `<names>` → list of specific patterns to refine.
- `--top K` → query recipe-emitted patterns sorted by `length(description)` desc, take K. Longest descriptions ≈ most substantive sections.
- `all` → confirm token budget once more, then proceed.

### Step 4b: Spawn Pattern-Ingester Per Target

<spawn id="pattern-ingester" background="true">

  <invocation>
    Agent(
      subagent_type: "pattern-ingester",
      prompt: "Refine the existing pattern node at id &lt;pattern_id&gt; in practice/design-patterns.bin.

The pattern was emitted by the '&lt;recipe-name&gt;' recipe and currently carries the section's verbatim body in `description`. Your job is to upgrade it to the agent quality bar:

1. Read existing node: assemble({id:'&lt;pattern_id&gt;'}). Note the raw body.
2. Move raw body to metadata.source_excerpt: mutate({operation:'update', graph:'practice', language:'design-patterns', id:'&lt;pattern_id&gt;', metadata:{'source_excerpt': &lt;raw_description&gt;}}).
3. Author SYNTHESIZED original prose (2-4 paragraphs) describing the pattern, its shape, when it applies, tradeoffs. Replace description.
4. Add 3-5 applies-when use_cases, 2-3 avoid-when use_cases, 2-3 examples (with language + attribution metadata), 2-3 references including primary-source citation.
5. Spot-check via assemble. Verify all four sections (Applies / Avoid / Examples / References) populated.

Source context: &lt;source-name + URL/path&gt;. Stop and ask if pattern boundaries are ambiguous (e.g. recipe-emitted section is actually two distinct patterns).",
      description: "Refine: &lt;pattern_name&gt;",
      run_in_background: true
    )
  </invocation>

  <parallel-rule>
    Spawn in parallel (background) when refining > 3 patterns.
    Surface completions as they come in; don't poll.
  </parallel-rule>

</spawn>

### Step 4c: Reindex After Each Batch

```jsonc
manage({ "operation": "rebuild_segments", "graph": "practice", "name": "design-patterns" })
```

Once per hydration batch (not per agent — batch-and-reindex is cheaper), and
only when immediate searchability matters; the pipeline rebuilds automatically.

## Closure

When user says done (or after `none`):

1. **Final reindex** (idempotent if Step 4c already ran).
2. **Close the associated work item** if any: `mutate({"operation":"update", "id":"<ticket_id>", "status":"closed"})`.
3. **No commit needed** — recipe + practice-graph writes are graph-resident.

<constraint id="ingest-patterns-anti-patterns" severity="hard">

  <anti-patterns>
    <pattern>Skipping extract even if you "know" the source needs full agent treatment — extraction enumerates sections you'd otherwise miss, and its rows are the dedup floor hydration works from</pattern>
    <pattern>Auto-hydrating every pattern by default — extract gives 100% coverage for nothing; hydrate is reserved for downstream-impactful patterns</pattern>
    <pattern>Reindexing per-pattern inside hydrate — batch-and-reindex once per user decision</pattern>
    <pattern>Spending model tokens during extract — extract is zero-LLM by design; if you find yourself summarizing rows to decide, you are hydrating early</pattern>
    <pattern>Writing to practice/knowledge-architecture.bin — that's the per-project catalog, hand-maintained; only design-patterns for library ingestion</pattern>
    <pattern>Discarding recipe-emitted body when refining — move to metadata.source_excerpt so verbatim text remains accessible</pattern>
  </anti-patterns>
