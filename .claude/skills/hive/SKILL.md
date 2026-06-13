---
name: hive
description: Join a hive as a worker (claim work, do it, report the result) or act as a coordinator (dispatch role-targeted work and read the outcomes). A hive is a cloud work-queue for coordinating multiple agents across machines. Use when you want agents on different machines to pass work to each other by capability/role instead of a human hand-carrying it.
argument-hint: join <hive> as <role>[, <role>…]   |   dispatch <hive>
---

# Hive: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline (background spawning, user touch points,
non-negotiation), reference /orchestrate. This skill is hive-specific.
</precedence>

A **hive** is a cloud work-queue that lets several agents — on the same machine or
on different machines — coordinate by **role**. One agent puts a unit of work into
the hive addressed to a role (`@knowledge-developer`); any worker holding that role
claims it, does it, and reports the result back. The result is readable by whoever
sent it.

**Grounding use case:** cross-machine capability teams. You run a `knowledge-developer`
worker on one machine and an `ml-developer` worker on another. When the ml-developer
hits a bug in a tool the knowledge-developer owns, it does not wait for a human to
carry the report over — it `send`s the bug report to `@knowledge-developer`, and the
knowledge-developer worker claims and fixes it autonomously.

You are dumb on purpose. **The client daemon is your operating system** — it monitors
your work, keeps your claim alive, and removes you if your machine dies. You never
heartbeat, never renew a lease, never track time. You only **claim** work and
**report** when it's done. Everything else is the daemon's job.

The whole agent surface is the **five `hive` ops**: `register`, `send`, `claim`,
`ack`, `fail`. Reads — who's registered, what's done, what's blocked — go through
ordinary `query` / `search`, because everything lives in the graph. The hive is a
hosted feature and requires login; a logged-out agent's hive calls fail loud.

Pick the mode from `$ARGUMENTS`:

- starts with **`join`** → **Worker mode** (claim → work → report).
- starts with **`dispatch`** (or you just want to send work and read results) → **Coordinator mode**.

---

## Worker mode — `/hive join <hive> as <role_1>, …, <role_N>`

You are a worker holding one or more roles. You claim work addressed to any of your
roles (or to the shared `@queue`), do it, and report the outcome.

### Step 1 — Register

```json
hive({ "op": "register", "hive": "<hive>", "name": "<your-label>", "roles": ["<role_1>", "<role_2>"] })
```

The hive is created on first register. `name` is a human-friendly label; your true
identity is your session, which the daemon tracks for you. Roles are free-form
strings — whatever the user named at join.

### Step 2 — Work loop

Repeat the following. **This is a loop**: after you finish one item, claim again. Keep
going until you are evicted (Step 4) or the human ends the session.

1. **Claim one item:**

   ```json
   hive({ "op": "claim", "hive": "<hive>" })
   ```

   `claim` atomically leases exactly one eligible item — work addressed to `@queue`
   or to one of your roles, highest priority first. Exactly one worker wins each item.

2. **Branch on the result:**
   - **`no eligible work to claim right now`** → nothing to do. Wait briefly, then
     claim again. Do not hot-loop.
   - **`claimed message <msg_id> …`** with a `body` → you hold the work. Note the
     `msg_id` and read the `body` (the task description / payload). Go to step 3.
   - **an error containing `evicted from the hive`** → you have been removed. Go to
     Step 4 (dormant). Do not retry.

3. **Do the work, then report.** Treat the `body` as a task for one of your roles and
   carry it out with your normal capabilities — e.g. a `knowledge-developer` claiming a
   bug report runs the fix end to end; an `implementer` claiming a ticket runs the
   implementation flow. When you finish:

   ```json
   hive({ "op": "ack", "hive": "<hive>", "msg_id": "<msg_id>", "result": "<what you did / the outcome>" })
   ```

   If you genuinely **cannot** finish it (missing access, the task is malformed, it is
   not something your role can do), report it blocked instead:

   ```json
   hive({ "op": "fail", "hive": "<hive>", "msg_id": "<msg_id>", "reason": "<why you cannot finish>" })
   ```

   A `fail` is terminal and visible to the sender — it is not a retry. Use it to surface
   a real could-not-finish, not to dodge hard work.

4. Loop back to step 1.

### Step 3 — Your only two obligations

Everything else is handled for you by the daemon. You are responsible for exactly two
things:

- **Report promptly.** `ack` (or `fail`) as soon as the work concludes, so the outcome
  is recorded. Do not sit on a finished item.
- **Go dormant if evicted** (Step 4).

You do **not** heartbeat, renew, or track a lease — the daemon watches your session and
keeps your claim alive while you work, even across a long sub-task that makes no hive
calls. Do not invent checkpoint pings.

### Step 4 — Dormant if evicted

If any hive call returns an error containing **`evicted from the hive`**, you have been
voted off: stop the work loop and stop calling `hive` entirely. The ban is enforced for
this whole session and cannot be evaded by re-registering. Re-joining requires a human to
start a fresh session — you cannot restart yourself. Tell the user you were evicted and
stand down.

---

## Coordinator mode — `/hive dispatch <hive>`

You put work into the hive and read what comes back. You do not claim your own work.

### Send work

```json
hive({ "op": "send", "hive": "<hive>", "to": "@<role>", "body": "<the task>", "priority": "<int, optional>", "reply_to": "<msg_id, optional>" })
```

- **`to`** routes the work and is required. Only two forms:
  - `@<role>` — any member holding that role may claim it.
  - `@queue` — any worker may claim it, role-agnostic.

  There is no `*` broadcast and no addressing a worker by name — work goes to a role or
  the shared queue, not to a specific agent.
- **`priority`** is an integer string; higher is claimed first.
- **`reply_to`** is the id of a message this one answers; the eventual result links back
  to it.

### Read outcomes — through the graph, not a hive op

There is no hive "receive." Everything is in the graph, so read it with `query` /
`search`:

- **Completed work** carries the worker's `result` inline, and its `ack` links back to
  the original via a `responds-to` reply edge — follow those edges to collect answers to
  the work you sent.
- **Blocked work** is a message marked blocked with the worker's `reason`. You **see**
  could-not-finish, not just successes — that visibility is the point.

Use `query`/`search` over the hive's message nodes to find done results and blocked items
with their reasons.

### Re-dispatch is deliberate

A blocked item is **terminal** — the system does not auto-retry or recycle it. If you
want it done, you decide: fix the task and `send` it again, route it to a different role,
or drop it. Re-dispatch is an explicit act, never automatic.

---

## What this skill does NOT do

- **No heartbeat / lease renewal / checkpoint pings.** The daemon owns liveness. The
  worker only claims and reports.
- **No retry logic.** A blocked item is terminal and surfaced; the coordinator
  re-dispatches deliberately.
- **No broadcast or direct-to-name addressing.** Work is routed to `@<role>` or
  `@queue` only.
- **No server push.** Pull only — `claim` each loop tick.
- **No fixed role taxonomy.** Roles are whatever strings the user configures at join.
