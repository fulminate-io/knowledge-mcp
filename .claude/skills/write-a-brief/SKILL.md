---
name: write-a-brief
description: Action rulebook for briefing a lane — discovery framing, no mechanisms, front-loading, verbatim relays, absolute paths, isolation, the reads to stamp, and what a re-brief must say it is demanding differently. Read before every spawn. Not user-invocable.
user-invocable: false
---

# WRITE-A-BRIEF — everything load-bearing goes in before the spawn

<!-- version: 6 -->
<!-- Read at: before every agent spawn. -->

## A brief is a decision, not a relay

A brief is where the orchestrator's reading of the situation becomes a
lane's instructions. It is written after the return protocol, never from a
report's summary line. If you cannot say in one sentence what this brief is
for and what it demands that the situation requires, you are not ready to
write it.

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

The brief that opens a round states the sha the round is measured on and is
the only message that may instruct a rebase. A brief that opens a fix round
says: the report that declares the round done follows the round's one commit
and is the hand-off; an unfinished item is stated in that report, never
committed after it; the implementer never pushes, the orchestrator does.

## Standing lines in every brief

- The artifact ids (ticket, prefill, commit) and the tree and branch to work at.
- Every finding, annotation, decision and ticket id in a brief is copied in
  full from a graph read you made for this brief, never transcribed from a
  report's prose. A prefix or a relayed id sends an auditor to resolve a
  mismatch instead of auditing.
- The name of a landed artifact (a manifest, a contract file, a schema) is
  relayed only after you opened it on the landed tree; a sibling lane's
  report of its spelling is not the artifact.
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
- The repository's own standing test and commit rules, quoted from where the
  repository states them.
- Any single operation projected over fifteen minutes is named before it runs.
- The report's size bound and the order of its contents, with the verdict,
  the ids and the not-done list first, so a report truncated in transit still
  carries what the next decision needs.

## Audits enumerate, fixes target a class

An audit brief names every enumerable axis the production has (spellings a
check must cover, matrix cells, venues, routes, module boundaries, readers
of a shared fixture) and demands each be exhausted in one round, every
element reported as covered, silent-and-expressible, or
silent-and-inexpressible. A fix-round brief names the class and its full
element list; a brief that names one instance sends the chain back for the
next instance. When a fix touches something shared (a fixture, a script, a
helper), the brief demands the census of everything that shares it, with a
disposition per entry, not the fix of the entry the finding named.

## What a re-brief must say

Every brief on a production that has already been audited carries a section
headed **What this brief demands differently, and why**: the pattern read
across the rounds so far in one sentence, and the change this brief makes
because of it (the axis enumerated whole; a complete table instead of the
gaps; a census instead of named instances; a different instrument; the
coarsest sufficient carrier; a venue a lane runs; a different producer). A
re-brief without that section is not sent. A re-brief whose section says
"the same as last round" is a decision to stop and bring the user a
recommendation, not a brief.

## What a brief never offers

Deferral as a disposition. The lane's only dispositions are do it, disprove it
with evidence, or surface it undecided to whoever owns the decision.
