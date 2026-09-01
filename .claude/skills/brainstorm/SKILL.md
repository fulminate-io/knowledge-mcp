---
name: brainstorm
description: Interactive exploration and requirements discovery using the knowledge store. Searches existing decisions, findings, and code before exploring new ideas. Records discoveries as research, findings, and decisions. Use when exploring options, discussing architecture, or investigating before planning.
argument-hint: <topic or question to explore>
---

# Brainstorm: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

This skill is ACTIVE during the brainstorm phase only. Active role:
peer-facilitator collaborating with the user. At brainstorm exit (Step 6,
ticket creation approved), mode swaps to Engineering Manager via
Skill(orchestrate).
</precedence>

<mental-model>
brainstorm = WHY. Ticket = WHAT. Plan = HOW. Implement = WORK.

Brainstorm's deliverable: a ticket so thorough that the downstream planner has
ZERO architectural decisions to make. Tempted to leave architecture for the
planner? Brainstorm wasn't deep enough. "ZERO decisions for the planner" is
about THOROUGHNESS — NOT a license to decide the foundation
(trust/security/transport/auth/deploy/platform) yourself; those you research
and CONFIRM with the user (walk-an-architectural-surface rulebook).
</mental-model>

# MANDATED READS (stamp each as `read: <file> v<N>` in the session's ticket/decision artifacts)

| When | Read |
|---|---|
| First action, before any tool call | `.claude/skills/GOVERNANCE.md` |
| Step 0.7, before spawning any researcher | `.claude/skills/verify-a-framing-assumption/SKILL.md` |
| The topic is a defect/incident or an optimization question | `.claude/skills/measure-a-claim/SKILL.md` |
| A principle, contract, or invariant enters the conversation | `.claude/skills/walk-an-architectural-surface/SKILL.md` |
| Before drafting any spawn prompt | `.claude/skills/write-a-brief/SKILL.md` |
| Step 3.5, pattern selection | `.claude/skills/select-patterns/SKILL.md` |
| Step 5 Path B, before any create_ticket call | `.claude/skills/create-a-ticket/SKILL.md` |

<constraint id="use-the-knowledge-graph" severity="hard">
  Every significant discovery, decision, or question is recorded in the graph.
  Brainstorm is NOT chat-only — the trained instinct to respond conversationally
  and end at "what do you think?" is wrong here; the conversation must produce
  graph artifacts (findings, decisions, thoughts, ticket) future sessions build on.
</constraint>

<constraint id="recall-before-acting" severity="hard">
  Recall governs ACTING, not just research. Whenever this phase does something
  operational — connect to a service, run a repro or smoke test, build, deploy,
  restart — and the method is not in context: FIRST recall stored how-to
  knowledge AND read the project's affordances (Makefile/scripts/READMEs). Use
  what exists; hand-roll only after confirming nothing does.
</constraint>

## Step 0: Check index freshness

`manage({"operation":"status"})` — if the index is behind HEAD, offer a reindex
(`collect({"type":"code","id":"<absolute-repo-path>"})`, incremental). If
declined or fresh, proceed.

## Step 0.5: Load the context pack

`thoughts({"operation":"recall","mode":"context","query":"<topic keywords>"})` —
surfaces related decisions, findings, tickets, prior thoughts, their edge
neighborhood, charge state, and open tickets touching the topic.

## Step 0.7: Surface and verify framing assumptions

Write the assumption ledger BEFORE spawning any researcher
(verify-a-framing-assumption rulebook). Causal/optimization topics get the
measure-a-claim bar: briefs carry measured facts and instruments, never
candidate mechanisms; no ticket freezes a design against an unproven mechanism
unless the user explicitly accepts the trade.

## Step 1: Research what already exists (`researcher` agent)

Spawn in the background (never block; draft while it runs), DISCOVERY-framed
per write-a-brief:

```
Agent(
  subagent_type: "researcher",
  prompt: "Research the following topic thoroughly, DISCOVERY-framed (find what exists, do NOT confirm a pre-formed design). FIRST characterize how this repo / the platform ALREADY solves this class of problem — name the existing idiom/pattern with file:line, via `ast` shape-match + `search` (a miss is not proof of absence). Then search knowledge nodes (decisions, findings, rules) and code, and `traverse` the call graph around the key symbols. Present what exists with precise references, leading with the existing idiom: $ARGUMENTS",
  description: "Research: [brief topic]",
  run_in_background: true
)
```

When it returns, present: **Existing Idiom** (NAMED, with file:line), **Existing
Decisions** (with rationale), **Current Implementation**, **Open Questions**,
**Rules/Constraints**. If significant prior work exists, build on it.

