---
name: select-patterns
description: Action rulebook for attaching patterns to tickets — prescriptive pattern_ids vs defensive language_patterns, the attachment decision table, fan-out discovery, and dead-pattern review. Read at ticket pattern-selection time. Not user-invocable.
user-invocable: false
---

# SELECT-PATTERNS — what the planner will faithfully build to

<!-- version: 1 -->
<!-- Read at: ticket pattern-selection (brainstorm Step 3.5) and any pattern
     re-attachment to an existing ticket. -->

Tickets carry two independent pattern lists: `pattern_ids` (architecture —
PRESCRIPTIVE: the planner BUILDS to whatever is attached) and
`language_patterns` (language anti-patterns — DEFENSIVE: vigilance markers
shaping review, not the build).

## Attachment discipline

Attach pattern_ids when the pattern is structurally load-bearing — the work IS
an instance of the pattern, or the planner needs it as the canonical shape. Use
no_patterns_reason when none is. Don't ask permission to attach an obvious
match — and don't pile on mediocre matches to look thorough, because the
planner WILL build to whatever is attached.

| Condition | Action |
|---|---|
| auto-suggest ≥ 0.65 + obvious match | attach without asking |
| multiple high-confidence patterns shaping distinct facets | attach 3-4 max |
| medium 0.40-0.65 "kind of fits" | verify or skip — never attach mediocre matches |
| user says "no patterns" / "skip" | honor it; no_patterns_reason; do NOT counter-propose |
| trivial / doc-only / scope-narrow / sui-generis | no_patterns_reason |

Discovery fans out: patterns live across multiple practice graphs — search them
as a set (`search({graph:"practice", language:"all", queries:[...]})`). Never
conclude "no pattern fits" from a single-graph miss.

The failure this table exists for: a ticket attached a registry pattern because
it "kind of fit"; the planner faithfully built exported Register/Unregister
with panic-on-duplicate, a mutex, and dedicated tests — for a closed set of
three ops where a plain switch was one screen of code.

## language_patterns (defensive)

When the ticket's language has an anti-pattern corpus, enumerate
deterministically (`query({graph:"practice", language:"<lang>",
type:"finding", meta:{"dsl_pattern":"*"}, fields:[...]})`). Attach only when
the implementation surface plausibly touches the anti-pattern AND the user
agrees — 3-4 strong matches is plenty; never bulk-attach. No language surface →
leave empty; the empty case is the default.

## Dead-pattern review

For each pattern encountered, check live usage:
`traverse({start:"<pattern_id>", edge_types:["uses"], direction:"in"})`. Zero
incoming edges = dead-candidate → ask the user: keep / update / delete.

## Auto-suggest calibration

With no_patterns_reason set, ticket creation runs a cross-practice BM25 fan-out
over name + description; vocabulary decides the outcome: lead the ticket NAME
with pattern nouns, and let the description's first sentence name the
architectural concern and target shape. Pure bug fixes / UI tweaks / doc edits:
no_patterns_reason is correct — don't game the auto-suggest.
