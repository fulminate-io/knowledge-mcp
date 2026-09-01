---
name: probe-a-gate
description: Action rulebook for proving a gate can catch its defect — both directions, the plausible-wrong third input, credited-gate probing, control path-sharing, faithful variants. Read at any step that credits, stores, or audits a gate; not user-invocable.
user-invocable: false
---

# PROBE-A-GATE — proving a gate can actually catch its defect

<!-- version: 1 -->
<!-- Read at: any flow step that credits, stores, or audits a gate's protective
     claim. Lens from the invoking step: author proves before storing; reviewer
     proves adversarially; implementer proves before trusting a named catcher. -->

## A probe that can only say yes

A criterion reporting success is UNVERIFIED until you have shown it capable of
reporting failure. Seeing green proves nothing on its own — the green may be the
only answer it can give. Before trusting any gate, construct the state it is
supposed to reject and confirm it goes red. The shapes differ every time: a
linter handed an empty file list reports "0 issues"; a test selector matching
nothing exits 0; a grep for `token=` is satisfied by a token with no value; an
empty capture coerces to 0 in a numeric test. Each produces a confident green;
the check is the same for all and costs one extra run.

## Both directions, then the third input

Ask of every gate: (1) does it fail if the implementer did NOTHING? (2) does it
pass against CORRECT work — including the plan's own prescribed text? A gate
that greps a literal your own step mandates, pins a count your prescribed text
changes, or breaks a fixture your surface touches is a scheduled false failure —
the more damaging direction, because its pressure is toward corrupting correct
work. Then stop reasoning and RUN it, both directions.

RED PLUS GREEN IS STILL NOT SUFFICIENT: a defective gate is often correct on
exactly those two inputs and wrong on a THIRD — the plausible-but-incorrect
implementation an honest engineer might write. Reach for it whenever the subject
is WHICH of several sibling containers holds a value: a grep proving a token is
somewhere in a region proves region membership, not the intended sub-container.
Construct the wrong-but-reasonable variant and run the stored command against
it. When tightening by narrowing to a sub-region, check the hazard the fix
creates: a region delimited by neighbouring declarations inherits an ordering
dependency nothing enforces — write the required order into the step.

## The inverse direction

Hunt the gates that FAIL against correct work — rarer to look for, more
damaging: the pressure is toward renaming correct symbols and widening correct
code until a broken gate goes green. The mechanism is usually tooling: a retired
identifier substringing a retained one; per-test output printed only on failure
at default verbosity; a bare-text grep a mandated comment matches; an undefined
invocation; a symbol renamed since authoring. Run gates against the plan's OWN
prescribed text.

## A credited gate is probed against its violation

Relying on an EXISTING gate instead of authoring a duplicate is right — but the
credit is a claim about what that gate's code asserts. Before writing "gate X
enforces property P": BUILD the violation of P and run X against it. Credit from
the run's output, never from the gate's name — and store the relied-upon gate's
bytes as a criterion in YOUR plan so it runs on your path.

## Controls must share the target's path — and localize

A known-positive control certifies only the read path it actually traverses.
Same run is necessary and NOT sufficient: draw the control from the SAME field,
same projection, same file set, same arm as the target. A control that fires
under every configuration proves the scan ran, not that it would have caught
THIS — pick the control that fires only when the specific setting or path is in
effect. A control confirming the hoped-for value without being able to produce a
DIFFERENT result under the alternative hypothesis is not a control.

## Fails-as-expected certifies only the executed prefix

A criterion observed red at its absent-target guard has proven only the guard —
everything after the first exiting statement is UNEXECUTED, and defects there
stay latent until the target exists, exactly when nobody is re-validating.
Record it as guard-verified; syntax-check stored commands under every shell they
may run in (`zsh -n` AND `bash -n`), and execute each command shape past its
guard at least once before trusting the class.

## Ungated invariants and faithful variants (audit sweep)

Collect every invariant sentence in ticket and plan prose and demand, for each,
the gate that fails when it is violated. When synthesizing the post-edit
artifact, also build at least one FAITHFUL VARIANT respelling everything the
plan does not prescribe (receiver names, locals, literal styles, formatting) and
run every gate against it — red on a faithful variant fails correct work; green
proved only against the author's own reference is the single-shape defect in the
passing direction.

## Pristine baseline for claims about the tree

A claim about the CHECKED-IN tree is measured against a pristine checkout —
never your working reference artifact, whose drift becomes false facts. The
reference build proves your design; only the pristine tree witnesses the
baseline. Name which instrument produced each figure.

## Prove the gate by breaking the code (executor's form)

When a plan claims a specific test catches a specific wrong implementation,
WRITE the wrong implementation in your worktree, run the suite, read which tests
go red. Minutes of cost; the only way to learn a control set is real. Revert
each mutation immediately; never commit one. A criterion that stays green
against a deliberately wrong implementation is a finding to report.
