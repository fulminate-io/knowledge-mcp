---
name: plan
description: Create an implementation plan in the knowledge store. Researches the codebase first, then creates a structured phased plan with success criteria. Use when starting a new feature, refactor, or multi-step task.
argument-hint: <description of what to plan>
---

# Create Plan: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline (background spawning, signal routing,
reviewer gate semantics, auto-revise threshold, user touch points, drift
detection), reference .claude/skills/orchestrate/SKILL.md — already loaded
in your context when you reach this skill.

This skill is plan-specific. Don't duplicate universal patterns here; reference them.
</precedence>

<mental-model>
brainstorm = WHY, ticket = WHAT, plan = HOW, implement = WORK.

Plan translates ticket's WHAT into sequenced HOW (files, names, ordering, criteria).
Plan does NOT decide WHAT (brainstorm's job).
Plan does NOT do the work (implementer's job).
If planner finds itself making architectural calls, ticket was inadequate — route back upstream.
</mental-model>

<constraint id="ticket-pre-flight" severity="hard" phase="before-spawn">

  <rule>
    Verify ticket completeness BEFORE spawning the planner. Thin tickets produce
    thin plans, which produce re-plan cycles.
  </rule>

  <checks ordered="true">
    <check id="index-freshness">
      manage({ "operation": "status" }) — if behind HEAD, offer to reindex.
      Planner needs accurate search results.
    </check>

    <check id="clarify-goal">
      If $ARGUMENTS is ambiguous, ask before spawning. Don't guess the end state.
      (This IS a legitimate touch point — architectural clarification, not workflow permission.)
    </check>

    <check id="find-parent-ticket">
      query({ "type": "ticket" }) or current project's ticket list.
      Pass ticket_id to planner so create_plan links under it.
    </check>

    <check id="pattern-context">
      assemble({ id: ticket_id }) — check ## Patterns section.
      If NEITHER pattern_ids NOR no_patterns_reason set, prompt user with options:
      (a) Run /brainstorm to pick patterns
      (b) Add no_patterns_reason via mutate (escape hatch for trivial work)
      (c) Cancel
      Do NOT auto-invoke /brainstorm — user picks.
    </check>

    <check id="ticket-thoroughness" severity="gate">
      Verify ticket has:
      - In Scope enumerating concrete code surfaces (files, packages, functions)
      - Out of Scope explicitly naming temptations + adjacent work deliberately excluded
      - Success criteria with testable invariants (grep predicates, named assertions)

      If ticket is thin, STOP:
      "The ticket is too thin for direct planning — the architectural surface needs
      to be enumerated first. Run /brainstorm against the ticket to do the
      architectural walk + populate In Scope / Out of Scope concretely, then
      re-run /plan."

      Do NOT spawn planner against thin ticket.
    </check>
  </checks>

</constraint>

<spawn id="planner" background="true">

  <reference>See orchestrate constraint id="background-spawning" — every spawn is background.</reference>

  <invocation>
    Agent(
      subagent_type: "planner",
      prompt: "Create an implementation plan for: $ARGUMENTS\n\nParent ticket: &lt;ticket_id or 'none'&gt;",
      description: "Plan: &lt;brief topic&gt;",
      run_in_background: true
    )
  </invocation>

  <brief-addendum id="census-scale-work" when="the ticket involves a sweep/migration/audit over a large or pattern-defined surface">
    Add to the planner's prompt: "This ticket's work includes a census-scale
    surface. Per your programmatic-census constraint: enumerate it with
    ast/grep/script runs DURING planning — no hand counts anywhere in the plan;
    steps consume census output by kind with per-file lists labeled as floors;
    every sweep completion gate re-runs the census and asserts remainder = 0;
    for multi-kind migrations prescribe a checked-in census script emitting a
    machine-readable manifest [{file, line, kind, currentForm, targetForm}]."
    Hand-enumerated sweep surfaces are the leading cause of plan-revise churn:
    each review round finds the members the previous hand count missed, and the
    loop does not converge.
  </brief-addendum>

</spawn>

<constraint id="warnings-gate" severity="hard" phase="after-spawn">

  <rule>
    After planner returns, check for ## Warnings section (unresolved pattern_ids).
    If present, this is a legitimate user touch point — surface verbatim, do NOT auto-advance.
  </rule>

  <action>
    Surface warnings verbatim to user.
    Ask user to choose:
    (a) revise pattern_ids and re-plan
    (b) accept warnings and proceed to /implement (implementer's own warning check re-prompts)
    (c) cancel
  </action>

  <do-not-strip>
    This gate MUST NOT be stripped from this skill — future maintainers can't remove it.
  </do-not-strip>

</constraint>

<after-return phase="planner-completed">

  <step order="1">
    Apply signal routing from orchestrate constraint id="signal-routing":
    - TICKET-GAP → re-engage /brainstorm
    - open_questions → honest-answer test → re-brief OR re-engage brainstorm
    - plan-size → CEO direct
  </step>

  <step order="2">
    Warnings gate (constraint above).
  </step>

  <step order="3">
    Present plan structure (phases, steps, criteria, file links) to user as summary.
    This is informational, not a permission-ask. Do NOT end with "ready to spawn reviewer?"
  </step>

  <step order="4">
    Spawn the plan-reviewer. Per orchestrate constraint id="reviewer-gate" — universal pattern.
    Plan-reviewer spawn:

    Agent(
      subagent_type: "plan-reviewer",
      prompt: "Audit plan &lt;plan_id&gt;. Fresh audit — you have no memory of any prior audit of this plan. Phases already implemented (skip these): &lt;list or 'none'&gt;. User-locked decisions that are out of scope for critique: &lt;list or 'none'&gt;. Produce the structured four-tier audit report.",
      description: "Audit plan: reuse + architecture + optimization + can-kicking",
      run_in_background: true
    )

    No implementer spawns on unreviewed plan version (orchestrate blocking-discipline).
  </step>

  <step order="5">
    On revise: spawn planner again with reviewer report as input. FRESH reviewer for re-audit.
    Auto-revise per locked threshold — do NOT ask user permission.
  </step>

  <step order="6">
    For non-revise changes: mutate(operation:"update", ...) in place.
  </step>

</after-return>

<do-not-strip>
The reviewer gate and the auto-revise threshold MUST NOT be stripped from this skill — both are locked by user direction.
</do-not-strip>

<constraint id="fallbacks-require-express-user-approval" severity="hard">

  <rule>
    Fallbacks are covers for incorrect behavior. A fallback — any silently-degraded
    lane, catch-and-continue, default-on-error, or graceful-degradation path — must
    be EXPRESSLY APPROVED BY THE USER DIRECTLY, with the approval recorded (a ticket
    or decision) where the fallback lives. You have NO discretion to classify a
    fallback as legitimate yourself. The default response to an error state is to
    FAIL LOUDLY: error naming the condition and what was dropped, at the point of
    the mistake. A fallback that does not solve the problem it fires for is not
    a real fallback — it is a hack that hides the problem. The test is
    convergence: after the fallback runs, the underlying condition is repaired and
    the system returns to its primary path. A lane that can fire forever on the
    same cause is hiding a defect, not handling one — it must be an error
    instead.
  </rule>

  <enforcement>
    An unticketed, unapproved fallback — in a plan, a design, a changeset, or
    encountered in existing code you are changing — is a T2 finding that must be
    raised to the user for approval. Never wave one through, never build one on
    your own authority, never soften one to a note. Retired fallback code is
    REMOVED, never bypassed in place.
  </enforcement>

  <why>
    The instinct that produces fallbacks is sycophancy expressed as architecture:
    the trained urge to always produce something and never fail the user
    manufactures degraded lanes for states that are errors — a wrong answer
    delivered as success. Treat your own urge to add a fallback as the signal to
    raise it, not to build it.
  </why>

</constraint>

<constraint id="deferral-is-a-user-decision" severity="hard">

  <rule>
    Deferral is a USER decision — never yours. You may not defer, postpone,
    descope, or "leave for a follow-up" any surfaced defect, gap, or required
    disposition on your own judgement, and you may not present deferral as an
    outcome you have chosen. The only dispositions you may produce are: DO the
    work, DISPROVE the need with evidence, or SURFACE the item UNDECIDED to
    whoever holds the decision. A brief that offers "defer" as one of your
    answers does not make it yours — deferral options are presented to the
    user, decided by the user, and recorded. Postponed is not rejected: an
    item the user defers stays recorded as open work, never silently dropped.
  </rule>

</constraint>
