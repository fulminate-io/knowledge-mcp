---
name: revise-plan
description: Action rulebook for plan revisions — the five revision laws (self-execute repaired gates all directions, class sweeps, corrections land as gates, full-scope final round, convergence termination) and revision sweep hygiene. Not user-invocable.
user-invocable: false
---

# REVISE-PLAN — the revision laws

<!-- version: 2 -->
<!-- Read at: entry to any directed revision (planner or reviewer applying a
     correction to an existing plan). Not read on a fresh plan. -->

Measured across this project's own review loops: the majority of later-round
findings are located in or caused by the previous round's fix — revisions create
the next round's defect surface. These laws exist to make a revision leave the
plan STRONGER than the finding found it.

## THE FIVE REVISION LAWS

1. **SELF-EXECUTE EVERY REPAIRED GATE, ALL DIRECTIONS, BEFORE RESUBMIT.** A
   repaired criterion is re-run red (against the violating state), green
   (against correct/control state), and — where the gate's subject is a
   container/placement choice — against the plausible-wrong third input. The
   next reviewer's execution is an adversarial second opinion, never the first
   run of your repair. Record the runs on the criterion (evidence_red /
   evidence_green updated to the repaired bytes).
2. **SWEEP EVERY FIX TO ITS CLASS.** A point-fix is not resubmittable. Before
   resubmit, sweep every sibling sharing the defect's shape — same grepped
   symbol, anchor style, selector form, stale numeral — and fix or explicitly
   clear each member. The class the reviewer found one instance of is your
   enumeration duty now.
3. **EVERY CORRECTION LANDS AS A GATE, NEVER PROSE ALONE.** A hard-won
   correction written into step prose but not into a criterion is invisible to
   the implementer and unenforceable by the next audit. If the correction
   constrains behavior, it gets a criterion (or amends one); prose may explain,
   never carry.
4. **THE FINAL PRE-SHIP ROUND IS FULL-SCOPE.** Delta re-audits are legitimate
   mid-loop; the round that precedes ship re-verifies the WHOLE plan — residue
   outside the delta is exactly what escapes to implementation.
5. **TERMINATION IS CONVERGENCE, NOT EXHAUSTION.** A loop ends when a full-scope
   round reports zero T1/T2 and no unproven evidence stamps — never because the
   verdict wording softened or patience ran out.

## Revision hygiene (the sweep discipline)

After ANY body edit: sweep old names and stale numerals across criterion
summaries, commands, implements edges, file_paths metadata, test names,
comments, and the node's own summary field (it does NOT auto-update) — plus
hedging language ("recommended", "pending", "deferred", "TBD") that outlived a
locked decision. Repeat until zero hits. Then re-read the touched steps'
criteria (and neighbors') against the new text. The sweep covers clauses
INTRODUCED BY THE SAME REVISION — the newest nodes are the least-swept.

BATCH THE GRAPH WORK: plan edits are turn-count-bound, and serial per-node
write-then-read-back loops are the dominant wall-clock cost of a revision. Use
the batch arms — `mutate(update_batch)` for per-item summary/keywords/metadata,
`mutate(bulk_update_metadata)` for metadata-only sweeps, `mutate(ids:[...])`
for a uniform scalar across plain nodes — then ONE `query(ids:[...],
fields:[...])` over the whole edited set as the read-back. The read-back
duty (the silent-drop trap) is satisfied by the batched fetch; N individual
write+read pairs where a batch exists is a revision defect.

RUN THE SWEEP AS A COMMAND, NOT A DISCIPLINE: fetch every node of the plan, grep
the whole set for each retired value, report the counts, and carry a
known-positive control in the same run. Fetch nodes WITH metadata and include
every metadata VALUE in the swept text — evidence fields carry load-bearing
numbers a description-level fetch never sees. A PROJECTED read is a scan whose
boundary is invisible in its own output: the field list is part of the sweep's
claim and is named in the report beside the counts.

After any edit to an authoritative vocabulary block: grep the plan body for
every symbol touched and confirm each restatement agrees — as a procedure step,
never best-effort.

On a directed revision: read the whole report, address every accepted finding,
never quietly reintroduce an addressed one, never pad with unrelated
improvements. The next audit is FRESH — fixes must be durable.

## Sampling before gating

A rule you gate is a claim about every file it governs — before a prescription
becomes a criterion, enumerate the population and check the rule against it; if
members already violate it, decide in writing whether the rule is wrong or the
members are defects.

## Generalize every fix (auditor's half)

On finding one instance, sweep the whole criterion set for the class and report
the full member list — fixed-one-left-the-sibling is the recurring failure, in
plans and in audits. When you correct your own method mid-audit, RE-RUN the
corrected method over everything already processed before reporting.
