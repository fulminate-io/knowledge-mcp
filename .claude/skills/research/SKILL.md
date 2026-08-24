---
name: research
description: Research a topic using the knowledge store. Searches code, knowledge nodes, and existing decisions to build understanding. Use when investigating how something works, exploring options, or gathering context before implementation.
argument-hint: <topic or question to research>
---

# Research: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline (background spawning, user touch points,
non-negotiation), reference /orchestrate. This skill is research-specific.
</precedence>

You are researching a topic using the knowledge store. Delegate to the `researcher` agent — it is optimized for thorough investigation using semantic search, code graph traversal, and knowledge node queries.

## Step 0: Check Index Freshness

```json
manage({ "operation": "status" })
```

If the index is behind HEAD, tell the user and offer to reindex. Fresh search results are critical for accurate research.

## Step 1: Spawn the Researcher Agent

<spawn id="researcher" background="true">

  <invocation>
    Agent(
      subagent_type: "researcher",
      prompt: "Research the following topic thoroughly. Start with thoughts({ operation: 'recall', mode: 'context', query: '<the topic>' }) to load the cross-type context pack (related decisions, findings, tickets, prior thoughts, their edge-connected neighbors, and recent activity). Search both knowledge nodes (decisions, findings, research, rules) and code. Use traverse(graph: 'code', edge_types: ['calls'], direction: 'both') on key functions to understand call graphs. Check for past architectural decisions. Present findings with precise file:line references and node IDs.\n\nTopic: $ARGUMENTS",
      description: "Research: [brief topic]",
      run_in_background: true
    )
  </invocation>

  <skip-spawn-when>
    Structured nodes (plans, test plans, agents) — use assemble(id: node_id) directly.
    Faster than spawning a researcher for known-structure nodes.
  </skip-spawn-when>

</spawn>

The researcher will: load the context pack → batch-search code (3-5 queries) → deep-dive key functions via traverse → check past decisions → web-search for external context if unsure → record charged thoughts → present findings with precise references.

**Findings charge hypotheses.** When a research finding confirms or refutes a hypothesis the session recorded as a thought, charge that thought — polarity positive if the finding's evidence SUPPORTS the hypothesis's claim, negative if it CONTRADICTS it — citing the finding node ID via the `evidence` param. A conclusion that never charges its hypothesis leaves the reasoning graph permanently under-evidenced.

## Step 2: Present Results

When the researcher returns, present findings to the user in this shape:

```
## Research: [Topic]

### Summary
[2-3 sentence answer]

### What Exists
- [Implementations with file:line]

### What's Been Decided
- [Past decisions with rationale + node IDs]

### How It Works
- [Key flows and components]

### What's Unclear
- [Open questions]
```

## Step 3: Follow-Up

- Follow-up question → spawn another researcher with the specific question.
- Quick lookup → use `search` or `query` directly.
- Ready to act → suggest `/plan` to create an implementation plan.

Before creating standalone research nodes, check for existing projects/tickets:

```json
query({ "type": "project" })
query({ "type": "ticket" })
```

`create_research` accepts optional `ticket_id` — pass it to link directly.

<constraint id="research-discipline" severity="hard">

  <anti-patterns>
    <pattern>Doing research inline instead of using the researcher agent</pattern>
    <pattern>Presenting findings without file:line references</pattern>
    <pattern>Suggesting improvements unless explicitly asked — just document what exists</pattern>
    <pattern>Skipping the index freshness check — stale results lead to wrong conclusions</pattern>
  </anti-patterns>

</constraint>

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
