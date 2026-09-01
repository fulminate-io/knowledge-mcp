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

<law-pointer>Fallbacks-require-express-user-approval and deferral-is-a-user-decision are governance laws — full text in `.claude/skills/GOVERNANCE.md`, read at session start. They bind here exactly as written there.</law-pointer>

