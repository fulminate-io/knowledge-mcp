# create_ticket

## Overview

`create_ticket` creates a ticket — a unit of work inside a project. A ticket holds
plans; in the work hierarchy it sits between the project and the plan: project →
**ticket** → plan → phase → step → criterion.

## When & how to use

Reach for `create_ticket` to carve a project into discrete, plannable units of
work. Pass `project_id` to link the ticket under its parent project. See the
Parameters table below for the required fields.

One cross-field rule the table can't express: **exactly one** of `pattern_ids`,
`no_patterns_reason`, or `proposed_patterns` must be supplied — the architecture
pattern this ticket extends, an audited reason none applies, or a sketch of the
patterns it introduces. (The `language_patterns` field is independent of this
rule.)

```jsonc
create_ticket({ "name": "Add OAuth2 login", "project_id": "proj_abc",
                "description": "Add social login via OAuth2 to the public API",
                "summary": "OAuth2 social login for the public API",
                "no_patterns_reason": "extends existing auth handler, no new pattern" })
```

For the full field reference, run `help("create_ticket")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `backend` | string |  |  | Backend identifier (e.g. "linear") to stamp on the ticket's `backend` metadata. Set by the client-side intercept after a successful remote create; never supplied by direct callers. |
| `description` | string | yes |  | Ticket description |
| `external_id` | string |  |  | External tracker ID (e.g. JIRA-123, GH-456) |
| `external_url` | string |  |  | Deeplink URL to the remote ticket. Maps to `external_url` metadata. |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured: {id, name, warnings}). |
| `labels` | string |  |  | Comma-separated labels or tags |
| `language_patterns` | array of string |  |  | Language-specific defensive patterns/findings (e.g., Go anti-patterns from practice/go with metadata.dsl_pattern set) the ticket should be vigilant of. Wired as ticket→<finding\|pattern> EdgeAudits edges. INDEPENDENT of pattern_ids / no_patterns_reason / proposed_patterns — accepts any non-empty subset, including none. Broken/unknown IDs produce a non-fatal warning under the `## Warnings` section. |
| `language_patterns[]` | string |  |  |  |
| `linear_group_id` | string |  |  | Linear team UUID inherited from the parent project. Maps to `linear_group_id` metadata. |
| `linear_group_key` | string |  |  | Linear team key (e.g. "ABC") inherited from the parent project. Maps to `linear_group_key` metadata. |
| `linear_id` | string |  |  | Linear-side ticket UUID returned by backend.CreateTicket. Maps to `linear_id` metadata. |
| `linear_project_id` | string |  |  | Linear-side project UUID for the parent project. Maps to `linear_project_id` metadata so the ticket carries an explicit backend-side parent pointer. |
| `name` | string | yes |  | Ticket name or title (synced to the Linear issue title, which caps at 255 chars). (max length: 255) |
| `no_patterns_reason` | string |  |  | Audited escape hatch when no pattern applies (trivial doc edit, scaffolding, etc.). Persisted as ticket-node metadata `no_patterns_reason`. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied. |
| `pattern_ids` | array of string |  |  | Canonical pattern node IDs this ticket extends. Wired as ticket→pattern uses edges. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied. Broken/unknown IDs produce a non-fatal warning surfaced in the response (under a `## Warnings` section), not an error — v1 tolerates patterns that have not yet been encoded. |
| `pattern_ids[]` | string |  |  | Pattern node ID |
| `priority` | string |  |  | Ticket priority (e.g. high, medium, low) |
| `project_id` | string | yes |  | Parent project node ID (required — links ticket to project) |
| `proposed_patterns` | array of object |  |  | Not-yet-cataloged patterns this ticket introduces. Each entry creates a pattern node with status='emerging' linked via uses. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied. |
| `proposed_patterns[]` | object |  |  | Proposed pattern object |
| `proposed_patterns[].name` | string |  |  | Proposed pattern name |
| `proposed_patterns[].sketch` | string |  |  | Interface sketch / pseudocode describing the proposed pattern shape (optional) |
| `summary` | string | yes |  | Required search-optimized one-line summary, max 500 chars (handler enforces). (max length: 500) |
<!-- END GENERATED: params -->
