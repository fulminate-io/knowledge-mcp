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
			"status: show graph stats. " +
			"pprof_start / pprof_stop: bracket a CPU profile of the knowledge stdio client (where collectors run). pprof_start lazily brings up the loopback pprof endpoint (127.0.0.1:15021); pprof_stop returns a URL to fetch the profile (go tool pprof http://127.0.0.1:15021/debug/pprof/capture). Both are handled client-side by the stdio binary. " +
			"rebuild_hnsw: rebuild vector index (supports graph='code', 'practice', 'cloud', 'cicd', or default knowledge). " +
			"rebuild_bm25: rebuild BM25 full-text index (supports graph='code', 'practice', 'cloud', 'cicd', or default knowledge). " +
			"For graph='code' the server discovers live files via 'git ls-files' under root (defaults to the server's --root) and tombstones code nodes whose FilePath is no longer on disk, then rebuilds BM25 over the remaining live nodes — yielding a completely accurate index in one call. " +
			"For other graphs no prune is performed — the call is a pure index rebuild. " +
			"delete_branch: remove a branch overlay index. list_branches: list indexed branch overlays. " +
			"topology: run every registered topology analyzer over every available graph; emit/refresh findings. " +
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
			"Requires graph=<cloud|cicd|practice|logs> and name=<graph identifier>. dry_run=true returns the action report without writing. " +
			"Use this immediately after the metadata-promotion subsystem ships to backfill existing graphs, or on demand when an operator wants to flip representations without waiting for the dream PROMOTE timer. " +
			"clear_llm_failures: clear summary_failure_reason and embed_failure_reason metadata on every node across one or all loaded graphs so the LLM pipeline collector re-discovers them on the next tick. Operator recovery path for terminally-failed nodes (4xx-other / context-too-large / config error). With graph= and name= empty, walks every loaded graph; with graph= set, scopes to that graph type; with both set, scopes to one named graph. " +
			"prune: hard-delete (garbage-collect) tombstoned nodes from a graph. Works generically on ANY graph type (knowledge, code, cloud, cicd, practice, logs, web, pdf, linkage, transformers) — it is 'delete tombstoned nodes,' nothing more, with no graph-type allowlist. Requires a non-empty graph (name it explicitly). before=<relative window like '24h'/'2d' OR absolute RFC3339> deletes only tombstones tombstoned before that cutoff; omit before to prune ALL tombstoned nodes. Returns the pruned count. " +
			"Required params by operation (in addition to the always-required operation): " +
			"rebuild_hnsw / rebuild_bm25 require graph for non-knowledge graphs; " +
			"delete_branch / list_branches require name + branch; " +
			"configure_log_backend requires name + provider + url + auth_type (credential optional for auth_type=kubeconfig); " +
			"discard_logs requires name (empty name drops all log graphs); " +
			"set_metadata_overrides / promote_metadata require graph + name; prune requires graph; " +
			"status / list_log_backends / list_logs / link / topology / clear_llm_failures / pprof_start / pprof_stop require nothing further.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"operation":      {Type: "string", Description: "Operation to perform", Enum: []string{"status", "pprof_start", "pprof_stop", "rebuild_hnsw", "rebuild_bm25", "delete_branch", "list_branches", "link", "configure_log_backend", "list_log_backends", "list_logs", "discard_logs", "set_metadata_overrides", "promote_metadata", "clear_llm_failures", "prune"}},
				"graph":          {Type: "string", Description: "Target graph type for rebuild_hnsw (code, practice, cloud, cicd, or default=knowledge) and clear_llm_failures (knowledge, code, practice, cloud, cicd)"},
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
				"dry_run":        {Type: "boolean", Description: "For promote_metadata: when true, run the decision pass and report intended actions without mutating the graph. Default false (executes flips)."},
				"force":          {Type: "boolean", Description: "For promote_metadata: when true, bypass the hysteresis bands and use the simple distinct<1000 rule. Operator one-shot path only."},
				"before":         {Type: "string", Description: "For prune: cutoff for which tombstoned nodes to hard-delete. A relative window ('24h', '2d') or an absolute RFC3339 timestamp; only tombstones tombstoned before it are pruned. Omit to prune ALL tombstoned nodes."},
			},
			Required: []string{"operation"},
		},
	}
}
