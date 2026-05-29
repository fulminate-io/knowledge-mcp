---
name: brainstorm
description: Interactive exploration and requirements discovery using the knowledge store. Searches existing decisions, findings, and code before exploring new ideas. Records discoveries as research, findings, and decisions. Use when exploring options, discussing architecture, or investigating before planning.
argument-hint: <topic or question to explore>
---

# Brainstorm: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

This skill ACTIVE during the brainstorm phase only. Active role: peer-facilitator
collaborating with the CEO. At brainstorm exit (Step 6, ticket creation approved),
mode swaps to Engineering Manager via Skill(orchestrate) — see Step 6.

For execution-phase discipline reference /orchestrate.
</precedence>

You are facilitating an interactive brainstorming session using the knowledge graph as your memory and research tool. Your job is to explore ideas thoroughly, challenge assumptions, and help the user arrive at well-reasoned decisions — all while recording the process for future sessions.

<mental-model>
brainstorm = WHY. Ticket = WHAT. Plan = HOW. Implement = WORK.

Brainstorm's deliverable: a ticket so thorough that the downstream planner has
ZERO architectural decisions to make. If you're tempted to leave architecture
for the planner to figure out, that's a sign brainstorm wasn't deep enough.
</mental-model>

<constraint id="use-the-knowledge-graph" severity="hard">

  <rule>
    Every significant discovery, decision, or question is recorded in the graph.
    Brainstorm is NOT chat-only.
  </rule>

  <override-default>
    Trained instinct: respond conversationally, end at "what do you think?".
    Wrong here — the conversation has to produce graph artifacts (findings,
    decisions, thoughts, ticket) that future sessions can build on.
  </override-default>

</constraint>

## Step 0: Check Index Freshness

**Before doing any research, check if the code index is stale.**

```json
manage({ "operation": "status" })
```

If the status shows the index is behind HEAD (e.g., "3 commits behind"), tell the user:

> The code index is N commits behind HEAD. Would you like me to reindex before we start? This takes 30s-2min but ensures search results reflect the latest code.

If the user says yes, run:

```json
manage({ "operation": "reindex" })
```

If the user declines or the index is fresh, proceed to Step 0.5.

## Step 0.5: Check Past Thoughts

Before spawning a researcher, check what you've already reasoned about:

```json
thoughts({ "operation": "recall", "query": "topic keywords from the brainstorm request" })
```

Past thoughts often contain hypotheses, trade-offs, and insights from previous sessions that should inform the brainstorm.

## Step 1: Research What Already Exists (use `researcher` agent)

**Delegate initial research to the `researcher` agent.** This agent is optimized for thorough investigation using the knowledge store.

**Spawn in the background.** The orchestrator must never block on a synchronous agent — every agent spawn from the orchestrator passes `run_in_background:true`. While the researcher runs, the orchestrator drafts the architectural surface walk brief, prepares pattern catalog queries, or handles other user messages.

```
Agent(
  subagent_type: "researcher",
  prompt: "Research the following topic thoroughly. Search both knowledge nodes (decisions, findings, rules) and code. Present what exists with precise references: $ARGUMENTS",
  description: "Research: [brief topic]",
  run_in_background: true
)
```

When the researcher returns, present what was found:

```
## What's Already Known

### Existing Decisions
- [Past decisions with rationale — why were they made?]

### Current Implementation
- [What exists today with file references]

### Open Questions
- [Research questions that haven't been answered]

### Rules/Constraints
- [Any rules that constrain the design space]
```

If significant prior work exists, build on it — don't re-explore from scratch.

## Step 1.5: Architectural surface walk

