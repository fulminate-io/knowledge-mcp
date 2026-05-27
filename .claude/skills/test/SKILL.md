---
name: test
description: Execute a test plan from the knowledge graph. Starts a run session, spawns tester agents to run each test step, and reviews pass/fail/skip results.
argument-hint: <test plan ID or name to execute>
---

# Test: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline (background spawning, user touch points,
non-negotiation), reference /orchestrate. This skill is test-execution-specific.
</precedence>

You are executing a test plan from the knowledge store. Delegate to `tester` agents — they follow steps, run automated criteria, verify manual criteria, and record pass/fail/skip results.

## Step 0: Find the Test Plan

```json
query({ "text": "$ARGUMENTS", "type": "test_plan", "limit": 5 })
```

If multiple match, present and ask the user which one to run. Get the `test_plan_id`.

Walk the plan to see all steps and criteria:

```json
assemble({ "id": "test_plan_id" })
```

## Step 1: Start a Run Session

```json
assemble({ "id": "test_plan_id", "new_run": true })
```

Returns a `run_session` UUID grouping all test_run nodes. Save both `test_plan_id` and `run_session` — pass to the tester agent.

## Step 2: Spawn Tester Agent(s)

<spawn id="tester" background="true">

  <single-tester-invocation>
    Agent(
      subagent_type: "tester",
      prompt: "Execute the test plan [test_plan_id] with run session [run_session]. For each test_run node, read the step details, execute the test (run automated criteria via Bash, verify manual criteria), and record results with mutate(operation: 'update', id: run_id, status: 'pass|fail|skip'). Report results when done.",
      description: "Test: [brief name]",
      run_in_background: true
    )
  </single-tester-invocation>

  <parallel-tester-invocation when="steps test independent components">
    Spawn in parallel (one message, multiple Agent calls):

    Agent(subagent_type: "tester", prompt: "Execute test_run [run_id_1] from plan [test_plan_id] run session [run_session]. ...", description: "Test: [step 1]", run_in_background: true)
    Agent(subagent_type: "tester", prompt: "Execute test_run [run_id_2] from plan [test_plan_id] run session [run_session]. ...", description: "Test: [step 2]", run_in_background: true)
  </parallel-tester-invocation>

  <parallelization-rule>
    Steps testing different components: parallel.
    Steps sharing state or requiring order: sequential.
  </parallelization-rule>

</spawn>

## Step 3: Review Results

```json
assemble({ "id": "test_plan_id", "run_session": "uuid" })
```

Present summary to user:
- Pass/fail/skip counts
- Specific failures with error output
- Any unexpected behavior or findings

Failures need user input (legitimate touch point — design decision).

## Step 4: Handle Failures

- **Fix and re-run**: suggest `/implement` to address, then re-run with new session.
- **Re-run specific tests**: spawn tester targeting failed test_run nodes from same plan.
- **Record findings**: systemic issues → `mutate(operation:"create", type:"finding")`.
- **Skip and continue**: known/acceptable failure → mark `skip` with documented reason.

<constraint id="test-execution-discipline" severity="hard">

  <anti-patterns>
    <pattern>Implementing fixes inline — use implementer agents for that</pattern>
    <pattern>Skipping review with the user before proceeding (failures = legitimate touch point)</pattern>
    <pattern>Parallelizing steps that share state or have ordering requirements</pattern>
    <pattern>Forgetting to pass both test_plan_id and run_session to tester agents</pattern>
  </anti-patterns>

</constraint>
