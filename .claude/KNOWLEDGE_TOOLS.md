<guidance>
  <principle name="tools-first">
    Default to `search`, `file_symbols`, `traverse`, and `ast` for code work.
    Grep / Glob / Read / sed are the fallback, not the starting point — the graph is
    indexed, semantically aware, and knows the call graph.
  </principle>
  <principle name="primitives-over-shortcuts">
    `query`, `traverse`, `mutate` are lean generic primitives — pick the graph
    with the `graph` param. A few composite shortcuts exist
    (`query(mode: plan_tree | lineage | evidence)`); for a new pattern, compose
    with the primitive and use `query(mode: "stats")` to discover the vocabulary.
  </principle>
  <principle name="discover-dont-memorize">
    Don't memorize node/edge types — run `query({ "mode": "stats", "graph": "code" })`
    to list them, then feed the result into `traverse`. `edge_types` is
    case-insensitive. Code-graph method IDs are receiver-qualified
    (`path/file.go:Type.Method`, not `file.go:Method`).
  </principle>
  <principle name="selector-vocabulary">
    `graph` selects the graph family, not the graph instance. `graph:"knowledge"`
    is the memory/thought/decision graph; the code graph for a repo named
    `knowledge` is `graph:"code", repo:"knowledge"`. Put instance names in
    their typed fields: `repo`, `account`, `language`, or `name`.
  </principle>
</guidance>

<discipline note="cross-cutting rules distilled from the agent + skill tool guidance">
  <exploration>
    <rule>A `search` / `ast` miss is NOT proof of absence — never conclude "greenfield" or "genuinely new" from one query. Run BOTH a concept `search` and a structural `ast` pass before deciding nothing exists.</rule>
    <rule>`ast` is the most under-used reuse tool. When the question is "is there code that DOES this" (shape), reach for `ast`; `search` answers "is there code NAMED this" (text). Search-only reuse checks are how re-implementations slip in.</rule>
    <rule>Verify before citing — docstrings, comments, and READMEs rot; only the file + actual source are authoritative. If a doc says X lives in `y.go`, open `y.go` and confirm X exists before repeating it in a finding, decision, or ticket.</rule>
    <rule>Read the top candidates with `file_symbols` / source before concluding — don't trust summaries.</rule>
    <rule>Check the staleness line on `search` results before trusting them. A stale index yields wrong conclusions; re-`collect` if it's far behind HEAD.</rule>
    <rule severity="hard" name="staleness-is-never-a-grep-license">THE #1 OBSERVED FAILURE MODE: the index goes a few commits stale → one weak search result → the session silently switches to shell grep FOR THE REST OF THE SESSION. A stale index is a reason to run `collect` (30s–2min, incremental), NEVER a reason to fall back to grep. The moment you notice staleness, re-collect and keep using the graph. Catch the tell: if you are about to grep because "the index is behind," that thought IS the failure — collect instead.</rule>
    <rule name="mode-switch-tripwire">Log forensics and code exploration are different domains. Shell is right for log files — but a shell session does not carry over: after grep/awk over logs, the NEXT code question goes back through `search`/`ast`/`file_symbols`/`traverse`. Tool-mode inertia ("I'm already in the shell") is how grep creep starts.</rule>
  </exploration>
  <shell-is-right note="the legitimate exceptions — named so the tools-first rule stays credible">
    <case>Log files, build output, runtime/process state — not in any graph; grep/tail/lsof are correct.</case>
    <case>Dotfiles, editor/CLI config, and other non-indexed local files.</case>
    <case>Generated or binary artifacts the indexer doesn't chunk.</case>
    <case>Reading a SPECIFIC file/range you already located — `Read` (optionally after `file_symbols`), not `sed -n`.</case>
    <case>Anything NOT in the list above that lives in indexed source: search/ast/file_symbols/traverse first.</case>
  </shell-is-right>
  <writes>
    <rule>`search` / `recall` BEFORE creating — decisions, findings, and research don't exist in isolation. Check for an existing node first to avoid duplicates.</rule>
    <rule>Name for retrieval. The node `name` and the first sentence of its `description` are what BM25 matches later; vague titles are search-invisible. State the concept/concern in plain terms.</rule>
    <rule>Born linked. Pass the optional `ticket_id` / `session` / `links` params on create (mutate create, record_decision, think) so new nodes carry contains/relates-to edges to the active ticket, session, and related nodes from the start — edge density is what makes graph-walk retrieval work later. Unresolvable IDs are dropped with a warning; they never block the write.</rule>
    <rule>A decision needs `rationale` AND `alternatives`. "We decided X" with no why isn't a decision record — and if there were no alternatives, was it a decision?</rule>
  </writes>
  <thoughts>
    <rule>recall → think → charge on every task — not optional. Past thoughts carry debugging notes and design rationale that save re-investigation; recall before starting, think during, charge when evidence arrives.</rule>
    <rule>Charge what is epistemically load-bearing, not just what is procedurally convenient. Charge the moment a USER CORRECTION or directive lands, when a DESIGN INSIGHT or decision rationale is reached, and whenever later evidence confirms or contradicts a prior thought — not only test pass/fail. Charges attach to thoughts only: to bring a decision or finding into the evidence graph, charge the thought that states its claim, or cite the decision/finding as `evidence`. Anti-pattern: charging ONLY step-by-step implementation progress — it inflates procedural bookkeeping into the highest-magnitude, most-influential nodes while genuine insights and corrections sit uncharged, inverting the evidence signal away from epistemic value.</rule>
    <rule>`branches_from` + invalidate is destructive — charges do NOT carry forward to the new thought. Don't supersede a prior thought, or mutate its status, without intent.</rule>
  </thoughts>
  <planning>
    <rule>`assemble` the node after creating it and read the rendered output before presenting — verify the tree landed as intended.</rule>
  </planning>
