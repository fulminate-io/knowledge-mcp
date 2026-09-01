---
name: create-a-ticket
description: Action rulebook for ticket creation — the blocking pre-ticket research gate, defect tickets' root-cause-check duty, the In/Out-of-Scope handoff shape, and the two mandatory project-closing tickets. Read before any create_ticket call. Not user-invocable.
user-invocable: false
---

# CREATE-A-TICKET — what must be true before a ticket exists

<!-- version: 1 -->
<!-- Read at: immediately before any create_ticket call, in any phase. -->

## THE RESEARCH GATE (absolute, blocking)

NO TICKET IS CREATED until, in order:
1. A RESEARCHER PASS answered: every open question the ticket would carry, the
   high-level design (which existing seams and idioms the work binds to), and —
   for a defect — the root cause OBSERVED at the mechanism level (in source or
   reproduction). A mechanism nobody observed enters the ticket labeled
   "unproven" or the ticket ships symptom-only; your own inference gets the
   same bar as any inbound claim. A live investigation already running on the
   same mechanism IS the researcher pass — the ticket waits for it.
2. A PRIOR-DECISION SWEEP over every touch point the ticket names, checking the
   proposed design against each recorded decision.
3. CONFLICTS ROUTED to the user as TICKET-GAPS before the ticket freezes — a
   design/decision disagreement is never resolved silently in either direction.
4. THE DECISION RECORD UPDATED with the user's ruling in the same breath as the
   ticket — a ticket contradicting a standing decision record is a landmine.
5. FOR DEFECT TICKETS: A ROOT-CAUSE CHECK MINTED FIRST — a check capturing the
   root-cause class exists in the checks graph, authored to the storing bar
   (fired on a bad fixture, silent on a good one; ast_pattern where mechanical,
   llm_only where it needs judgment — llm_only is exclusive with fixtures). A
   root cause that genuinely cannot be a check is surfaced to the user WITH the
   ticket as an explicit exception — never self-exempted.

The one exception: a symptom-only ticket for an ACTIVE INCIDENT may be created
immediately to carry mitigation state — its mechanism section says UNKNOWN, the
researcher pass amends it before any implementation dispatch, and the
root-cause check is minted with that amendment.

The tell you are about to violate the gate: a ticket description carrying your
own unverified mechanism as fact, an open question left for the planner, or a
design no researcher checked against the decision graph.

## The handoff shape

Tickets are the handoff document — never one-liners. Every non-trivial ticket
carries two marked sections:
- **"In scope — what we're building"**: feature shape, integration points,
  decided design, key files, constraints, verbatim quotes of load-bearing rules.
- **"Out of scope — what we are NOT building"**: the temptations — patterns
  deferred, features the user declined, defense-in-depth layers, "while we're
  in there" cleanup. Planners stay in their lane only if the lanes are drawn.

Sniff test: every shape the user pushed back on belongs in Out of Scope
verbatim; if three viable shapes existed and the user picked one, the other two
go in Out of Scope. Scope-expansion rule: a need surfacing later that isn't in
the ticket STOPS and routes to the user before any agent acts on it.

Pattern fields: exactly one of pattern_ids (prescriptive — the planner builds
to whatever is attached; never attach mediocre matches), no_patterns_reason, or
proposed_patterns. language_patterns (defensive) is independent and optional.
Lead the ticket NAME with pattern nouns when patterns apply; pure bug fixes and
doc edits take no_patterns_reason honestly.

## Mandatory project-closing tickets

Every project gets TWO tickets beyond its feature tickets, created in the same
batch and sequenced last — never deferred to memory:
1. **Comment & documentation cleanup**: sweep every comment, doc comment, and
   doc file whose claims the project's changes could have invalidated —
   including files whose prose describes the changed behavior from OUTSIDE.
   Verify each suspect claim against final merged source; correct what is
   false; leave true prose byte-identical.
2. **Project-wide smoke test**: exercise every feature ticket's deliverable
   end-to-end against the live/built system (the run-a-smoke-test rulebook),
   per-ticket pass/fail with reproduction commands. Feature tickets stay open
   until the smoke ticket verifies them live.

Deferring either is the user's explicit call alone.
