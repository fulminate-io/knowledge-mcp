---
name: author-a-corpus-check
description: Action rulebook for corpus checks — when a criterion is really a check, defect classes owe class checks, fixture-pair discipline, auditing and executing check-backed criteria. Not user-invocable.
user-invocable: false
---

# AUTHOR-A-CORPUS-CHECK — durable structural gates in the checks graph

<!-- version: 2 -->

WHY THIS SYSTEM EXISTS (from its owner): grep gates were being shoe-horned onto
structural assertions, and a grep is too narrow to catch a broad defect class —
it matches one spelling while the class relocates one construct over. Corpus
checks are the first-class instrument for shape assertions: pattern-matched
against parsed syntax, admitted only with a proven fixture pair, durable across
plans. Reaching for grep on a shape is the defect this rulebook exists to end.
<!-- Read at: any step that fixes a defect with a structural signature, authors a
     shape-asserting criterion, or audits/executes a check-backed criterion. -->

## When a criterion is really a check

When a criterion asserts a SHAPE in source — this call pattern is gone, this
construct never appears inside that one — author it as a corpus check
(`graph:"checks"`) and have the criterion name it, instead of a grep command. A
check cannot enter the graph until it has FIRED on a bad fixture and stayed
SILENT on a good one, so admission IS execution and the evidence label is earned
mechanically. The bad fixture is the defect's own shape; the good one is a
NEAR-MISS carrying the same construct where it is legitimate — an unrelated file
proves nothing. A fixture pair proves only the axes it VARIES: give the good
fixture one case per axis the check claims to discriminate on.

## Defect classes owe class checks

When work fixes a defect whose signature is expressible as a code shape (an
`ast` pattern, optionally with a where-tree or dataflow leg), the CLASS check is
authored alongside the instance fix. A fixed instance without its class check
leaves the next author free to reintroduce the shape. The honest split: a check
enforces shape or declaration PRESENCE deterministically; semantic truth stays a
review duty — a check pretending to adjudicate semantics it cannot see is worse
than none. Criteria that are not statically decidable (a rollout landed, a human
accepted, a latency held) stay commands or manual, labeled honestly.

## Auditing a check-backed criterion

A named check has passed admission — that proves it is not inert, NOT that it is
narrow. Audit the fixture pair, not the verdict: which axes does the pair vary?
Run the known-positive control on the PATTERN itself — swap a load-bearing
literal for a value that cannot exist and re-run against real source; an
unchanged match count means that literal never constrained anything. Then READ
the hits — a count is not a finding, and an overbroad pattern is fastest to
expose by looking at what it caught.

## Executing a check-backed criterion

RUN it against the tree this turn like any other gate — nothing about a passed
admission is persisted against your diff. Read the hits, not the count; a hit
you cannot explain is a finding to surface. If the check is wrong — overbroad,
or silent on the thing the step changed — that is a plan defect to surface,
never a pattern to quietly widen or narrow so the step goes green.

## Runs are documented by the runner

The tool does not record runs and its executed-count is a floor (a check that
ran and matched nothing leaves no trace). When a check run is part of your
evidence, record it yourself: the corpus walked, the checks run, and what the
flagged or clean result means. A bare "checks passed" cites an artifact that
does not exist. Existing checks covering the shapes a plan touches are run over
the plan's surface as part of any audit — an existing check the edits would
break is a scheduled red; an existing check that blesses a shape the plan
retires needs a disposition in the plan, not silence.
