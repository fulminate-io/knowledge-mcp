---
name: audit-plan
description: Action rulebook for the adversarial plan audit — intent-fidelity diff, requirement→criterion coverage, rule-vs-number check, post-edit artifact synthesis, criteria-verify-the-thing, census verification, coverage-claim evidence, tangential findings. Not user-invocable.
user-invocable: false
---

# AUDIT-PLAN — the adversarial audit procedure detail

<!-- version: 1 -->
<!-- Read at: audit start (plan-reviewer), after loading ticket and plan. -->

## Intent-fidelity audit

For every rule the plan asserts, locate the ORIGINAL statement (ticket quote,
recorded decision) and diff semantically: duty-holder, cost-bearer,
prevent-vs-compensate, absolute-vs-best-effort. Drift is T1/T2 — it invalidates
every downstream artifact. A mechanism that only executes in a state the rule
forbids is evidence the premise twisted upstream — the mechanism's existence is
the finding even when its implementation is flawless. Vocabulary sweeps cover
inflections and verb forms; a suspiciously clean census widens the pattern.

## Requirement → criterion coverage

Walk In Scope as a checklist; every required behavior maps to a criterion that
would FAIL if absent; decompose compounds ("X and Y" is two behaviors — the
silent-omission path ships X, drops Y, looks complete). A specified behavior
with no failing-when-absent catcher is T2.

## Check each rule against the number beside it

For every RULE a plan states, check the FIGURE reported beside it is reachable
under the rule AS WRITTEN. The recurring defect: the author probed one way and
wrote the spec another, so the measurement was taken under corrected behavior
while the prose describes the uncorrected one. The tell: a figure that cannot be
derived from the rule printed next to it, or one BELOW an unfixed baseline. On a
heavily revised plan this is the single highest-yield check.

## Synthesize the post-edit artifact

Build the artifact the plan's own mandated text WOULD produce, then run every
gate against it. It answers in one pass whether any criterion is unsatisfiable
by the text its own step prescribes, and whether two criteria contradict each
other. Cheap whenever the edits are enumerable. Build the faithful variant too
(see the probe-a-gate rulebook).

## Walk criteria to their artifacts

For every criterion, identify the EXACT artifact it reads and verify against
THAT artifact, never the paragraph describing it. Two directed probes per plan:
CROSS-NODE CONTRACT DIFF (where two nodes produce and consume the same field,
diff exact spellings — two gates demanding opposite spellings of one field is a
proven finding class) and SCOPE CHECK (what scope does each rule sentence bind,
and does any gate apply it at a different scope?).

## Criteria verify the thing, not the pointer

The recurring shape: a criterion naming the right subject and asserting
something weaker than the property. Ask: what must be true for this to pass
while the defect is present? Named variants: THE ANALOG'S WIRING (a copied
component's registration lives in files the plan never opened); THE ANALOG'S
CONTROL FLOW (`*IfNotExists` skip-vs-merge decides whether a field ever
applies); THE GATE'S CONFIG (naming a linter/CI check rests on config that may
exclude the very paths). For provisioning calls, IAM grants, registrations,
resource bounds, external gates: require DEPLOYED-STATE or behavioral
observation — "the request sets X" is not "X is set". Rank vacuous criteria by
blast radius.

## Your prescriptions are criteria too

Every fix you prescribe is held to the standard you audit by: validate against
the plan's MANDATED values and the current tree, including the disproving
direction; simulate across the plan's PHASE SEQUENCE; when a phase's artifacts
have landed since the last audit, re-execute every criterion that reads them.
Absence claims: run the query shaped to find the thing where it would live.

## Practice-graph check on design-bearing steps

For each step prescribing an algorithm or design mechanism, search the practice
graphs YOURSELF at implementation vocabulary (3-5 phrasings from what the code
actually does, never ticket-title wording). Plan cites a pattern → verify the
citation and adherence. Plan says "no match" → re-run with YOUR phrasings before
accepting absence. Plan silent and your search finds an applicable pattern → a
finding (T2 when the pattern names a failure mode the design has). A
design-bearing step with no citation and no recorded search is a T3.

## Census verification (sweep/migration plans)

Re-run every census with the plan's own recorded commands — never trust its
counts; probe with a BROADER pattern; hand-enumerated counts are T2; completion
gates re-run the census asserting remainder = 0 by kind; a multi-kind migration
without a checked-in census script emitting a manifest is T2.

## Coverage claims carry evidence

Never write "every", "all", "nowhere", "the only" without enumerating the whole
corpus; if you sampled, say so and name what you skipped. Before filing an
absence finding: name the corpus that would have to be complete, confirm you
covered THAT corpus, and re-fetch thought/finding nodes unprojected. Verify
claims that AGREE with your expectations with the same rigor as costly ones. A
refuted finding is withdrawn unconditionally and plainly — a defended dead
finding costs the planner a cycle and you the credibility that makes real
findings land.

## Tangential findings

A small correctness gap in code you read, related but out of ticket scope, gets
no tier — a separate TANGENTIAL FINDINGS section with four fields: serves the
ticket's spirit (one sentence); DEFECT magnitude (stated separately from fix
size); fix size (production lines + criteria); proof grade PROVEN vs SUSPECTED.
Do not fix, tier, or frame as optional.

## Reachability and fences

For any hazard the plan calls unreachable or fenced: enumerate the conjunct
sites yourself, AND verify the guard operates on a conjunct the reachable flow
actually exercises — a write-side guard does not fence a read-side seam.
