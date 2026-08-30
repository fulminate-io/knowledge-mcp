---
name: record-decision
description: Record an architectural or design decision in the knowledge graph with full rationale. Use after making a significant choice that future developers should know about.
argument-hint: <brief description of the decision>
---

# Record Decision: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline reference /orchestrate.
This skill is decision-recording-specific.
</precedence>

Record an architectural or design decision in the knowledge store. Good decisions are searchable months later — they tell future developers WHY things are the way they are.

<constraint id="user-only-decisions" severity="hard">

  <rule>
    Decisions are recorded only when the USER makes them. Never record_decision
    for Claude/agent implementation choices. And record_decision requires a summary —
    a deliberate, search-optimized one-liner you author; it is never derived for you.
  </rule>

  <override-default>
    Trained behavior: capture every reasoning step as a "decision." Wrong here —
    a record_decision node implies a user-owned choice with full alternatives
    considered. Agent-level implementation notes go to `thoughts(operation:"think")` and findings, not decision nodes.
  </override-default>

</constraint>

<constraint id="decisions-must-be-complete" severity="hard">

  <rule>
    A decision without rationale is useless. A decision without alternatives is suspicious.
    Always capture the full context before recording.
  </rule>

  <required-fields>
    - WHAT was decided (specific, concrete)
    - WHY this option over alternatives (evidence-based)
    - WHAT ELSE was considered, and why rejected
    - WHAT CONSTRAINTS drove the decision
  </required-fields>

  <do-not-proceed-on>
    Vague or incomplete decisions. Ask the user for the missing fields before recording.
    (Architectural clarification = legitimate touch point.)
  </do-not-proceed-on>

</constraint>

## Step 1: Clarify the Decision

If the description is vague, ask:
- What was decided?
- Why this option over alternatives?
- What else was considered and why rejected?
- What constraints drove the decision?

Optionally use `thoughts(operation:"think")` to record reasoning before committing — creates a chargeable trail linkable to the decision later.

**Recording a decision IS evidence arriving — charge the thoughts that drove it.** The hypotheses the session weighed on the way to this decision should not stay uncharged: charge each driving thought (polarity positive for the option chosen, negative for one the decision rejects), and cite the new decision node as `evidence`. A decision node itself is not chargeable (charges are thought-only); the chargeable trail is the thoughts behind it. Skipping this is the most common way load-bearing rationale ends up at zero charges.

## Step 2: Search for Context

```json
query({ "text": "$ARGUMENTS", "limit": 5 })
query({ "type": "decision" })
```

Look for: prior decisions on the same topic (this might supersede), findings that informed this decision, active projects/steps this decision relates to.

## Step 3: Record the Decision

```json
record_decision({
  "name": "Clear, searchable title",
  "description": "Full explanation + context",
  "choice": "What was decided — specific option",
  "rationale": "Why this option. Reference constraints, findings, requirements.",
  "alternatives": "What else considered. For each, explain why rejected."
})
```

<constraint id="decision-quality" severity="medium">

  <field-quality name="name">
    Write as if someone searches "why did we choose X" or "how does Y work".
    Good: "Serve reflect modes from the loop cache instead of recomputing per call"
    Bad: "Database decision"
  </field-quality>

  <field-quality name="choice">
    Specific and concrete.
    Good: "Use interleaved bin packing with 200 max chunks and 40k token target"
    Bad: "Use batch processing"
  </field-quality>

  <field-quality name="rationale">
    Reference evidence, not opinions.
    Good: "Benchmarks showed 3x throughput. See finding node abc123."
    Bad: "It seemed faster"
  </field-quality>

  <field-quality name="alternatives">
    Explain WHY each was rejected, not just list them.
    Good: "SQLite: rejected because it requires file locking, incompatible with concurrent MCP server access"
    Bad: "Also considered SQLite"
  </field-quality>

</constraint>

## Step 4: Link Related Nodes

If Step 2 surfaced relations:

```json
mutate({ "operation": "link", "from": "decision_id", "to": "related_id", "relationship": "informed-by" })
mutate({ "operation": "link", "from": "decision_id", "to": "old_decision_id", "relationship": "supersedes" })
```

Common relationships:
- `informed-by` — finding/research that led here
- `supersedes` — replaces previous decision
- `relates-to` — general association

## Step 5: Confirm

Show recorded decision + connections + node ID for future reference.

<constraint id="decision-anti-patterns" severity="hard">

  <anti-patterns>
    <pattern>Recording decisions without rationale — "we decided X" is not a decision record</pattern>
    <pattern>Skipping search for related context — decisions don't exist in isolation</pattern>
    <pattern>Vague titles — the title is what future searches match against</pattern>
    <pattern>Skipping alternatives — if there were no alternatives, was it really a decision?</pattern>
  </anti-patterns>

</constraint>

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
