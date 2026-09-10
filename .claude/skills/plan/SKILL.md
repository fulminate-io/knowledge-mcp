---
name: plan
description: Produce and audit the prefill for a validated ticket — the implementer's preloaded context as a plan node with no steps. Gates on the ticket's validation stamp, spawns the planner, resolves every citation mechanically, spawns the prefill reviewer for its one round, has the planner apply every finding, and ships to implementation. Use after a ticket is validated and before any implementation.
argument-hint: <validated ticket id or name>
---

# Prefill: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For pipeline discipline (background spawns, one writer per artifact, verdict
routing) reference /orchestrate. This skill is prefill-specific.
</precedence>

<mental-model>
ticket = what the user needs · prefill = the implementer's context ·
review = the prefill's accuracy bar against the ticket.
A prefill contains no steps and no criteria; the implementer chooses its own
order and writes its own tests from the what-to-test list.
</mental-model>

## Step 0: Gate on the ticket

Fetch the ticket by id with metadata. `metadata.validated` must be present and
name a research node; without it, stop and route to /research. Read the
ticket's open items: an undecided design the user owns is settled with the
user before a planner spawns, never left for the planner.

`manage({"operation":"status"})`; collect if the index is behind the tree the
ticket lands on.

## Step 1: Spawn the planner (background)

One planner, one prefill. The brief carries the ticket id, the tree and branch
to resolve at, sibling work in flight on the same branch with its touched
files, the user's load-bearing rules verbatim, and the reads to stamp. It
names no design and no mechanism.

```
Agent(subagent_type: "planner",
      prompt: "Produce the prefill for ticket <id> at <tree> on <branch>, per the prefill rulebook: no steps, every line resolved by tool with its command, the what-to-test list covering every numbered requirement, the harnesses that reach each seam. Siblings in flight: <list or none>. Deliver the report to main.",
      description: "Prefill: <ticket>",
      run_in_background: true)
```

## Step 1a: Settle open items before anything freezes

When the planner's report carries open items, settle every one the ticket,
the user's rules or memory already answers, and route the rest to the user,
before resolving citations. Each settlement goes back to the planner that
raised it, idle with its context intact, to incorporate into the prefill. The
prefill is not frozen for review until that planner has read the settlement
back into the node. At reviewer spawn the ticket's `updated_at` is older than
the prefill's, or the prefill goes back to the planner first. A ticket
amendment after that point reopens the prefill, never the audit.

## Step 2: Resolve citations mechanically

Before any reviewer spawns, fetch the prefill's `citations` block and resolve
each entry yourself with its recorded command at the named tree (a scratch
copy or `git show <tree>:<path>`; never the shared checkout's working tree).
A citation that fails to resolve returns the prefill to a fresh planner with
the list attached; no reviewer audits a prefill with a dead citation. This is
the cheap half of the audit and it runs without a model.

## Step 3: Spawn the prefill reviewer (background)

One reviewer, one round. The brief carries the ticket id, the prefill id, the
tree, the user's rules verbatim, the isolation rule, and the fixed checklist
of miss classes the reviewer exhausts beside its own derivation of the scope.

```
Agent(subagent_type: "plan-reviewer",
      prompt: "Audit prefill <plan id> against ticket <ticket id> at <tree>, once and whole: coverage of every numbered requirement, every citation resolved by its command, censuses re-run, seams both sides real on the named harness, and the checklist: a credential-handling row wherever the change touches credentials, secrets or tokens; a stated red for every pending pin; test lists derived from the parity target's own suite; an independent expectation for every derived id or time value; every read outcome (denied, unreachable, partial, failed mid-page) reaching the completeness verdict; every premise checked against rulings recorded since the prefill resolved. Every finding carries its exact fix, because nobody audits after you. Persist findings; deliver verdict and tier counts to main.",
      description: "Audit prefill: <ticket>",
      run_in_background: true)
```

## Step 4: Apply the findings and ship

There is one review round, whatever its verdict and tiers. Route it:

- The same planner, idle with its context intact, applies EVERY finding in one
  pass by exact replacement with read-back and re-freezes the prefill. No
  second reviewer is spawned; you read the annotations back against the
  sections yourself, confirm nothing the findings do not name moved, and
  invoke /implement.
- A finding the reviewer places under "resolvable only by" the implementer
  (behavior only the built change can show) is carried into the implementer's
  brief as a binding what-to-test entry, red then green, and the hand-off is
  recorded on the prefill root.
- A finding against the ticket's premises is not the planner's: route it to
  /research and hold the prefill.
- T1 and T2 counts go on the ledger as process signals with the hole named
  (ticket, prefill rulebook, brief, instrument); they never buy a second
  audit.

<constraint id="plan-discipline" severity="hard">
  Spawning a planner on a ticket without the validation stamp · a brief that
  names a design · spawning a reviewer before the citations resolved · spawning
  a reviewer against a ticket amended after the prefill froze · settling a
  planner's open item on the ticket without sending it back to the planner ·
  resuming a planner whose prefill is under review · a second review round on
  a prefill · an implementer spawned before the planner's fix pass is read
  back.
</constraint>
