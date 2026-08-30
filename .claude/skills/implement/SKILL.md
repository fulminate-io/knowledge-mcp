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
    1. find-plan: query({"text":"$ARGUMENTS","type":"plan","limit":5}) — multiple
       matches → ask the user which (architectural disambiguation = legitimate
       touch point).
    2. walk-tree: query({"mode":"plan_tree","id":"plan_id"}).
    3. reviewer-audit (GATE): confirm the plan passed reviewer audit —
       traverse({start:plan_id, edge_types:["informed-by","relates-to"],
       direction:"both"}) for the audit thought/research. No audit → spawn
       plan-reviewer first per orchestrate reviewer-gate; do NOT proceed
       without it. Locked user rule: "the planner making snowflake
       implementations instead of reusing code is UNACCEPTABLE" — the gate
       enforces it and cannot be bypassed.
  </checks>

</constraint>

<constraint id="one-implementer-per-plan" severity="hard" phase="before-spawn">

  <rule>
    Default: ONE implementer executes the WHOLE plan, all phases sequentially,
    in one worktree, producing one batched commit — see orchestrate constraint
    id="dispatch" / one-implementer-per-plan for the full rule. Split the work
    across implementers ONLY on an ABSOLUTE need: the user directed it;
    genuinely parallel file-disjoint work where wall-clock matters (worktree
    lanes); a hard external boundary between phases (a merge, a daemon restart,
    live verification impossible from the worktree); or measured context
    exhaustion (re-spawn with precise resumption state — recovery, not
    planning). "Phases are conceptually separate", "verify each phase before
    continuing", and "the plan is large" are manufactured needs — never
    sufficient. When a sanctioned split does happen: shared-file lanes are
    sequential; each lane marks only its own steps; each lane's brief carries
    its linked files and any discoveries from prior lanes VERBATIM.
  </rule>

</constraint>

<constraint id="verification-phases-inline" severity="medium" phase="before-spawn">
  Pure verification phases run inline by the orchestrator, NOT via spawn —
  ~10 tool calls with no novel code get 3-5x overhead from spawn ceremony, and
  spawned agents go exploring instead of just running checks. Inline: full
  suite + lint + build with a validation thought; one-line gofmt/typo/import
  fixes; final integration/docs passes with no real code changes; status
  closure (plan → ticket → project). Spawn: actual code changes, multi-file
  edits, non-trivial investigation.
</constraint>

