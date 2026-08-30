// SPDX-License-Identifier: Apache-2.0

// Package tools — help topic for the thoughts tool (the persistent reasoning
// graph). Split out of help_content2.go so neither file creeps over the
// 500-line hard cap; the constant is unchanged, only rehomed.
package tools

const helpThoughts = `# thoughts — Persistent reasoning graph (think / charge / recall / trace / propagate)

The thought graph externalizes reasoning so it survives sessions, restarts,
and compactions. Hypotheses become first-class nodes; charges add evidence;
DeGroot propagation derives valence (consensus direction), magnitude
(significance), consistency, and self-trust from charge topology.

## Operations

  - think     : record a thought (hypothesis / observation / plan)
  - charge    : attach evidence that supports/contradicts a thought's claim
  - recall    : search thoughts with composable filters
  - trace     : follow reasoning chains forward/backward from a thought
  - propagate : manually run DeGroot propagation across all thoughts;
                with similarity:true, ASYNC-trigger the topic-similarity lever
  - similarity_report : fetch the result of the async topic-similarity pass
  - adjacency : bulk graph adjacency read used by client-side cluster detection
  - charges_for : bulk per-thought charge fetch

That is the complete operation vocabulary — eight. An unrecognized operation
TERMINATES here with the canonical unknown-operation diagnostic; nothing
downstream claims this tool.

Common cycle:  recall → think → (do work) → charge → recall again to confirm
the hypothesis landed. Examine a single thought via query(mode: "examine",
id: thought_id). Link a thought to another node via mutate(operation:
"link", from: thought_id, to: node_id, relationship: "informed-by"|
"supports"|"contradicts"|"relates-to"|"produced").

## operation: "think" — Record a unit of reasoning

  content       — the thought (required)
  summary       — search-optimized one-line summary, max 500 chars (REQUIRED).
                  Authored deliberately — this is what makes the thought findable
                  via recall. Nothing composes one from content; charge requires
                  a summary of its own (see the charge section).
  session       — group name for related thoughts (e.g. "backend-auth-design")
  ticket_id     — born-link the thought under its work item (ticket--contains-->thought).
                  An unresolvable id is dropped with a warning, never blocking the think.
  origin        — developer-origin role of the agent recording this (planner |
                  implementer | reviewer | researcher | tester | orchestrator |
                  main; absent => main). An OPEN string, not an enum gate — a
                  custom value is stored as-is. When it resolves to a seeded
                  agent node an agent--produced-->thought hub edge is written.
  branches_from — thought ID this replaces (after invalidating the original).
                  SETTING IT REQUIRES verified_quote: a supersession is a
                  negation-class call, and the gate demands a verbatim substring
                  of the superseded node's CURRENT source before any write.
                  cited_range ("path/file.go:start-end") optionally pins where
                  that quote must resolve. Neither is persisted.
  links         — node IDs to connect to (decisions, steps, code)
  status        — hypothesized (default) | validated | invalidated

  Examples:
    thoughts({ "operation": "think", "content": "Cache invalidation bug caused by X",
               "summary": "Cache bug root cause: stale key X never invalidated on write",
               "session": "debug-cache" })
    thoughts({ "operation": "think", "content": "Approach: use Y instead of Z",
               "summary": "Decided to use Y over Z for cache layer",
               "links": ["decision_id"] })

  When to use:
    - Before implementing: record your planned approach
    - When debugging: what's broken, your hypothesis
    - After fixing: what was wrong and how you fixed it

## operation: "charge" — Add evidence to a thought

  thought   — thought node ID (required)
  polarity  — "positive" (evidence SUPPORTS the thought's claim) or "negative"
              (evidence CONTRADICTS it). Required. This is about the claim's
              truth, NOT good-vs-bad news; sentiment goes in reasoning.
  weight    — significance 1-10 (required)
  reasoning — why this charge applies (required)
  summary   — search-optimized one-line summary, max 500 chars (REQUIRED).
              It summarizes the EVIDENCE THIS CHARGE RECORDS, which is a
              different sentence from the thought's own summary: the thought
              states a claim, the charge states what was observed about it.
              Nothing composes one from reasoning.
  evidence  — node IDs of supporting evidence: cite the SPECIFIC thought,
              finding, decision, or charge IDs the charge actually drew on
              (not a vague hand-wave). Citing a related thought records a
              charge→thought evidenced-by edge that feeds cross-cluster trust
              differentiation, so a well-cited charge strengthens exactly the
              thoughts it relied on — leave it empty and that signal is lost.

  Examples:
    // claim CONFIRMED → positive (the evidence supports the thought)
    thoughts({ "operation": "charge", "thought": "t_abc",
               "polarity": "positive", "weight": 8,
               "reasoning": "Tests pass, behavior confirmed in prod — supports the claim",
               "summary": "prod run confirms the fix holds under real traffic",
               "evidence": ["t_root_cause", "finding_bench_id"] })
    // claim REFUTED → negative (the evidence contradicts the thought)
    thoughts({ "operation": "charge", "thought": "t_abc",
               "polarity": "negative", "weight": 5,
               "reasoning": "Benchmark shows no regression — contradicts the claim",
               "summary": "the benchmark measured no regression at this call site" })
    // polarity tracks the CLAIM, not the sentiment. Thought claims "competitor X
    // poses a significant threat"; confirming the threat is POSITIVE because the
    // evidence supports the claim — even though it is bad news for us.
    thoughts({ "operation": "charge", "thought": "t_threat",
               "polarity": "positive", "weight": 7,
               "reasoning": "Confirmed X ships feature Y at lower price — supports the claim that the threat is real (this is bad news for us, but the charge is positive because it supports the thought)." })

## operation: "recall" — Search thoughts with composable filters

  query           — semantic search text (omit to browse)
  session         — filter by session name
  status          — hypothesized | validated | invalidated
  valence_min/max — computed valence range (-1.0 to 1.0)
  magnitude_min   — minimum magnitude threshold
  consistency_max — max consistency (low = contested thoughts)
  connected_to    — node ID thoughts must be connected to
  time_start/end  — date range (ISO 8601)
  limit           — max results (default 20, max 50)
  mode            — search | timeline | charges | graph | clusters | context
  all_types       — with mode:"clusters", run cluster detection over EVERY node
                    type rather than thoughts only. This is the ONLY spelling of
                    the all-node cluster view; query(mode:"clusters") is
                    thought-only and rejects a type filter outright.
  format          — text (default) | json

  Examples:
    thoughts({ "operation": "recall", "query": "cache invalidation" })
    thoughts({ "operation": "recall", "session": "debug-cache",
               "status": "hypothesized" })
    thoughts({ "operation": "recall", "mode": "timeline", "limit": 10 })
    thoughts({ "operation": "recall", "valence_min": 0.5, "magnitude_min": 2.0 })
    thoughts({ "operation": "recall", "mode": "context", "query": "cache invalidation" })

  mode:"context" is the SESSION-START pack, and it is NOT thought-only: it
  composes five bounded reads into one deterministically-ordered result —
  a cross-type semantic seed search, a one-hop edge expansion over the seeds
  (informed-by / relates-to / contains / depends-on / answers), each thought's
  charge state rendered as validated/contested, a recency overlay, and the open
  tickets (the terminal-status set is excluded, so an unknown custom workflow
  state stays visible). Every section is capped, so the pack fits one tool turn.

## operation: "trace" — Follow reasoning chains

  thought           — starting thought node ID (required)
  direction         — forward | backward | both (default: both)
  depth             — max hops (default: 5)
  include_charges   — include charge nodes in the trace
  include_artifacts — include linked artifacts (code, decisions, PRs)
  format            — text (default) | json

  Examples:
    thoughts({ "operation": "trace", "thought": "t_abc" })
    thoughts({ "operation": "trace", "thought": "t_abc",
               "direction": "backward", "depth": 3, "include_artifacts": true })

  When to use:
    - Walking the reasoning that led to a decision
    - Auditing whether a hypothesis was actually charged with evidence
    - Finding all the artifacts a thought informed

  A thought recorded with an origin role (the think origin param) carries an
  agent--produced-->thought hub edge, so its trace surfaces the originating
  agent node (e.g. the planner agent) as a provenance step — intentional
  lineage, not noise.

## operation: "propagate" — Manually run DeGroot propagation

  No required parameters.

  The background loop normally fires propagation on its own — invoke this
  when you want immediate convergence after a batch of charges, or in tests
  that need to observe the post-propagation state without a timer wait.

  Optional:
    force_full (bool) — Run the full-corpus backstop pass now: bypasses the
      quiet-tick skip and incremental scoping, recomputes every component, and
      resets the backstop cadence. Use for an on-demand full reflection (ops /
      debug) instead of waiting for the periodic backstop tick. Errors if the
      reflection loop is not running in this process.

    similarity (bool) — ASYNC-trigger the topic-similarity lever (drain →
      centroids → reconcile → merge cascade → summaries → drift → links →
      densify). The pass can run many minutes — LONGER than the tool-call
      timeout — so the trigger does NOT wait: it acquires the single-flight
      guard, starts the pass in the background, and returns IMMEDIATELY with a
      copy-pasteable thoughts({"operation":"similarity_report"}) fetch call and
      a duration estimate. Only ONE pass runs at a time; a second trigger while
      one is in flight coalesces (returns "already running" + the same fetch
      contract, no second pass). Optional link_threshold / merge_threshold /
      densify_threshold / densify_k / densify_edge_budget override the HIGH
      defaults.

  Returns: thoughts_processed, components, iterations, converged (plain
  propagate); or the async-trigger contract (similarity:true).

  Example:
    thoughts({ "operation": "propagate" })
    thoughts({ "operation": "propagate", "force_full": true })
    thoughts({ "operation": "propagate", "similarity": true })

## operation: "similarity_report" — Fetch the async similarity pass result

  Optional:
    id (string) — a specific past pass to fetch. Omit for the LATEST pass.

  Renders by status:
    running   — in-progress + elapsed + the duration estimate + may-take-longer
    completed — the FULL rendered report (links, merge cascade, summaries,
                reconciliation, densification, the threshold-tuning histogram)
    failed    — the failure, surfaced loudly
    no pass yet — a clear empty-state message naming how to trigger one

  The report is persisted as an event node and fetched by id/marker — it is NOT
  vector-searchable (event nodes do not embed); this op is the way to read it.

  Example:
    thoughts({ "operation": "similarity_report" })
    thoughts({ "operation": "similarity_report", "id": "<pass-id>" })

## operation: "adjacency" — Bulk graph adjacency read

  scope       — "all" | "all_types" (required)
                "all" builds the NodeThought-filtered view with session-sibling
                expansion (cluster detection over thoughts only).
                "all_types" is every node except proxies, with no edge filter
                (cross-type cluster detection).
  thought_ids — optional subset projection; when set the response is narrowed to
                just these IDs.

  The read client-side cluster detection runs on. Prefer the rendered views
  (query(mode:"clusters"), thoughts(recall, mode:"clusters")) unless you are
  computing your own topology.

  Example:
    thoughts({ "operation": "adjacency", "scope": "all" })

## operation: "charges_for" — Bulk per-thought charge fetch

  thought_ids — the thoughts whose charges to fetch (required)

  Returns {charges_by_thought: {thought_id: [charge_node, ...]}} in ONE call,
  rather than N per-thought round trips.

  Example:
    thoughts({ "operation": "charges_for", "thought_ids": ["t_abc", "t_def"] })
`
