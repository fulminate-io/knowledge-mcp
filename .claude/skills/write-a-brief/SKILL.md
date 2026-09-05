---
name: write-a-brief
description: Action rulebook for briefing a lane — discovery framing, no mechanisms, front-loading, verbatim relays, absolute paths, isolation, and the reads to stamp. Read before every spawn. Not user-invocable.
user-invocable: false
---

# WRITE-A-BRIEF — everything load-bearing goes in before the spawn

<!-- version: 2 -->
<!-- Read at: before every agent spawn. -->

## Discovery, never confirmation

A research brief names the problem and asks what already exists; it never
names a solution. An investigation brief carries the observation (what ran,
what it returned, on which tree and plane) and the instruments; it never
carries a candidate mechanism. A planning brief names the ticket and the tree;
it never names a design. If your brief contains a mechanism you have not
reproduced or a design the user has not decided, you have poisoned the lane.

## Front-load everything

A lane reads its mailbox only between turns and consumes queued messages after
its current work, so a mid-flight correction is a note found later, maybe
never acted on, and a message to an idle lane resumes it. Everything
load-bearing goes into the spawn prompt. A post-spawn correction is a
re-spawn, or a follow-up sent only after the lane's idle notice with the lane
then treated as live again.

## Standing lines in every brief

- The artifact ids (ticket, prefill, commit) and the tree and branch to work at.
- ABSOLUTE PATHS for every read, build and run; a persistent shell cwd drifts.
- The user's load-bearing rules (money, access, security, data handling,
  scope) as VERBATIM QUOTES, with your interpretation beside the quote,
  labeled yours.
- The isolation rule: tests and probes run against spawned services on picked
  ports with an isolated home; the operator's running services, stores and
  credentials are never restarted, reconfigured or written into.
- The reads to stamp, always including `.claude/skills/knowledge-tools/SKILL.md`,
  and the line that the report ends with a tool census.
- Sibling work in flight on the same branch, with its touched files.
- The repository's standing test rules (no forced re-run flags, no private
  build caches, no git identity changes, hooks never bypassed).
- Any single operation projected over fifteen minutes is named before it runs.

## What a brief never offers

Deferral as a disposition. The lane's only dispositions are do it, disprove it
with evidence, or surface it undecided to whoever owns the decision.
