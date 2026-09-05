---
name: brainstorm
description: Turn a user's goal into a validated ticket. Clarify the goal with the user, research what exists, draft the ticket in the user's words with requirements as observables and provenance on every premise, then spawn a researcher to validate it by execution. Use when starting any piece of work, exploring options, or when a need surfaces that no ticket covers.
argument-hint: <the goal, problem, or question to work through>
---

# Brainstorm: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

Active role: peer collaborator with the user. The deliverable is a validated
ticket. When the ticket is validated the session switches to /orchestrate.
</precedence>

<mental-model>
The user has goals. This phase breaks them into what can be built, finds what
already exists, and gets on the same page with the user about what "done"
means. Its output is a ticket: the goal in the user's words, the requirements
as observables, what is in and out of scope, the user's direction, and every
premise labeled with where it came from. A researcher then validates the
draft by execution before anyone plans against it.

The ticket is the one artifact no later audit can correct. Time spent here is
the cheapest time in the pipeline.
</mental-model>

# MANDATED READS (stamp each as `read: <file> v<N>` on the ticket)

| When | Read |
|---|---|
| First action, before any tool call | `.claude/skills/GOVERNANCE.md` |
| Before drafting the ticket | `.claude/skills/ticket/SKILL.md` |
| Before any spawn | `.claude/skills/write-a-brief/SKILL.md` |

## Step 0: Recall

`thoughts({"operation":"recall","mode":"context","query":"<topic keywords>"})`
and `search` over the knowledge graph for decisions, findings, tickets and
prior thoughts on the topic. Present what the graph already knows before
saying anything about the topic yourself: a prior decision binds until the
user changes it; a prior finding is a lead to reproduce, not a fact.

Then `manage({"operation":"status"})`; if the index is behind, collect.

## Step 1: The assumption ledger

Write the framing's load-bearing assumptions as a thought, each marked
UNVERIFIED, with existence claims flagged ("there is no X", "we need to build
Y"). Existence defaults to "it already exists" until disproven; the burden of
proof is on absence.

## Step 2: Research what exists

Spawn a `researcher` in discovery mode, in the background, with a discovery
brief: how does this repository or the platform already solve this class of
problem, with the existing idiom named and the call graph around the key
symbols. Never name a solution in the brief. Draft with the user while it
runs; when it returns, fold what exists into the conversation and update the
ledger.

## Step 3: Clarify with the user

One question at a time. What problem is actually being solved; what happens if
it is not; what the simplest working version is; what would break. When the
user states a principle ("the server never touches the filesystem"), walk
every surface it governs before scoping: classify each as violates, honors,
adjacent or unrelated, and brief a researcher for the full inventory when the
surface is wide. Foundational choices (trust boundaries, transport, auth,
deployment, data isolation, platform) are confirmed with the user before they
enter any artifact; specifics are yours.

Record as you go: findings at conclusions, decisions when the user decides
(present the exact text first; record in the user's words with the
alternatives they saw), charges when evidence lands.

## Step 4: Draft the ticket

Per the ticket rulebook: goal verbatim, numbered requirements as observables,
premises with provenance, in and out of scope with the rejected shapes
verbatim, direction, landing. Every premise you did not reproduce yourself is
labeled `unverified`. Pattern fields per the rulebook. Create it with
`create_ticket` under the project; a new project gets the two closing
tickets.

Path A, for a handful of file edits with no coordination: ask "want me to make
this change now?", then edit, verify, report. No ticket.

## Step 4a: Check every derived clause before validation

Before a validating researcher spawns, list every requirement, premise,
scope clause and direction line of the draft that is not a quotation of the
user. For each one, recall the graph and read the memory index for rulings on
its subject, and re-read the user's messages in this conversation. A derived
clause that contradicts either goes to the user as a call, with both
statements quoted; never pick a reading yourself. The same check runs on
every amendment made after validation. A ticket whose derived clauses were
not checked is not sent to research.

## Step 5: Validate the ticket

Spawn a `researcher` in validation mode, in the background, with the ticket
id. It reproduces every premise on the current tree, corrects the facts,
fills the detail, and stamps the ticket validated, or reports what stays
unverified and what would change intent. Intent changes come back to the user
here, in this conversation; you amend the ticket in the user's words.

A ticket is never planned against without the stamp. A fix ticket you draft
mid-session gets the same researcher; your own confirmation is `unverified`.

## Step 5a: Choose the lane

The prefill exists to give the implementer what the ticket does not:
resolved touch points, reuse targets, contracts and seams, harnesses, landing
constraints. When the validated ticket already carries those, a prefill
restates it, and the ticket goes straight to implementation with its numbered
requirements as the what-to-test list and its research findings as the touch
points. Decide per ticket, here at hand-off, and record the determination on
the ticket (`metadata.lane: fast | full`, with the reason) so the code
reviewer knows which list it audits against.

A ticket takes the fast lane when all five hold:

1. **One mechanism, already reproduced.** The validation names the file and
   line and the facts determine the fix shape, with no design left to decide.
2. **Narrow blast radius.** The touch points sit in one package plus its
   tests, every caller of a changed symbol is censused in the research, and
   no wire, schema, config or public contract moves.
3. **The harness exists.** The tests the requirements need run on a harness
   the package already has, with a red statable today.
4. **The constraints are on the ticket.** Every rule that bounds the fix (an
   ordering an invariant depends on, a byte-identical output, a bound a landed
   gate pins, a structural requirement with its check shape) is a premise with
   provenance, so the implementer chooses nothing the user owns.
5. **No sibling coupling.** No in-flight branch touches the same files, or
   the overlap is one file with a stated landing order.

A ticket takes the full lane when any holds: an open design item or an
unresolved specifics question, a wire or contract change, more than one
package's production code or more than a handful of files, a new harness or
test infrastructure, a structural requirement whose check is not yet shaped,
a performance claim that needs a before-figure, or a premise still
`unverified`.

The fast lane skips only the prefill and its audit. Validation, the
full-scope code review against the ticket's requirements, the corpus checks,
the landing gates and the live confirmation all stand.

## Step 6: Hand off

When the ticket is validated, its lane is recorded, and the user has approved
it, invoke `Skill(skill: "orchestrate")`. A need that surfaces during later
phases and is not on any ticket comes back here, with the user.

<constraint id="brainstorm-anti-patterns" severity="hard">
  Presenting a solution before research · a premise stated as fact without a
  provenance label · a mechanism named before it was reproduced · a ticket
  drafted from a reported finding nobody ran · scope written without what is
  NOT being built · a decision recorded that the user did not make · asking
  the user a question the source or the graph answers · a derived clause
  that contradicts the user's words or a recorded ruling, resolved by your
  own reading instead of the user's call · a lane chosen without the five
  qualifications checked, or a fast lane on a ticket with an open design item.
</constraint>
