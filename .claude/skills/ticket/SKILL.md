---
name: ticket
description: Action rulebook for the ticket — the record of what the user needs. The draft schema, the premise provenance rule, the research validation stamp, and what a ticket never carries. Read before creating or amending any ticket. Not user-invocable.
user-invocable: false
---

# TICKET — the record of what the user needs

<!-- version: 1 -->
<!-- Read at: before any create_ticket or ticket amendment (brainstorm, orchestrator,
     researcher). The ticket is the reference every later audit compares against;
     a wrong ticket is the one error no downstream lane can catch. -->

## What a ticket is for

The ticket answers one question: what does the user need, in the user's words,
precisely enough that a researcher can validate it, a planner can prefill for
it, a reviewer can audit against it, and a code reviewer can judge the result
by it. Building the wrong thing correctly is the ticket's failure; every other
failure has a later audit.

## The draft schema

Every non-trivial ticket carries these sections, in this order:

1. **Goal**, in the user's words, verbatim where the user stated it.
2. **Requirements**: one line each, each an OBSERVABLE of the finished system
   ("a create with a label the tracker already holds reuses it and issues no
   create call"), never a description of code. Number them; the prefill's
   what-to-test list and the code review key on the numbers.
   A STRUCTURAL requirement, a shape that must or must never appear in source
   ("no unbounded body read on a fetch path", "every connection read pages to
   exhaustion"), is written as a CHECK requirement: it names the corpus check
   that will enforce it, existing (by id) or to be authored, with the bad
   shape and the near-miss good shape its fixture pair must vary. A structural
   requirement carried only as prose is not testable and is not admissible.
3. **Premises**: every fact the ticket rests on, each labeled with provenance:
   `user-stated`, `reproduced` (with the command and output on the research
   node), `source-read` (file:line, opened), or `unverified`. A ticket with an
   `unverified` premise is a draft and cannot leave draft status.
4. **In scope**: the surfaces the work touches, the decided design, the
   verbatim quotes of every load-bearing rule the user gave.
5. **Out of scope**: the temptations, the shapes the user rejected (verbatim),
   the adjacent work deliberately excluded, the "while we're in there" cleanup.
6. **Direction**: how the user wants it done, where the user stated a
   preference; recorded as a preference, never inflated into a requirement.
7. **Landing**: the branch or PR the work lands on and anything it must land
   after.

## Provenance is not optional

A reported finding, a prior thought, a comment, a user observation of a
symptom, or an orchestrator's inference is an input to research, never a
premise. The mechanism enters the ticket only as `reproduced` or
`source-read`, and only from the researcher's validation pass. An orchestrator
drafting a fix ticket mid-session gets no exemption: its "confirmed first-hand"
is `unverified` until a researcher reproduces it. The tell you are about to
break this: a ticket sentence naming a mechanism you have not run, or a
provenance id you did not fetch this session.

## The validation stamp

A researcher validates the draft: reproduces every premise by execution,
corrects the facts on the ticket, fills the detail the draft did not know, and
writes `metadata.validated` with the research node id, the tree the premises
were reproduced at, and the date. Intent is never changed by the researcher: a
correction that changes scope or direction goes to the user, who amends the
goal or the scope in their own words. No planner is spawned on a ticket without
the stamp.

## What a ticket never carries

Steps, phases, criteria, commands to run, or a design the user did not decide.
A detail a planner can find is the planner's; a decision the user has not made
is an open item on the ticket, surfaced, never defaulted.

## Scope discipline

A need that surfaces later and is not in the ticket goes to the user with a
recommendation before any agent acts on the new need; the ticketed scope keeps
moving meanwhile. A shape the user pushed back on belongs in Out of scope
verbatim. If three viable shapes existed and the user picked one, the other two
are named in Out of scope.

## Patterns

`pattern_ids` are prescriptive: the implementer builds to whatever is attached,
so attach only a pattern the work is genuinely an instance of, never a mediocre
match to look thorough. `no_patterns_reason` is the honest choice for defect
fixes, doc edits and sui-generis work. `language_patterns` are defensive
vigilance markers and optional. Discovery fans out across every practice graph
(`search({graph:"practice", language:"all", queries:[...]})`); a single-graph
miss is not "no pattern fits".

## Closing tickets for a project

Every project carries two tickets sequenced last: a comment-and-documentation
sweep over every claim the project's changes could have invalidated, verified
against final merged source; and a project-wide live confirmation that
exercises every feature ticket's deliverable end to end on the built system.
Feature tickets stay open until the confirmation verifies them.
