# thoughts

## Overview

`thoughts` is the persistent reasoning graph. It externalizes your reasoning so it
survives sessions, restarts, and context compaction: hypotheses become
first-class nodes, charges attach evidence to them, and a propagation pass derives
a valence (which way the evidence leans), a magnitude (how significant), a
consistency score, and a self-trust score from the charge topology. It is
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
| `charge` | `thought`, `polarity`, `weight`, `reasoning` | Attach positive/negative evidence (weight 1-10). |
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

// Charge it once tests confirm
thoughts({ "operation": "charge", "thought": "t_abc", "polarity": "positive",
           "weight": 8, "reasoning": "Tests pass; behavior confirmed" })
```

Link a thought to other nodes with `mutate(operation: "link", from: thought_id,
to: node_id, relationship: "informed-by" | "supports" | "contradicts" | …)`. For
the full operation reference, run `help("thoughts")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `branches_from` | string |  |  | (think) Thought ID this branches from (usually after invalidation of the original). |
| `connected_to` | string |  |  | (recall) Must be connected to this node ID. |
| `consistency_max` | number |  |  | (recall) Maximum consistency (low values find contested thoughts). |
| `content` | string |  |  | (think) The thought content — what you're thinking and why. |
| `depth` | number |  |  | (trace) Max hops (default 5). |
| `direction` | string |  | forward, backward, both | (trace) Traversal direction. |
| `evidence` | array of string |  |  | (charge) Node IDs of evidence (tests, PRs, incidents, other charges). |
| `evidence[]` | string |  |  |  |
| `format` | string |  |  | (recall) Output format: 'text' (default) or 'json' (structured). |
| `include_artifacts` | boolean |  |  | (trace) Include linked artifacts (code, decisions, PRs). |
| `include_charges` | boolean |  |  | (trace) Include charge nodes in the trace. |
| `limit` | number |  |  | (recall) Max results (default 20). |
| `links` | array of string |  |  | (think) Node IDs to link this thought to (decisions, findings, code, etc.). |
| `links[]` | string |  |  |  |
| `magnitude_min` | number |  |  | (recall) Minimum magnitude (significance threshold). |
| `mode` | string |  | search, timeline, charges, graph, clusters | (recall) Output format. |
| `operation` | string | yes | think, charge, recall, trace, propagate, adjacency, charges_for | Which thoughts op to run. |
| `polarity` | string |  | positive, negative | (charge) Charge direction. |
| `query` | string |  |  | (recall) Semantic search text (optional — omit to browse all thoughts). |
| `reasoning` | string |  |  | (charge) WHY this charge is being applied — what evidence supports it. |
| `scope` | string |  | all, all_types | (adjacency) Which adjacency view to build. 'all' = NodeThought-filtered with session-sibling expansion (cluster detection on thoughts only). 'all_types' = every node except NodeProxy with no edge filter (cross-type cluster detection). |
| `session` | string |  |  | (think, recall filter) Session name to group related thoughts (e.g., 'backend-auth-design'). Creates session if new on think. |
| `status` | string |  | hypothesized, validated, invalidated | (think initial status / recall filter) Default hypothesized for think. |
| `summary` | string |  |  | (think, REQUIRED) Search-optimized one-line summary of the thought, max 500 chars. Authored deliberately — this is what makes the thought findable via recall. NOT auto-derived from content. (max length: 500) |
| `thought` | string |  |  | (charge, trace) Thought node ID. Required for charge and trace. |
| `thought_ids` | array of string |  |  | (adjacency, charges_for) Optional subset filter (adjacency) / required charge sources (charges_for). When set on adjacency, response is projected down to just these IDs. |
| `thought_ids[]` | string |  |  |  |
| `time_end` | string |  |  | (recall) End of time range (ISO date). |
| `time_start` | string |  |  | (recall) Start of time range (ISO date, e.g. 2026-03-01). |
| `valence_max` | number |  |  | (recall) Maximum valence (-1.0 to 1.0). |
| `valence_min` | number |  |  | (recall) Minimum valence (-1.0 to 1.0). |
| `weight` | number |  |  | (charge) Charge significance (1-10). Higher = stronger evidence. |
<!-- END GENERATED: params -->
