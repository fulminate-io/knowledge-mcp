# hive

## Overview

`hive` is the agent-facing surface of the cloud work-queue: a single
op-dispatched tool through which agents coordinate by claiming and completing
units of work in a per-account named hive. It is graph-native (hives, work
items, and members are all nodes) and **cloud-only** — a self-hosted server with
no login fails loud on every hive op.

A hive **member is a session**: your true, unfakeable identity is your MCP
session-id; the `name` you register is just a human-friendly label. Work flows
through a strict terminal model — a claimed item ends either `done` (you acked
it with a result) or `blocked` (you failed it with a reason). There is no retry
and no reclaim: a non-completed item is never silently re-dispatched.

## When & how to use

Use `hive` when several agents need to share a pool of work. A coordinator
`send`s units of work; workers `register` with their roles, `claim` one eligible
item at a time (the claim is an atomic, exactly-one-wins lease), do the work, and
`ack` it — or `fail` it if they genuinely cannot finish.

Reads — who is registered, what is done or blocked — are NOT hive ops: query the
graph directly (everything is a node). A worker that is finished just
disconnects.

```jsonc
// Worker: declare yourself, then claim and complete one item
hive({ "op": "register", "hive": "build-farm", "name": "alice", "roles": ["worker"] })
hive({ "op": "claim", "hive": "build-farm" })
hive({ "op": "ack", "msg_id": "<claimed id>", "result": "built + tested green" })

// Coordinator: enqueue a unit of work routed to any worker
hive({ "op": "send", "hive": "build-farm", "to": "@queue", "body": "rebuild module X", "priority": "5" })
```

`to` accepts ONLY `@<role>` (any member holding that role may claim) or `@queue`
(any worker may claim, role-agnostic). Broadcast (`*`) and direct (bare `<name>`)
addressing are not supported in v1 and are rejected client-side. For the full
reference, run `help("hive")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `body` | string |  |  | The work body (send) — the task description / payload for the claiming worker. |
| `hive` | string |  |  | Hive name (register/send/claim). The hive is created implicitly on first register/send. |
| `msg_id` | string |  |  | The message (work item) ID to ack or fail — the id of the item you claimed. |
| `name` | string |  |  | Your human-friendly member label (register). Your true identity is your session. |
| `op` | string | yes | register, send, claim, ack, fail | Operation: register (declare self) \| send (enqueue work) \| claim (lease one eligible item) \| ack (complete with result) \| fail (block with reason). |
| `priority` | string |  |  | Work priority (send), an integer string; claim is priority-first (higher first). |
| `reason` | string |  |  | Failure reason (fail) — why you could not finish; reported to the sender and queryable. |
| `reply_to` | string |  |  | Message ID this work answers (send) — the ack of the reply links back to it via responds-to. |
| `result` | string |  |  | Completion result (ack) — stored inline on the done message. |
| `roles` | array of string |  |  | Roles you hold (register). claim matches work addressed to @queue OR @<one-of-your-roles>. |
| `roles[]` | string |  |  |  |
| `to` | string |  |  | Work routing (send): '@<role>' (any member holding that role may claim) or '@queue' (any worker may claim, role-agnostic). ONLY @<role> or @queue — no '*' broadcast, no bare '<name>' direct addressing (those are a deferred messaging layer). |
<!-- END GENERATED: params -->
