# create_test_plan

## Overview

`create_test_plan` builds a structured test plan in one call: the plan, its steps,
and the pass/fail criteria on each step. Each step describes what to test and the
expected result; criteria can be automated (with a shell `command`) or manual.

## When & how to use

Reach for `create_test_plan` to define a test suite's scope and pass/fail
conditions before running it — typically after the implementation work it
verifies. Once created, `assemble({ id: test_plan_id, new_run: true })` starts a
run you record results against.

Required fields: `name`, `goal`, `summary`, and `steps`. Each step carries `name`,
`description`, `summary`, and optional `criteria`.

```jsonc
create_test_plan({
  "name": "Auth integration tests",
  "goal": "Verify token auth on public endpoints",
  "summary": "Auth smoke suite",
  "steps": [
    { "name": "Valid token accepted", "description": "POST /api with a valid token returns 200",
      "summary": "valid token",
      "criteria": [{ "description": "200 returned", "type": "manual" }] },
    { "name": "Expired token rejected", "description": "POST /api with an expired token returns 401",
      "summary": "expired token" }
  ]
})
```

For the full field reference, run `help("create_test_plan")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `format` | string |  |  | Output format: 'text' (default, walks the tree) or 'json' (structured: {id, name, step_ids}). |
| `goal` | string | yes |  | What this test plan verifies |
| `name` | string | yes |  | Test plan name |
| `steps` | array of object | yes |  | Ordered list of test steps. Each step REQUIRES name, description, and summary (handler enforces). |
| `steps[]` | object |  |  | Step object: {"name":"...","description":"...","summary":"required search-optimized one-line summary, max 500 chars","criteria":[{"description":"...","command":"...","type":"automated\|manual"}]} |
| `steps[].criteria` | array of object |  |  | Pass/fail criteria for this test step. |
| `steps[].criteria[]` | object |  |  | Criterion object |
| `steps[].criteria[].command` | string |  |  | Verification command (for automated criteria) |
| `steps[].criteria[].description` | string |  |  | What the criterion verifies |
| `steps[].criteria[].type` | string |  | automated, manual | Criterion type: automated or manual |
| `steps[].description` | string |  |  | Test step description (required) |
| `steps[].name` | string |  |  | Test step name (required) |
| `steps[].summary` | string |  |  | Required search-optimized one-line summary, max 500 chars (max length: 500) |
| `summary` | string | yes |  | Required search-optimized one-line summary, max 500 chars (handler enforces). (max length: 500) |
<!-- END GENERATED: params -->