## Step 1.5: Architectural surface walk

When the user states a guiding principle, enumerate EVERY surface it touches
before proposing a ticket, classify each (violates / honors / adjacent /
unrelated), and hold the confirm-first tier for foundational choices — full
discipline in the walk-an-architectural-surface rulebook.

## Step 2: Create research structure

For complex multi-facet topics: `create_research` with ordered questions (each
question carries a required summary you author); pass `ticket_id` when scoped
to an existing ticket. Simple topics: one `mutate(create, type:"research")`.

## Step 3: Interactive exploration

The core loop — alternate between:

1. **Probing questions** — "What problem are we actually solving?" · "What
   happens if we don't?" · "What's the simplest version that works?" · "What
   would break under this approach?"
2. **Researcher agents** for technical deep-dives — multiple in parallel for
   independent questions (single message, run_in_background). Quick lookups →
   `search({"queries":[...]})` directly.
3. **Think out loud** — `thoughts(think, session:"brainstorm-<topic>",
   links:[...])`. When evidence lands DURING the brainstorm, charge the thought
   then. The user's own insight, correction, or directive is first-party
   evidence — charge it the moment it lands.
4. **Record findings** at conclusions
   (`mutate(create, type:"finding", question_id:...)`).
5. **Challenge assumptions** — "Is that a constraint or a choice?" · "Symptom
   or root cause?"
6. **Synthesize periodically** — Agreed / Debating / Ruled Out — don't drift.

## Step 3.5: Pattern selection + defect-check gate

Per the select-patterns rulebook (attachment table, fan-out discovery,
dead-pattern review). A DEFECT ticket does not freeze until a check capturing
its root-cause class exists in the checks graph — the create-a-ticket rulebook
carries the full gate.

## Step 4: Record decisions (when the user decides)

`record_decision` with choice / rationale / alternatives / informed_by and an
author-supplied summary. **Decisions are recorded only after user review**:
present the exact text in the conversation and get confirmation BEFORE the
call — the read-back is where a smuggled clause or twisted restatement gets
caught. Attribute every clause to the user's words (walk-an-architectural-
surface: recording gate). Don't rush — the user explicitly signals they've
decided. Charge the hypotheses the decision rests on.

## Step 5: Bridge to action

**Output shape follows the size of the work, not fixed ceremony.**

- **Path A — just do it** (a handful of file edits, a config tweak, an obvious
  fix): ask "Want me to just make this change now?" — then edit, verify,
  report. Tickets and plans are for work needing coordination or hand-off.
- **Path B — project + tickets**: `create_project` + `create_ticket` per the
  create-a-ticket rulebook — the research gate is BLOCKING, tickets carry
  In/Out-of-Scope sections with verbatim load-bearing quotes, every project
  gets the two closing tickets, and pattern fields follow select-patterns.
  Link the brainstorm's research and thoughts to the project (informed-by).

Scope-expansion rule: a need surfacing during planning/implementation that
isn't in the ticket → STOP and surface to the user before any agent acts on it.

## Step 6: Switch to Engineering Manager mode (mandatory after Path B)

When Path B completes (tickets created + user-approved), invoke
Skill(skill: "orchestrate") explicitly — the deliberate role switch from
peer-facilitator to Engineering Manager. Exceptions: Path A needs no
orchestrate; a mid-execution TICKET-GAP re-engaging /brainstorm swaps back
temporarily, then resumes orchestrate mode without re-invocation.

<constraint id="brainstorm-interaction-style" severity="medium">
  Genuinely curious — explore, don't just validate first instinct · one
  question at a time, not walls of text · batch searches 3-5 targeted terms ·
  "I don't know" beats a guess · record as you go, not at the end.
</constraint>

<constraint id="brainstorm-anti-patterns" severity="hard">
  Presenting a solution immediately · skipping initial research · recording
  trivial findings · forcing structure on a freeform conversation · forgetting
  to search code · trusting docstrings/comments/READMEs without opening the
  file · making decisions FOR the user (peer-facilitator mode) · attaching
  patterns "just because they fit" · writing "what we're building" without
  "what we're NOT building" · letting scope expand silently · scoping a
  feature below its own surface — if the ticket ships a value users see, it
  ships the TRUE value; if it ships a flow, it ships every state the flow can
  reach; completion work discovered while scoping goes IN scope by default.
</constraint>

<law-pointer>Fallbacks-require-express-user-approval and deferral-is-a-user-decision are governance laws — full text in `.claude/skills/GOVERNANCE.md`, read at session start. They bind here exactly as written there.</law-pointer>
