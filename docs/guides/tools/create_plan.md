# create_plan

## Overview

`create_plan` builds a full plan tree in one call: a plan, its phases, the steps
within each phase, and the success criteria on each step. Instead of issuing one
`mutate(create)` per node and wiring the `contains` edges by hand, you describe
the whole hierarchy as nested JSON and get it created atomically.

A plan sits inside the work hierarchy: project → ticket → **plan** → phase → step
→ criterion.

## When & how to use

Reach for `create_plan` once a piece of work is understood well enough to break
into ordered, verifiable steps — typically after research and a ticket exist. Pass
`ticket_id` to link the plan under its parent ticket.

See the Parameters table below for the required fields. Each step can carry
`file_paths` (the files it modifies) and `criteria` with a `command` for automated
checks.

One cross-field rule the table can't express: **exactly one** of `pattern_ids`,
`no_patterns_reason`, or `proposed_patterns` must be supplied — the architecture
pattern this plan extends, an audited reason none applies, or a sketch of the
patterns it introduces. (The `language_patterns` field is independent of this
rule.)

```jsonc
create_plan({
  "name": "Add auth", "goal": "Ship token-based auth for the public API",
  "summary": "Auth middleware + handler plumbing + integration tests",
  "ticket_id": "ticket_abc",
  "no_patterns_reason": "extends existing middleware chain, no new pattern",
  "phases": [{
    "name": "Phase 1", "overview": "Wire middleware", "summary": "middleware",
    "steps": [{
      "name": "Add auth middleware", "description": "Validate tokens in the request pipeline",
      "summary": "auth middleware", "file_paths": "pkg/auth/token.go",
      "criteria": [{ "description": "Tests pass", "type": "automated", "command": "go test ./pkg/auth/..." }]
    }]
  }]
})
```

After creating a plan, `assemble({ id: plan_id })` to verify the tree landed as
intended. For the full field reference, run `help("create_plan")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `format` | string |  |  | Output format: 'text' (default, walks the tree + warnings) or 'json' (structured: {id, name, node_ids, warnings}). |
| `goal` | string | yes |  | What this plan aims to achieve |
| `language_patterns` | array of string |  |  | Language-specific defensive patterns/findings (e.g., Go anti-patterns from practice/go with metadata.dsl_pattern set) the plan should be vigilant of. Wired as plan→<finding\|pattern> EdgeAudits edges. INDEPENDENT of pattern_ids / no_patterns_reason / proposed_patterns — accepts any non-empty subset, including none. Broken/unknown IDs produce a non-fatal warning under the `## Warnings` section. |
| `language_patterns[]` | string |  |  |  |
| `name` | string | yes |  | Plan name |
| `no_patterns_reason` | string |  |  | Audited escape hatch when no pattern applies (trivial doc edit, scaffolding, etc.). Persisted as plan-node metadata `no_patterns_reason`. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied. |
| `open_questions` | array of object |  |  | Open questions that need user input before implementation can proceed. Creates question nodes (status: open) linked to the plan. |
| `open_questions[]` | object |  |  | Question object: {"question":"...","context":"why this question matters and what options exist"} |
| `open_questions[].context` | string |  |  | Why this question matters and what options exist |
| `open_questions[].question` | string |  |  | The open question to surface for user input |
| `pattern_ids` | array of string |  |  | Canonical pattern node IDs this plan extends. Wired as plan→pattern uses edges. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied. Broken/unknown IDs produce a non-fatal warning surfaced in the response (under a `## Warnings` section), not an error — v1 tolerates patterns that have not yet been encoded. |
| `pattern_ids[]` | string |  |  | Pattern node ID |
| `phases` | array of object | yes |  | Ordered list of phases. Each phase REQUIRES name and summary; each step REQUIRES name, description, and summary (handler enforces). |
| `phases[]` | object |  |  | Phase object: {"name":"...","overview":"...","summary":"required search-optimized one-line summary, max 500 chars","steps":[{"name":"...","description":"...","summary":"required search-optimized one-line summary, max 500 chars","file_paths":"...","criteria":[{"description":"...","command":"...","type":"automated\|manual"}]}]} |
| `phases[].name` | string |  |  | Phase name (required) |
| `phases[].overview` | string |  |  | Phase overview |
| `phases[].steps` | array of object |  |  | Ordered list of steps in this phase. |
| `phases[].steps[]` | object |  |  | Step object |
| `phases[].steps[].criteria` | array of object |  |  | Success criteria for this step. |
| `phases[].steps[].criteria[]` | object |  |  | Criterion object |
| `phases[].steps[].criteria[].command` | string |  |  | Verification command (for automated criteria) |
| `phases[].steps[].criteria[].description` | string |  |  | What the criterion verifies |
| `phases[].steps[].criteria[].type` | string |  | automated, manual | Criterion type: automated or manual |
| `phases[].steps[].description` | string |  |  | Step description (required) |
| `phases[].steps[].file_paths` | string |  |  | Comma-separated file paths this step touches |
| `phases[].steps[].name` | string |  |  | Step name (required) |
| `phases[].steps[].summary` | string |  |  | Required search-optimized one-line summary, max 500 chars (max length: 500) |
| `phases[].summary` | string |  |  | Required search-optimized one-line summary, max 500 chars (max length: 500) |
| `proposed_patterns` | array of object |  |  | Not-yet-cataloged patterns this plan introduces. Each entry creates a pattern node with status='emerging' linked via uses. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied. |
| `proposed_patterns[]` | object |  |  | Proposed pattern object |
| `proposed_patterns[].name` | string |  |  | Proposed pattern name |
| `proposed_patterns[].sketch` | string |  |  | Interface sketch / pseudocode describing the proposed pattern shape (optional) |
| `research_id` | string |  |  | Research project ID that informed this plan (optional — creates informed-by edge) |
| `summary` | string | yes |  | Required search-optimized one-line summary, max 500 chars (handler enforces). (max length: 500) |
| `ticket_id` | string |  |  | Ticket node ID to link this plan under (optional) |
<!-- END GENERATED: params -->
