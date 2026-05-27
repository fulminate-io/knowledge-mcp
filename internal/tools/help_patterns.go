// SPDX-License-Identifier: Apache-2.0

package tools

const helpPatterns = `# Pattern Catalog

The pattern catalog lives in two roles across multiple practice graphs (the practice graph partitions by ` + "`language`" + ` slug — overloaded here for non-language slugs):

**Architecture patterns (PRESCRIPTIVE — wired via ` + "`pattern_ids`" + ` + ` + "`uses`" + ` edges):**

- ` + "`language=\"knowledge-architecture\"`" + ` — concrete patterns instantiated in this codebase. 11 entries today (see survey).
- ` + "`language=\"design-patterns\"`" + ` — codebase-agnostic library of generic templates. Earned bottom-up: only patterns that have ≥1 concrete instance in a project graph.

**Language patterns (DEFENSIVE — wired via ` + "`language_patterns`" + ` + ` + "`audits`" + ` edges):**

- ` + "`language=\"go\"`" + `, ` + "`language=\"python\"`" + `, ` + "`language=\"typescript\"`" + `, etc. — language-specific anti-patterns and best-practices, identified by ` + "`type=\"finding\"`" + ` + ` + "`metadata.dsl_pattern`" + ` set. The Go corpus has 19+ entries as of 2026-05 (e.g., http.DefaultClient, sync.Map, exec.CommandContext without LookPath). The scanner worker enumerates these via ` + "`query({graph:\"practice\", language:\"go\", type:\"finding\", meta:{dsl_pattern:\"*\"}, fields:[...]})`" + `.

The two roles are independent on tickets/plans — a ticket can carry any combination of ` + "`pattern_ids`" + ` (architecture) and ` + "`language_patterns`" + ` (language). The planner builds to architecture; the reviewer audits the plan against language smells.

## Querying the catalog

  query({ "graph": "practice", "language": "knowledge-architecture" })
  query({ "graph": "practice", "language": "knowledge-architecture", "text": "registry" })
  query({ "id": "<pattern_id>", "graph": "practice", "language": "knowledge-architecture" })
  assemble({ "id": "<pattern_id>" })

` + "`assemble`" + ` walks the child tree and renders ` + "`## Applies when`" + ` / ` + "`## Avoid when`" + ` / ` + "`## Examples`" + ` / ` + "`## References`" + ` from the use_case, example, and reference nodes linked off the parent.

## Pattern node fields

Patterns are parent nodes with typed child nodes linked via edges — not a single monolithic markdown blob. The parent carries naming and prose; children carry the situational / illustrative material that ` + "`assemble`" + ` renders into sections.

  pattern (parent)       Name=slug (e.g. ` + "`fan-out-fan-in`" + `), Summary=one-liner, Description=long-form prose describing what the pattern is. Content unused on parents.
  use_case (child)       Linked from parent via ` + "`applies-when`" + ` edge (positive: reach for this when …) or ` + "`avoid-when`" + ` edge (negative / anti-pattern: do NOT use when …). Name=short title, Description=the situation.
  example (child)        Linked from parent via ` + "`contains`" + ` edge. Content=code snippet verbatim. Metadata.language=fence tag (` + "`go`" + `, ` + "`python`" + `, …). Metadata.attribution=source (e.g. ` + "`MIT — kat-co/concurrency-in-go-src`" + `).
  reference (child)      Linked from parent via ` + "`references`" + ` edge. Metadata.url / .title / .book / .page / .line — any subset; ` + "`assemble`" + ` formats whatever is present.

## Authoring a new pattern

No ` + "`create_pattern`" + ` batch handler yet (known followup) — author with a sequence of mutate calls.

Step 1 — create the pattern parent:

  mutate({
    "operation": "create", "type": "pattern",
    "graph": "practice", "language": "design-patterns",
    "name": "fan-out-fan-in",
    "summary": "Split work across N goroutines, merge results on a single channel.",
    "description": "Producer dispatches items to a pool of worker goroutines; a merger collects their outputs into one downstream channel."
  })

Step 2 — create each use_case and link with ` + "`applies-when`" + ` (positive) or ` + "`avoid-when`" + ` (negative):

  mutate({
    "operation": "create", "type": "use_case",
    "graph": "practice", "language": "design-patterns",
    "name": "parallelizable-work",
    "description": "The same operation applies to many items independently; order of completion does not matter."
  })
  mutate({
    "operation": "link", "from": "<pattern_id>", "to": "<use_case_id>",
    "relationship": "applies-when",
    "graph": "practice", "language": "design-patterns"
  })

  mutate({
    "operation": "create", "type": "use_case",
    "graph": "practice", "language": "design-patterns",
    "name": "strict-ordering-required",
    "description": "Downstream consumers require items in submission order — fan-out breaks that contract."
  })
  mutate({
    "operation": "link", "from": "<pattern_id>", "to": "<use_case_id>",
    "relationship": "avoid-when",
    "graph": "practice", "language": "design-patterns"
  })

Step 3 — create each example with language + attribution metadata, link via ` + "`contains`" + `:

  mutate({
    "operation": "create", "type": "example",
    "graph": "practice", "language": "design-patterns",
    "name": "fan-out-fan-in-basic",
    "content": "<code snippet verbatim>",
    "description": "Basic fan-out goroutine pool with channel merge.",
    "metadata": { "language": "go", "attribution": "MIT — kat-co/concurrency-in-go-src" }
  })
  mutate({
    "operation": "link", "from": "<pattern_id>", "to": "<example_id>",
    "relationship": "contains",
    "graph": "practice", "language": "design-patterns"
  })

Step 4 — create each reference (book, blog, repo) with citation metadata, link via ` + "`references`" + `:

  mutate({
    "operation": "create", "type": "reference",
    "graph": "practice", "language": "design-patterns",
    "name": "Concurrency in Go — Cox-Buday 2017",
    "metadata": { "book": "Concurrency in Go (O'Reilly, ISBN 9781491941195)", "page": "108", "url": "https://www.oreilly.com/library/view/concurrency-in-go/9781491941294/" }
  })
  mutate({
    "operation": "link", "from": "<pattern_id>", "to": "<reference_id>",
    "relationship": "references",
    "graph": "practice", "language": "design-patterns"
  })

## Cross-graph link to a library entry

When a project pattern is an instantiation of a generic library pattern, link them with the ` + "`instantiates`" + ` edge:

  mutate({
    "operation": "link",
    "from": "<project_pattern_id>",
    "to": "<library_pattern_id>",
    "relationship": "instantiates"
  })

## Promotion / staleness

- Project patterns gain status="emerging" on first use, "validated" on second.
- Stale-pattern invalidation (T3): orphan project patterns whose exemplars all disappear are auto-deleted; library-linked patterns get marked staleness=high instead.
- Library patterns are never stale-deleted — they're codebase-agnostic.

## Planner gate

create_plan / create_ticket requires exactly one of pattern_ids, no_patterns_reason, or proposed_patterns (the architecture-pattern tristate). Broken pattern_ids produce a non-fatal warning surfaced in the response under a ` + "`## Warnings`" + ` section.

` + "`language_patterns`" + ` is INDEPENDENT of that tristate — empty/non-empty is a free choice. Broken language_pattern_ids produce a parallel non-fatal warning ("language_pattern_id %q ..."). Validation accepts ` + "`type=finding`" + ` with non-empty ` + "`metadata.dsl_pattern`" + ` OR ` + "`type=pattern`" + `; anything else is unresolved.

See help("create_plan") and help("create_ticket").
`
