---
name: price-an-operation
description: Action rulebook for wall-clock governance — pricing verification scope before issuing it, the marginal-value test for hours-scale work, hard-stop mechanics, and gate-state as a timestamped observation. Read before verification-scope rulings and long-running dispatches. Not user-invocable.
user-invocable: false
---

# PRICE-AN-OPERATION — wall-clock is the orchestrator's to spend

<!-- version: 1 -->
<!-- Read at: before issuing any verification-scope ruling or long-running
     dispatch; before approving a scope widening. -->

Only the orchestrator can see whether an operation is serial on a lane's
critical path — a subordinate prices its own step, never the pipeline. Every
verification-scope ruling is priced BEFORE issue; every long-running dispatch
carries a time expectation; every brief carries the standing rule that any
single operation projected over ~15 minutes is named before it runs. "More
verification" is never free — and the failure is seductive after a run of
scope-too-narrow corrections: when "wider" was the fix five times, the sixth
widening gets approved unpriced. That is the moment to price it.

## The marginal-value test

Hours-scale work needs a written marginal-value sentence: what does this buy
that a cheaper instrument does not? No sentence → the work does not run. Common
case: whole-package suites already running as boundary gates subsume most of a
per-criterion sweep over an untouched sibling surface — the narrow form plus a
STATED non-coverage sentence beats an unbounded re-run. Hours-scale trades go
to the USER with the price attached.

The tell: writing "budget the N hours" in a dispatch — a cost estimate
processed as logistics instead of as a decision input. A duration in a
subordinate's report IS a decision input. Test plans mandating long execution
carry per-test time estimates and a stated total.

## Hard-stop mechanics

When the user orders running work halted, a mailbox message is NOT a stop. Stop
the task (kill it), verify no orphaned processes survive, then instruct on
resume with the redirect as the first thing read. Preserve partial results —
evidence already paid for is kept.

## Gate state is a timestamped observation

A criterion's measured state is an observation with a timestamp, not a
property — trees move under long-running lanes. Before publishing any
gate-status expectation list, re-measure against the CURRENT tree in the same
act. A stale red sends an implementer after finished work and erodes the
list's authority.
