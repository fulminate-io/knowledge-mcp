<guidance>
  <principle name="tools-first">
    Default to `search`, `file_symbols`, `traverse`, and `ast` for code work.
    Grep / Glob / Read are the fallback, not the starting point — the graph is
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
</guidance>

<discipline note="cross-cutting rules distilled from the agent + skill tool guidance">
  <exploration>
    <rule>A `search` / `ast` miss is NOT proof of absence — never conclude "greenfield" or "genuinely new" from one query. Run BOTH a concept `search` and a structural `ast` pass before deciding nothing exists.</rule>
    <rule>`ast` is the most under-used reuse tool. When the question is "is there code that DOES this" (shape), reach for `ast`; `search` answers "is there code NAMED this" (text). Search-only reuse checks are how re-implementations slip in.</rule>
    <rule>Verify before citing — docstrings, comments, and READMEs rot; only the file + actual source are authoritative. If a doc says X lives in `y.go`, open `y.go` and confirm X exists before repeating it in a finding, decision, or ticket.</rule>
    <rule>Read the top candidates with `file_symbols` / source before concluding — don't trust summaries.</rule>
    <rule>Check the staleness line on `search` results before trusting them. A stale index yields wrong conclusions; re-`collect` if it's far behind HEAD.</rule>
  </exploration>
  <writes>
    <rule>`search` / `recall` BEFORE creating — decisions, findings, and research don't exist in isolation. Check for an existing node first to avoid duplicates.</rule>
    <rule>Name for retrieval. The node `name` and the first sentence of its `description` are what BM25 matches later; vague titles are search-invisible. State the concept/concern in plain terms.</rule>
    <rule>A decision needs `rationale` AND `alternatives`. "We decided X" with no why isn't a decision record — and if there were no alternatives, was it a decision?</rule>
  </writes>
  <thoughts>
    <rule>recall → think → charge on every task — not optional. Past thoughts carry debugging notes and design rationale that save re-investigation; recall before starting, think during, charge when evidence arrives.</rule>
    <rule>`branches_from` + invalidate is destructive — charges do NOT carry forward to the new thought. Don't supersede a prior thought, or mutate its status, without intent.</rule>
  </thoughts>
  <planning>
    <rule>`assemble` the node after creating it and read the rendered output before presenting — verify the tree landed as intended.</rule>
  </planning>
</discipline>

---

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
    <param name="graph">code (default) | knowledge | practice</param>
    <param name="repo">repo name, or "all" for cross-repo</param>
    <param name="branch">branch overlay (auto-detected from current branch if omitted)</param>
    <param name="language">required for graph:"practice" (e.g. "go")</param>
    <param name="limit">max results</param>
  </params>
  <example caption="code search, batched">
    search({ "queries": ["auth middleware", "JWT validation", "session handler"], "limit": 10 })
  </example>
  <example caption="knowledge nodes (decisions, findings, rules)">
    search({ "query": "authentication", "graph": "knowledge" })
  </example>
  <example caption="per-language best practices">
    search({ "query": "concurrency patterns", "graph": "practice", "language": "go" })
  </example>
  <note>Results carry a staleness indicator (e.g. "Indexed 2h ago, 3 commits behind HEAD"). If stale, re-collect.</note>
</tool>

<tool name="file_symbols" purpose="list every symbol in a file">
  <summary>Cheaper than reading the whole file — call before diving into specific functions or editing.</summary>
  <example>file_symbols({ "file_path": "path/to/file.go" })</example>
</tool>

