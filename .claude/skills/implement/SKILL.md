---
name: implement
description: Execute an implementation plan from the knowledge graph step by step. Updates status, verifies criteria, records thoughts about what you encounter, and charges thoughts when evidence arrives. Use after a plan has been created and approved.
argument-hint: <plan project ID or name to implement>
---

# Implement: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline (background spawning, signal routing,
reviewer gate, drift detection, user touch points, non-negotiation), reference
.claude/skills/orchestrate/SKILL.md — already loaded in your context.

This skill is implement-specific. The implementer agent definition
(.claude/agents/implementer.md) carries the agent-facing execution mandate.
</precedence>

<mental-model>
brainstorm = WHY, ticket = WHAT, plan = HOW, implement = WORK.

By the time implement runs: brainstorm captured WHY, ticket nailed WHAT, plan locked HOW.
Implementer's job is to execute — not to decide.
Decisions surfacing during implementation = upstream artifacts inadequate; route back, do not invent.
</mental-model>

<constraint id="pre-flight" severity="hard" phase="before-spawn">

  <rule>
    Before spawning any implementer, the plan must have passed plan-reviewer audit.
    No implementer spawns on an unreviewed plan version.
  </rule>

  <checks ordered="true">
    <check id="find-plan">
      query({ "text": "$ARGUMENTS", "type": "plan", "limit": 5 }) — if multiple match, ask user which.
      (Architectural disambiguation = legitimate touch point, not workflow permission.)
    </check>

    <check id="walk-tree">
      query({ "mode": "plan_tree", "id": "plan_id" }) — identify which steps touch disjoint files (parallelizable) vs shared files (sequential).
    </check>

    <check id="reviewer-audit" severity="gate">
      Confirm plan has passed reviewer audit. traverse({ start: plan_id, edge_types: ["informed-by", "relates-to"], direction: "both" }) — look for audit thought/research.

      If no audit: spawn plan-reviewer first per orchestrate constraint id="reviewer-gate". Do NOT proceed to implementation without it.

      Locked user rule: "the planner making snowflake implementations instead of reusing code is UNACCEPTABLE." Reviewer gate exists to enforce; cannot bypass.
    </check>
  </checks>

</constraint>

<constraint id="parallelization" severity="medium" phase="before-spawn">

  <rule>
    Parallelize implementers where it's safe; serialize where it's not.
  </rule>

  <decision-table>
    <row condition="steps touch disjoint files">
      Spawn implementers in parallel (one message, multiple Agent calls).
    </row>
    <row condition="steps touch shared files">
      Sequential. Parallel edits to same file cost more than they save.
    </row>
    <row condition="steps have needs-dependencies">
      Sequential, wait for dependency.
    </row>
    <row condition="parallel implementers spawned">
      Each marks only its own step active/complete. Pass step's linked files in agent prompt so it doesn't hunt.
    </row>
  </decision-table>

</constraint>

<constraint id="verification-phases-inline" severity="medium" phase="before-spawn">

  <rule>
    Pure verification phases run inline by the orchestrator, NOT via spawn.
  </rule>

  <reason>
    Verification phases (~10 tool calls, no novel code) get 3-5x overhead from spawn ceremony.
    Spawned agents often go exploring (re-investigating settled things) instead of just running checks.
    Verification ≠ implementation.
  </reason>

  <inline-cases>
    - "Run full test suite + lint + build, record validation thought"
    - One-line gofmt / typo / import fixes
    - Final integration verification / docs pass with no real code changes
    - Status closure (mark plan → ticket → project completed)
  </inline-cases>

  <spawn-cases>
    - Phase requires actual code changes
    - Multi-file edits
    - Non-trivial investigation
  </spawn-cases>

</constraint>

