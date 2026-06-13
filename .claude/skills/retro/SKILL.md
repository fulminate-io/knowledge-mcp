---
name: retro
description: Capture the session feedback loop after work is verified for real — record a reproduction/interaction guide, charge the session's reasoning with the real-world evidence, record findings, and close the ticket. Use AFTER a feature is smoke-tested and confirmed working, or an investigation is remediated — never before.
argument-hint: <optional focus — the feature/fix/investigation this session delivered>
---

# Retro: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

This is the terminal phase of the workflow chain: **brainstorm (WHY) →
orchestrate (execution) → retro (feedback loop)**. The main LLM runs it directly —
it is NOT a delegating phase that spawns subagents. It runs in the loop that holds
the full session context, because only that loop knows what was actually built,
what was observed working, and which hypotheses the real-world result confirmed.

For execution-phase discipline reference /orchestrate. For metacognition over the
existing thought graph (a different job — see Step 6) reference /reflect.
</precedence>

<mental-model>
brainstorm = WHY. orchestrate = the WORK. retro = the FEEDBACK LOOP.

retro's deliverable: durable, recall-able session knowledge — a reproduction guide
future sessions can act on, plus the reasoning validated against what actually
happened. The session's thoughts, the broken→fixed evidence, and the "how do I
reproduce / test / connect to this" knowledge otherwise live only in scrollback and
evaporate. retro writes them into the graph before they are lost.

retro CREATES session artifacts. /reflect ANALYZES the existing thought graph.
They are different skills; retro may hand off to /reflect at the end (Step 6).
</mental-model>

<constraint id="retro-precondition-gate" severity="hard" phase="entry">

  <rule>
    retro captures what HAPPENED. It is only complete and only worth running AFTER
    the work has been verified in the real world: a feature smoke-tested and
    confirmed working, or an investigation whose remediation is confirmed resolved.
    Running it earlier captures a half-done picture — you cannot write down steps
    that have not happened yet.
  </rule>

  <self-gate>
    Before capturing anything, confirm the precondition holds. Positive evidence is
    ONE of:
    - the user confirmed it works live ("verified", "confirmed", "it works", "shipped and working"), OR
    - this session ran a real smoke test / exercised the thing end-to-end and OBSERVED it succeed, OR
    - (investigation) the remediation was applied and confirmed to resolve the original symptom.

    The following are NOT sufficient evidence on their own — they are the premature
    trap, and you must not capture on them alone:
    - green unit tests
    - plan completion / all phases done
    - tickets closed / "implementation finished"

    If the precondition does NOT hold, STOP. Say so plainly, name what still needs
    to be exercised for real, and do not write the guide. Premature capture is this
    skill's cardinal failure.
  </self-gate>

</constraint>

You are capturing the feedback loop for the work this session delivered. Work
through the steps in order. Be concrete and reproducible — the guide is written for
a future session (or teammate) who has none of your context.

## Step 0: Self-gate

Apply `<constraint id="retro-precondition-gate">` above. Only proceed past this
point when the real-world-verification precondition holds. If it does not, stop
here and report what still needs to be exercised.

## Step 1: Gather the session

Pull the reasoning and the work item this session produced.

```json
thoughts({ "operation": "recall", "mode": "context", "query": "<this session's topic / feature / fix>", "limit": 20 })
query({ "type": "project" })
query({ "type": "ticket" })
```

Identify the project/ticket the work belongs to (there may be none — e.g. a
freeform investigation). Assemble its context so you capture against the real
record, not memory:

```json
assemble({ "id": "<project_or_ticket_id>" })
```

## Step 2: Capture the verification evidence

State plainly what was actually observed that proves the work is real — the smoke
test that passed, the command that now succeeds, the symptom that is gone, the live
interaction that worked. Capture the exact, reproducible form (commands, inputs,
expected output).

This observed evidence is reused twice below: it is the **How to test** section of
the guide (Step 3) and the reasoning the charge cites (Step 4). Capture it once,
precisely, here.

## Step 3: Write the reproduction/interaction guide (a `document` node)

Write the guide as a `document` node — the existing graph type for durable
overviews. Do NOT invent a new node type.

```json
mutate({
  "operation": "create",
  "type": "document",
  "name": "<feature/fix> — reproduction & interaction guide",
  "summary": "<one-line: what this delivers + how to reach/test/connect to it>",
  "content": "<the guide — see the five required sections below>"
})
```

The `content` MUST contain these five sections:

- **Overview** — what the feature/fix is and why it exists.
- **Steps to reproduce** — how to reach the working state (or reproduce the original
  issue the work fixed), step by step.
- **How to test** — the smoke test and any commands, from Step 2 — how to confirm it
  works.
