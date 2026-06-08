# create_project

## Overview

`create_project` creates a project — the top-level container for a body of related
work. A project holds tickets, and tickets hold plans: **project** → ticket → plan
→ phase → step → criterion.

## When & how to use

Reach for `create_project` to open a new initiative that will span several tickets
— an overhaul, a new subsystem, a migration. For one-off work a ticket under an
existing project is usually enough.

See the Parameters table below for the required fields.

```jsonc
create_project({ "name": "Auth overhaul",
                 "description": "Refactor the authentication system end to end",
                 "summary": "End-to-end refactor of the authentication system" })
```

For the full field reference, run `help("create_project")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `backend` | string |  |  | Backend identifier (e.g. "linear") to stamp on the project's `backend` metadata. Set by the client-side intercept after a successful remote create; never supplied by direct callers. |
| `description` | string | yes |  | Project description (must stay under 250 chars for Linear). (max length: 249) |
| `external_url` | string |  |  | Deeplink URL to the remote project. Maps to `external_url` metadata. |
| `format` | string |  |  | Output format: 'text' (default) or 'json' (structured: {id, name}). |
| `group` | string |  |  | Optional. Backend group key (Linear team key, Jira project key, GitHub repo, etc.) — required when LINEAR_API_KEY (or other backend env var) is set AND multiple groups exist; auto-defaults when only one group exists; ignored when no backend is enabled. |
| `linear_group_id` | string |  |  | Linear team UUID the project landed under. Maps to `linear_group_id` metadata. |
| `linear_group_key` | string |  |  | Linear team key (e.g. "ABC") the project landed under. Maps to `linear_group_key` metadata. |
| `linear_id` | string |  |  | Linear-side project UUID returned by backend.CreateProject. Maps to `linear_id` metadata. |
| `name` | string | yes |  | Project name (synced to the Linear project name, which caps at 255 chars). (max length: 255) |
| `summary` | string | yes |  | Required search-optimized one-line summary, max 500 chars. (max length: 500) |
<!-- END GENERATED: params -->
