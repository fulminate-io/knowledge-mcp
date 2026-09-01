---
name: write-a-brief
description: Action rulebook for briefing subordinate agents — discovery framing, front-loading, mailbox-is-not-live, cross-plan wave discipline, absolute paths, and the collect-first line. Read before every spawn. Not user-invocable.
user-invocable: false
---

# WRITE-A-BRIEF — everything load-bearing goes in before the spawn

<!-- version: 1 -->
<!-- Read at: before every agent spawn (orchestrator, and any skill that spawns). -->

## Discovery, not confirmation

The brief is where assumptions transmit. Write research briefs as DISCOVERY
("how does this repo / the platform already solve this class of problem"), never
CONFIRMATION ("verify my design X"). If your brief names a SOLUTION you have
already poisoned it — name the PROBLEM and let research surface the solution
that may already exist. Investigation briefs carry measured facts and
instruments, never candidate mechanisms (see measure-a-claim). Never write a
brief that offers DEFERRAL as a disposition a subordinate may choose — the only
agent dispositions are do it, disprove it with evidence, or surface it
UNDECIDED.

## Front-load everything — the mailbox is not live

Subagents do not read their mailbox until their original task completes. A
message to a RUNNING agent is a note found after the fact, maybe never acted on:
- FRONT-LOAD everything load-bearing into the spawn prompt. A post-spawn
  correction must be assumed NOT to reach the agent mid-flight — send it as a
  post-completion follow-up or budget a re-spawn.
- VERIFY every "done" report against the LATEST instruction set, not the
  spawn-time one; a mid-flight addition missing from the report is the EXPECTED
  outcome — check explicitly, re-issue when absent.
- "I sent them a message" is never "they know." Sequencing two agents via
  mid-flight messages is a race by construction: wait for A's completion and
  carry the information in B's spawn prompt.

## Cross-plan wave discipline

With several plans in flight against shared packages, only the orchestrator
sees the interactions: every planner brief NAMES sibling in-flight plans with
touched files AND deleted symbols — a test pinning a sibling's deleted symbol is
a scheduled red with no sanctioned repair. When a ruling amends a plan's SCOPE,
amend the TICKET's description text in the same breath — a metadata sidecar
does not amend the fence.

## Standing brief lines

- Verbatim relay of load-bearing user rules (money, access, security, data) —
  quotes, with your interpretation beside the quote, labeled yours.
- On co-tenant/worktree work: ABSOLUTE PATHS (or an explicit cd in the same
  invocation) for every read, build, and run — a persistent shell cwd drifts to
  the wrong tree and the failure looks plausible instead of erroring.
- Never advise a subagent to prefer shell grep because the index is stale — the
  correct line is "collect first if behind, then search/ast/file_symbols/
  traverse, verify hits against the file". Collection is the orchestrator's
  alone; subordinates note the need in their report.
- Any single operation projected over ~15 minutes is named to the orchestrator
  before it runs, with its expected duration.
- Lane-N discoveries constraining lane N+1 go into the next brief VERBATIM — a
  lesson only in a finished transcript constrains nobody.