</discipline>

<category name="Exploration (read)">

<tool name="search" purpose="unified BM25 + semantic search across graphs">
  <summary>
    Routes to the right graph by the `graph` param and returns results inline.
    Use instead of Grep/Glob for "where is X / what does X". Batch queries to
    cover more ground in one call.
  </summary>
  <params>
    <param name="query">single search string</param>
    <param name="queries">array of strings — PREFERRED, one call covers more</param>
    <param name="graph">code (default) | knowledge | practice | cloud | cicd | linkage | logs</param>
    <param name="mode">hybrid (default) | text (BM25 only) | vector (semantic only); also ppr/graph_reach + recent/temporal for knowledge</param>
    <param name="repo">repo name, or "all" for cross-repo</param>
    <param name="branch">branch overlay (auto-detected from current branch if omitted)</param>
    <param name="account">cloud account key — required for graph:"cloud" (omit to list available cloud graphs)</param>
    <param name="name">query_id — required for graph:"logs"</param>
    <param name="limit">max results (default 10, max 50)</param>
  </params>
  <note>Results carry a staleness indicator (e.g. "Indexed 2h ago, 3 commits behind HEAD"). If stale, re-collect.</note>
</tool>

<tool name="traverse" purpose="walk graph edges (call graph, history)">
  <summary>
    Often more useful than reading files. Discover edge types first with
    `query(mode:"stats")`, then traverse them.
  </summary>
  <params>
    <param name="start">node ID — e.g. "path/file.go:FunctionName"</param>
    <param name="graph">'' or knowledge (default) | code | cloud | cicd | practice | logs | linkage</param>
    <param name="edge_types">array, e.g. ["calls"] (case-insensitive)</param>
    <param name="direction">in | out | both (default out)</param>
    <param name="depth">max traversal depth (default 1)</param>
    <param name="repo">code graph name</param>
    <param name="include_edge_metadata">surface Weight/Confidence/Method/Evidence/LastValidated on every edge</param>
  </params>
  <note>Cross-graph traversal auto-resolves via linkage proxies — pass a knowledge node ID as `start` with graph:"code"|"cloud"|"practice".</note>
</tool>

