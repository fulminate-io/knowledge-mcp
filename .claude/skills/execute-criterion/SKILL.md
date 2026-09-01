---
name: execute-criterion
description: Action rulebook for executing stored criteria — run the stored bytes from the graph, classify with pasted evidence, vacuous-pass shapes, skip-is-not-a-pass. Read at every criterion-execution flow step; not user-invocable.
user-invocable: false
---

# EXECUTE-CRITERION — running a stored gate and recording what it said

<!-- version: 1 -->
<!-- Read at: the flow step that executes criteria (planner after create_plan;
     reviewer during the audit's execution pass; implementer/tester at VERIFY). -->

## Run the stored bytes

A brief or plan naming a gate in prose is a POINTER you resolve in the graph.
Fetch the criterion node and run the command from THAT response, written to a
file (inline retyping mangles quoting classes — a measured false red). A file in
/tmp whose name matches the gate is another lane's materialization, possibly of
a superseded revision. Establish an instrument's provenance in one call before
theorizing about its result; after transcribing any gate, prove your copy can
fail (one documented red) before trusting its green. Plans are revised while you
work: fetch at the START of each unit, never from an earlier read.

## Classify with evidence

Every execution is classified with observed exit status and first output line
PASTED: FAILS-AS-EXPECTED · PASSES-ALREADY (legitimate only for labeled
characterization guards and scope fences; otherwise vacuous — a finding) ·
FAILS-MALFORMED (a finding) · NOT-RUN (concrete reason; a destructive command is
not run — that is itself a finding). A label without pasted evidence, or
contradicted by your run, is a finding about the rigor story.

## Vacuous-pass shapes (run, don't inspect — each looked right and wasn't)

always-exit-0 pipelines (`&& echo BAD || echo OK`) · wrong-module `go test
./...` under a go.work (cd into the module in the command) · `-run` matching
nothing (anchor the runner's `--- PASS: Name ` line — trailing space, never
`$`-anchored; `$`-anchor the selector) · missing build tag (a linter/compiler
not told a tag never checks those files) · empty capture coerced to 0 (`test ""
-eq 0` passes in zsh; guard with `test -n`) · a condition already true
pre-implementation · a gate whose script or target the project never defined ·
unquoted glob making n=1 on an empty dir · zsh not word-splitting unquoted
expansions (garbage exit 1 mimics your expected red) · a FAILS-AS-EXPECTED probe
on a conjunction proving only the first failing leg (satisfy the cheap legs so
every expensive leg executes once).

## Skip is not a pass

A criterion that SKIPPED its real check did not pass: a harness printing
"SKIPPED — dependency absent", a run matching zero tests, a build no-op'd by a
missing tag. Diagnose, re-run, or surface as not-validly-executed. A cancelled
or unseen batch counts as NOT RUN.

## Run what you prescribe

A command you propose AS A FIX is a claim, executed before you file it — against
the real corpus, not a narrowed sample. The tell: a fix whose command differs in
ANY character from the one you executed. A prescribed gate unsatisfiable by
correct work is worse than the gap it closes: the only route to green becomes
deleting the evidence the ticket asked for.

## An all-green sweep over absent artifacts indicts the harness

An all-green execution sweep over a plan whose artifacts do not exist yet is
prima facie evidence YOUR harness is broken (an empty command executed N times
reports N greens) — re-fetch commands by id with metadata and re-run before
believing any green.

## Plan against the working tree

The working tree is ground truth; uncommitted work is often deliberate. Know
which load-bearing facts are uncommitted and say so. Whole-tree measurements can
be moved by foreign dirty files — check git status, attribute or exclude, and
never record a dirty-tree result as a property of HEAD.
