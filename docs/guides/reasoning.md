# Reasoning

## Overview

The thought graph is what makes knowledge more than a search engine over your
code and decisions. Search tells you what exists; the thought graph records *how
you came to believe it* — the hypotheses you formed, the evidence for and against
each one, and the consensus and significance that emerge once that evidence is
weighed. It is persistent, evidence-weighted reasoning that survives across
sessions, restarts, and context compaction, so the next session (yours or a
teammate's, human or agent) inherits the reasoning rather than re-deriving it.

The model is small. A **thought** is a node — a hypothesis, an observation, a
plan. A **charge** attaches a piece of evidence to a thought and says whether
that evidence *supports* or *contradicts* the thought's claim. From the charges
on a thought, knowledge derives a handful of scalars (which way the evidence
leans, how significant it is, how contested it is). A background **propagation**
pass then spreads consensus and significance across thoughts that are linked to
one another, so a belief shared across a well-connected cluster converges the way
opinions do among people who listen to each other.

This is the product differentiator. Most of what an LLM "knows" mid-task lives in
a context window that is erased the moment the window closes. The thought graph
moves that reasoning into durable, queryable, evidence-weighted state — you can
recall it, link it, charge it with new evidence, and watch consensus shift as the
evidence does.

## When & how to use

Reach for the thought graph whenever you are *reasoning*, not just *recording a
fact*. A confirmed conclusion is a finding; a settled architectural choice is a
decision; the messy in-between — a hypothesis you are testing, a trade-off you
are weighing, a surprising behavior you are trying to explain — is a thought. Use
it during research (forming hypotheses), implementation (your approach and the
trade-offs behind it), debugging (always record the broken→fixed transition), and
after testing (charge the thought with the result).

The discipline is a cycle: **recall → think → (do the work) → charge → recall
again → reflect.**

- **recall** — start here, always. Past thoughts carry debugging notes, design
  rationale, and gotchas that save you from re-investigating something a previous
  session already settled. Recall before you begin, and again at each decision
  point mid-task — especially before you are about to contradict a prior thought.
- **think** — record the hypothesis or observation: what you believe and *why*,
  not just the conclusion.
- **do the work** — implement, test, investigate.
- **charge** — when evidence arrives, attach it to the thought. Charge whether
  the evidence *supports* or *contradicts* the claim; the result either way is
  evidence worth keeping.
- **recall again** — confirm the thought (and its new charge) landed, and see it
  in the context of everything else you have recorded.
- **reflect** — periodically step back and let the graph surface consensus,
  tensions, and blind spots across all your reasoning (see
  [Reflection](#reflection)).

The point of writing reasoning down rather than holding it in your head is that
the graph outlives the conversation. A thought recorded today is recallable,
linkable, and chargeable months from now — and as new evidence lands, the
consensus the graph reports updates without you re-stating the original argument.

## The eight `thoughts` operations

Every operation lives under the single `thoughts` tool, dispatched by the
`operation` field. All of them run client-side (the daemon intercepts `thoughts`
calls and serves them locally). This section narrates what each op is *for*; for
the full parameter reference — every field, its type, and whether it is required
— see the generated table in [tools/thoughts.md](tools/thoughts.md).

The five everyday operations:

- **think** — record a thought. Requires `content` (what you're thinking and why)
  and `summary` (a deliberate, search-optimized one-line — this is what makes the
  thought findable later, so author it, don't let it be a throwaway). Optional:
  `session` to group related thoughts, `links` to connect it to other nodes,
  `ticket_id` to file it under a work item, `status`, `branches_from`, and
  `origin`.
- **charge** — attach evidence to a thought. Requires `thought` (the target ID),
  `polarity` (`positive` or `negative`), `weight` (1–10), and `reasoning` (why
  this charge applies). Optional: `evidence`, a list of node IDs the charge drew
  on (tests, PRs, other thoughts).
- **recall** — search thoughts. Nothing is required; you compose filters: a
  semantic `query`, valence / magnitude / consistency thresholds, `status`,
  `session`, `connected_to`, a time range, and a `mode` (`search`, `timeline`,
  `charges`, `graph`, `clusters`).
- **trace** — follow a reasoning chain forward or backward from a starting
  thought. Requires `thought`; optional `direction`, `depth`, `include_charges`,
  `include_artifacts`.
- **propagate** — manually trigger a propagation pass (it normally runs on its
  own in the background — see [Propagation](#propagation)). Nothing is required;
  `force_full` runs the full-corpus backstop pass on demand.

The remaining three are **client-side plumbing**, not everyday calls — they exist
so the cluster-detection and async-similarity machinery has a wire surface, and
you rarely invoke them by hand:

- **adjacency** — a bulk graph-adjacency read used by client-side cluster
  detection.
- **charges_for** — a bulk per-thought charge fetch.
- **similarity_report** — fetches the result of the asynchronous topic-similarity
  pass.

## The evidence model

A charge carries two inputs you choose and feeds four scalars the system derives.

The two inputs: **polarity** tracks the *claim*, not the news. `positive` means
the evidence *supports* the thought's claim; `negative` means it *contradicts*
it. This is independent of whether the news is good or bad — a passing test that
confirms a thought "this is broken" is a *positive* charge on that claim. Put any
sentiment about the subject in the `reasoning` text, never in the polarity sign.
**weight** (1–10) is how significant the evidence is — a decisive proof is a 9, a
weak hint is a 2.

From the charges on a thought, knowledge derives four scalars. The formulas are
shown in parentheses for the curious — the authoritative definitions live in
`cmd/knowledge/internal/thought/properties.go` — but you reason in plain terms:

- **valence** (−1 to +1) — which way the evidence leans. +1 is unanimously
  supported, −1 is unanimously contradicted, 0 is evenly split. (It is
  `(positiveWeight − negativeWeight) / totalWeight`.)
- **magnitude** (0 to ~10) — how significant the thought is, from the total
  charge weight on it. It grows *logarithmically*, so ten small charges do not
  outweigh one decisive one, and piling on evidence has diminishing returns.
  (It is `log(1 + totalWeight)`.)
- **consistency** (0 to 1) — how one-sided the evidence is. 1 means every charge
  agrees; 0 means it is evenly contested. (It is `1 − minSide/maxSide`, where the
  sides are the positive and negative weight totals.)
- **self-trust** — how much a thought resists being pulled toward its neighbors
  during propagation. A thought with consistent, plentiful evidence trusts itself
  and moves little; a thinly- or evenly-charged one is more easily swayed. (It is
  `0.1` baseline + `consistency × log(1 + chargeCount)`.)

## Propagation

Charges describe a single thought in isolation. **Propagation** is what spreads
belief *between* thoughts, so reasoning behaves like a community of opinions
rather than a pile of disconnected facts. It spreads two things — consensus
(valence) and significance (magnitude) — and it does so the way the algorithm is
described in `cmd/knowledge/internal/thought` (a DeGroot learning model); you do
not need the math to use it.

- **Consensus spreads by trust.** Knowledge builds a trust matrix of
  who-listens-to-whom across linked thoughts, and each thought's next valence
  becomes a trust-weighted average of its neighbors' — the DeGroot update. An
  opinion shared across a well-connected cluster converges; an isolated thought
  stays where its own charges put it.
- **Significance spreads but decays.** A neighbor's magnitude bleeds in
  *attenuated* — at a 0.7 factor (`magnitudeDecay`,
  `cmd/knowledge/internal/thought/propagation_matrix.go`) — so importance does
  not propagate unbounded across the graph; it fades with distance from the
  thoughts that actually earned it.
- **Self-trust resists the pull.** The self-trust scalar from the evidence model
  is exactly what keeps a well-evidenced thought from being dragged toward a noisy
  neighbor.

Propagation runs **automatically, hourly** (`PropagationInterval`,
`cmd/knowledge/internal/thought/loop.go`), with a full-corpus **backstop every 24
hours** (the `--reflect-backstop-interval` default, see
[binaries.md](binaries.md)) that resets accumulated incremental drift. You can
also trigger it **on demand** with `thoughts(operation:"propagate")`, or force a
full recompute now with the `force_full` flag.

## Reflection

Once propagation has run, a set of `query(mode:...)` reflection modes read the
derived state back out and turn it into reports about your reasoning as a whole.
The full mode table is in the generated [tools/query.md](tools/query.md); the
seven reflection modes are:

- **personality** — per-cluster trust scalars: a value above 1.0 reads as
  open / easily-swayed, below 1.0 as stubborn.
- **influence** — the thoughts that most shape the rest of the graph.
- **tensions** — pairs of opposite-valence thoughts that are explicitly linked to
  each other (the disagreements in your reasoning).
- **blind_spots** — facets of epistemic risk, such as confident-but-untested claims,
  fragile single-point-of-evidence thoughts, and beliefs that cite code which has
  since changed.
- **summary** — a high-level read of the thought graph's state.
- **evolution** — how the scalars changed between two clusters or points in time.
- **clusters** — the detected groupings of related thoughts.

**Cold-loop caveat.** Reflection serves the cache that propagation fills. If the
background propagation loop has not run yet — or has been disabled with the
`--no-propagation-runtime` flag (see [binaries.md](binaries.md)) — the
reflection modes return a cold-loop message ("propagation loop not running in
this process") rather than a stale or invented report. If you hit that message,
the fix is to let the propagation runtime run: confirm the daemon was not started
with `--no-propagation-runtime`, then give the hourly loop a tick or trigger a
pass by hand with `thoughts(operation:"propagate")`.

## Related node types and linking

Charges are not the only way reasoning connects. A few neighboring node types and
the edges between them complete the picture:

- **Chargeable nodes are thought, finding, and research only.** A charge carries
  valence and magnitude that only make sense on a claim. To bring a *decision*
  into the evidence graph, charge the thought that states the decision's claim, or
  cite the decision as `evidence` on a related charge — you cannot charge the
  decision node directly.
- **decision** — recorded via `record_decision`, which requires a `name`, the
  `choice`, and a `rationale`, and strongly encourages `alternatives` (a record
  with no rationale and no alternatives is not really a decision). It is *not*
  directly chargeable. See [tools/record_decision.md](tools/record_decision.md)
  for the full parameter reference.
- **finding** — a confirmed conclusion. When a hypothesis you recorded as a
  thought is proven, the durable result is a finding.
- **research** — an open investigation, a question you have not yet answered.

The edges matter as much as the nodes. **Explicit thought↔thought
`contradicts` / `relates-to` edges are what let tensions surface.** Born-linking a
thought only to a ticket or session does *not* let the graph see that two
thoughts disagree — when a new thought opposes an existing one, draw the edge
yourself with `mutate(operation:"link", relationship:"contradicts")` (or
`"relates-to"` when it merely relates). The tensions and consensus surfacing
depends on those edges existing.

## Workflow discipline

A few habits make the difference between a thought graph that compounds in value
and one that fills with noise.

- **Pick the right node for the content.** Use `think` for raw reasoning —
  hypotheses, intuitions, the *why* behind an approach. Record a *confirmed*
  conclusion as a finding, an *open* investigation as research, and a settled
  *choice* as a decision. A graph of only "starting step N" thoughts is
  low-value; the reasoning and the evidence are what make it worth recalling.
- **Charge what is epistemically load-bearing — not step-by-step progress.**
  Charge the moment a *user correction or directive* lands, when a *design
  insight* is reached, and whenever *later evidence confirms or contradicts* a
  prior thought. Charging only routine progress inflates bookkeeping into the
  highest-influence nodes in the graph while genuine insights sit uncharged,
  inverting the signal.
- **Close the feedback loop — the highest-grade, most-skipped charge.** When work
  is verified in the *real world* — shipped and working, the symptom gone, the
  user confirmed, a prediction held or failed — charge the *originating
  hypothesis* with that outcome. Green unit tests are not this; reality is. The
  `/retro` skill exists to close this loop, but charge the moment any real-world
  outcome lands rather than waiting.
- **Negation needs first-hand proof; charging does not.** Never negate,
  contradict, supersede, or invalidate a thought without proof you have read
  *yourself, in the current source, this session* — another agent's report, a
  comment, a docstring, or a prior thought's assertion is not enough, because
  thoughts rot exactly like comments do. Prefer supersede-with-a-source-cited-
  reason (a `branches_from` plus a status update on the original) over a blanket
  invalidate, since charges do not carry forward across `branches_from` and a
  careless invalidate destroys the evidence on the original. Charging is the
  opposite act: it records evidence *for or against* a claim and needs no proof
  beyond the evidence itself — so charge a user correction the moment it lands,
  never withholding it the way you would withhold a negation.