<tool name="traverse" purpose="walk graph edges (call graph, history)">
  <summary>
    Often more useful than reading files. Discover edge types first with
    `query(mode:"stats")`, then traverse them.
  </summary>
  <params>
    <param name="start">node ID — e.g. "path/file.go:FunctionName"</param>
    <param name="graph">code | knowledge | cloud | practice | versions</param>
    <param name="edge_types">array, e.g. ["calls"] (case-insensitive)</param>
    <param name="direction">in | out | both</param>
    <param name="include_source">attach source text to results</param>
    <param name="repo">code graph name</param>
  </params>
  <example caption="callers + callees of a function">
    traverse({ "start": "path/file.go:FunctionName", "graph": "code",
               "edge_types": ["calls"], "direction": "both", "include_source": true })
  </example>
  <example caption="who calls this? (callers only)">
    traverse({ "start": "path/file.go:FunctionName", "graph": "code",
               "edge_types": ["calls"], "direction": "in" })
  </example>
  <example caption="version history (changeset chain)">
    traverse({ "start": "node_id", "graph": "versions",
               "edge_types": ["changed_to"], "direction": "out" })
  </example>
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

  <example op="count" caption="size before hydrating">
    ast({ "operation": "count", "language": "go", "pattern": "defer $X.Close()" })
  </example>
  <example op="match" caption="error-returning func decls whose name starts with load">
    ast({ "operation": "match", "language": "go",
          "pattern": "func $NAME($$$ARGS) error { $$$BODY }",
          "where": { "matches": { "of": "NAME", "regex": "^load" } } })
  </example>
  <example op="match" caption="wildcard + $match gate — every method declaration">
    ast({ "operation": "match", "language": "go", "pattern": "$_",
          "where": { "kind": { "of": "$match", "is": "method_declaration" } } })
  </example>
  <example op="replace" caption="preview rewriting every defer Close() (writes nothing)">
    ast({ "operation": "replace", "language": "go",
          "pattern": "defer $X.Close()", "replacement": "defer safeClose($X)",
          "dry_run": true })
    <!-- returns diffs{relpath -> unified diff} + {files_touched, matches_replaced}.
         set dry_run:false to apply. -->
  </example>

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
    <param name="id">fetch one node by ID</param>
    <param name="type">browse by node type (decision | finding | rule | plan | project | ticket | test_plan | agent | skill)</param>
    <param name="text">text search across knowledge</param>
    <param name="mode">examine | stats | entropy | plan_tree | lineage | evidence | personality | tensions | blind_spots | summary</param>
    <param name="show_history">append version history to a node fetch</param>
  </params>
  <modes>
    <mode name="examine">deep inspect — ancestry, edges, version history. Use when debugging why a node has an unexpected status or is missing from what_next.</mode>
    <mode name="stats">all node types + edge types for a graph (discovery)</mode>
    <mode name="entropy">most-volatile nodes (by patch count); or per-node with `id`</mode>
    <mode name="plan_tree">walk plan/project/ticket hierarchy</mode>
    <mode name="lineage">trace provenance chains</mode>
    <mode name="evidence">what shaped a decision (follows informed-by)</mode>
    <mode name="personality | tensions | blind_spots | summary">reflection over the thought graph</mode>
  </modes>
  <example caption="deep inspect a node">query({ "mode": "examine", "id": "node_id" })</example>
  <example caption="browse decisions">query({ "type": "decision" })</example>
  <example caption="discover the vocabulary">query({ "mode": "stats", "graph": "code" })</example>
  <example caption="walk a plan tree">query({ "mode": "plan_tree", "id": "plan_id" })</example>
</tool>

</category>

<category name="Knowledge & reasoning (write)">

<tool name="mutate" purpose="generic write primitive — create / update / link / delete">
  <summary>Create and update nodes, link edges, across any graph (select with `graph`). Cross-graph links auto-create proxies — never duplicate a node into a second graph.</summary>
  <operations>
    <operation name="create">new node (finding, rule, practice node, ...)</operation>
    <operation name="update">change fields / status on an existing node</operation>
    <operation name="link">edge between two nodes (informed-by | supports | contradicts | relates-to | uses | ...)</operation>
    <operation name="delete">tombstone a node</operation>
  </operations>
  <example caption="create a finding (summary REQUIRED on embed-only types) — returns the new ID">
    mutate({ "operation": "create", "type": "finding",
             "name": "Large functions lose identity when split",
             "description": "Functions over maxChunkTokens become anonymous Block chunks, losing name + symbol registration",
             "summary": "Oversized functions split into anonymous chunks lose their symbol identity",
             "evidence": "indexer/reindex.go — buildGraphWithOptions splits at 500 tokens" })
  </example>
  <example caption="update status">
    mutate({ "operation": "update", "id": "ticket_abc", "status": "in_progress" })
  </example>
  <example caption="link nodes">
    mutate({ "operation": "link", "from": "finding_id", "to": "decision_id", "relationship": "informed-by" })
  </example>
  <example caption="create in a practice graph">
    mutate({ "operation": "create", "type": "finding", "name": "Use errgroup for concurrent goroutines",
             "description": "...", "summary": "Prefer errgroup over raw WaitGroup for cancelable fan-out",
             "graph": "practice", "language": "go" })
  </example>
  <note>`status` is an intentionally open string — use the backend's workflow state names (e.g. Linear team states) when syncing tickets.</note>
