---
name: test
description: Run the live confirmation — a test plan executed against the built system by the tester agent, with pass, fail or skip recorded per step. The audit of the whole chain against reality; the last gate before a ticket closes. Use after code review ships and the system is rebuilt from the branch.
argument-hint: <test plan id or name, or the ticket to confirm>
---

# Confirm: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For pipeline discipline reference /orchestrate. This skill is confirmation-specific.
</precedence>

<mental-model>
Tests catch bugs; the confirmation confirms. A defect found here is a missing
test by definition, and the fix ticket names the test class that was absent.
The confirmation never substitutes for a test the implementer could have
written.
</mental-model>

## Step 0: The plan

A ticket's confirmation plan is short: per numbered requirement, one live
observation on the built system, plus the write round-trip read back by
identifier, the processing observation in the component's own output, and the
deterministic-identifier lifecycle probe where the ticket touches a lifecycle
(the run-a-smoke-test rulebook). Write it as a test plan node with
`create_test_plan` under the ticket; the orchestrator authors it directly,
since it is a list of observations, not a design.

## Step 1: The system under test

The confirmation runs against a build of the branch under test. Where the
project sanctions a rebuild of a shared service, the orchestrator performs it
at the sanctioned point and records the build identity; a tester never
rebuilds, restarts or reconfigures the operator's running services. Where a
spawned system suffices, the tester spawns its own on picked ports with an
isolated home.

## Step 2: Spawn the tester (background)

```
Agent(subagent_type: "tester",
      prompt: "Execute test plan <id> with run session <uuid> against <build identity>. Run every automated criterion from its stored bytes, verify manual criteria by observation, record pass|fail|skip per test_run with full output, and report to main. The operator's services and stores are never touched.",
      description: "Confirm: <ticket>",
      run_in_background: true)
```

Steps that share state or order run in one tester; independent steps may run
in parallel testers with distinct test_run ids.

## Step 3: Route the results

- All pass → close the ticket; if the project's closing tickets are met, offer
  /retro.
- A failure → a researcher reproduces it and names the test class that would
  have caught it; the fix lands with that test through the pipeline; never a
  fix without its test, never a skip the user did not designate.
- Amber → report the asymmetry to the user; neither green nor red.
