---
name: plan-structure
description: Action rulebook for plan shape — phases across context boundaries, phases are not commit units, reproduction-before-regression, perf shape, literals' hidden claims, lifecycle/crash-window obligations, critical-review structure. Not user-invocable.
user-invocable: false
---

# PLAN-STRUCTURE — phases, lifecycle, perf, and the shape of a plan

<!-- version: 1 -->
<!-- Read at: plan authoring, before the first step is written; audit step-walk. -->

## Phases survive context boundaries

Assume every phase is executed by a different implementer who never read the
others. Every cross-phase dependency is a LOCKED NAME (identifiers named at plan
time, repeated in creating AND consuming phases, matching exactly) or a WRITTEN
ARTIFACT (measurements, census outputs, red-first raw output — named, with a
completeness criterion; a phase whose predecessor's artifact is missing STOPS).
Prose-only prerequisites are can-kicking — hoist into steps with criteria.
Phases with disjoint FILES are not independent if their completion GATES span
each other's surfaces. Red-first degrades to red-NEVER across a boundary unless
the raw red output is a handoff artifact.

## Phases are not commit units

A phase is a work-and-review unit; the ticket's changeset lands as ONE commit at
completion. Never prescribe per-phase commits. Express ordering safety as
WORKING-TREE invariants: when an intermediate tree state between phases is
hazardous, name the hazardous state, add an always-on invariant-guard test red
in exactly that state, forbid real operations against a tree in it. For each
phase boundary, enumerate what running the system at that boundary's tree state
would do — final-state-only reasoning hides the worst hazards.

## Reproduction before regression

A defect-fixing step specifies a REPRODUCTION run RED FIRST against the unfixed
tree (naming the expected failure message, so a setup error is distinguishable)
and a REGRESSION that lives in the suite; state whether they are one test or
two. A reproduction that would also pass with the mechanism absent proves
nothing: asserting a control is CONFIGURED rather than that it ACTS; a validator
rejecting when nothing issues the good input; waiting on a signal nothing
raises; a fixture deriving two distinct values from one field. The reproduction
must COMPILE against the unfixed tree (raw literals over not-yet-existing
constants); label honestly which assertions start red vs which are
CHARACTERIZATION GUARDS — claiming a guard as red-first is a false statement
nobody re-runs the before-state to catch.

## Perf shape (first-class in this project)

For every step with non-trivial code, decide the perf shape at plan time citing
the in-tree primitive: CPU-bound per-item → the existing parallel primitives;
store/service loops → the batch helpers; graph reads → the indexes; hot loops →
hoist regexes, pre-size, marshal once. Serial is fine for single-call ops — say
so in a sentence. Never write anti-perf clauses into steps; if the ticket
carries one, surface it.

## Literals carry hidden second claims

Every LITERAL in a step body — a SQL default, config value, file destination,
grep pattern, third-party field name — carries a hidden second claim about the
SYSTEM THAT CONSUMES it: the driver's scan path, the file's remaining line
budget, the formatter that owns the byte layout, the linter's exclusions. Each
is usually ONE command to check — run it BEFORE the literal enters the step.

## Does the new code fit

A destination is a claim about the consuming system — the file's remaining
budget under whatever size gate the repo enforces at commit time. For any step
adding substantial code to an existing file: measure current size, add the
estimate, compare against the enforced cap, pin the result with a criterion.
Read the hook's own config for cap and glob. Watch the inverse: a plan
splitting unnecessarily may be designing around a broken gate.

## Surface and lifecycle discipline

- DECLARED-VERSUS-CONSUMED PARTITION: for any request/config/selector surface,
  every declared item is classified — consumed by this arm, or explicitly and
  namedly ignored — and every item the code reads is declared. The partition
  table derives FROM THE DISPATCH CODE with a parity assertion. Before wiring a
  strict rejection, verify the surface already declares everything it reads.
- COUNTS ARE COMMANDS: a tree-measured count enters the plan as the command
  that produced it plus a re-derive instruction; only plan-MANDATED counts are
  locked literals.
- TWO-STAMPER RULE: any predicate comparing or keying two values names WHO
  STAMPS EACH SIDE, by file and symbol. Where authorities differ, the
  comparison is a defect unless justified. Prefer REMOVING the comparison over
  tightening it. Where a key omits a dimension the data has, name and decide it.
- CRASH-WINDOW OBLIGATION: every step that deletes, prunes, supersedes, evicts,
  or reorders enumerates the intermediate states: what is durable at each
  instant, what a restart imports, what a concurrent pass observes.
  (a) DESTROY-BEFORE-PERSIST — does any step destroy a record a later consumer
  needs? (b) CONDITIONAL-PUBLISH WITH UNCONDITIONAL-KILL — does part two still
  run when part one was skipped?
- CEILING WITH THE PATH: any new or modified accumulation path declares its
  bound and truncation signal at plan time: ceiling constant, rationale, the
  truncation field the caller sees, and a criterion with a known-positive
  fixture proving the ceiling engages.

## Critical-review structure

When the ticket carries `metadata.critical_review` (auth, billing, security
boundaries, data integrity/deletion, perf-critical paths), the plan encodes
post-implementation review gates as REAL structure: after each implementation
phase on the critical surface, a review STEP with a machine-checkable verdict
CRITERION (report node id captured, tier counts stated, T1 = 0 AND T2 = 0
confirmed, naming what it blocks). Mark each boundary `review_mode: "pipelined"`
(default) or `"blocking"` (only where the next phase directly consumes this
phase's API). One CUMULATIVE whole-changeset review before deploy, naming the
cross-phase seams per-phase reviews cannot see. A flagged ticket whose plan
lacks these gates is incomplete.

## Contract over comments

Names, receivers, and package placement are NOT authority over the ticket.
Never scope a step down because a symbol LOOKS domain-bound — verify actual
behavior (body + callers) before scoping. Prefer REMOVING a cause over MANAGING
a hazard: before authoring a DO-NOT block to let two things coexist, ask whether
the weakest-justified side can be dropped so the collision becomes impossible.