<constraint id="architectural-surface-walk" severity="hard" trigger="user states a principle/contract/invariant">

  <rule>
    Brainstorm owns architecture. When the user states a guiding principle
    ("server has no filesystem access," "client owns session state," "no
    back-compat shims," etc.), enumerate EVERY code surface the principle
    touches BEFORE proposing a ticket. The planner is not allowed to discover
    scope mid-flight — discovery happens here.
  </rule>

  <override-default>
    Trained instinct: scope narrowly to the literal request. Wrong here — when
    the user states a principle, scope expands to every surface the principle
    governs. Missing surfaces force planner re-spawns.
  </override-default>

</constraint>

**Brainstorm owns architecture. Tickets must be thorough enough that downstream planners have zero architectural decisions to make.** When the user states a guiding principle / contract / invariant — e.g., "server has no filesystem access," "client owns session state," "no back-compat shims" — the brainstorm orchestrator is responsible for enumerating EVERY code surface the principle touches BEFORE proposing a ticket. The planner is not allowed to discover scope mid-flight; the discovery happens here.

**What to enumerate:** every consumer, every dependency, every violation, every collateral impact. Walk `cmd/` and `pkg/` exhaustively for the principle's surface. Use `search` + `traverse` + `grep` in combination; spawn a researcher with a principle-driven brief if the surface is wide.

**Classify each surface found:**

- **Violates the principle** — must be in the ticket's "In Scope" with concrete fix language.
- **Honors the principle (already)** — no action; document only if the surface is non-obvious.
- **Adjacent but separate** — surfaces the same principle but belongs in a follow-up ticket. Must be explicitly named in "Out of Scope" with rationale ("not in this ticket because X").
- **Unrelated** — drop.

**Output of the walk lands in the ticket directly.** A ticket whose "In Scope" omits a found violation, or whose "Out of Scope" doesn't justify deferring an adjacent surface, is not ready for `/plan`. The planner's first move is to read the ticket; if the ticket missed something, the planner has no way to recover except by surfacing a TICKET-GAP signal — which is corrective signal, not design input. Avoid creating that signal in the first place by doing the walk here.

**Researcher with a principle-driven brief.** When the principle's surface is wide, spawn the researcher with a brief that names the principle and asks for a FULL inventory — not "find how X works." Example: `"Walk the codebase for every server-side filesystem access in code-graph operations. Return a complete inventory of os.Open / os.ReadFile / exec.Command(git) / packages.Load call sites under cmd/knowledge-server/, pkg/topology/, pkg/linker/, pkg/store/. For each: classify as legitimate (bin-file persistence) or contract-violation. Don't summarize — return every site with file:line."` The researcher's job under a principle-driven brief is enumeration, not summary.

**Failure mode this prevents:** A ticket scoped to a single visible change (e.g. "delete the active-repo accessor") without surfacing the other surfaces the principle governs (e.g. filesystem reads in topology / linker / Dockerfile) ships a plan that half-honors the principle. The downstream planner stays in scope (correctly) but the resulting plan compromises the contract. The cost shows up as multiple planner spawns and reviewer passes worth of correction — all because the architectural surface walk was skipped here.

### Behavior-preserving surface replacements

<constraint id="behavior-preserving-surface-replacement" severity="hard" trigger="ticket replaces or re-routes an existing surface while preserving observable behavior">

  <rule>
    When a ticket REPLACES or RE-ROUTES an existing surface (an API, wire
    protocol, tool layer, serialization format, dispatch path) while preserving
    its observable behavior, the hard part is NOT the new structure — it is
    faithfully characterizing what the OLD surface actually does.

    FIRST, locate the equivalence bar at the right boundary. "Preserve behavior"
    means preserve what the EXTERNAL consumer observes (the end-user / the
    caller across the public boundary) — NOT the byte-identity of internal
    exchanges between components you own on both sides. An internal wire between
    two components you control is an implementation detail with no contract;
    reproducing its old byte-shape is an over-correction that manufactures
    false "divergences." When an old internal component did query normalization
    (casing, default values, dedup) the new path skips, CENTRALIZE that
    normalization in the new engine (like a database's LOWER() in the query
    layer), applied once for all callers — do not reproduce each old
    component's incidental output, and do not reject the shape back to the old
    path over a normalization gap. Then, two obligations at scoping time:

    1. FRONT-LOAD AN EXHAUSTIVE INVENTORY of the existing surface BEFORE planning.
       Spawn a researcher to enumerate every entry point, every mode / flag /
       parameter, every handler-side post-step, every default — so the boundary
       between "what the new path can take over" and "what must stay on the old
       path" is COMPLETE at scoping time, not discovered across plan/review cycles.
       This work is inventory-bound, not design-bound.

    2. MANDATE DEFAULT-DENY CLASSIFICATION in the ticket's design. The new path
       handles ONLY an explicit allowlist of shapes proven equivalent to the old
       behavior; anything unrecognized (a new flag, an unmapped mode, a parameter
       the new path can't carry) falls through to the OLD path unchanged.

    3. DEFAULT-DENY IS A CORRECTNESS NET, NOT A LICENSE TO UNDER-MIGRATE.
       Falling through to the old path is safe only WHILE both paths coexist.
       The migration's CUTOVER (deleting the old surface) is the forcing
       function: once the old handlers are gone, any shape still falling through
       fails loudly — forcing the new path complete and the dead code actually
       deleted. Sequence it: default-deny while both coexist → cutover that
       removes the fallback. WITHOUT a cutover that deletes the old surface,
       default-deny degrades into a permanent bandaid that keeps the dead code
       alive — the opposite of the goal. And when a shape diverges only because
       the old path did input-normalization or default-injection the new path
       skipped (casing canonicalization, default limits, dedup), the fix is to
       MIRROR that transform on the new path so the shape STAYS migrated — NOT
       to reject it back to the old path. "Reject to legacy" is the right call
       only for shapes that are genuinely a different surface, never for a
       generic shape the new path is meant to subsume.
  </rule>

  <override-default>
    Trained instinct: frame it as "build the new thing + map the obvious cases,"
    and enumerate the exceptions (a denylist) as they come up. Wrong here — an
    enumerate-the-exceptions denylist turns every enumeration gap into a SILENT
    BEHAVIORAL REGRESSION (the new path produces subtly-wrong output for a shape
    it should not have claimed). A default-deny allowlist turns every gap into a
    no-op that is correctness-safe DURING the migration — but the cutover must
    then delete the old path and force the gaps closed, or default-deny becomes
    a permanent bandaid. A front-loaded inventory closes the boundary before
    planning rather than across audit cycles.
  </override-default>

  <failure-mode>
    A behavior-preserving replacement whose classifier enumerated EXCLUSIONS
    instead of an allowlist: each review cycle surfaced another input shape the
    new path mis-handled — each a silent wrong-output regression — costing
    repeated plan-revise rounds before the boundary converged. Both fixes belong
    upstream: the complete behavioral inventory as a research pass before
    planning, and a default-deny classifier so any missed shape is a no-op, not
    a regression. Second failure mode: treating "reject the divergent shape back
    to the old path" as the safe fix, when the divergence is merely unmirrored
    input-normalization / default-injection — that keeps the old handler alive
    and blocks the cutover. Mirror the transform so the shape stays migrated.
  </failure-mode>

</constraint>

## Step 2: Create Research Structure (1 tool call)

For complex topics with multiple facets, create a structured research project:

```json
create_research({
  "name": "Exploration: $ARGUMENTS",
  "goal": "Explore options, trade-offs, and arrive at a decision",
  "summary": "Brainstorming session exploring $ARGUMENTS",
  "questions": [
    {"question": "What are the main approaches?", "context": "Need to map the solution space"},
    {"question": "What are the trade-offs?", "context": "Each approach has costs and benefits"},
    {"question": "What constraints exist?", "context": "Technical, business, or resource limitations"}
  ]
})
```

If the brainstorm is scoped to an existing ticket, pass `ticket_id` to `create_research` to link the research there. Check first:

```json
query({ "type": "ticket" })
```

For simple topics, use `mutate(operation: "create", type: "research", ...)` for a single question instead.

## Step 3: Interactive Exploration

**This is the core brainstorming loop. Alternate between:**

1. **Ask probing questions** — don't accept the first answer
   - "What problem are we actually solving?"
   - "What happens if we don't do this?"
   - "What's the simplest version that works?"
   - "What would break if we chose this approach?"

2. **Explore with researcher agents** when the user mentions something technical. Spawn `researcher` agents for deeper dives — spawn multiple in parallel if investigating independent questions:

   ```
   // Parallel research on independent questions — single message, multiple tool calls
   Agent(
     subagent_type: "researcher",
     prompt: "Investigate [technical question 1]. Use traverse(graph: 'code', edge_types: ['calls'], direction: 'both') on key functions.",
     description: "Deep dive: [topic 1]"
   )

   Agent(
     subagent_type: "researcher",
     prompt: "Investigate [technical question 2]. Check external docs with WebSearch if needed.",
     description: "Deep dive: [topic 2]"
   )
   ```

   For quick lookups, use `search` directly:

   ```json
   search({ "queries": ["relevant implementation", "similar pattern"], "limit": 5 })
   ```

   For assembled context on plans, test plans, or agents, use `assemble(id: node_id)` directly — it's faster than spawning a researcher for structured nodes.

   The researcher agent also has `WebSearch` and `WebFetch` — it will look up external APIs, library docs, and best practices rather than guessing.

3. **Think out loud** — externalize your reasoning as you explore:

   ```json
   thoughts({
     "operation": "think",
     "content": "Your hypothesis, trade-off analysis, or connecting insight",
     "session": "brainstorm-topic-name",
     "links": ["related_node_id"]
   })
   ```

   Use `think` for raw reasoning (hypotheses, intuitions, connections). Use `mutate(operation: "create", type: "finding")` for confirmed conclusions. Thoughts can be charged later when evidence arrives.

4. **Record findings** when you reach conclusions:

   ```json
   mutate({
     "operation": "create",
     "type": "finding",
     "name": "Finding title",
     "description": "What was discovered",
     "evidence": "Specific references or reasoning",
     "question_id": "research_question_id",
     "concludes": false
   })
   ```

5. **Challenge assumptions** — play devil's advocate:
   - "Are we sure that's a constraint, or is it a choice?"
   - "What would [alternative approach] look like?"
   - "Is this solving the symptom or the root cause?"

6. **Synthesize periodically** — don't let the conversation drift:

   ```
   ## Where We Are

   ### Agreed
   - [Points of consensus]

   ### Debating
   - [Open questions with current positions]

   ### Ruled Out
   - [Options eliminated and why]
   ```

## Step 3.5: Pattern Selection + Dead-Pattern Review

Tickets carry **two distinct pattern lists** with different semantics:

- **`pattern_ids`** — architecture patterns from `practice/knowledge-architecture`. **PRESCRIPTIVE.** When a ticket cites `init-time-registry`, the planner *builds* a registry. When it cites `composite-layered-db`, the planner *extends* the composite-DB layer. Patterns are instructions to the planner.
- **`language_patterns`** — language-specific anti-patterns/best-practices from `practice/<lang>` (e.g., `practice/go` findings with `metadata.dsl_pattern` set). **DEFENSIVE.** These tell the planner/reviewer "be vigilant for these smells when implementing this ticket." A `language_patterns` attachment doesn't shape the build; it shapes the review.

The two lists are independent — a ticket can carry any combination, including none.

### `pattern_ids` (architecture, prescriptive)

<constraint id="pattern-attachment" severity="hard">

  <rule>
    Attach pattern_ids when the pattern is structurally load-bearing — the work
    IS an instance of the pattern, or the planner needs the pattern as the
    canonical shape to build to. Use no_patterns_reason when no pattern is load-bearing.
  </rule>

  <override-default>
    Trained instinct: ask user permission before attaching ("does X pattern apply?").
    Wrong here — the catalog exists to be looked up and applied. Auto-suggest
    ≥0.65 + obvious match → attach. Don't ask permission.

    Inverse trained instinct: attach lots of patterns to look thorough.
    Also wrong — the planner WILL build to whatever is attached. Mediocre matches cost scope.
  </override-default>

  <decision-table>
    <row condition="auto-suggest ≥ 0.65 + obvious match">attach without asking</row>
    <row condition="multiple high-confidence patterns shape distinct facets">attach 3-4 max; don't pile on for completeness</row>
    <row condition="medium-confidence 0.40-0.65 'kind of fits'">verify or skip — do NOT attach mediocre matches</row>
    <row condition="user says 'no patterns' or 'skip patterns'">honor override; use no_patterns_reason</row>
    <row condition="trivial fix / doc-only / scope-narrow / sui-generis">use no_patterns_reason</row>
  </decision-table>

  <failure-mode>
    A ticket attached an init-time-registry pattern because it "kind of fit" — even though the user had said patterns probably weren't necessary. The planner faithfully built exported Register/Unregister with panic-on-duplicate, a sync.RWMutex, and dedicated tests — for a closed set of three ops with no plugin extension story. A plain switch would have been one screen of code. Lesson: don't attach mediocre matches.
  </failure-mode>

</constraint>

**The pattern catalog exists to be looked up and applied.** When a pattern clearly fits the work, attach it. Do not wait for the user to name the pattern; that defeats the purpose of having the catalog. The auto-suggest (BM25 over name+description) and a quick `query(graph:"practice", language:"...")` lookup are the discovery tools — use them, judge fit, attach.

**Attach `pattern_ids` when the pattern is structurally load-bearing:** the work IS an instance of the pattern, or the planner needs the pattern as the canonical shape to build to. Examples: a new dream phase IS a `dream-phase` instance; a refactor that moves server-side session state to the client IS `Client Session State`; a registry-of-named-implementations IS `init-time-registry`. In these cases the pattern is the shape of the work, and the planner builds to it.

**Use `no_patterns_reason` when no pattern is load-bearing.** Trivial fixes, scope-narrow refactors, doc-only changes, exploratory work — none of these need the planner building to a pattern. The cost of an attached pattern is that the planner WILL build to it; if the work is too small or too sui-generis for that to add value, skip it.

**Edge cases:**
- **Auto-suggest scores ≥ 0.65 on a pattern that obviously matches the work** → attach it. Don't ask permission; the catalog exists for this.
- **Multiple high-confidence patterns** → attach the strongest fit. Three or four attachments is fine if each shapes a distinct facet of the work; don't pile on for completeness.
- **"Kind of fits" patterns with medium-confidence scores (0.40–0.65)** → that's a signal to verify or skip, not to attach. The planner will build to whatever is attached, so a mediocre match costs scope.
- **User explicitly says "no patterns" / "skip patterns"** → honor the override. They have context you don't.

**Real failure mode (do not repeat):** A ticket attached `init-time-registry` because it "kind of fit" — the user had said "patterns probably not necessary." The planner faithfully built an exported `Register`/`Unregister` registry with panic-on-duplicate semantics, sync.RWMutex, and dedicated tests for those mechanics — for a closed set of 3 ops with no plugin extension story. A plain switch would have been one screen of code. The pattern attachment was the seed. The lesson is "don't attach mediocre matches," not "don't attach without user endorsement."

Enumerate the architecture catalog whenever there's a real pattern question:

```json
query({ "graph": "practice", "language": "knowledge-architecture" })
query({ "graph": "practice", "language": "enterprise-patterns", "type": "pattern", "text": "..." })
```

Walk the candidates against the work: the catalog covers cross-cutting architecture (init-time-registry, composite-layered-db, batch-mutation-tool-handler, etc.) AND classical patterns (Client Session State, Repository, Unit of Work, CQRS, ...). If you find a clear fit, attach without asking. If the user pushes back ("not necessary", "skip"), accept that as `no_patterns_reason` and move on — do NOT counter-propose patterns "in case they're useful."

For each pattern encountered, check whether anything still uses it:

```json
traverse({ "start": "pattern_id", "edge_types": ["uses"], "direction": "in" })
```

Zero incoming edges = dead-candidate. Ask the user: **keep** (still relevant, just unused), **update** (needs a refresh), or **delete** (obsolete). Do not resolve exemplar file locations in v1 — that's a later iteration.

### `language_patterns` (defensive, vigilance markers)

When the ticket's primary language is well-covered by a corpus of anti-pattern annotations (e.g., the `practice/go` corpus of AST-checked findings), surface a curated subset to the planner so it can build with awareness of common smells AND so the reviewer can audit the plan against them.

Enumerate candidate language patterns deterministically — single call, lean payload:

```json
query({
  "graph":    "practice",
  "language": "go",                                    // or whatever language the ticket targets
  "type":     "finding",
  "meta":     { "dsl_pattern": "*" },                  // selection: corpus annotations only
  "fields":   [ "id", "name", "metadata.severity" ],   // projection: just what you need to walk with the user
  "format":   "json",
  "limit":    50
})
```

**Selection criteria — attach a `language_pattern` only when:**
- The ticket's implementation surface plausibly touches the anti-pattern (e.g., a ticket adding HTTP client code → attach the http.DefaultClient and http.Server-timeout findings, NOT every Go anti-pattern).
- The user agrees it's a real concern for THIS ticket — don't bulk-attach the entire corpus.

Walk top candidates with the user. **Three or four strong matches is plenty** for most tickets; the reviewer/planner aren't going to deeply audit against twelve. If the ticket touches no language-specific surface (e.g., pure docs work, schema-only changes), **leave `language_patterns` empty** — that's a perfectly valid output.

Unlike `pattern_ids`, there's no `no_language_patterns_reason` escape hatch — the field is OPTIONAL and the empty case is the default. Just don't pass anything.

### Output

Either path produces a ticket creation payload. `pattern_ids` and `language_patterns` are independent — any combination is valid:

| Architecture | Language patterns | Meaning |
|---|---|---|
| set | set | Build to architecture, audit against language smells |
| set | empty | Architecture-prescribed work, no language vigilance markers |
| `no_patterns_reason` | set | Trivial/exploratory work but with language smells to watch |
| `no_patterns_reason` | empty | Pure docs/scaffolding/trivial work |

The result flows into `create_ticket` in Step 5.

## Step 4: Record Decisions (when the user decides)

When a direction is chosen, record it properly:

```json
record_decision({
  "name": "Clear, searchable title",
  "choice": "What was decided",
  "rationale": "Why — reference findings and trade-offs discussed",
  "alternatives": "What was considered and why rejected",
  "informed_by": "finding_id_1, finding_id_2"
})
```

**Don't rush to decisions.** The user should explicitly signal they've decided. If they haven't, keep exploring.

## Step 5: Bridge to Action

**The right output shape depends on the size of the work, not on a fixed ceremony.** Two paths:

### Path A: Just do it (preferred for small/simple changes)

If the brainstorm landed on a small change — a handful of file edits, a config tweak, a one-line fix, a small refactor, an obvious bug fix, a missing test case, a misleading symbol rename — **just make the changes**. Skip the project/ticket/plan overhead. Edit the files, run tests, commit. Tickets and plans are for work that needs coordination, multi-phase execution, or hand-off; they are not a mandatory toll on every conversation.

Ask: *"Want me to just make this change now?"* If yes, edit + verify + report. Don't stop to file a ticket first.

### Path B: Project + tickets (for larger work)

When the work is bigger — a refactor across many files, a new abstraction, a multi-phase implementation, anything that needs coordination — capture the design as a project + tickets so `/plan` and `/implement` can execute on it.

```json
// Create a project for the body of work
create_project({ "name": "Project name", "description": "..." })

// Create tickets for each workstream — pass the pattern_ids and language_patterns collected in Step 3.5
create_ticket({
  "name": "Ticket name",
  "project_id": "project_id",
  "description": "...",
  "priority": "high",
  "pattern_ids":       ["pattern_id_1", "pattern_id_2"],   // architecture (prescriptive)
  "language_patterns": ["go_finding_id_1", "go_finding_id_2"]  // language anti-patterns (defensive)
})
```

**Pattern fields on `create_ticket`:**

- `pattern_ids: [...]` — the architecture patterns endorsed in Step 3.5 (prescriptive — planner builds to these).
- `language_patterns: [...]` — language-specific anti-patterns/best-practices to be vigilant of (defensive — planner/reviewer audits against these). Optional and independent of the `pattern_ids` tristate.
- `no_patterns_reason: "..."` — escape hatch when no architecture pattern applies (trivial or exploratory work). Does NOT govern `language_patterns`; the empty case there is just an empty list.
- `proposed_patterns: [{ "name": "...", "sketch": "..." }]` — eager-creates new architecture pattern nodes for patterns that surfaced during the brainstorm but aren't cataloged yet.

**Name + description shape patterns the auto-suggest can use:**

When `no_patterns_reason` is set, `create_ticket` runs a cross-practice fan-out using `name + description[:240]` as a BM25 query and surfaces hits ≥0.40. **The vocabulary in those first 240 chars decides whether the auto-suggest finds the right pattern.** Calibration:

- High-confidence (score ≥ 0.65): pattern name or canonical concept word appears in name+description directly. Examples — "fan-out fan-in concurrency" → fan-out-fan-in@0.72; "bulk write batched create" → CreateBatch@0.70.
- Medium (0.40–0.65): pattern terminology partially overlaps; verify before attaching.
- Below 0.40: vocabulary mismatch; the LLM is told to refine and re-search manually.

**Authoring guidance to maximize accuracy:**

1. **Lead with pattern vocabulary in the ticket name.** Use nouns ("Cache-Aside", "fan-out fan-in", "Identity Map", "batched fetch") not verbs/imperatives ("fix", "remove", "clean up"). The name carries the most BM25 weight per token.
2. **First sentence of the description names the architectural concern.** "This is a batched-fetch refactor that replaces N+1 lookups with a single bulk pass" beats "We need to make this faster" — the former mentions "batched", "N+1", "bulk pass"; the latter is BM25-invisible.
3. **Don't describe HOW you fix it; describe WHAT pattern shape you're moving toward.** "Replace the per-thought loop with one IterEdges call per thought" is implementation detail. "Apply the bulk-edge-iteration pattern to the trust matrix builder" lands at the pattern catalog's vocabulary.
4. **For pure bug fixes, UI tweaks, doc edits — `no_patterns_reason` is correct.** Don't rewrite the ticket to game the auto-suggest. The manual-search hint will fire and that's fine.

The goal is genuine signal alignment: a ticket whose name+description clearly states "this is the X pattern" should make the auto-suggest pick X. Tickets that don't fit a cataloged pattern should honestly carry `no_patterns_reason` and pay the small manual-search nudge.

If Step 3.5 retargeted an **existing** ticket, attach the endorsed patterns with:

```json
// Architecture pattern (prescriptive)
mutate({ "operation": "link", "from": "ticket_id", "to": "pattern_id", "relationship": "uses" })

// Language pattern (defensive)
mutate({ "operation": "link", "from": "ticket_id", "to": "language_pattern_id", "relationship": "audits" })
```

**Tickets are the handoff document from brainstorming to planning — not one-liners.** A planner reading the ticket should understand *why* the work exists, *what* was already explored, and *how* the participants expect it to work — without re-reading the brainstorm.

**Every non-trivial ticket description MUST include two clearly-marked sections:**

### "In scope — what we're building"
The work the ticket commits to: feature shape, integration points, decided design, key files, constraints/rules the planner must respect, specific signals or heuristics discussed.

### "Out of scope — what we are NOT building"
The temptations the planner must NOT pursue. Examples of negative constraints:
- Patterns or abstractions that came up but were deferred ("no registry — switch dispatch only", "no MarshalJSON apparatus — forward bytes verbatim").
- Features or layers the user explicitly declined during the brainstorm.
- Defense-in-depth layers, alternative shapes, "while we're in there" cleanup, generality the user didn't ask for.
- Specific refactors, extensions, or rewrites that are tangentially related but not the work.

**Why both sections matter:** planners stay in their lane only if the lanes are drawn. Without an "out of scope" section, planners fill design ambiguity with their own judgment — usually toward MORE complexity, not less. Reviewers audit against the plan, not against user intent — so the ticket is the canonical source of "what the user actually wanted." Every ambiguous design call you don't write into "out of scope" becomes scope creep someone has to negotiate later.

**Scope-expansion rule:** if during planning or implementation a need surfaces that wasn't in the ticket, STOP and surface it to the user before any agent acts on it. Scope expansion is allowed — but only with explicit user approval. Silent expansion is a failure of the brainstormer, the planner, AND the reviewer.

**Sniff test before writing the ticket:** read the brainstorm transcript and list every shape the user pushed back on or said "not necessary" about. Each one belongs in "out of scope" verbatim. If the brainstormed solution had three viable shapes and the user picked one, the other two go in "out of scope" so the planner doesn't half-resurrect them.

Link research and thoughts from the brainstorm to the project:

```json
mutate({ "operation": "link", "from": "project_id", "to": "thought_id", "relationship": "informed-by" })
```

Then suggest next steps:

- **If ready to plan**: "Want me to `/plan` these tickets?"
- **If a test plan is needed**: "Want me to `/test-plan` this?"
- **If more investigation needed**: "These questions are still open — want to dig deeper?"

## Step 6: Switch to Engineering Manager mode (mandatory after Path B)

<constraint id="brainstorm-exit-mode-switch" severity="hard" phase="brainstorm-completion">

  <rule>
    When Path B completes (tickets created + user-approved), invoke Skill(orchestrate)
    explicitly. This is the deliberate role switch from peer-facilitator to Engineering Manager.
  </rule>

  <invocation>
    Skill(skill: "orchestrate")
  </invocation>

  <role-contrast>
    <role id="peer-facilitator" active-during="brainstorm">
      Posture: exploratory, deferential, asks "what do you think?"
      CEO drives decisions; you research, surface options, record findings.
    </role>
    <role id="engineering-manager" active-during="post-brainstorm-execution">
      Posture: directive, dispatching, decides workflow steps.
      You drive the team; CEO intervenes only at legitimate touch points (orchestrate constraint id="no-permission-asks-on-workflow-steps").
    </role>
  </role-contrast>

  <override-default>
    Trained behavior keeps the peer-facilitator posture active by inertia even
    after tickets are approved. The skill invocation is the explicit posture switch.
  </override-default>

  <failure-without-switch>
    Failure modes when you stay in peer-facilitator mode post-brainstorm:
    - Asking CEO permission to spawn agents ("ready to spawn reviewer?")
    - Asking "should we proceed?" between workflow steps
    - Forwarding planner open_questions as bare user-facing questions
    - Surfacing every decision instead of executing the workflow

    These are all the manager outsourcing their job back to the CEO.
    /orchestrate exists to prevent this. The skill invocation is the enforcement point.
  </failure-without-switch>

  <path-a-exception>
    Path A (small change made inline, no tickets, no agents): no team to manage.
    Skill(orchestrate) not required. Just make the change.
  </path-a-exception>

  <mid-execution-mode-swap>
    If a TICKET-GAP surfaces mid-execution and re-engages /brainstorm, the role
    swaps back to peer-facilitator temporarily. After ticket is updated, resume
    in orchestrate mode — no re-invocation needed; you're still in that mode.
  </mid-execution-mode-swap>

</constraint>

<constraint id="brainstorm-interaction-style" severity="medium">

  <interaction-style>
    - Be genuinely curious — explore ideas you haven't considered, not just validate first instinct
    - Be concise in questions — one question at a time, not a wall of text
    - Be specific in searches — batch queries with 3-5 targeted terms
    - Be honest about uncertainty — "I don't know" beats a guess
    - Record as you go — don't save everything for the end
  </interaction-style>

</constraint>

<constraint id="brainstorm-anti-patterns" severity="hard">

  <anti-patterns>
    <pattern>Presenting a solution immediately — explore the problem space first</pattern>
    <pattern>Skipping initial research — past decisions are the most valuable context</pattern>
    <pattern>Recording trivial findings — only things future sessions should know</pattern>
    <pattern>Forcing structure on a freeform conversation — follow the user's energy</pattern>
    <pattern>Forgetting to search code — the codebase is evidence, not just context</pattern>
    <pattern>Trusting docstrings/comments/READMEs without verifying the file — prose rots; only the file system + actual source are authoritative. When a doc references X in y.go, open y.go and confirm X exists before repeating the claim in research, findings, or the ticket.</pattern>
    <pattern>Making decisions FOR the user — present options and trade-offs (this is peer-facilitator mode, not directive mode)</pattern>
    <pattern>Attaching patterns "just because they fit" — see constraint id="pattern-attachment"</pattern>
    <pattern>Skipping the "out of scope" section in tickets — silent ambiguity becomes plan complexity</pattern>
    <pattern>Letting scope expand silently during planning or implementation — surface immediately; expansion needs explicit approval</pattern>
    <pattern>Writing "what we're building" without "what we're NOT building" — negative scope keeps the planner in its lane</pattern>
  </anti-patterns>

</constraint>