- **How to connect / interact** — wiring, config, endpoints, flags, the MCP/registration
  steps, or whatever it takes to drive the thing.
- **Gotchas / limitations** — known sharp edges, things that look broken but aren't,
  what is deliberately out of scope.

## Step 3.5: Persist feature knowledge as thoughts (new features + feature changes)

The guide document is the operator runbook; feature knowledge must ALSO live in the
thought graph where `recall` finds it. For **every genuinely new feature/surface the
session delivered** — a new tool, mode, op, param, lever, or behavior — record
**multiple thoughts, one per facet**, so the knowledge survives independently of
this session's transcript:

1. **WHAT it is** — the feature described in plain terms: what it delivers and why
   it exists. The summary leads with the feature's name/invocation so recall matches.
2. **HOW it works** — the mechanism: key functions/files, the data flow, the design
   decisions that shape behavior (link the decision nodes).
3. **HOW to use it** — the invocation shape: tool + params, example calls, expected
   output, the failure modes a caller will actually see.

```json
thoughts({ "operation": "think",
           "content": "<one facet — what/how-it-works/how-to-use>",
           "summary": "<feature name + facet, recall-optimized>",
           "session": "<this session>",
           "links": ["<guide document id>", "<decision id>"] })
```

**For changes to an existing feature, do NOT mint duplicates.** First
`thoughts({"operation":"recall", "query": "<feature name>"})` for the prior feature
thoughts; if the change alters their content, update them (or supersede via
`branches_from` when the old description is now wrong, invalidating the original).
Only create fresh facet thoughts for facets that did not exist before.

Charge each facet thought immediately with the Step 2 verification evidence — the
feature is verified (the precondition gate passed), so these are validated knowledge,
not hypotheses. Skip this step entirely for sessions that delivered no feature-level
change (pure refactors, doc fixes, investigations with no new surface).

## Step 4: Charge validated thoughts + record findings

The real-world result is evidence. Charge the session's key hypotheses with it so
the broken→fixed transition is recorded as validated, not just asserted. Polarity
tracks the claim: positive when the observed result supports the hypothesis,
negative when it contradicts one the session held.

```json
thoughts({ "operation": "charge", "thought": "<session hypothesis id>",
           "polarity": "positive", "weight": 7,
           "reasoning": "<the observed real-world evidence from Step 2>" })
```

Record any durable conclusion the session reached that is not yet a node:

```json
mutate({ "operation": "create", "type": "finding",
         "name": "<concise, searchable title>",
         "description": "<what was concluded>",
         "evidence": "<reference / reasoning>" })
```

## Step 5: Link everything + close the ticket

Link the guide (and findings) to the project/ticket so they are reachable from the
work record:

```json
mutate({ "operation": "link", "from": "<project_or_ticket_id>", "to": "<document_id>", "relationship": "produced" })
```

Then, **if a ticket is associated with the work, close it** — set its status to your
tracker's done/closed workflow state:

```json
mutate({ "operation": "update", "id": "<ticket_id>", "status": "<your tracker's done/closed state>" })
```

If there is no ticket (e.g. a freeform investigation), skip the close silently —
there is nothing to close.

## Step 6: Optional hand-off to /reflect

retro CREATED this session's artifacts. /reflect ANALYZES the existing thought graph
(personality, tensions, blind spots) — a different job. If the session was
reasoning-heavy or surfaced contested beliefs worth examining, offer:

> "Want me to `/reflect` on how this session's reasoning fits the broader thought graph?"

Otherwise, summarize what was captured (the guide, the charges, the findings, the
closed ticket) and stop.

<constraint id="retro-discipline" severity="hard">

  <anti-patterns>
    <pattern>Capturing before the work is verified for real — the precondition gate exists to stop exactly this. Green tests are not verification.</pattern>
    <pattern>Writing a vague guide. "It works now" is not a reproduction step. Every section must be concrete enough for someone with zero context to follow.</pattern>
    <pattern>Inventing a new node type for the guide — the `document` node is the type. Do not build a parallel "runbook"/"guide" primitive.</pattern>
    <pattern>Skipping the charge step — an unvalidated hypothesis and a hypothesis confirmed by a live smoke test are different graph states; record the difference.</pattern>
    <pattern>Forgetting to close the ticket when one exists — the work is verified and captured; leave the tracker reflecting that.</pattern>
    <pattern>Closing a ticket that does not exist, or naming a specific tracker product — the close is conditional and generic.</pattern>
    <pattern>Conflating retro with /reflect — retro writes new artifacts; reflect introspects existing ones.</pattern>
  </anti-patterns>

</constraint>