<spawn id="implementer" background="true">

  <reference>See orchestrate constraint id="dispatch".</reference>

  <mandatory-prompt-block>
    Every implementer spawn prompt MUST open with this block VERBATIM:

    ```
    EXECUTION DIRECTIVE — read first, enforce throughout:

    BEFORE YOUR FIRST WRITE, verify this brief's picture of the environment. Whatever
    it tells you about the working tree and about which steps remain was true when it
    was written and may not be true now. One call each: the tree against what the brief
    says is there, the step statuses against what it says is left. If they disagree,
    STOP and report — do not write, and never revert, stash, or clean uncommitted work
    that is not yours to reach the base the brief promised. A tree already carrying your
    task's changes means another worker is live on it.

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

    This block addresses recurring drift modes documented in orchestrate constraint id="agent-returns":
    - Cherry-picking phases
    - Scope estimation pauses
    - Test deletion for green-suite
    - Silent substitution
    - Stale comments left behind after a behavioral edit
  </mandatory-prompt-block>

  <whole-plan-template note="the default — one implementer, whole plan">
    Agent(
      subagent_type: "implementer",
      prompt: "&lt;EXECUTION DIRECTIVE block&gt;\n\nImplement plan &lt;plan_id&gt; end-to-end. Use assemble / query(mode:plan_tree) to find the next step; follow each phase to completion. Stop only on blocking exceptions or completion of all phases.",
      description: "Implement: &lt;plan name&gt;",
      run_in_background: true
    )
  </whole-plan-template>

  <scoped-lane-template note="ONLY under a sanctioned split per one-implementer-per-plan">
    Agent(
      subagent_type: "implementer",
      prompt: "&lt;EXECUTION DIRECTIVE block&gt;\n\nImplement &lt;the scoped phases/steps&gt; of plan &lt;plan_id&gt;. Files to modify: &lt;file list&gt;. Prior-lane discoveries: &lt;verbatim, or 'none'&gt;. Stop at your brief's boundary; report verification status.",
      description: "Implement: &lt;lane name&gt;",
      run_in_background: true
    )
  </scoped-lane-template>

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
    1. Read the implementer's phase report; verify steps passed criteria + tests + lint.
    2. If clean: one-line confirmation logged; the SAME implementer continues into
       the next phase (one implementer per plan — you verify at phase reports, you
       do not re-spawn per phase).
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

  <reference>See orchestrate constraint id="agent-returns" for the universal principle.</reference>

  <implementer-specific-drift-patterns note="pattern → re-spawn directive">
    - cherry-picked-phases → "the prior implementer did phases X+Y but skipped A+B — you do A+B in order, then we're done."
    - scope-estimate-pause ("this is 8-12 hours") → "implementer does not estimate. Execute every step."
    - test-deletion-green-suite → "revert test deletions. Substantive completion is the criterion, not literal pass."
    - orphaned-stub (server stubs returning interceptRequired with no client claimant) → "the construction half is missing — add the client intercepts that claim the calls before the stubs are reached."
    - stale-comments-left (consumer lists, routing/fallback paths, return shapes, invariants describing old behavior) → "the code is right but the comments now lie — sweep every touched file and update each comment the change falsified."
  </implementer-specific-drift-patterns>

</constraint>

## Inline smoke protocol (mandatory for live-behavior claims)

Catches the defect classes invisible in a green build — silent acceptance,
proxy divergence, lifecycle windows — by exercising the running system the way
a caller does. Runs after any rebuild or restart of a serving component, after
a batch of changes lands, during lulls (waiting on a build, a lane, a review),
and after ANY fix whose claim is about live behavior, regardless of unit-test
color. The concrete operations are whatever the system under test serves; the
protocol is the shape.

**Minimum battery (post-rebuild):**
1. Read paths: exercise each primary read surface once against content whose
   expected result is known in advance.
2. Write round-trip: create a record through the public write path, then read
   it back BY ITS IDENTIFIER through a separate read path and confirm the
   specific field written — the write's success response is not the readback.
3. Processing observation: confirm in the serving component's own output
   (logs, metrics, traces) that the stages you expect actually fired for the
   operation you just performed. An absent stage where sibling stages are
   present is a finding; absence where NOTHING was recorded is a broken probe.
4. Record what each step is a proxy FOR. A read returning your record proves
   the serving state contains it — not that every processing stage ran;
   retrieval and derivation are different layers.

**Deterministic-identifier lifecycle probes:** create → verify → delete →
verify → recreate → verify, with a caller-supplied stable identifier so every
phase is addressable. Each verify goes through BOTH the derived/indexed read
path AND the direct by-identifier path — they can disagree, and their
disagreement is the signal. The recreate phase is what finds lifecycle
defects; skipping it reduces the probe to a happy-path smoke.

**Probe eligibility comes first:** the probe record's type or category must be
eligible for the derived surface being asserted against — an excluded category
makes every phase vacuous while producing plausible output. Establish
eligibility with a known-positive of the same category BEFORE the probe, and
state the check in the result.

**Adversarial controls:** for any check passing on a zero, an absence, or an
equality — inject the failure, observe red, revert, observe green. A check
never seen red has unknown discriminating power, including the battery itself.

**Baseline-vs-treatment asymmetry is the signal:** run the same probe through
two paths that SHOULD behave alike and compare. A fresh-create completing in
seconds against a recreate that never completes localizes the defect to what
differs; one number alone cannot distinguish slow from never. Record both.

**Confound elimination before classification**, in order, one command each:
is the running binary/build the one you think (inspect it for the change under
test); does the record exist at all (fetch by identifier); has enough time
passed for the mechanism you await (interval derived FROM SOURCE, not memory);
are you observing the right instance, environment, or build flavor.

**Preserved live fixtures:** when a probe reproduces a defect whose fix is
queued, LEAVE THE FIXTURE IN PLACE and name it as that fix's acceptance
criterion — the only artifact proving the fix works against the state that
actually occurred.

**AMBER is the third result:** verified at the storage level, NOT verified at
the serving level. Neither green nor red; its only valid disposition is
reporting the observed asymmetry for investigation. Declaring green on
storage-level evidence is the failure this result exists to prevent; declaring
red without the confound elimination above is the mirror failure.

<constraint id="fallbacks-require-express-user-approval" severity="hard">
  Fallbacks are covers for incorrect behavior. Any silently-degraded lane,
  catch-and-continue, default-on-error, or graceful-degradation path requires
  EXPRESS USER APPROVAL, recorded (ticket or decision) where the fallback lives —
  no agent has discretion to classify one as legitimate. The default response to
  an error state is to FAIL LOUDLY, naming the condition and what was dropped, at
  the point of the mistake. CONVERGENCE TEST: a real fallback repairs the
  condition it fires for and returns the system to its primary path; a lane that
  can fire forever on the same cause is hiding a defect, not handling one — it
  must be an error. An unticketed, unapproved fallback — in a plan, a design, a
  changeset, or existing code you are changing — is a T2 finding raised to the
  user; never wave one through, build one on your own authority, or soften one
  to a note. Retired fallback code is REMOVED, never bypassed in place. The
  instinct that produces fallbacks is sycophancy expressed as architecture —
  treat your own urge to add one as the signal to raise it, not to build it.
</constraint>

<constraint id="deferral-is-a-user-decision" severity="hard">
  Deferral is a USER decision — never yours. Never defer, postpone, descope, or
  "leave for a follow-up" any surfaced defect, gap, or required disposition on
  your own judgement, and never present deferral as an outcome you have chosen.
  The only dispositions you may produce: DO the work, DISPROVE the need with
  evidence, or SURFACE the item UNDECIDED to whoever holds the decision — with
  the honest cost of doing it now. A brief that offers "defer" as one of your
  answers does not make it yours. Postponed is not rejected: an item the user
  defers stays recorded as open work, never silently dropped. Most deferral
  impulses are work avoidance — if the item is in scope and tractable, do it.
  COMPLETENESS IS THE DEFAULT DISPOSITION: a gap discovered in the surface under
  work — a displayed approximation of a value the system can produce for real,
  an unrouted capability the feature plainly needs, an unhandled reachable
  state — is COMPLETION work. Report it as "incomplete without X; building X
  costs Y", never as an optional extra ("available if you want it later",
  "could be a fast-follow") — that framing inverts the decision by taxing the
  user into demanding completeness, when incompleteness is what needs explicit
  approval.
</constraint>
