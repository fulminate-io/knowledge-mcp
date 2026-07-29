---
name: propose
description: Propose net-new projects — new features or gap-fills in existing features — by mining past tickets and thoughts, walking the target repo for feature seams that stop short, and running web research including market/competitor analysis. Mostly non-interactive; presents 3-5 evidence-backed proposals and offers /brainstorm to dig into any of them. Distinct from /improve (optimizations and testing fixes) and /research (documents what exists).
argument-hint: <optional repo or subsystem focus; omit to use the session's focused repo or the current working directory>
---

# Propose: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline (background spawning, user touch points),
reference /orchestrate. This skill is proposal-discovery-specific.
</precedence>

<mental-model>
/research = WHAT exists. /explore = WHY it exists. /improve = what's WRONG
(optimizations, testing fixes). /propose = what's NEXT (net-new capability).

The discriminator for every candidate: **"does this create a capability that
doesn't exist?"** Yes → it belongs here. Makes an existing capability faster,
safer, or better-tested → it belongs to /improve; exclude it (note it in one
line so the signal isn't lost).

Output feeds /brainstorm. Proposals are INPUTS to a future brainstorm, never
work items: this skill creates NO tickets and NO projects.
</mental-model>

## Step 0: Resolve Scope + Index Freshness

**Scope resolution (in order):**
1. `$ARGUMENTS` names a repo or subsystem → that's the focus.
2. The session has already been working in a specific repo → that repo.
3. Otherwise → the repo of the current working directory.

**Freshness:** run `manage({ "operation": "status" })`. If the focus repo's
index is behind HEAD, run `collect({ "type": "code", "id": "<absolute-repo-path>" })`
before spawning lenses — this skill is mostly non-interactive, so reindex
without asking. Stale evidence produces stale proposals.

## Step 0.5: Build the Dedup Set (inline, before spawning)

Proposals that collide with existing work are noise. Assemble three lists:

```json
thoughts({ "operation": "recall", "mode": "context", "query": "<focus repo/product area>" })
query({ "type": "ticket", "limit": 50 })
query({ "type": "project", "limit": 30 })
thoughts({ "operation": "recall", "session": "propose-rejected" })
```

- **Already planned** — open tickets/projects covering a candidate → drop it.
- **Already rejected** — the `propose-rejected` session lists proposals the
  user dismissed in past runs → don't re-propose without new evidence.
- **Already explored** — prior decisions/findings that settled an area →
  candidates there need to engage that history, not ignore it.

## Step 1: Spawn the Three Lens Researchers (background, parallel)

Spawn all three in ONE message so they run concurrently, each
`run_in_background: true`. Each is a `researcher` with a lens-specific,
DISCOVERY-framed brief. Every brief carries the discriminator ("net-new
capability only — exclude optimizations and test fixes") and the fixed return
shape:

```
Per candidate: name (retrieval-optimized), the gap or opportunity (2-3 sentences),
evidence (node IDs, file:line refs, URLs — every claim cited), rough size
(S/M/L), why-now (what makes this timely). Max 8 candidates per lens.
```

**Lens 1 — Friction (knowledge graph):**

> Mine past tickets, thoughts, and findings related to <focus> for
> MISSING-CAPABILITY signals: "couldn't do X", workarounds users built,
> declined or deferred scope that keeps resurfacing, feature requests that
> never became tickets. Classify each signal: capability-missing (keep) vs
> capability-broken-or-slow (exclude — that's optimization territory). Use
> recall + query over tickets/decisions/findings; cite node IDs.

**Lens 2 — Feature Gaps (code + docs):**

> Walk <focus repo> for places existing features visibly stop short. Three
> sweeps: (1) documented-but-absent — claims in READMEs/docs/help output with
> no implementation behind them (verify absence with ≥2 search phrasings + an
> ast shape-match; a single miss is not proof); (2) enumerable seams — tables
> of providers/collectors/integrations/formats where obvious entries are
> missing; (3) adjacencies — "the product does X and Y but stops before the Z
> a user would expect next." Cite file:line for every claim.

**Lens 3 — Market (web):**

> First characterize what the product IS from its own README/docs. Then
> research the market and ecosystem around it: comparable products and their
> feature sets vs this one, where the ecosystem is moving, integration
> surfaces users increasingly expect, positioning gaps. Market research is a
> first-class goal, not garnish — competitor capability tables beat vague
> trend prose. Every claim carries a URL. Frame as discovery: what exists out
> there, not confirmation of a pre-formed idea.

## Step 2: Synthesize (main loop — not an agent)

When the lenses return:

1. **Merge + dedup** across lenses and against the Step 0.5 dedup set.
2. **Apply the discriminator** — anything optimization/testing-shaped is cut
   (keep a one-line "belongs to /improve" list).
3. **Verify load-bearing claims.** A researcher's "the product lacks X" is a
   signpost, not an answer. Before a proposal is presented, verify its central
   existence/absence claim yourself against current source (search + ast +
   opening the file). A proposal built on a fiction poisons the downstream
   brainstorm.
4. **Score and cut to 3-5** by evidence density × strategic value. Log how
   many candidates were filtered and why (dedup / discriminator / weak
   evidence) — silent truncation reads as "covered everything."

## Step 3: Record Proposals in the Graph

One `research` node per surviving proposal — proposals are open
investigations, which is exactly what research nodes model:

```json
mutate({
  "operation": "create", "type": "research",
  "name": "<retrieval-optimized proposal name>",
  "description": "<the gap/opportunity, leading with the capability concern>",
  "summary": "<one-line search-optimized summary>",
  "content": "<evidence: node IDs, file:line, URLs, rough size, why-now>",
  "status": "proposed",
  "session": "propose-<focus-slug>",
  "links": ["<evidence node IDs>"]
})
```

Name for retrieval: the node name and first description sentence are what
/brainstorm's context-pack recall will match later.

## Step 4: Present the Options

```
## Proposals: <focus>

### 1. <Proposal name>  (size: M)
**Gap:** <what capability is missing and who hits the wall>
**Evidence:** <node IDs, file:line, URLs>
**Why now:** <timing signal>

### 2. ...

---
Filtered: <N> candidates dropped (<n> already ticketed, <n> belong to /improve, <n> weak evidence).

To dig into any of these: `/brainstorm <proposal name>` — the proposal node and
its evidence will surface in the brainstorm's recall automatically.
```

End there. Do NOT create tickets or projects, do NOT start a brainstorm
unprompted, do NOT ask a follow-up question chain — the output IS the
deliverable.

## Step 5: Record Rejections

If the user dismisses proposals (explicitly, or by brainstorming one and
ignoring others across sessions), append to the dedup session:

```json
thoughts({
  "operation": "think",
  "content": "Rejected this run: <list + brief why>",
  "summary": "propose run <focus>: rejected candidates <names>",
  "session": "propose-rejected"
})
```

<constraint id="propose-discipline" severity="hard">

  <anti-patterns>
    <pattern>Creating tickets or projects from proposals — tickets come out of /brainstorm, never out of /propose; an unvetted proposal ticketed is an aspirational ticket</pattern>
    <pattern>Proposing optimizations, refactors, or test fixes — that's /improve; apply the discriminator</pattern>
    <pattern>Proposing what's already ticketed, planned, or previously rejected — build the dedup set first</pattern>
    <pattern>Asserting "the product lacks X" from a single search miss — absence claims need ≥2 phrasings + ast shape-match, verified in the main loop, not just by the lens agent</pattern>
    <pattern>Market claims without URLs — uncited trend prose is speculation, not research</pattern>
    <pattern>Interactive candidate-picking mid-run — this skill runs to output; the user's choice point is the final options list</pattern>
    <pattern>Presenting more than 5 proposals — volume dilutes; score and cut</pattern>
    <pattern>Skipping the graph artifacts — proposals that live only in the transcript are invisible to the /brainstorm handoff</pattern>
  </anti-patterns>

</constraint>