</tool>

<tool name="record_decision" purpose="record an architectural decision with rationale">
  <params>
    <param name="name">short title</param>
    <param name="choice">what was decided</param>
    <param name="rationale">why</param>
    <param name="alternatives">what was rejected</param>
  </params>
  <example>
    record_decision({ "name": "Use two-pass edge resolution",
                      "choice": "Resolve edges in a second pass after all nodes are added",
                      "rationale": "Edges reference symbols defined in files processed later",
                      "alternatives": "Single-pass with deferred resolution, or post-hoc fixup" })
  </example>
</tool>

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
      add evidence. params: thought (id), polarity (positive | negative), weight (1-10), reasoning — all required
    </operation>
    <operation name="recall">
      search thoughts. params: query (semantic text), session, status, valence_min/max,
      magnitude_min, consistency_max, connected_to, time_start/end, mode (search | timeline | charges | graph | clusters), limit
    </operation>
    <operation name="trace">follow reasoning chains. params: thought (id), direction (forward | backward | both), depth, include_charges, include_artifacts</operation>
    <operation name="propagate">manually run DeGroot propagation for immediate convergence after a batch of charges</operation>
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
  <example op="charge" caption="when evidence arrives">
    thoughts({ "operation": "charge", "thought": "t_abc", "polarity": "positive", "weight": 8,
               "reasoning": "Tests pass — 71 callers found for store.Open after adding resolveEdges" })
  </example>
  <when-to-use>Before implementing (planned approach) · debugging (hypothesis, then the broken→fixed transition) · design trade-offs · surprising behavior · after testing (charge with results).</when-to-use>
</tool>

</category>

<category name="Planning & implementation">

