// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// ManageToolDef returns the unified manage tool definition.
//
// The MCP tool catalog is client-owned: loadSchemas composes this def into
// tools/list. Pure kgtools.MCPTool literal.
func ManageToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "manage",
		Description: "Unified server and graph lifecycle management tool. " +
			"status: show graph stats plus a per-graph durable LLM-coverage table (total / summarized / embedded / summary-fail / embed-fail per sync-eligible graph); the pipeline runtime counters in the same output are process-lifetime (reset on restart / clear_llm_failures), NOT durable coverage. " +
			"pprof_start / pprof_stop: bracket a CPU profile of the knowledge stdio client (where collectors run). pprof_start lazily brings up the loopback pprof endpoint (127.0.0.1:15021); pprof_stop returns a URL to fetch the profile (go tool pprof http://127.0.0.1:15021/debug/pprof/capture). Both are handled client-side by the stdio binary. " +
			"delete_branch: remove a branch overlay index. list_branches: list indexed branch overlays. " +
			"link: run cross-graph linking (image, Helm, Dockerfile signals) to create code-to-cloud edges. " +
			"configure_log_backend: create-or-update a log_backend node (name, provider, url, auth_type, credential, kube_context). " +
			"credential accepts the raw credential value (stored encrypted at rest in the knowledge graph) or a $ENV_VAR reference. " +
			"For provider=\"k8s\" use auth_type=\"kubeconfig\" and set kube_context to a kubeconfig context name; credential is not required (client-go resolves auth from the operator environment). " +
			"list_log_backends: list all configured log_backend nodes. " +
			"list_logs: list active ephemeral log graphs under ~/.knowledge/logs/. " +
			"discard_logs: drop a specific log graph (name=query_id) or all log graphs when name is empty. " +
			"set_metadata_overrides: replace the per-graph OverrideConfig for metadata keys (force_scalar pins keys to the inline map; force_edge pins keys to value-node edges). " +
			"Requires graph=<type> and name=<graph identifier> for non-knowledge graphs. Both force_scalar and force_edge accept JSON arrays; at least one must be non-empty. " +
			"promote_metadata: refresh per-graph metadata cardinality stats then iterate every key and dispatch PromoteKey/DemoteKey via ApplyDecision based on the current hysteresis bands (or, with force=true, the simple distinct<1000 rule that bypasses hysteresis — operator one-shot only). " +
			"Requires graph=<cloud|cicd|practice|logs or a registered custom graph type> and name=<graph identifier>. dry_run=true returns the action report without writing. " +
			"Use this immediately after the metadata-promotion subsystem ships to backfill existing graphs, or on demand when an operator wants to flip representations without waiting for the dream PROMOTE timer. " +
			"clear_llm_failures: clear summary_failure_reason and embed_failure_reason metadata on every node across one or all loaded graphs so the LLM pipeline collector re-discovers them on the next tick. Operator recovery path for terminally-failed nodes (4xx-other / context-too-large / config error). With graph= and name= empty, walks every loaded graph; with graph= set, scopes to that graph type; with both set, scopes to one named graph. " +
			"pipeline_status: report whether the LLM summary/embed pipeline is RUNNING or latched PAUSED, with the pause reason and the resume instruction. The search staleness footer also surfaces the paused state loudly. " +
			"pause_pipeline: manually latch BOTH the summary and embed worker pools paused (global, in-memory) with an optional reason=; the workers stop pulling new batches until resumed. " +
			"resume_pipeline: clear the paused latch and wake the workers. It is the ONLY exit from a circuit-break trip — the pipeline auto-pauses after a full error round (every LLM call erroring with zero successes, e.g. a quota/auth wall or repeated timeouts) and stays paused with NO self-heal until resume_pipeline is run. All pause/resume state is in-memory and cleared on restart. " +
			"prune: hard-delete (garbage-collect) tombstoned nodes from a graph. Works generically on ANY graph type (knowledge, code, cloud, cicd, practice, logs, web, pdf, linkage, transformers) — it is 'delete tombstoned nodes,' nothing more, with no graph-type allowlist. Requires a non-empty graph (name it explicitly). before=<relative window like '24h'/'2d' OR absolute RFC3339> deletes only tombstones tombstoned before that cutoff; omit before to prune ALL tombstoned nodes. Returns the pruned count. " +
			"prune-cache: ONE-TIME reclaim of orphaned client-side L2 search segments — the superseded .seg blobs the invalidation-driven reclaim never unlinked. It enumerates the segment-bearing graphs (knowledge/default + every code repo) and, per graph+format, force-full-loads the engine so the live set is COMPLETE (an unloaded-but-live segment is never false-pruned), unions the HNSW embed + deterministic live sets (shared cache root), cross-checks the server's List(0) and SKIPS any pool whose live set is incomplete, then diffs the on-disk ids and removes the orphans. PREVIEWS by default (reports the would-remove orphans per graph+format, deletes nothing); execute=true performs the removal. Not periodic — a one-shot backlog sweep. " +
			"rebuild_cache: DROP a code repo's per-repo content-hash caches (summary + embed) and RE-DERIVE them from the CURRENT base-graph nodes with ZERO model calls — a FREE re-derivation, NOT a 'clear' (a clear would guarantee a full re-pay for LLM/Voyage). The caches let a merged-and-recollected node reuse the summary/embedding it earned on a branch overlay. Use it for recovery (lost/corrupted cache), manual invalidation (the model/prompt-change lever), or backfill/migration (graphs populated before the feature shipped, so branch work benefits immediately). Requires graph=code (name=repo) or graph=knowledge (name defaults to 'default', BASE layer only — no '@'-overlay names in v1); practice/cloud/cicd are not supported. ASYNC: the server drops + re-derives the caches on a background goroutine and returns a STARTED acknowledgement immediately (a large graph's walk would otherwise exceed the edge timeout). Confirm completion via the server logs (\"rebuild_cache.complete\"). " +
			"rebuild_segments: BACKFILL a code repo's BM25+HNSW search segments from nodes that are ALREADY embedded but have ZERO shipped segments (embedded before the segment-ship path existed, or after a SegmentStore prune) — WITHOUT re-embedding. The server is engine-free, so the WORK is CLIENT-driven: the client pages the already-embedded nodes (with their stored vector + server-composed BM25 fields), rebuilds the segments DETERMINISTICALLY (fixed seed + serial-within / concurrent-across), and ships them to the server SegmentStore. Requires graph=code (or the builtin knowledge graph, or a registered custom graph type) + name; for graph=knowledge the name defaults to 'default' (its one canonical instance) and only the BASE layer is supported (an '@'-suffixed overlay name is rejected in v1). Single-flight per (graph,name). IDEMPOTENT: a deterministic build makes a re-run over an unchanged node set a byte-identical content-hash-diffed NO-OP (the first rebuild over an embed-segmented graph ships the deterministic segments and prunes the superseded embed ones; every rebuild after is a no-op). Runs SYNCHRONOUSLY and reports the scanned/built/pruned counts. " +
			"drop_graph: tear down a WHOLE non-logs graph (the persisted store plus its loaded state) via one DROP_GRAPH mutation — the same wire teardown discard_logs uses for log graphs. Requires graph=<knowledge|code|cloud|cicd|practice|web|pdf|transformers|linkage or a registered custom type> plus the instance field that family requires (code→name as repo, cloud/cicd→name as account, practice→name as language, the rest→name; knowledge needs no name). Log-graph teardown is NOT handled here — use discard_logs (graph=logs is rejected). DESTRUCTIVE: the default EXECUTES the drop; dry_run=true issues ZERO mutations and renders a 'would drop' preview so you can confirm the target first. " +
			"Required params by operation (in addition to the always-required operation): " +
			"delete_branch / list_branches require name + branch; " +
			"configure_log_backend requires name + provider + url + auth_type (credential optional for auth_type=kubeconfig); " +
			"discard_logs requires name (empty name drops all log graphs); " +
			"set_metadata_overrides / promote_metadata require graph + name; prune requires graph; prune-cache requires nothing further (it enumerates knowledge/default + every code repo itself) and previews by default, deleting only on execute:true; rebuild_cache requires graph=code or graph=knowledge + name (for graph=knowledge the name defaults to 'default', base layer only); rebuild_segments requires graph=code or graph=knowledge (or a registered custom graph type) + name (for graph=knowledge the name defaults to 'default', base layer only); drop_graph requires graph (plus the family instance field);" +
			"pause_pipeline accepts an optional reason; resume_pipeline / pipeline_status require nothing further; " +
			"status / list_log_backends / list_logs / link / clear_llm_failures / pprof_start / pprof_stop require nothing further.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"operation":      {Type: "string", Description: "Operation to perform", Enum: []string{"status", "pprof_start", "pprof_stop", "delete_branch", "list_branches", "link", "configure_log_backend", "list_log_backends", "list_logs", "discard_logs", "set_metadata_overrides", "promote_metadata", "clear_llm_failures", "pause_pipeline", "resume_pipeline", "pipeline_status", "prune", "prune-cache", "rebuild_cache", "rebuild_segments", "drop_graph"}},
				"graph":          {Type: "string", Description: "Target graph type for clear_llm_failures (knowledge, code, practice, cloud, cicd)"},
				"name":           {Type: "string", Description: "Repository name (or log_backend name for configure_log_backend; or query_id for discard_logs)"},
				"branch":         {Type: "string", Description: "Branch name (for delete_branch, list_branches)"},
				"root":           {Type: "string", Description: "Root directory path for reindex"},
				"default_branch": {Type: "string", Description: "Override default branch detection for reindex — treats this branch name as the default so the current branch gets a full reindex instead of branch overlay"},
				"precise_calls":  {Type: "boolean", Description: "Enable precise Go call graph via RTA (slower but more accurate CALLS edges)"},
				"format":         {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured)"},
				"provider":       {Type: "string", Description: "Log backend provider for configure_log_backend (cloudwatch, loki, elasticsearch, stackdriver, k8s, ...)"},
				"url":            {Type: "string", Description: "Log backend base URL for configure_log_backend"},
				"auth_type":      {Type: "string", Description: "Authentication mechanism for configure_log_backend (bearer, basic, aws_profile, api_key, service_account, kubeconfig, ...)"},
				"credential":     {Type: "string", Description: "Credential value for configure_log_backend — stored encrypted at rest. Accepts the raw value (e.g., a bearer token, API key, or service account JSON) or a $ENV_VAR reference resolved at query time. Optional when auth_type=kubeconfig."},
				"kube_context":   {Type: "string", Description: "Kubeconfig context name from ~/.kube/config. Required when provider=k8s and auth_type=kubeconfig. Auth is resolved via client-go using the operator's environment (gcloud/aws-iam-authenticator/service-account tokens)."},
				"force_scalar":   {Type: "array", Items: &kgtools.Property{Type: "string"}, Description: "Metadata keys pinned to the scalar map for set_metadata_overrides. Replaces the existing list."},
				"force_edge":     {Type: "array", Items: &kgtools.Property{Type: "string"}, Description: "Metadata keys pinned to value-node edges for set_metadata_overrides. Replaces the existing list."},
				"dry_run":        {Type: "boolean", Description: "For promote_metadata: when true, run the decision pass and report intended actions without mutating the graph. For drop_graph: when true, render a 'would drop' preview and issue ZERO mutations. Default false (executes)."},
				"force":          {Type: "boolean", Description: "For promote_metadata: when true, bypass the hysteresis bands and use the simple distinct<1000 rule. Operator one-shot path only."},
				"execute":        {Type: "boolean", Description: "For prune-cache: when true, DELETE the orphaned segments; default false renders a would-remove preview only."},
				"before":         {Type: "string", Description: "For prune: cutoff for which tombstoned nodes to hard-delete. A relative window ('24h', '2d') or an absolute RFC3339 timestamp; only tombstones tombstoned before it are pruned. Omit to prune ALL tombstoned nodes."},
				"reason":         {Type: "string", Description: "For pause_pipeline: optional operator reason surfaced by pipeline_status. Defaults to a generic 'manually paused by operator' string when omitted."},
			},
			Required: []string{"operation"},
		},
	}
}