<spawn id="implementer" background="true">

  <reference>See orchestrate constraint id="background-spawning".</reference>

  <mandatory-prompt-block>
    Every implementer spawn prompt MUST open with this block VERBATIM:

    ```
    EXECUTION DIRECTIVE — read first, enforce throughout:

    Execute every step of every phase in the order the plan specifies. Do not skip
    steps. Do not cherry-pick phases. Do not estimate scope. Do not pause to ask
    for sequencing direction. Do not freelance a "better" approach.

    If you cannot complete a step (provable blocker — broken dependency, missing
    symbol, criterion that's wrong), STOP at that step and report. Do not skip
    ahead. Do not "do other things while stuck."

    If your work would leave any path returning interceptRequired/not-implemented
    that prior steps depended on remaining functional, you have introduced a
    regression. Fix it in-phase or surface the blocker. Do not delete tests to
    make the suite green — substantive completion is the criterion, not literal pass.

    Updating comments is part of the change, NOT optional cleanup. When your edit
    changes what code does, how it routes, what it consumes or returns, or which
    invariant holds, fix every comment and docstring the edit makes wrong in the
    SAME step — especially comments that list consumers, describe a routing/fallback
    path, or name a return shape. A stale or misleading comment is as bad as
    incorrect test logic: both assert something false the next reader trusts. A
    touched file whose comments still describe the old behavior is a FAILED step.

    If you run out of context, capture precise resumption state (what's done,
    what's pending, exact file state) so a successor agent picks up cleanly. Do
    not self-truncate by pausing "to be safe."

    Just do the work, every step, in order. Mark each step completed in the graph
    as you go. Report at the END.
    ```

    This block addresses recurring drift modes documented in orchestrate constraint id="non-negotiation":
    - Cherry-picking phases
    - Scope estimation pauses
    - Test deletion for green-suite
    - Silent substitution
    - Stale comments left behind after a behavioral edit
  </mandatory-prompt-block>

  <single-phase-template>
    Agent(
      subagent_type: "implementer",
      prompt: "&lt;EXECUTION DIRECTIVE block&gt;\n\nImplement Phase N of plan &lt;plan_id&gt;. Files to modify: &lt;file list&gt;. Stop at the phase boundary; report verification status.",
      description: "Implement: Phase N &lt;name&gt;",
      run_in_background: true
    )
  </single-phase-template>

  <whole-plan-template>
    Agent(
      subagent_type: "implementer",
      prompt: "&lt;EXECUTION DIRECTIVE block&gt;\n\nImplement plan &lt;plan_id&gt; end-to-end. Use assemble / query(mode:plan_tree) to find the next step; follow each phase to completion. Stop only on blocking exceptions or completion of all phases.",
      description: "Implement: &lt;plan name&gt;",
      run_in_background: true
    )
  </whole-plan-template>

  <when-to-pick>
    Per-phase spawning when phase contexts are large; fresh context per phase benefits the implementer.
    Whole-plan spawning when phases are tight and context-sharing helps.
    Either way, background.
  </when-to-pick>

</spawn>

<constraint id="auto-advance-between-phases" severity="hard" phase="agent-return">

  <rule>
    Phase boundaries are NOT user gates. Advance automatically until plan completes
    or blocking exception. Do not ask CEO to gate phase transitions.
  </rule>

  <override-default>
    Trained instinct: surface progress, ask "ready for the next phase?" before continuing.
    Wrong here — phase boundaries are workflow steps the CEO already authorized at plan-approval time.
  </override-default>

  <surface-only-on>
    - Plan completion (all phases closed, closure rolled plan → ticket → project). End-of-plan summary; suggest /test if test plan linked; ask about reindex.
    - Blocking exception (TICKET-GAP discovered mid-impl, unresolvable conflict, failing criterion needing design decision).
    - Mid-plan failure needing intervention (repeated test/lint failures, infrastructure problems, scope-expansion temptations flagged but not acted on).
  </surface-only-on>

  <between-phases-action>
    1. Read implementer's phase report; verify steps passed criteria + tests + lint.
    2. If clean: one-line confirmation logged; spawn next phase's implementer immediately.
    3. If off: pause and surface to user (legitimate touch point — drift detected, design judgment needed).
  </between-phases-action>

  <reason>
    Plan was already reviewed by plan-reviewer before any implementer spawned.
    Each phase's success criteria were verified at plan time.
    Asking "next phase?" at every boundary makes multi-phase plan a slog of confirmations
    the user has no useful signal to gate on. Gates belong at brainstorm (principle),
    planner specifics-questions, and completion — NOT in between phases.
  </reason>

</constraint>

<constraint id="implementer-drift-handling" severity="hard" phase="agent-return">

  <rule>
    On implementer drift: re-spawn with EXECUTION DIRECTIVE block + specific drift named.
    Do not negotiate. Do not let drift land.
  </rule>

  <reference>See orchestrate constraint id="non-negotiation" for the universal principle.</reference>

  <implementer-specific-drift-patterns>
    <pattern id="cherry-picked-phases">
      Implementer did some phases, skipped others.
      Re-spawn directive: "the prior implementer did phases X+Y but skipped phases A+B — you do A+B in order, then we're done."
    </pattern>
    <pattern id="scope-estimate-pause">
      Implementer estimated "this is 8-12 hours" and refused to proceed.
      Re-spawn directive: "implementer does not estimate. Execute every step."
    </pattern>
    <pattern id="test-deletion-green-suite">
      Implementer deleted/skipped tests to make suite pass.
      Re-spawn directive: "revert test deletions. Substantive completion is the criterion, not literal pass."
    </pattern>
    <pattern id="orphaned-stub">
      Implementer left server stubs returning interceptRequired with no client claimant.
      Re-spawn directive: "the construction half is missing — add the client intercepts that claim the calls before the stubs are reached."
    </pattern>
    <pattern id="stale-comments-left">
      Implementer changed behavior but left comments/docstrings describing the old behavior (consumer lists, routing/fallback paths, return shapes, invariants).
      Re-spawn directive: "the code is right but the comments now lie — sweep every touched file and update each comment the change falsified; a stale comment is as bad as wrong test logic."
    </pattern>
  </implementer-specific-drift-patterns>

</constraint>
