# thoughts

## Overview

`thoughts` is the persistent reasoning graph. It externalizes your reasoning so it
survives sessions, restarts, and context compaction: hypotheses become
first-class nodes, charges attach evidence to them — positive when the evidence
supports the thought's claim, negative when it contradicts it — and a propagation
pass derives a valence (which way the evidence leans on the claim), a magnitude
(how significant), a consistency score, and a self-trust score from the charge
topology. It is
dispatched by the `operation` field.

Think of it as memory with an opinion — not just notes, but notes that know how
well-supported they are.

## When & how to use

Use `thoughts` to record reasoning as you work: the approach you are about to
take, a hypothesis while debugging, the broken-to-fixed transition once you solve
something, and the evidence that confirmed or refuted it. The natural cycle is
`recall` (start here — past thoughts carry debugging notes and rationale) → `think`
→ do the work → `charge` with the result → `recall` again to confirm it landed.

Each operation has its own required inputs:

| Operation | Required (besides `operation`) | What it does |
| --- | --- | --- |
| `think` | `content`, `summary` | Record a hypothesis/observation/plan. `summary` is authored deliberately — it is what makes the thought findable. |
| `charge` | `thought`, `polarity`, `weight`, `reasoning` | Attach evidence that supports (positive) or contradicts (negative) the thought's claim (weight 1-10). |
| `recall` | — | Search/browse thoughts; filter by `query`, `session`, `status`, valence/magnitude, etc. |
| `trace` | `thought` | Follow a reasoning chain forward/backward from a thought. |
| `propagate` | — | Manually run propagation for immediate convergence after a batch of charges. |

Examples:

```jsonc
// Record an approach before implementing
thoughts({ "operation": "think",
           "content": "Approach: invalidate the cache key on every write",
           "summary": "Cache fix: invalidate key X on write path",
           "session": "debug-cache" })

// Charge it once tests confirm. Polarity tracks the CLAIM, not good-vs-bad news:
// positive because the evidence SUPPORTS the thought's claim that the fix works.
thoughts({ "operation": "charge", "thought": "t_abc", "polarity": "positive",
           "weight": 8, "reasoning": "Tests pass; behavior confirmed — supports the claim that invalidating key X fixes the bug" })
```