<tool name="create_project | create_ticket | create_plan | create_research | create_test_plan"
      purpose="build the work hierarchy">
  <hierarchy>project → ticket → plan → phase → step → criterion</hierarchy>
  <statuses>
    projects: active | completed | archived · tickets: open | in_progress | closed
    (ticket sync to an external tracker uses that tracker's workflow state names)
  </statuses>
  <required>
    Every create_* call requires a `summary` — a deliberate one-line searchable
    string (≤500 chars) that makes the node findable. Phases, steps, and research
    questions each require their own `summary` too.
    `create_ticket` and `create_plan` ADDITIONALLY require exactly ONE of:
    `pattern_ids` (catalog pattern node IDs the work extends) · `proposed_patterns`
    (new pattern sketches) · `no_patterns_reason` (audited escape hatch — e.g. a
    trivial doc edit). Supplying none of the three is an error.
  </required>
  <example caption="project → ticket — project returns {id,name}; ticket returns {id,name,warnings}">
    create_project({ "name": "Auth Refactor", "description": "Modernize the auth system",
                     "summary": "Modernize the authentication subsystem" })
    create_ticket({ "name": "Migrate to OAuth2", "project_id": "proj_abc",
                    "summary": "Replace legacy session auth with OAuth2",
                    "priority": "high", "labels": "auth,security", "external_id": "GH-42",
                    "no_patterns_reason": "greenfield auth flow, no catalog pattern applies" })
  </example>
  <example caption="plan with phases → steps → criteria — returns {id, node_ids:[...]}">
    create_plan({
      "name": "Fix edge resolution",
      "goal": "Callers/callees traversal returns accurate results",
      "summary": "Add an edge ID resolution layer between tree-sitter output and storage",
      "no_patterns_reason": "internal indexer fix",
      "ticket_id": "ticket_abc",
      "phases": [ {
        "name": "Phase 1: Edge Resolution",
        "overview": "Build resolveEdges and integrate into graph building",
        "summary": "Resolve pkg.Symbol edge IDs to filepath:Symbol node IDs",
        "steps": [ {
          "name": "Create resolveEdges",
          "description": "Map pkg.Symbol -> filepath:Symbol via the symbol index",
          "summary": "resolveEdges maps tree-sitter edge IDs to graph node IDs",
          "file_paths": "indexer/edges.go",
          "criteria": [ { "description": "Tests pass", "command": "go test ./indexer/ -run TestResolveEdges", "type": "automated" } ]
        } ]
      } ]
    })
  </example>
  <example caption="research before planning — returns {id, question_ids:[...]}">
    create_research({ "name": "How edge IDs are generated",
                      "goal": "Trace the pipeline from tree-sitter to graph",
                      "summary": "Where edge FromID/ToID diverge from graph node IDs",
                      "questions": [ { "question": "What format does tree-sitter use for edge IDs?",
                                       "summary": "tree-sitter edge ID format",
                                       "context": "chunker.go emitDeclarationEdges" } ] })
  </example>
  <example caption="test plan (template; assemble new_run creates test_run nodes) — returns {id, step_ids:[...]}">
    create_test_plan({ "name": "Auth smoke tests", "goal": "Endpoints handle valid + invalid creds",
                       "summary": "Smoke tests for login/logout/token-refresh",
                       "steps": [ { "name": "Login valid", "description": "POST /auth/login returns 200 with JWT",
                                    "summary": "valid login returns 200 + JWT",
                                    "criteria": [ { "description": "Returns 200", "type": "automated" } ] } ] })
  </example>
</tool>

<tool name="what_next" purpose="find the next actionable steps">
  <example>what_next({ "project_id": "proj_abc" })</example>
</tool>

<tool name="assemble" purpose="fully assembled view of a structured node + related context">
  <summary>Pulls in linked context automatically (decisions, research, phases, runs). Call with no args for auto-recovery after a compaction.</summary>
  <example caption="plan with linked decisions + research">assemble({ "id": "plan_id" })</example>
  <example caption="ticket with plans/research/decisions">assemble({ "id": "ticket_abc" })</example>
  <example caption="start a test run (creates test_run nodes per step)">assemble({ "id": "test_plan_id", "new_run": true })</example>
  <example caption="auto-recovery after compaction">assemble()</example>
</tool>

</category>

<category name="Operations">

<tool name="collect" purpose="index / reindex a source into a graph">
  <summary>
    Discovers, chunks, and writes nodes/edges for a source. This is how you
    (re)index code — there is no `manage(reindex)`. Incremental: unchanged nodes
    carry summaries/vectors forward; only changed files re-summarize.
  </summary>
  <params>
    <param name="type">code | cloud | cicd | web | pdf | logs</param>
    <param name="id">source identifier — for code, an ABSOLUTE repo path (relative paths are rejected)</param>
  </params>
  <example caption="index a code repo">collect({ "type": "code", "id": "/absolute/path/to/repo" })</example>
  <note>First pass is 30s–2min for typical repos. Ask the user before reindexing; don't auto-trigger.</note>
</tool>

<tool name="manage" purpose="server + graph operations">
  <operations>
    <operation name="status">pipeline metrics (summary/embed queued/running/succeeded/failed) per graph</operation>
    <operation name="list_branches | delete_branch">branch overlays — params: name (repo), branch</operation>
    <operation name="prune">hard-delete tombstoned nodes — requires explicit `graph`; optional `before` ("30d" or RFC3339)</operation>
    <operation name="rebuild_hnsw">rebuild a graph's vector index (practice/cloud need name)</operation>
    <operation name="clear_llm_failures">clear summary/embed failure markers (optional graph/name scope)</operation>
    <operation name="link">run image/Helm/Dockerfile linkers (code↔cloud edges)</operation>
    <operation name="configure_log_backend | list_log_backends | list_logs | discard_logs">log graph management</operation>
  </operations>
  <example caption="server status">manage({ "operation": "status" })</example>
  <example caption="list branch overlays">manage({ "operation": "list_branches", "name": "myrepo" })</example>
  <note>Reindexing is NOT a manage op — use `collect`. The pipeline picks up changed nodes automatically.</note>
</tool>

<tool name="help" purpose="reference docs for any tool / type / workflow">
  <example>help()                              // overview of all tools</example>
  <example>help({ "topic": "ast" })            // deep-dive a tool</example>
  <example>help({ "topic": "node_types" })     // vocabulary: node_types | edge_types | statuses</example>
  <example>help({ "topic": "workflows" })      // common multi-tool patterns</example>
</tool>

</category>

<category name="Practice graphs">
  Per-language graphs of best practices, patterns, and conventions — separate from
  the main knowledge graph. Reach them via the `graph:"practice"` + `language` params
  on `search`, `query`, and `mutate` (see those tools above).
  <example caption="search practices">search({ "query": "error handling", "graph": "practice", "language": "go" })</example>
  <example caption="browse a language's graph">query({ "graph": "practice", "language": "go" })</example>
</category>