// SPDX-License-Identifier: Apache-2.0

package tools

const helpPatterns = `# Pattern Catalog

The pattern catalog lives in two roles across multiple practice graphs (the practice graph partitions by ` + "`language`" + ` slug — overloaded here for non-language slugs):

**Architecture patterns (PRESCRIPTIVE — wired via ` + "`pattern_ids`" + ` + ` + "`uses`" + ` edges):**

- ` + "`language=\"knowledge-architecture\"`" + ` — concrete patterns instantiated in this codebase. 11 entries today (see survey).
- ` + "`language=\"design-patterns\"`" + ` — codebase-agnostic library of generic templates. Earned bottom-up: only patterns that have ≥1 concrete instance in a project graph.

**Language patterns (DEFENSIVE — wired via ` + "`language_patterns`" + ` + ` + "`audits`" + ` edges):**

- ` + "`language=\"go\"`" + `, ` + "`language=\"python\"`" + `, ` + "`language=\"typescript\"`" + `, etc. — language-specific anti-patterns and best-practices, identified by ` + "`type=\"finding\"`" + ` + ` + "`metadata.dsl_pattern`" + ` set. The Go corpus has 19+ entries as of 2026-05 (e.g., http.DefaultClient, sync.Map, exec.CommandContext without LookPath). The scanner worker enumerates these via ` + "`query({graph:\"practice\", language:\"go\", type:\"finding\", meta:{dsl_pattern:\"*\"}, format:\"json\", fields:[...]})`" + `.

The two roles are independent on tickets/plans — a ticket can carry any combination of ` + "`pattern_ids`" + ` (architecture) and ` + "`language_patterns`" + ` (language). The planner builds to architecture; the reviewer audits the plan against language smells.

## Querying the catalog

  query({ "graph": "practice", "language": "knowledge-architecture" })
  query({ "graph": "practice", "language": "knowledge-architecture", "text": "registry" })
  query({ "id": "<pattern_id>", "graph": "practice", "language": "knowledge-architecture" })
  assemble({ "id": "<pattern_id>" })

` + "`assemble`" + ` walks the child tree and renders ` + "`## Applies when`" + ` / ` + "`## Avoid when`" + ` / ` + "`## Examples`" + ` / ` + "`## References`" + ` from the use_case, example, and reference nodes linked off the parent.

## Pattern node fields

Patterns are parent nodes with typed child nodes linked via edges — not a single monolithic markdown blob. The parent carries naming and prose; children carry the situational / illustrative material that ` + "`assemble`" + ` renders into sections.

  pattern (parent)       Name=slug (e.g. ` + "`fan-out-fan-in`" + `), Summary=one-liner, Description=long-form prose describing what the pattern is. Content unused on parents.
  use_case (child)       Linked from parent via ` + "`applies-when`" + ` edge (positive: reach for this when …) or ` + "`avoid-when`" + ` edge (negative / anti-pattern: do NOT use when …). Name=short title, Description=the situation.
  example (child)        Linked from parent via ` + "`contains`" + ` edge. Content=code snippet verbatim. Metadata.language=fence tag (` + "`go`" + `, ` + "`python`" + `, …). Metadata.attribution=source (e.g. ` + "`MIT — kat-co/concurrency-in-go-src`" + `).
  reference (child)      Linked from parent via ` + "`references`" + ` edge. Metadata.url / .title / .book / .page / .line — any subset; ` + "`assemble`" + ` formats whatever is present.

## Corpus checks

A finding in a CHECKS graph becomes an EXECUTABLE CHECK when it carries check_type metadata. One node carries both halves: the prose in the body fields, the machine check in metadata. A check is a finding — there is no separate node type.

WHERE CHECKS LIVE: ONE graph, addressed as graph:"checks" with no language or name — it is a singleton. Language is a LABEL on each node (the ` + "`language`" + ` contract key), not a graph selector, so a scan for one language narrows within the single graph. NOT the practice graph. Practice graphs hold prose guidance and model entries an LLM reads; checks graphs hold executable assertions and the fixture example nodes that validate them. The separation is structural, and it is what keeps fixture code — written deliberately to be wrong so a check has something to fire on — out of the ranked corpus that answers questions about good practice. CHECK nodes ARE indexed and findable by intent through ranked and semantic search; FIXTURE example nodes are excluded from every ranked corpus by the server's per-graph node-type allow-list, which is what keeps deliberately-wrong code from ever answering a question.

THE ADMISSION RULE, in the ticket's own words: an admitted check must FIRE on its linked bad example node and stay SILENT on the good one. No fixture, no admission.

The eight contract keys:

  check_type           the execution kind — one of ast_pattern, graph_assertion, topology_threshold, flow_model
  severity             the finding severity the check emits at (info / notice / warning / critical)
  language             the tree-sitter language slug the check is written against
  dsl_pattern          the ast pattern DSL body; required for ast_pattern
  check_where          optional ast where-tree, as JSON text
  check_fixture_bad    node id of the example the check MUST match
  check_fixture_good   node id of the example the check MUST NOT match
  llm_only             "true" on prose that has no deterministic expression; exclusive with the CHECK-BODY keys (check_type, dsl_pattern, check_where, check_fixture_bad, check_fixture_good). It still REQUIRES language: every corpus read is language-scoped, so an unlabeled llm_only node is returned to nobody and the needs-judgment lane silently empties.

Six consumer rules:

  a. One node carries both halves — prose in the body fields, the machine check in metadata. Do not split a check across two nodes.
  b. Fixtures are bound by METADATA (check_fixture_bad / check_fixture_good, node ids in the check's OWN checks graph — checks and their fixtures live together, so the binding never crosses a graph boundary). The applies-when / avoid-when edges are display-only: no executor consults them, and a fixture is reachable only through the metadata keys above. Their direction is fixed — check --avoid-when--> the check_fixture_bad node (the shape the check fires on is the one to avoid), check --applies-when--> the check_fixture_good node (the conforming near-miss) — so two authors cannot produce a graph where half the checks point the opposite way. manage_checks(operation:"create") draws both.
  c. NOTHING about a passed validation is persisted — no validated_at, no digest, no marker to read. A consumer that must not execute an unvalidated check re-validates immediately before executing, by calling corpus.ParseCheck + corpus.ValidateFixtures itself.
  d. THE BOUNDARY: a node with no check_type is not a check, and this contract constrains nothing about its other metadata. Catalog content an executor consults (source/sink tables, symbol lists, threshold tables) is data, carries no check_type, and is never gated. There is no fixture-exempt check type — a shape that cannot be silent on a good example is data, not a check.
  e. VALIDATION'S RUNTIME REQUIREMENT: the API is in-memory (fixture id + content, no repo path and no collected graph), but the executor MATERIALIZES each fixture into a temp directory and runs the real ast walk over it, because ast exports no in-memory matcher. So every caller needs a writable temp directory, pays roughly 32-46ms per check (two one-file walks; 48-69ms when a where-tree triggers the calibration probe), and sees one discovery WARN line per fixture.
  f. WHAT ParseCheck RETURNS: Check is the machine half only — identity is Check.ID, and the prose half lives on the source node, which consumers must retain. isCheck true means EXECUTABLE CHECK; an accepted llm_only node returns isCheck FALSE with Check.LLMOnly true, so a consumer's skip branch must test LLMOnly BEFORE skipping, or the needs-LLM-judgment lane goes invisible and the honest machine-verified / needs-judgment split cannot be produced.

WHY THE FIXTURE GATE EXISTS. A detector written from one incident's text matches that incident and nothing else: an alert's syntax is a fingerprint, not a population — a shape-only check narrows nothing, because the same call shape occurs on safe and unsafe arguments alike. The silent-on-the-good-example half of the gate is that lesson's mechanical form; the fire-on-the-bad-example half only proves the check is not inert.

## Corpus models

Source / sink / sanitizer model entries are query-time corpus DATA the flow engine consults — NOT executable checks. A model is a finding in a practice graph carrying model_kind; presence of that key is what marks the node a model.

THE BOUNDARY WITH THE CHECK CONTRACT. A model MUST NOT carry check_type, check_fixture_bad, check_fixture_good or llm_only. A node with no check_type is not a check, so the fixture admission gate never runs over a model. The separation is deliberate rather than an exemption: a bare SINK model cannot satisfy a fire-on-bad / silent-on-good gate, because a call shape matches the same callee on safe and unsafe arguments alike. A shape that cannot be silent on a good example is data, not a check.

EVERY MODEL CARRIES BOTH HALVES. The prose half lives in the body fields and is written for a reader: what makes the input untrusted or the sink dangerous, what a reviewer looks for, what the safe handling is, and the falsifiers — the states in which the entry's claim stops being true. The machine half lives in metadata: a call or field SHAPE the executor compiles and matches. An entry with prose and no shape is not a model; an entry with a shape and no prose defeats the corpus's purpose.

The model vocabulary:

  model_kind         source | sink | sanitizer | note — the discriminant
  model_slug         stable machine identifier, matched by a consumer needing ONE entry
  model_class        the vulnerability or evaluation class (command-execution, path-traversal, …)
  model_clears_taint sanitizer semantics; see the three-way predicate below
  model_symbols      comma-separated, import-path-qualified symbols the entry covers
  model_shape        the ast pattern DSL body, in SOURCE spelling
  model_where        optional ast where-tree, as JSON text
  model_severity     the severity a finding is emitted at (info / notice / warning / critical)

language is shared with the check contract above and carries the same tree-sitter language slug — the constant value, never a display name.

MODELS DESCRIBE CALL SHAPES, NEVER GRAPH ENDPOINTS. External symbols have no nodes in the code graph, so a sink model is not an edge endpoint. Matching is intra-declaration and structural: the declaration is re-parsed and the shape matched inside it. Most entries discriminate on ARGUMENT POSITION, on an argument's node kind, or on an argument literal's text, so whatever supplies the terminal hop must carry per-argument syntactic structure rather than merely a callee spelling.

### model_clears_taint: the three-way predicate

model_kind "sanitizer" is not sufficient to answer what clears taint, and neither is excluding the entries that clear nothing. Excluding none is necessary but NOT sufficient: full is the only unconditional clear; none means not a sanitizer at all; every other value is a conditional claim whose condition must hold first.

  full           the value is TRANSFORMED into something that cannot carry the syntax. CLASS-SCOPED: clean for the injection classes a catalog models, silent on arithmetic and resource sinks. It attaches to the transformation's RESULT, not to the original string.
  lossy          transformed and confined, but not for every input
  context        clears only for a named context (URL escaping does nothing for shell or SQL)
  guard          a BOOLEAN PREDICATE: the value is UNCHANGED and clearing exists only on the branch where it held
  confined-sink  the OPERATION is confined rather than the value; the value stays tainted for every other sink. SINK-SCOPED — a different axis from full's class-scoping.
  structural     the clearing is a property of the POSITION the value occupies (a SQL bind parameter; a distinct non-zero argv element)
  none           DOES NOT CLEAR TAINT — the deliberate negative entry, and the only value that inverts the entry's meaning

So a consumer implements: full means an unconditional clear; none means not a sanitizer; anything else is conditional and the condition must be established before the clear applies. Reading every non-none value as clean suppresses true positives.

A CLEARING VALUE IS A LABEL AND DOES NOT PARTITION BY MECHANISM. Entries sharing one mechanism can span several clearing values, so a class-level rule bound to a clearing VALUE misses members by construction. Key on the mechanism the entry names.

THE SHARPEST INSTANCE, and the most exploitable trap in the Go catalog: the clearing performed by filepath.IsLocal, by an anchored allowlist regexp, and by filepath.Base is LEXICAL — each inspects the path STRING and never touches the filesystem — so all three return a clean verdict on a name that is a symlink out of the directory, and os.Root / os.OpenRoot ranks above every one of them for filesystem access. Those three entries span TWO clearing values (guard, guard and lossy), so a reader keying the trap off the word guard misses filepath.Base, which is the commonest of the three in real code. Say lexical, not guard.

### Resolving models for a language

Models for language L live in the practice graph whose instance name is that language's slug. The read is a type-scoped browse selecting finding nodes, DRAINED TO COMPLETION, keeping the nodes that carry model_kind. The drain is non-negotiable — an undrained read silently truncates the corpus and the scan then runs vacuously. Note that a browse supplying no limit is stamped with the LLM-facing default, so a loader in that shape reads only the first handful of models and looks like it worked. Where the model_kind filter is applied is open: a server-side metadata predicate or an in-memory filter over the drained findings both satisfy it.

Query-time is load-bearing: models are read when a scan runs and are never baked into collected flow facts, so adding a framework's sinks makes every already-collected graph answer immediately with no re-collect.

A CONSUMER MUST NOT ASSUME THE CORPUS IS GLOBALLY CONSISTENT. It is authored content. A sanitizer entry and a sink entry can both match one call site while neither is wrong in isolation, and some pairs are co-extensive by construction with the residual hazard stated on the entry. Report such a conflict naming both entries rather than resolving it silently — suppressing is the dangerous direction, because it turns a critical sink into silence.

## Authoring a new pattern

No ` + "`create_pattern`" + ` batch handler yet (known followup) — author with a sequence of mutate calls.

Step 1 — create the pattern parent:

  mutate({
    "operation": "create", "type": "pattern",
    "graph": "practice", "language": "design-patterns",
    "name": "fan-out-fan-in",
    "summary": "Split work across N goroutines, merge results on a single channel.",
    "description": "Producer dispatches items to a pool of worker goroutines; a merger collects their outputs into one downstream channel."
  })

Step 2 — create each use_case and link with ` + "`applies-when`" + ` (positive) or ` + "`avoid-when`" + ` (negative):

  mutate({
    "operation": "create", "type": "use_case",
    "graph": "practice", "language": "design-patterns",
    "name": "parallelizable-work",
    "description": "The same operation applies to many items independently; order of completion does not matter."
  })
  mutate({
    "operation": "link", "from": "<pattern_id>", "to": "<use_case_id>",
    "relationship": "applies-when",
    "graph": "practice", "language": "design-patterns"
  })

  mutate({
    "operation": "create", "type": "use_case",
    "graph": "practice", "language": "design-patterns",
    "name": "strict-ordering-required",
    "description": "Downstream consumers require items in submission order — fan-out breaks that contract."
  })
  mutate({
    "operation": "link", "from": "<pattern_id>", "to": "<use_case_id>",
    "relationship": "avoid-when",
    "graph": "practice", "language": "design-patterns"
  })

Step 3 — create each example with language + attribution metadata, link via ` + "`contains`" + `:

  mutate({
    "operation": "create", "type": "example",
    "graph": "practice", "language": "design-patterns",
    "name": "fan-out-fan-in-basic",
    "content": "<code snippet verbatim>",
    "description": "Basic fan-out goroutine pool with channel merge.",
    "metadata": { "language": "go", "attribution": "MIT — kat-co/concurrency-in-go-src" }
  })
  mutate({
    "operation": "link", "from": "<pattern_id>", "to": "<example_id>",
    "relationship": "contains",
    "graph": "practice", "language": "design-patterns"
  })

Step 4 — create each reference (book, blog, repo) with citation metadata, link via ` + "`references`" + `:

  mutate({
    "operation": "create", "type": "reference",
    "graph": "practice", "language": "design-patterns",
    "name": "Concurrency in Go — Cox-Buday 2017",
    "metadata": { "book": "Concurrency in Go (O'Reilly, ISBN 9781491941195)", "page": "108", "url": "https://www.oreilly.com/library/view/concurrency-in-go/9781491941294/" }
  })
  mutate({
    "operation": "link", "from": "<pattern_id>", "to": "<reference_id>",
    "relationship": "references",
    "graph": "practice", "language": "design-patterns"
  })

## Cross-graph link to a library entry

When a project pattern is an instantiation of a generic library pattern, link them with the ` + "`instantiates`" + ` edge:

  mutate({
    "operation": "link",
    "from": "<project_pattern_id>",
    "to": "<library_pattern_id>",
    "relationship": "instantiates"
  })

## Promotion / staleness

- Project patterns gain status="emerging" on first use, "validated" on second.
- Stale-pattern invalidation (T3): orphan project patterns whose exemplars all disappear are auto-deleted; library-linked patterns get marked staleness=high instead.
- Library patterns are never stale-deleted — they're codebase-agnostic.

## Planner gate

create_plan / create_ticket requires exactly one of pattern_ids, no_patterns_reason, or proposed_patterns (the architecture-pattern tristate). Broken pattern_ids produce a non-fatal warning surfaced in the response under a ` + "`## Warnings`" + ` section.

` + "`language_patterns`" + ` is INDEPENDENT of that tristate — empty/non-empty is a free choice. Broken language_pattern_ids produce a parallel non-fatal warning ("language_pattern_id %q ..."). Validation accepts ` + "`type=finding`" + ` with non-empty ` + "`metadata.dsl_pattern`" + ` OR ` + "`type=pattern`" + `; anything else is unresolved.

See help("create_plan") and help("create_ticket").
`