<tool name="ast" purpose="structural search-and-replace via tree-sitter">
  <summary>
    Pattern-match (and optionally REWRITE) against parsed syntax trees in any
    indexed language. Use when the question is about CODE SHAPE, not text —
    "every defer that calls Close()", "func decls returning error", "goroutines
    inside loops". Tree-sitter sees through whitespace, comments, and token
    order; grep doesn't. Runs client-side. Prefer over grep for structural
    search and over sed/perl for uniform multi-file rewrites.
  </summary>

  <operations>
    <operation name="match">Hydrated matches: file, start/end line, named captures, plus the enclosing function/method node ID and signature from the code graph.</operation>
    <operation name="count">Same walk, skips hydrate. Returns {total, by_file, files_scanned, files_skipped, duration_ms}. Run first to size a result set.</operation>
    <operation name="replace">WRITE counterpart. Interpolate captures into a `replacement` template, splice, re-parse gate. `dry_run` defaults TRUE (preview unified diffs, no write); `dry_run:false` applies atomically.</operation>
    <operation name="explain">Parse one `snippet` into an indented node-kind tree. Debug aid; no graph touch.</operation>
    <operation name="list_node_kinds">Enumerate a language's node-kind vocabulary. Returns {count, language, node_kinds[], source}. Use when authoring a `kind` leaf.</operation>
  </operations>

  <placeholder-dsl>
    <form syntax="$X">capture a single node as X</form>
    <form syntax="$_">wildcard single node (no capture)</form>
    <form syntax="$$$X">capture a sequence of zero-or-more siblings as X</form>
    <form syntax="$$$_">wildcard sequence (no capture)</form>
  </placeholder-dsl>

  <where-tree note="optional JSON boolean filter on captures">
    <composers>all (AND) | any (OR) | not (negation)</composers>
    <leaves>kind (node-kind) | matches (regex) | equals (literal) | same_node (AST identity) | same_text (same source text) | inside_pattern (ancestor matches sub-pattern) | contains_pattern (descendant matches sub-pattern)</leaves>
    <capture-refs>"X" local capture | "$match" outermost matched node (built-in) | "$outer.X" parent scope (chain "$outer.outer." to go deeper)</capture-refs>
  </where-tree>

  <params>
    <param name="operation">match | count | replace | explain | list_node_kinds (required)</param>
    <param name="language">go | python | typescript | rust | ... (required)</param>
    <param name="pattern">single DSL pattern — exclusive with `patterns`</param>
    <param name="patterns">array of patterns for sibling-form alternation (results unioned, same `where`)</param>
    <param name="where">JSON where-tree (optional)</param>
    <param name="replacement">replacement template in the $X grammar (replace only)</param>
    <param name="dry_run">replace only; default TRUE (preview), false applies</param>
    <param name="snippet">source text (explain only)</param>
    <param name="repo">code graph name (defaults to active when one is loaded)</param>
    <param name="package_prefixes">restrict the walk to repo-relative path prefixes</param>
    <param name="include_tests">include _test files (default false; vendor/testdata/etc. always skipped)</param>
    <param name="limit">cap on match results (default 100)</param>
  </params>

  <safety op="replace">
    `dry_run` defaults TRUE — preview unified diffs + blast-radius (files_touched,
    matches_replaced), write nothing. Apply writes each file atomically (temp + rename).
    Re-parse gate: a rewrite that no longer parses is REJECTED (rejected_files), never
    written. Overlapping/nested matches in one file REFUSE that file whole (refused_files).
    Single pass — no iterate-to-fixpoint; verbatim byte-range splice (no re-indentation).
  </safety>

  <when-to-use>
    Counting "every place that does X structurally" · auditing anti-patterns at
    scale (empty error checks, naked goroutines, fmt.Errorf without %w) · migration
    prep (every callsite shape that changed) · boolean composition over structure
    (X inside A but not inside inner B). For "what is the call graph?" use
    traverse(edge_types:["calls"]) instead — that's edge data, not shape.
  </when-to-use>
</tool>

<tool name="query" purpose="generic read primitive — lookup, browse, modes">
  <summary>Node lookup, type browse, graph stats, and a set of composite modes. Pick the graph with `graph`.</summary>
  <params>
    <param name="id">fetch one node by ID (or `ids` array for bulk hydrate)</param>
    <param name="type">browse by node type (decision | finding | rule | plan | project | ticket | test_plan | agent | skill)</param>
    <param name="text">text search across knowledge</param>
    <param name="graph">knowledge (default) | code | cloud | cicd | practice | linkage | logs | all</param>
    <param name="mode">hybrid | text | stats | examine | file_symbols | modules | personality | influence | tensions | blind_spots | evolution | summary | simulate | timeline | charges | clusters | graph_reach | recent | topology | pivot | correlations | explain | resolver | lineage | evidence | plan_tree | metadata_stats</param>
    <param name="include_edges">include edges in node results</param>
    <param name="format">text (default) | json</param>
  </params>
  <modes>
    <mode name="examine">deep inspect — ancestry, edges, version history. Use when debugging why a node has an unexpected status or is missing from a plan tree.</mode>
    <mode name="stats">all node types + edge types for a graph (discovery)</mode>
    <mode name="plan_tree">walk plan/project/ticket hierarchy</mode>
    <mode name="lineage">trace provenance chains</mode>
    <mode name="evidence">what shaped a decision (follows informed-by)</mode>
    <mode name="topology">run a topology analyzer (`algorithm`, e.g. pagerank, scc) over a graph</mode>
    <mode name="metadata_stats">per-graph cardinality histogram for every metadata key</mode>
    <mode name="recent">recency-ordered browse (UpdatedAt half-life). Empty `text` → pure recency; add `types` to scope (e.g. a lightweight active-work view).</mode>
    <mode name="personality | tensions | blind_spots | summary">reflection over the thought graph</mode>
  </modes>
  <example caption="deep inspect a node">query({ "mode": "examine", "id": "node_id" })</example>
  <example caption="browse decisions">query({ "type": "decision" })</example>
  <example caption="discover the vocabulary">query({ "mode": "stats", "graph": "code" })</example>
  <example caption="walk a plan tree">query({ "mode": "plan_tree", "id": "plan_id" })</example>
  <example caption="recent work items by recency">query({ "mode": "recent", "types": ["project", "ticket", "plan", "phase", "step", "question"] })</example>
