// SPDX-License-Identifier: Apache-2.0

package tools

const helpManage = `# manage — Server operations

## Server status
  manage({ "operation": "status" })  — pipeline metrics (summary/embed queued/running/succeeded/failed) per graph

## CPU profiling (client-side)
  manage({ "operation": "pprof_start" })  — start a CPU profile of the knowledge CLIENT (where the collectors run)
  manage({ "operation": "pprof_stop" })   — stop it and report where to pull the profile

  pprof_start lazily brings up the loopback pprof endpoint and returns it —
  http://127.0.0.1:15021/debug/pprof/ by default. Reproduce the slow operation
  while the profile is running, then call pprof_stop; it reports the profile size
  and the two commands that fetch it from /debug/pprof/capture (go tool pprof
  <url>, or curl -s <url> -o cpu.pprof). Both operations are handled client-side
  by the knowledge binary; neither takes any further parameter. pprof_start with
  a profile already running is refused, as is pprof_stop with none running.

## Code graph management
  manage({ "operation": "list_branches", "name": "myrepo" })
  manage({ "operation": "delete_branch", "name": "myrepo", "branch": "feature-x" })

  manage({ "operation": "repair_edges", "graph": "code", "name": "myrepo" })                   — PREVIEW the CONTAINS fossils
  manage({ "operation": "repair_edges", "graph": "code", "name": "myrepo", "execute": true })  — remove them

  repair_edges is a ONE-SHOT operator repair for CONTAINS fossils: file-to-symbol
  edges whose target symbol lives in a DIFFERENT file than the file node claiming
  it. A re-collect never repairs them — the collect path replaces a source node's
  edge set only from the collect forward, so an edge landed under a wrong source
  stays resident however many times the repo is re-collected. It PREVIEWS by
  default (enumerates and reports counts per graph, mutating nothing);
  execute=true removes the enumerated edges and nothing else. Requires graph=code
  plus name=<repo> to scope to one repo, or graph=code with an empty name to
  sweep every code graph. Against a CLOUD backend the empty-name execute sweep is
  REFUSED naming the backend (those deletes land on live production data) — name
  the repo; a local empty-name execute sweep is allowed. A repair covers the base
  graph AND every branch overlay of the targeted repo; branch=<name> narrows it
  to exactly that one overlay, and branch requires name.

  Note: There is no operator-driven manage(reindex) command. The pipeline picks
  up changed nodes automatically.
  Re-run the appropriate collector (collect type:code, or a registered
  type) to refresh source nodes; the per-graph collector inside
  the server discovers gaps (missing summary, missing vector) and the worker
  pool drains them on the next tick.

## LLM pipeline operator commands

  manage({ "operation": "clear_llm_failures" })                               — clear summary_failure_reason + embed_failure_reason markers across every loaded graph
  manage({ "operation": "clear_llm_failures", "graph": "code" })              — scope to one graph type
  manage({ "operation": "clear_llm_failures", "graph": "code", "name": "knowledge" })  — scope to one named graph

  manage({ "operation": "pipeline_status" })                                  — report whether the summary/embed pipeline is RUNNING or PAUSED (+ reason + how to resume)
  manage({ "operation": "pause_pipeline", "reason": "quota investigation" })  — manually latch BOTH axes paused (reason optional, surfaced by pipeline_status)
  manage({ "operation": "resume_pipeline" })                                  — clear the paused latch and re-enable the workers

  The circuit breaker is PER-AXIS — summary and embed each have their own — and
  either axis AUTO-PAUSES on whichever of two trip conditions fires first: a
  zero-success window of 20 consecutive errored LLM calls on that axis (the
  class-agnostic quota/auth/timeout wall), or 2 consecutive SAME-CLASS
  deterministic-terminal failures, which fast-trips because such a failure
  reproduces identically and retrying is futile. A success on an axis resets its
  counters. A trip on one axis cross-trips the other only when the dominant error
  class is auth or quota and both axes share one provider.
  Whatever tripped it, the axis latches PAUSED with NO self-heal and NO
  auto-probe: resume_pipeline is the ONLY exit, for an auto-trip and a manual
  pause_pipeline alike. The search staleness footer ALSO surfaces the paused
  state loudly. Pause/resume state is in-memory and is cleared on restart.

## Garbage collection
  manage({ "operation": "prune", "graph": "knowledge" })                       — hard-delete ALL tombstoned nodes from the knowledge graph
  manage({ "operation": "prune", "graph": "code", "name": "myrepo" })          — GC tombstoned code nodes for one repo
  manage({ "operation": "prune", "graph": "practice", "name": "go", "before": "30d" })  — GC tombstones older than 30 days

  prune works GENERICALLY on any graph type (knowledge, code,
  practice, web, pdf, linkage, checks) — it deletes tombstoned
  nodes and nothing else. There is no graph-type allowlist; just name the graph.
  before accepts a relative window ("24h"/"2d") or an absolute RFC3339 timestamp;
  only tombstones tombstoned before it are pruned. Omit before to prune all.

## Whole-graph teardown
  manage({ "operation": "drop_graph", "graph": "code", "name": "myrepo", "dry_run": true })  — PREVIEW
  manage({ "operation": "drop_graph", "graph": "code", "name": "myrepo" })                   — DROPS IT

  drop_graph tears down a WHOLE graph — the persisted store plus its loaded
  state — via one DROP_GRAPH mutation. Requires
  graph=<knowledge|code|web|pdf|checks|linkage or a registered custom type>
  plus the instance field that family needs (code→name as repo, the rest→name;
  knowledge needs no name). practice is REFUSED outright: practice graphs are
  never a destructive target, and practice nodes leave through delete by
  source hub. A RETIRED family name is refused, naming the removal.
  DESTRUCTIVE, AND THE DEFAULT EXECUTES. dry_run:true issues ZERO mutations and
  renders a "would drop" preview so the target can be confirmed first. Note this
  is the OPPOSITE polarity from prune-cache, which previews by default and acts
  only on execute:true.

## Metadata representation
  manage({ "operation": "set_metadata_overrides", "graph": "<registered type>", "name": "acme",
           "force_scalar": ["region"], "force_edge": ["owner"] })
  manage({ "operation": "promote_metadata", "graph": "<registered type>", "name": "acme", "dry_run": true })

  set_metadata_overrides REPLACES a graph's per-key OverrideConfig: force_scalar
  pins keys to the inline scalar map, force_edge pins them to value-node edges.
  Both accept JSON arrays and both REPLACE the existing list rather than merging;
  at least one must be non-empty, else the call is refused. Non-knowledge graphs
  need graph=<type> + name=<graph identifier>.

  promote_metadata refreshes the per-graph metadata cardinality stats, then walks
  every key and dispatches PromoteKey/DemoteKey through ApplyDecision using the
  current hysteresis bands — or, with force=true, the simple distinct<1000 rule
  that bypasses hysteresis (an operator one-shot). Requires graph=<practice
  or a registered custom graph type> + name=<graph identifier>.
  dry_run=true runs the decision pass and reports the intended actions without
  writing. keys=<comma-separated> narrows it to the named keys; empty considers
  every key the stats snapshot observed.

## Embed-identity migration (the ONE path that changes a recorded identity, and the one that spends)
  manage({ "operation": "migrate_embed_identity", "graph": "code", "name": "myrepo", "profile": "code4" })

  A graph records the embedder that produced its vectors — provider, model,
  dimension, dtype — at its FIRST embed, and that record is authoritative
  afterwards. No config change re-embeds anything: editing, renaming or deleting
  the profile a graph was embedded under does not reach back to it. This
  operation is the only thing that changes the record, and it is deliberately
  explicit because it is the only thing that can commit you to a corpus-scale
  embedding bill.

  What it does, in order: records the new identity from the NAMED PROFILE;
  CLEARS every vector the superseded identity produced (a graph may never hold
  two vector widths — the image serializer refuses that set, so a migration that
  skipped this would report success and then be unable to checkpoint); re-opens
  every cleared node so the pipeline re-embeds it; and prunes the cache rows
  keyed under the old identity, which the widened cache key has already made
  unreachable.

  It reports the identity transition — the identity being superseded, read from
  the SERVER'S record rather than from your config, and the one being adopted —
  and how many vectors it cleared. THAT COUNT IS THE BILL: every one of those
  nodes will be re-embedded by the pipeline, at the new identity, through your
  embedding provider.

  profile must name a profile the config defines ([embedder.profile.<name>], or
  "default" for the single [embedder] table). An unknown name is refused naming
  the profiles that ARE defined — never silently defaulted, because the identity
  it would record is permanent short of another migration.

  Re-running an interrupted migration finishes it: every step is idempotent, and
  the second run reports zero vectors cleared because the first already cleared
  them.

## Content-hash cache rebuild (free re-derivation)
  manage({ "operation": "rebuild_cache", "graph": "code", "name": "myrepo" })  — drop + re-derive a repo's summary/embed caches from base nodes

  rebuild_cache DROPS a builtin graph's per-graph content-hash caches (summary +
  embed) and RE-DERIVES them from the CURRENT base-graph nodes with ZERO model
  calls. It is a FREE re-derivation, NOT a "clear" (a clear would guarantee a full
  re-pay for LLM/Voyage). The caches let a merged-and-recollected node reuse the
  summary/embedding it earned on a branch overlay instead of re-summarizing/
  re-embedding byte-identical content. Use rebuild_cache for: recovery (lost/
  corrupted cache), manual invalidation (the model/prompt-change lever), or
  backfill/migration (graphs populated before the feature shipped). Builtin-graph
  only: requires graph=code (name=repo) or graph=knowledge (name defaults to
  "default" — BASE layer only, no "@"-overlay names in v1); practice and
  registered custom types are not supported.
  ASYNC: the server drops + re-derives on a background goroutine and returns a
  STARTED acknowledgement immediately (a large repo's walk would otherwise exceed
  the edge timeout); confirm completion via the server logs ("rebuild_cache.complete").

## Search-segment rebuild (client-driven backfill, no re-embed)
  manage({ "operation": "rebuild_segments", "graph": "code", "name": "myrepo" })  — rebuild a repo's BM25+HNSW search segments from already-embedded nodes

  rebuild_segments BACKFILLS a code repo's BM25 + HNSW search segments from nodes
  that are ALREADY embedded but have ZERO built segments (embedded before the
  segment build path existed, or after a segment-cache prune) — WITHOUT re-embedding.
  The server is engine-free, so the WORK is CLIENT-driven: the client pages the
  already-embedded nodes (with their stored vector + server-composed BM25 fields),
  rebuilds the segments DETERMINISTICALLY (fixed seed + serial-within / concurrent-
  across), and writes them to its LOCAL segment cache. Requires graph=code (name=repo),
  the builtin knowledge graph (name defaults to "default" — BASE layer only, no
  "@"-overlay names in v1), or a registered custom graph type + name. Single-flight
  per (graph,name). IDEMPOTENT: a deterministic build means a
  re-run over an unchanged node set is a byte-identical, content-hash-diffed NO-OP
  (the first rebuild over an embed-segmented graph ships the deterministic segments
  and prunes the superseded embed ones; every rebuild after is a no-op). Runs
  SYNCHRONOUSLY and reports the scanned/built/pruned counts.
  INCREMENTAL BY DEFAULT: the scan pages from a persisted watermark, so it covers
  only what changed since the last rebuild that landed. reset=true zeroes that
  watermark and re-emits the WHOLE corpus — the escape hatch for when the shipped
  segments are suspect and the incremental path has nothing to correct them with.
  A zero-scan report says which of the two you got, so "nothing since the
  watermark" never reads as "this graph has nothing to build from".

## Orphaned-segment cache prune (one-shot backlog reclaim)
  manage({ "operation": "prune-cache" })                  — PREVIEW: report the orphaned L2 segments per graph+format, delete nothing
  manage({ "operation": "prune-cache", "execute": true }) — DELETE the orphaned L2 segments

  prune-cache is a ONE-TIME (not periodic) reclaim of orphaned client-side L2
  segment blobs: the accumulated superseded .seg files the invalidation-driven
  reclaim never unlinked. It enumerates the segment-bearing graphs (knowledge/default
  + every code repo) and, per graph+format, diffs the on-disk .seg ids against that
  graph's COMPLETE current live set, removing the orphans. It PREVIEWS by default
  (execute=false renders a would-remove report and deletes NOTHING); execute=true
  performs the removal.
  The complete-live-set diff is the whole safety, and it is the ONLY safety: each
  graph is FORCE-FULL-LOADED from the local cache before its live set is read, so an
  unloaded-but-live segment still counts and is never false-pruned. A bare export from
  an engine that has loaded nothing is EMPTY, and diffing on-disk ids against an empty
  live set would condemn every segment there is — which is why the reload is not an
  optimization but the precondition. Honors format=json for the structured report.

## Style-rule import
  manage({ "operation": "import_style_rules", "source": "hub_id", "path": "/abs/path/rules.json" })
  manage({ "operation": "import_style_rules", "source": "hub_id", "path": "/abs/path/rules.json", "dry_run": true })

  import_style_rules reads a generic RULE-LIST file and writes its rules as
  PRACTICE NODES under one language hub. The file is JSON — a top-level object
  with a "rules" array — and each rule carries:

    name        required, the rule's short name
    summary     required, the author-supplied one-line summary
    text        required, the rule itself, stored as the node description
    severity    required, one of info | notice | warning | critical
    scope       optional { "repo": "...", "paths": ["..."] }
    linter      optional { "name": "...", "rule_id": "..." }
    check       optional { "dsl_pattern", "check_where", "fixture_bad", "fixture_good" }
    id          optional; supply it to UPDATE an existing rule

  IT WRITES PRACTICE NODES AND NOTHING ELSE. No check node, no fixture node and
  no sister_check cross-link is ever created by an import. A sister check is
  authored later by its own manage_checks(create) call, after an LLM has passed
  on the practice node; a rule's "check" block rides the practice node as inert
  data until then and is never compiled or executed.

  The whole list lands in ONE create_batch, so every rule gets its hub metadata
  key and its sourced-from edge atomically. A rule whose id already exists is
  UPDATED per field rather than re-created — a create would store the body whole
  and clear the fields the file omitted — so re-importing the same list produces
  no duplicate and clears no field.

  EVERY REFUSAL NAMES THE RULE'S INDEX IN YOUR FILE, and nothing is written when
  any rule is refused: a missing name, summary or text; a severity off the
  ladder; a scope path that is absolute, carries a ".." segment, or is not the
  repo-relative spelling; an unknown key at any level; a duplicate id within one
  file; an unreadable or unparseable file. dry_run=true validates and reports
  what it would write, writing nothing.

  Read the imported rules back with the style-rule index:
  query({ "graph": "practice", "mode": "style_index", "source": "hub_id" })

## Repo manifest registration
  manage({ "operation": "register_repo", "name": "myrepo", "root": "/abs/path/to/myrepo" })  — record a repo name → checkout dir

  register_repo records a repo name → absolute checkout directory in the
  machine-local manifest (~/.knowledge/repos.json) — the same registry a code
  collect populates. It is PURELY CLIENT-SIDE and MACHINE-LOCAL: the mapping is
  written to this machine's disk and never forwarded to the server, because the
  path is machine-specific and must never leave this host. It is IDEMPOTENT:
  re-registering a name overwrites its recorded path, so a name can be re-pointed
  at a moved or renamed checkout.
  Use it so a cross-repo ast walk (repo:<name>) can resolve a bare repo name to
  its real directory WITHOUT having to collect the repo first. root must be an
  existing absolute directory; name is the repo name to record.

## Cross-graph linking
  manage({ "operation": "link" })  — run the Dockerfile linker to create code-to-code edges

## Gotchas
  - clear_llm_failures clears markers but does NOT re-discover; the pipeline picks the node up on its next tick (default 250ms)
  - a circuit-break trip (auto-pause) does NOT self-heal — diagnose the cause (quota/auth/timeouts), then run resume_pipeline to re-enable; pause state is lost on restart
  - prune requires an explicit graph (it never defaults to knowledge); without before it hard-deletes EVERY tombstoned node in that graph
`
