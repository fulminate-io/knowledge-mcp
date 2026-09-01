---
name: verification-evidence
description: Action rulebook for implementation verification — done-means-verified, same-probe red and green, never-fake-green, tests that cannot fail, zero-needs-a-known-positive, comments are part of the change, live claims need live probes. Not user-invocable.
user-invocable: false
---

# VERIFICATION-EVIDENCE — what "done" and "green" are allowed to mean

<!-- version: 1 -->
<!-- Read at: the implementation loop's VERIFY step (implementer, first time per
     plan) and the tester's EXECUTE step. -->

## Done means verified

Mark a step complete ONLY against verification run THIS turn whose output you
read. Before every completion: (1) the edit PERSISTED on disk (git status/diff
or re-Read), not merely "I issued an Edit"; (2) every criterion command EXECUTED
and returned this turn — real exit status, real output. If you catch a phantom
completion of your own, reopen the step, redo it, disclose plainly.

Every behavior the step AND ticket specify is in the diff — not just what
existing tests cover. Read step + ticket text as a CHECKLIST; decompose
compounds (the second clause of one sentence is the canonical silent drop). A
specified behavior with NO test that goes red when absent: ADD the
failing-when-absent test, or surface the unverified requirement LOUDLY. Green
proves what you BUILT works, never that you built everything specified.

## Same probe, red and green

Red-first means the EXACT command and selector that will later report green was
run against the unfixed tree and observed to FAIL — not a different invocation,
not an inspection. Paste both raw outputs. Treat as RED-NEVER: a runner
reporting no tests ran, a skipped harness, a build no-op'd by a missing tag, a
selector matching nothing. If the repro PASSES against unfixed code, STOP — do
not fix, do not weaken the test: either it doesn't exercise the defect, the
defect isn't present (the plan's premise is wrong — a genuine finding), or it's
conditional on state the test didn't establish. Determine which and report.

## Never fake green

Do NOT delete, skip, or comment out tests to pass. A test failing because your
step intentionally changes behavior gets updated to assert the NEW behavior —
say so. Deleting a test is correct ONLY when its surface genuinely no longer
exists, stated with the file:line proving it gone. Forbidden ways to leave a
failure standing: "pre-existing / not my regression", "out of scope", "flaky"
without a proven cited mechanism, skip/xfail/commented-out assertions. The one
escalation: the failure reveals the CODE is wrong — fix it, or surface the found
bug with evidence.

## A test that cannot fail is worse than none

- PROVE IT CAN FAIL: break the implementation, watch the test go red, check the
  message names the property. A test written after working code has never been
  red once — it needs the flip most and gets it least. Verify the edit landed
  and defeat the test cache for the experiment, or it manufactures the vacuous
  pass it hunts.
- An assertion is only as strong as the input space its fixtures span: before
  asserting a field stays empty, name the input that would populate it and
  confirm a case supplies it.
- A regression test pins the RULE, not the reproduction — cover the spellings
  nobody used on the day.
- A double standing in for a DEPENDENCY is fine; one standing in for the CODE
  UNDER TEST is the defect. A double taught to mirror the other side of a seam
  agrees with your implementation by construction.
- A fixture that builds the input the code will read tests the code against
  your ASSUMPTION — tie it to the real artifact, or assert the real one matches
  the fixture's shape.
- Where a value crosses a boundary, one test drives the real construction end
  to end; two tests flanking it prove neither side hands over.
- "Not covered here / gated separately" is a debt marker nobody collects: name
  the check covering the other side or record it as open. Never claim a test
  enforces an invariant unless that same change contains it.

## Zero needs a known-positive

Any test whose pass condition is a zero, an emptiness, or a set equality must
contain, in the same run, a case driving the same measurement non-zero — else a
counter never wired and a genuinely-clean result are indistinguishable.
Sharpest case: two sets that lost the SAME members are still equal — compare
against a fixture-derived constant, never one set's length against the other's.

## The record meets the work's standard

A MANUAL criterion is complete only when the evidence it names is recorded ON
the graph node — evidence living only in your report does not exist for the next
reader. An explanatory comment or finding claiming a causal mechanism asserts
something testable — EXECUTE the claim's negative before writing it.

## Comments are part of the change

When your edit changes what code does, routes, consumes, returns, or which
invariant holds, every comment and docstring the edit makes wrong is fixed in
the SAME step. Highest-risk: comments enumerating consumers/callers, describing
routing/dispatch, naming a return carrier, stating an invariant. After any move
or deletion, re-read every sentence describing the moved thing at BOTH ends. A
declared-but-inert lever is wired or removed, never left as a documented no-op.

## Live claims need live probes

Store-level verification never licenses a serving-level claim. If the
requirement is about what a caller observes, observe it as a caller. Store
verifies but serving does not → report AMBER: neither green nor a declared
failure; its only valid disposition is handing the asymmetry back.

## Hygiene around the gates

Run the project's configured pre-commit checks against EVERY file in `git
status --porcelain` — files you extended as much as files you created. Trust
the toolchain's test cache (no routine force-rerun flags; scope invocations to
what changed; background long commands). One commit per plan/changeset once
verified — never per phase; intermediate commits only where they protect real
value.