</tool>

</category>

<category name="Knowledge & reasoning (write)">

<tool name="thoughts" purpose="persistent reasoning graph — think / charge / recall / trace / propagate">
  <summary>
    Externalize reasoning so it survives sessions and compactions. Hypotheses are
    nodes; charges add evidence; propagation derives consensus + significance.
    Prefer this over file-based memory — thoughts are searchable, linkable, chargeable.
  </summary>
  <cycle>recall (start here) → think → (do work) → charge → recall again to confirm it landed</cycle>
  <operations>
    <operation name="think">
      record a hypothesis/observation/plan.
      params: content (required), summary (REQUIRED — deliberate one-line searchable
      summary, NOT auto-derived), session, links, status (hypothesized | validated | invalidated)
    </operation>
    <operation name="charge">
      add evidence. params: thought (id), polarity (positive | negative), weight (1-10), reasoning — all required.
      polarity = positive when the evidence SUPPORTS the thought's claim, negative when it CONTRADICTS it —
      this is about the claim's truth, never good-vs-bad news; sentiment about the subject lives in reasoning text.
      Cite the thought/finding node IDs the charge drew on via the evidence param — a cited thought records
      an evidenced-by edge that feeds cross-cluster trust attribution.
    </operation>
    <operation name="recall">
      search thoughts. params: query (semantic text), session, status, valence_min/max,
      magnitude_min, consistency_max, connected_to, time_start/end, mode (search | timeline | charges | graph | clusters), limit
    </operation>
    <operation name="trace">follow reasoning chains. params: thought (id), direction (forward | backward | both), depth, include_charges, include_artifacts</operation>
    <operation name="propagate">manually run DeGroot propagation for immediate convergence after a batch of charges</operation>
    <operation name="adjacency">bulk graph adjacency read (cluster detection). params: scope ('all' | 'all_types'), thought_ids (optional subset projection)</operation>
    <operation name="charges_for">bulk per-thought charge fetch. params: thought_ids (required). Returns {charges_by_thought: {tid: [charge_node, ...]}}</operation>
  </operations>
  <example op="recall" caption="always start work here">
    thoughts({ "operation": "recall", "query": "edge resolution callers" })
  </example>
  <example op="think">
    thoughts({ "operation": "think",
               "content": "Edge IDs use pkg.Symbol but graph uses filepath:Symbol — need a resolution layer",
               "summary": "Edge/node ID mismatch: tree-sitter pkg.Symbol vs graph filepath:Symbol",
               "session": "fix-traversal" })
  </example>
  <example op="charge" caption="when evidence arrives — polarity tracks the CLAIM, not the news">
    thoughts({ "operation": "charge", "thought": "t_abc", "polarity": "positive", "weight": 8,
               "reasoning": "Tests pass — 71 callers resolve after adding resolveEdges; the evidence supports this thought's claim that the resolution layer fixes traversal" })
  </example>
  <when-to-use>Before implementing (planned approach) · debugging (hypothesis, then the broken→fixed transition) · design trade-offs · surprising behavior · after testing (charge with results).</when-to-use>
  <when-to-charge>Charge beyond test results: the moment a USER CORRECTION or directive lands, when a DESIGN INSIGHT or decision rationale is reached, and when later evidence confirms/contradicts a prior thought. Decisions/findings are not directly chargeable (charges are thought-only) — charge the thought that states the claim, or cite the node as `evidence`. Do NOT charge only step-by-step progress: that buries load-bearing insights and corrections under procedural bookkeeping and inverts the evidence signal.</when-to-charge>
  <feedback-loop-charge>The HIGHEST-grade charge is the feedback loop: when the work is verified in the REAL WORLD — shipped and it works, the symptom is gone, the user confirmed, the prediction held or failed — charge the ORIGINATING hypothesis with that outcome (positive if reality supported the claim, negative if it contradicted it). Green unit tests are not this; reality is. It is the charge most often skipped, because by the time the result lands the session has moved on — but it is the one that turns an asserted hypothesis into validated knowledge. Closing this loop is exactly what the retro phase exists to do; do it whenever an outcome lands, not only at retro.</feedback-loop-charge>
</tool>

</category>
