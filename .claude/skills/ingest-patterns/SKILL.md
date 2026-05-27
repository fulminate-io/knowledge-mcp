---
name: ingest-patterns
description: Ingest design patterns from an authoritative source (book, public catalog, reference site) into the practice/design-patterns.bin library graph. Two-phase workflow — recipe-driven bulk pass first (cheap, ~minutes/source), then optional hand-crafted refinement on selected patterns via the pattern-ingester agent (high-fidelity synthesis of use_cases / examples / references). Default to recipe-only and refine selectively; full agent runs are reserved for sources where every pattern needs decision-grade synthesis.
argument-hint: <source-slug-or-pdf-path-or-ticket-id>
---

# Ingest patterns: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline reference /orchestrate.
This skill is pattern-ingestion-specific.
</precedence>

Two-phase ingestion:

- **Phase 1 (always)** — recipe-driven bulk pass. Pattern node per real source section, body folded in via `children_concat`. Near-zero output-token cost; replayable on edition updates.
- **Phase 2 (optional, scoped)** — hand-crafted refinement of selected patterns via `pattern-ingester` agent. Synthesizes original prose + applies-when/avoid-when use_cases + working code examples + primary-source references. ~50k output tokens per refined pattern.

<constraint id="phase-2-default-off" severity="hard">

  <rule>
    Default is Phase 1 only. Don't auto-refine all patterns.
  </rule>

  <override-default>
    Trained instinct: produce maximum-quality output, refine everything.
    Wrong here — refinement budget should track downstream design impact,
    not abstract interestingness.
  </override-default>

  <when-to-refine>
    Pattern actually shapes design decisions in downstream /brainstorm or /plan sessions.
    Not: "pattern looks interesting in the abstract."
  </when-to-refine>

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

## Phase 1: Recipe Pass

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

### Step 1b: Author or Update Recipe

**New-recipe mode.** Canonical PDF template using v4 builtins:

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

**Fix mode.** Pull existing recipe, edit content, `mutate(operation:"update")`. Common edits: tighter front-matter filters, swap `description := page.name` for `description := children_concat(...)` after v4 lexer fix.

### Step 1c: Dry-Run

```jsonc
collect({
  "type": "<source-type>", "id": "<slug-or-pdf-path>",
  "transformer": "recipe", "recipe": "<recipe-name>",
  "dry_run": true
})
```

Healthy shape: `wrote ≈ N` (one pattern per real article — title-only stubs filtered), `link_misses` low, `lookup_misses=0` if no `lookup` rules, `skipped=0`. Too high → chapter scaffolding leaking; too low → filters too aggressive. Iterate the recipe.

### Step 1d: Real Run

```jsonc
collect({
  "type": "<source-type>", "id": "<slug-or-pdf-path>",
  "transformer": "recipe", "recipe": "<recipe-name>",
  "force": true
})
```

`force:true` deletes prior practice nodes whose `translated-from` edge Evidence names this source slug. Confirm `force_deleted` matches prior pattern count + `wrote` matches dry-run.

### Step 1e: Reindex

```jsonc
manage({ "operation": "rebuild_bm25", "graph": "practice", "name": "design-patterns" })
manage({ "operation": "rebuild_hnsw", "graph": "practice", "name": "design-patterns" })
```

Spot-check via `assemble({id: <pattern_id>})` — body should be coherent multi-paragraph extract, not title-only. If title-only, recipe didn't pick up children — verify edge type matches source graph storage (PDF uses `CONTAINS`).

**Phase 1 done.** Surface results and pause.

## Phase 2: Refinement (Interactive)

After Phase 1, ask user:

> Recipe pass complete: N patterns emitted. Ready to refine selectively?
> Reply: `none` (Phase 1 only) / `all` (refine every pattern — expensive) / `<comma-separated names>` / `--top K` (longest descriptions).

<constraint id="phase-2-token-cost-confirmation" severity="hard">

  <rule>
    Don't proceed silently into Phase 2 — ~50k output tokens per pattern.
    Wait for user's explicit choice. "all" requires double-confirmation.
  </rule>

  <reason>
    No-op "all" is the costly mistake to avoid. The user touch point here is
    legitimate — token-budget decisions belong to the CEO.
  </reason>

</constraint>

### Step 2a: Pick Refinement Targets

- `none` → done. Exit.
- `<names>` → list of specific patterns to refine.
- `--top K` → query recipe-emitted patterns sorted by `length(description)` desc, take K. Longest descriptions ≈ most substantive sections.
- `all` → confirm token budget once more, then proceed.

### Step 2b: Spawn Pattern-Ingester Per Target

<spawn id="pattern-ingester" background="true">

  <invocation>
    Agent(
      subagent_type: "pattern-ingester",
      prompt: "Refine the existing pattern node at id &lt;pattern_id&gt; in practice/design-patterns.bin.

The pattern was emitted by the '&lt;recipe-name&gt;' recipe and currently carries the section's verbatim body in `description`. Your job is to upgrade it to the agent quality bar:

1. Read existing node: assemble({id:'&lt;pattern_id&gt;'}). Note the raw body.
2. Move raw body to metadata.source_excerpt: mutate({operation:'update', id:'&lt;pattern_id&gt;', metadata:{'source_excerpt': &lt;raw_description&gt;}}).
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

### Step 2c: Reindex After Each Batch

```jsonc
manage({ "operation": "rebuild_bm25", "graph": "practice", "name": "design-patterns" })
manage({ "operation": "rebuild_hnsw", "graph": "practice", "name": "design-patterns" })
```

Once per refinement batch (not per agent — batch-and-reindex is cheaper).

## Step 3: Closure

When user says done (or after `none`):

1. **Final reindex** (idempotent if Step 2c already ran).
2. **Close associated ticket** if any: `mutate({"operation":"update", "id":"<ticket_id>", "status":"closed"})`.
3. **No commit needed** — recipe + practice-graph writes are graph-resident.

<constraint id="ingest-patterns-anti-patterns" severity="hard">

  <anti-patterns>
    <pattern>Skipping Phase 1 even if you "know" the source needs full agent treatment — recipe enumeration catches sections you'd otherwise miss; recipe-emitted patterns serve as dedup floor for Phase 2</pattern>
    <pattern>Auto-refining all patterns by default — Phase 1 produces 100% coverage cheaply; Phase 2 is reserved for downstream-impactful patterns</pattern>
    <pattern>Reindexing per-pattern inside Phase 2 — batch-and-reindex once per user decision</pattern>
    <pattern>Writing to practice/knowledge-architecture.bin — that's the per-project catalog, hand-maintained; only design-patterns for library ingestion</pattern>
    <pattern>Discarding recipe-emitted body when refining — move to metadata.source_excerpt so verbatim text remains accessible</pattern>
  </anti-patterns>

</constraint>
