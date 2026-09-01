---
name: walk-an-architectural-surface
description: Action rulebook for architectural surface walks — enumerate every surface a stated principle touches, confirm-first vs owned decision tiers, and behavior-preserving surface replacement discipline. Read when a principle, contract, or invariant enters a design conversation. Not user-invocable.
user-invocable: false
---

# WALK-AN-ARCHITECTURAL-SURFACE — principles expand scope to every surface they govern

<!-- version: 1 -->
<!-- Read at: a user-stated principle/contract/invariant entering a brainstorm;
     a researcher brief carrying a principle; a replacement/re-route design. -->

## The surface walk

When the user states a guiding principle ("server has no filesystem access,"
"client owns session state," "no back-compat shims"), enumerate EVERY code
surface the principle touches BEFORE proposing a ticket — every consumer,
dependency, violation, collateral impact. Scoping narrowly to the literal
request is the wrong instinct: a stated principle expands scope to every surface
it governs; a missed surface costs a full correction cycle downstream.

Classify each surface: VIOLATES → ticket In Scope with concrete fix language ·
HONORS already → no action (document only if non-obvious) · ADJACENT but
separate → named in Out of Scope with rationale · UNRELATED → drop. When the
surface is wide, brief a researcher for a FULL inventory — "return every site
with file:line, classified" — enumeration, not summary.

## Confirm-first vs owned

Owning architecture means owning the RESEARCH and SURFACING — not deciding the
foundation unilaterally. Two tiers:
- CONFIRM-FIRST (user sign-off BEFORE it enters any artifact): trust/security
  boundaries, transport security, auth/authz model, deployment mechanism,
  data-isolation model, runtime/platform assumptions, any choice with broad
  blast radius or depending on context the user holds.
- OWNED (decide without asking): specifics — paths, names, ordering, which
  existing function/pattern to reuse — and any surface fully resolvable AND
  verifiable from research.

Before proposing ANY architecture, research what the platform already provides.
Never assume greenfield. The rule-twisting tells: reading "owns architecture"
as license for a solo foundational call; relabeling a foundational decision a
"specific"; resolving decide-vs-confirm ambiguity in your own favor; calling a
proposal "locked" before the user signed off. Ambiguous fit → CONFIRM.

At every recording boundary: ATTRIBUTE EVERY CLAUSE to the user's words that
authorized it — an unattributable clause leaves the record and surfaces as an
OPEN PARAMETER. When the user rejects a framing mid-session, every conclusion
derived under it is VOID and is re-confirmed under the agreed framing before
entering any decision, ticket, or brief.

## Behavior-preserving surface replacement

When a design REPLACES or RE-ROUTES an existing surface (API, wire protocol,
tool layer, serialization, dispatch path) while preserving observable behavior:
- Locate the equivalence bar at the right boundary: "preserve behavior" means
  what the EXTERNAL consumer observes — not byte-identity of internal
  exchanges. Normalization an old internal component did (casing, defaults,
  dedup) is CENTRALIZED in the new engine, applied once for all callers.
- FRONT-LOAD AN EXHAUSTIVE INVENTORY: every entry point, mode/flag/parameter,
  handler-side post-step, default — so the new/old boundary is COMPLETE at
  scoping time.
- DEFAULT-DENY CLASSIFICATION: the new path handles only an explicit allowlist
  of shapes proven equivalent; anything unrecognized falls through to the OLD
  path unchanged. A denylist turns every enumeration gap into a SILENT
  wrong-output regression; the allowlist turns it into a correctness-safe no-op.
- DEFAULT-DENY IS A NET, NOT A LICENSE TO UNDER-MIGRATE: the cutover deleting
  the old surface is the forcing function — once old handlers are gone,
  anything still falling through fails loudly. Sequence: default-deny while
  both coexist → cutover that removes the fallback. When a shape diverges only
  from unmirrored normalization, MIRROR the transform so the shape stays
  migrated.
