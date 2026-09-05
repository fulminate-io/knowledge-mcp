---
name: implement
description: Implement a ticket from its reviewed prefill, then audit the code. Gates on the review verdict, spawns one implementer in its own worktree, spawns a code reviewer against the ticket and the what-to-test list, routes the verdict, and lands the branch. Use after a prefill has shipped review.
argument-hint: <ticket id or name with a reviewed prefill>
---

# Implement: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For pipeline discipline (background spawns, one writer per artifact, verdict
routing, landing chains) reference /orchestrate. This skill is
implementation-specific.
</precedence>

<mental-model>
implementation = turning the ticket and its prefill into code and the tests
that prove it · code review = the implementation's accuracy bar against the
ticket. The implementer finishes the engineering between the lines itself and
stops only for decisions the user owns.
</mental-model>

## Step 0: Gate

The prefill's latest review verdict is `ship`. Fetch it; no implementer spawns
on an unreviewed or revise-verdict prefill. In the fast lane there is no
prefill: the ticket carries the lane determination with its reason (the
orchestrate skill's lane section), and the ticket's numbered requirements are
the what-to-test list. Create the implementer's worktree on the ticket's
branch at the current tip; record the tip.

## Step 1: Spawn the implementer (background)

One implementer, the whole ticket, one worktree, one commit. The brief opens
with this block verbatim:

```
EXECUTION DIRECTIVE. Ticket <id>; prefill <plan id, or "none: fast lane, the ticket's numbered requirements are the what-to-test list and the research findings are the touch points">; worktree <absolute path> on <branch> at <tip>.
Before your first write, confirm the worktree is at that tip and carries no other lane's work.
Build every numbered requirement. For every what-to-test entry: the test, red on the tree before your change, the change, green after, both pasted. Seams run both real sides on the harness the prefill names. Corpus checks over the touched shapes, hits read. Comments and docs the change made wrong are part of the change.
Where the prefill is silent, decide as a senior engineer on this codebase would and record the choice as a finding. Stop only for a decision the user owns: removing scope, a wire shape, a destructive operation, a security posture.
Tests run against spawned services on picked ports with an isolated home; the operator's services and stores are never touched. Never delete or skip a test to get green. Never -count flags, never a private build cache, never a git identity change, never --no-verify.
One commit on <branch>; do not push, rebase or merge. Report what is NOT done first, then the commit, the test table with red and green output, the seams, the checks, and the choices you made.
```

## Step 2: On return

Read the whole report. Verify against the tree, not the report: the commit
exists on the branch, the touched files match the prefill's touch points (the
research findings' touch points in the fast lane), no test was deleted or
skipped, the red and green output is pasted for every entry. A report with claims and no output goes back once with the gap named.

## Step 3: Spawn the code reviewer (background)

Fresh reviewer. The brief carries the ticket id, the prefill id, the commit
sha and branch, the user's rules verbatim, and the isolation rule.

```
Agent(subagent_type: "code-reviewer",
      prompt: "Audit commit <sha> on <branch> against ticket <id> and prefill <plan id>: every requirement built and observed by a test you re-ran red and green in a scratch copy, seams both sides real, input classes covered, checks run and hits read. Persist findings; deliver verdict and tier counts to main.",
      description: "Code audit: <ticket>",
      run_in_background: true)
```

## Step 4: Route the verdict

- `ship` → land the branch per /orchestrate (rebase, gates, fast-forward,
  push, confirm the remote, then remove the worktree), reindex, and move the
  ticket to the live confirmation.
- `revise` → the same implementer, once, with the findings attached (it owns
  the bug it shipped); then a fresh code reviewer.
- A second `revise` stops the chain and goes to the user with both audits.

<constraint id="implement-discipline" severity="hard">
  Spawning on an unreviewed prefill · more than one implementer per ticket
  without a file-disjoint, user-directed reason · gating phases with the user
  · landing before the code review ships · a landing chain whose destructive
  tail is not gated on a confirmed push.
</constraint>
