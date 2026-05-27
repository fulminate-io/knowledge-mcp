---
name: test-plan
description: Design a test plan collaboratively. Researches what needs testing, discusses scope and criteria interactively, then creates a structured test plan with steps and pass/fail criteria.
argument-hint: <description of what to test>
---

# Create Test Plan: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline (background spawning, user touch points),
reference /orchestrate. This skill is test-planning-specific.
</precedence>

You are creating a structured test plan in the knowledge store. Delegate to the `test-planner` agent — it researches the codebase, checks existing decisions and rules, then creates a test plan with steps and pass/fail criteria.

## Step 0: Check Index Freshness

```json
manage({ "operation": "status" })
```

If behind HEAD, offer to reindex. Test-planner needs accurate code search.

## Step 1: Understand the Goal

Read `$ARGUMENTS`. If unclear or ambiguous, ask specific clarifying questions before spawning. (Architectural clarification = legitimate touch point.)

## Step 2: Spawn the Test-Planner Agent

<spawn id="test-planner" background="true">

  <invocation>
    Agent(
      subagent_type: "test-planner",
      prompt: "Design a test plan for the following. Research the codebase first (search knowledge nodes, code, past decisions). Then discuss test scope, step ordering, and pass/fail criteria with the user. Create the test plan with create_test_plan once agreed. Verify with assemble.\n\nWhat to test: $ARGUMENTS",
      description: "Test plan: [brief topic]",
      run_in_background: true
    )
  </invocation>

</spawn>

The test-planner will: search knowledge nodes → batch-search code → check past decisions/rules → deep-dive key functions via traverse → think about trade-offs → discuss scope/ordering/criteria with user → create test plan → verify with assemble.

## Step 3: Present the Test Plan

When the test-planner returns:
- Plan structure (steps + ordering)
- Pass/fail criteria per step
- Any assumptions or scope decisions

User reviews before execution (legitimate touch point — plan approval, like /plan reviewer gate).

## Step 4: Bridge to Execution

- Ready: suggest `/test` to execute.
- Needs changes: spawn another test-planner with modifications, or `mutate(operation:"update", ...)` directly.
- Needs more research: suggest `/research` on the unclear area.

<constraint id="test-planning-discipline" severity="hard">

  <anti-patterns>
    <pattern>Planning inline — use the test-planner agent</pattern>
    <pattern>Creating a test plan without understanding the goal first</pattern>
    <pattern>Skipping the index freshness check</pattern>
    <pattern>Proceeding to execution without user approval of the plan</pattern>
  </anti-patterns>

</constraint>