Link a thought to other nodes with `mutate(operation: "link", from: thought_id,
to: node_id, relationship: "informed-by" | "supports" | "contradicts" | …)`. For
the full operation reference, run `help("thoughts")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `all_types` | boolean |  |  | (recall, mode:'clusters') Run cluster detection over EVERY node type rather than thoughts only — the boolean spelling of the adjacency arm's scope:'all_types'. |
| `branches_from` | string |  |  | (think) Thought ID this branches from (usually after invalidation of the original). |
| `cited_range` | string |  |  | (think) Optional locality hint accompanying verified_quote on a supersession, as "path/file.go:start-end". When set, the verbatim substring must resolve to the cited path. TOP-LEVEL param, consumed by the gate before any write and never persisted. |
| `connected_to` | string |  |  | (recall) Must be connected to this node ID. |
| `consistency_max` | number |  |  | (recall) Maximum consistency (low values find contested thoughts). |
| `content` | string |  |  | (think) The thought content — what you're thinking and why. |
| `densify_edge_budget` | number |  |  | (propagate, similarity:true) Cap on edges the densification phase may add in one pass. Absent uses the package default. |
| `densify_k` | number |  |  | (propagate, similarity:true) Neighbor count per node for the densification phase. Absent uses the package default. |
| `densify_threshold` | number |  |  | (propagate, similarity:true) Similarity floor for the post-link within-topic kNN densification phase. Absent uses the package default. |
| `depth` | number |  |  | (trace) Max hops (default 5). |
| `direction` | string |  | forward, backward, both | (trace) Traversal direction. |
| `evidence` | array of string |  |  | (charge) Node IDs of evidence — tests, PRs, incidents, related thoughts, or other charges. Citing a related thought records a charge→thought evidenced-by edge that feeds cross-cluster trust differentiation. |
| `evidence[]` | string |  |  |  |
| `force_full` | boolean |  |  | (propagate) Run the full-corpus backstop pass now — bypasses the quiet-tick skip and incremental scoping, recomputes every component, and resets the backstop cadence. Use for an on-demand full reflection (ops/debug) instead of waiting for the periodic backstop tick. Errors if the reflection loop is not running in this process. |
| `format` | string |  |  | (recall) Output format: 'text' (default) or 'json' (structured). |
| `id` | string |  |  | (similarity_report) Optional id of a specific past similarity pass to fetch. Omit to fetch the LATEST pass (running → in-progress + estimate; completed → the full rendered report; failed → the failure). |
| `include_artifacts` | boolean |  |  | (trace) Include linked artifacts (code, decisions, PRs). |
| `include_charges` | boolean |  |  | (trace) Include charge nodes in the trace. |
| `limit` | number |  |  | (recall) Max results (default 20, max 50). |
| `link_threshold` | number |  |  | (propagate, similarity:true) Per-call override for the topic-link similarity threshold. Absent uses the package default. Accepts a number or its quoted-string form; any other value is surfaced loudly rather than defaulted. |
| `links` | array of string |  |  | (think) Node IDs to link this thought to (decisions, findings, code, etc.). |
| `links[]` | string |  |  |  |
| `magnitude_min` | number |  |  | (recall) Minimum magnitude (significance threshold). |
| `merge_threshold` | number |  |  | (propagate, similarity:true) Per-call override for the topic-merge cascade threshold. Absent uses the package default. Accepts a number or its quoted-string form. |
| `mode` | string |  | search, timeline, charges, graph, clusters, context | (recall) Which recall view to run. search (default), timeline and charges are renders of the thought pipeline, and graph renders as search does; clusters and context are separate arms — clusters runs cluster detection, and context composes the five-section session-start context pack (cross-type seed search, bounded edge expansion, charge state, recency overlay, open tickets), so it is NOT thought-only. |
| `operation` | string | yes | think, charge, recall, trace, propagate, adjacency, charges_for, similarity_report | Which thoughts op to run. |
| `origin` | string |  |  | (think) Developer-origin role of the agent recording this thought — conventional values planner\|implementer\|reviewer\|researcher\|tester\|orchestrator\|main; absent => main. Open string (flex-parsed, NOT an enum gate): a custom value is stored as-is. Stamped as origin metadata, and when it resolves to a user-authored agent node, an agent--produced-->thought hub edge is written. |
| `polarity` | string |  | positive, negative | (charge) Whether the evidence SUPPORTS the thought's claim ("positive") or CONTRADICTS it ("negative"). NOT good-news/bad-news about the subject — sentiment about the subject belongs in reasoning/content text. |
| `query` | string |  |  | (recall) Semantic search text (optional — omit to browse all thoughts). |
| `reasoning` | string |  |  | (charge) WHY this charge applies — the specific evidence that supports or contradicts the thought's claim. Put any sentiment about the subject HERE, never in the polarity sign. |
| `scope` | string |  | all, all_types | (adjacency) Which adjacency view to build. 'all' = NodeThought-filtered with session-sibling expansion (cluster detection on thoughts only). 'all_types' = every node except NodeProxy with no edge filter (cross-type cluster detection). |
| `session` | string |  |  | (think, recall filter) Session name to group related thoughts (e.g., 'backend-auth-design'). Creates session if new on think. |
| `similarity` | boolean |  |  | (propagate) Trigger the topic-similarity lever ASYNCHRONOUSLY (drain → centroids → reconcile → merge cascade → summaries → drift → links). Returns immediately with a similarity_report fetch call and a duration estimate; only one pass runs at a time and a second trigger coalesces. |
| `status` | string |  | hypothesized, validated, invalidated | (think initial status / recall filter) Default hypothesized for think. |
| `summary` | string |  |  | (think and charge, REQUIRED on both) Search-optimized one-line summary, max 500 chars, authored deliberately — this is what makes the node findable via recall, and nothing composes one for you. On think it summarizes the THOUGHT'S CLAIM; on charge it summarizes the EVIDENCE the charge records, which is a different sentence from the thought it attaches to. (max length: 500) |
| `thought` | string |  |  | (charge, trace) Thought node ID. Required for charge and trace. |
| `thought_id` | string |  |  | (charge) Singular alias for `thought` — the charge arm accepts either spelling for the thought node ID. |
| `thought_ids` | array of string |  |  | (adjacency, charges_for) Optional subset filter (adjacency) / required charge sources (charges_for). When set on adjacency, response is projected down to just these IDs. |
| `thought_ids[]` | string |  |  |  |
| `ticket_id` | string |  |  | (think) Active ticket/project ID — born-linked as ticket--contains-->thought so the thought is grouped under the work item that produced it. An unresolvable ticket_id is dropped with a warning, never blocking the think. |
| `time_end` | string |  |  | (recall) End of time range (ISO date). |
| `time_start` | string |  |  | (recall) Start of time range (ISO date, e.g. 2026-03-01). |
| `valence_max` | number |  |  | (recall) Maximum valence (-1.0 to 1.0). |
| `valence_min` | number |  |  | (recall) Minimum valence (-1.0 to 1.0). |
| `verified_quote` | string |  |  | (think) Negation-gate proof of work — a TOP-LEVEL param on the call. REQUIRED whenever branches_from is set (a supersession): a verbatim substring of the superseded node's CURRENT source. Consumed by the gate before any write and never persisted. |
| `weight` | number |  |  | (charge) Charge significance (1-10). Higher = stronger evidence. |
<!-- END GENERATED: params -->
