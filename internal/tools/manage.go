// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	clientlinker "github.com/fulminate-io/knowledge-mcp/internal/linker"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptsync"
)

// pipelineMetricser is the local view of ClientDeps that manage(status)
// uses to overlay live LLM-pipeline counters onto the server's response.
// Declared here (not on ClientDeps) so test fakes don't have to grow a
// pipeline import just to compile — production *client satisfies this
// structurally, fakes don't, and the type-assert below degrades to "no
// overlay" when the assertion misses.
type pipelineMetricser interface {
	PipelineMetrics() (pipeline.Metrics, bool)
}

// pipelineResetter resets the session-lifetime failed counters after
// clear_llm_failures removes on-disk markers. Same structural-typing
// discipline as pipelineMetricser.
type pipelineResetter interface {
	ResetPipelineFailedCounters()
}

// transcriptUploadHealther is the local view of ClientDeps that manage(status) uses to
// overlay the background transcript-upload loop's health onto the status body. Declared
// here (not on ClientDeps) with the SAME structural-typing discipline as
// pipelineMetricser: production *client satisfies it, the existing ClientDeps test fakes
// don't, and the render helpers degrade to nothing when the type-assert misses.
type transcriptUploadHealther interface {
	TranscriptUploadHealth() (transcriptsync.UploadHealth, bool)
}

// pipelinePauser is the local view of ClientDeps the pause_pipeline /
// resume_pipeline / pipeline_status manage ops use to drive the circuit
// breaker. Same structural-typing discipline as pipelineMetricser: production
// *client satisfies it; test fakes don't, and the handlers degrade to an
// errorResult when the type-assert misses. ResumePipeline is the ONLY exit
// from a circuit break (auto-trip or manual pause).
type pipelinePauser interface {
	PausePipeline(reason string)
	ResumePipeline()
	PipelineStatus() (pipeline.PipelineStatus, bool)
}

// cloudStatusInfo is the optional view of ClientDeps that manage(status)
// uses to learn (a) whether the user is logged in to Fulminate Cloud and
// (b) the cloud host to surface in the status body. Declared here (not on
// ClientDeps) for the SAME reason as pipelineMetricser: the 18+ existing
// test fakes that satisfy ClientDeps must not each grow a new stub method.
// Production *client satisfies it structurally; handleServerStatus
// degrades to the existing local-daemon path when the type-assert misses
// or CloudStatusInfo reports loggedIn=false.
type cloudStatusInfo interface {
	CloudStatusInfo() (loggedIn bool, host string)
}

// InterceptManage dispatches manage operations that run client-side:
//   - status: liveness + server stats.
//   - pprof_start / pprof_stop: bracket a CPU profile of the knowledge client
//     (collector work runs here), retrievable from the loopback pprof
//     endpoint. See package profiling.
//
// There is no manage(reindex) operation — the per-graph collector + global
// pipeline drains naturally. Code collect runs via the dedicated `collect` MCP
// tool (or `make collect`), which drives the client-side collector and ships
// its chunks through a RemoteUploadSink. (There is no codegraph package and no
// SyncBranch entry point: both names predate the client-side collector.)
func InterceptManage(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	a, claimed, refusal := decodeManageCall(params)
	if !claimed {
		return false, kgtools.ToolResult{}
	}
	if refusal != nil {
		return true, *refusal
	}
	switch a.Operation {
	case "graph_inventory":
		return true, handleGraphInventory(ctx, deps)
	case "status":
		return true, handleServerStatus(ctx, deps, a.Format)
	case "account_for_session", "account_use":
		return true, handleAccountManage(ctx, deps, a)
	case "pprof_start", "pprof_stop":
		return true, handlePprofManage(a.Operation)
	case "link":
		return true, handleClientLinker(ctx, deps, a)
	case "migrate_embed_identity":
		return true, handleMigrateEmbedIdentity(ctx, deps, a)
	case "promote_metadata":
		return true, handleManagePromoteMetadata(ctx, deps, a, params.Arguments)
	case "clear_llm_failures":
		return true, handleClientClearLLMFailures(ctx, deps, a)
	case "pause_pipeline", "resume_pipeline", "pipeline_status":
		return true, handlePipelineLifecycleManage(deps, a)
	case "set_metadata_overrides":
		return true, handleClientSetMetadataOverrides(ctx, deps, a)
	case "delete_branch", "list_branches":
		return true, handleBranchOverlayManage(ctx, deps, a)
	case "prune":
		return true, handleClientPrune(ctx, deps, a)
	case "rebuild_cache":
		return true, handleClientRebuildCache(ctx, deps, a)
	case "rebuild_segments":
		return true, handleClientRebuildSegments(ctx, deps, a)
	case "prune-cache":
		return true, handleClientPruneCache(ctx, deps, a)
	case "drop_graph":
		return true, handleClientDropGraph(ctx, deps, a)
	case "repair_edges":
		return true, handleClientRepairEdges(ctx, deps, a)
	case "register_repo":
		return true, handleRegisterRepo(a)
	case OpStyleRulesImport:
		return true, handleImportStyleRules(ctx, deps, a)
	default:
		// A KNOWN operation this switch does not dispatch belongs to a claimant
		// further down the chain — decline so it gets there. Anything else is
		// genuinely unknown and terminates here. THE DECLINE ARM CANNOT FIRE
		// TODAY: every name in manageOperations is dispatched above, since the
		// downstream claimant that owned four of them left with the built-in log
		// collectors. It stays because the known-set is DATA (manage_operations.go)
		// and the next operation added there before its arm is written must reach
		// its claimant rather than be rejected here.
		if manageOperationKnown(a.Operation) {
			return false, kgtools.ToolResult{}
		}
		return true, unknownOperationResult("manage", a.Operation, manageOperations)
	}
}

// handleClientLinker dispatches manage(link) to the client-side cross-
// graph linker (cmd/knowledge/internal/linker). The linker body lives
// client-side because it walks the graphs via gc.Call and emits derived
// edges through mutate(link, link_graph:"linkage").
func handleClientLinker(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("manage(link): GraphCaller is unavailable — the client is running in degraded mode")
	}
	res, err := clientlinker.RunAll(ctx, gc, clientlinker.LinkOptions{})
	if err != nil {
		return errorResult("manage(link): " + err.Error())
	}
	// THE THREE RETIRED PASSES ARE ABSENT FROM BOTH RENDERS RATHER THAN ZERO IN
	// THEM. image, helm and workload_identity each read a cloud graph on one side
	// and went with the built-in cloud collectors; reporting image=0 would tell
	// an operator the pass ran and matched nothing, which is a different fact
	// from the pass not existing.
	if a.Format == "json" {
		payload := map[string]any{
			"dockerfile_links": res.DockerfileLinks,
		}
		errs := make([]string, 0, len(res.Errors))
		for _, e := range res.Errors {
			errs = append(errs, e.Error())
		}
		if len(errs) > 0 {
			payload["errors"] = errs
		}
		return jsonResult(payload)
	}
	return textResult(fmt.Sprintf(
		"Linker complete: %d total links (dockerfile=%d), errors=%d",
		res.DockerfileLinks, res.DockerfileLinks, len(res.Errors)))
}

// manageArgs covers the fields the client-side manage intercepts read.
//
// THE FIVE LOG-BACKEND FIELDS ARE GONE WITH THEIR OPERATIONS. provider, url,
// auth_type, credential and kube_context existed for configure_log_backend,
// which wrote a log-backend node carrying the operator's credential; the
// operation, its redacting reader and the node type all left with the built-in
// log collectors, so the fields have no dispatcher to reach for them. Keeping
// them would leave `credential` in the manage argument surface with nothing that
// reads it.
type manageArgs struct {
	Operation string `json:"operation"`
	Graph     string `json:"graph"`
	Name      string `json:"name"`
	Branch    string `json:"branch"`
	Root      string `json:"root"`
	// Source is import_style_rules' hub selector and Path is its rule-list file.
	// `source` is the spelling because the name is FREE on this schema: the
	// practice hub selector publishes `source` wherever it is free and
	// `source_hub` only where that name is already taken.
	Source string `json:"source"`
	Path   string `json:"path"`
	Format string `json:"format"`
	// Account is the id or slug the two account operations name. There is
	// deliberately no session field beside it: account_for_session binds the
	// session the call arrived on, and a session id on the wire would let one
	// caller retarget another's session.
	Account string `json:"account"`
	// Profile names the embedder profile migrate_embed_identity migrates a graph
	// TO. It is a profile NAME rather than four inline identity fields so a
	// migration cannot name an embedder no profile describes — the client that
	// must construct that embedder for a query resolves its credential from the
	// profile, and an identity with no profile behind it has nowhere to get one.
	Profile string `json:"profile"`

	// promote_metadata flags read by the client-side
	// intercept to gate batch-narrative emission and re-marshal the
	// payload with format=json forced.
	DryRun bool `json:"dry_run"`
	Force  bool `json:"force"`

	// Execute gates prune-cache: it PREVIEWS by default (Execute=false renders a
	// would-remove report and deletes NOTHING) and removes orphaned segments ONLY
	// when Execute=true. This is the OPPOSITE default polarity from drop_graph's
	// DryRun (which executes by default, previews on dry_run:true) — prune-cache is
	// data-destructive against the L2 cache, so the false-prune history mandates a
	// preview-first default that an operator must explicitly opt out of.
	Execute bool `json:"execute"`

	// set_metadata_overrides force-lists, read by the client
	// intercept and lowered onto the Index RPC params payload. They mirror the
	// fields the retired server-side override handler read.
	ForceScalar []string `json:"force_scalar"`
	ForceEdge   []string `json:"force_edge"`

	// prune cutoff: a relative window ("24h"/"2d") or an absolute RFC3339
	// timestamp. Tombstones tombstoned BEFORE it are hard-deleted; empty
	// prunes ALL tombstoned nodes.
	Before string `json:"before"`

	// pause_pipeline operator reason, surfaced verbatim by pipeline_status.
	// Empty falls back to a generic "manually paused by operator" string.
	Reason string `json:"reason"`

	// Reset is the rebuild_segments from-scratch escape hatch. A rebuild normally
	// scans only what changed since the last one that landed; Reset ignores that
	// watermark and the deleted ids retained with it, so the whole corpus is
	// re-emitted. It is what an operator reaches for when the shipped segments are
	// suspect and the incremental path has nothing to correct them with.
	Reset bool `json:"reset"`
}

// handleServerStatus reports liveness (and, when the server is up, basic
// graph stats). Rendered either as text or JSON per the format arg.
//
// Pipeline counters (summary_*/embed_*) come from the CLIENT-side
// pipeline via overlayPipelineMetrics. The server's response always
// returns zero for those fields — the LLM pipeline runs in the
// client so its live counts only exist here. When the
// pipeline is disabled (--no-llm-pipeline, or neither summarizer nor
// embedder configured), the counters render as "(pipeline disabled)"
// instead of zeros so the operator can tell the difference between
// "queue empty" and "pipeline never wired."
func handleServerStatus(ctx context.Context, deps ClientDeps, format string) kgtools.ToolResult {
	// Logged-in users target the CLOUD graph for the node/edge/vector totals via
	// the already-routed Stats RPC. The cloud branch's JSON body ALSO carries the
	// local-daemon fields (pid, graph_path, pipeline counters, coverage[],
	// doctor[]) via the shared addLocalDaemonJSON helper — CEO decision: always
	// show local-daemon fields, even when logged in — so the Daemon Status page's
	// Doctor/Pipeline/Coverage cards populate regardless of login. The type-assert
	// + the loggedIn check degrade to the local-daemon path below when either
	// misses, so the logged-out behavior is unchanged.
	if csi, ok := deps.(cloudStatusInfo); ok {
		if loggedIn, host := csi.CloudStatusInfo(); loggedIn {
			return handleCloudStatus(ctx, deps, host, format)
		}
	}
	gc := deps.LocalLiveness()
	if !gc.Healthy() {
		// No daemon to probe here, but the client version is always known
		// in-process — render it so `manage(status)` always carries the client
		// line (no daemon line, no skew line without a daemon to compare).
		clientVer := clientVersionOnly(deps)
		// The installed server BINARY is readable with no daemon running — its
		// version comes off disk, not off a live process — so the binary-skew
		// line is available on this branch even though the daemon line is not.
		serverBinVer, serverBinKnown := serverBinarySection(deps)
		if format == "json" {
			// THE ACCOUNT ROUTING IS A PER-REQUEST FACT, not a local-daemon one:
			// it is resolved from the calling request's own context and holds
			// whether or not a local graph server is up. It lands on THIS body
			// too — the one branch that does not go through addLocalDaemonJSON —
			// because a consumer reads account_source's presence to decide
			// whether the daemon can answer the question at all, and a body that
			// omitted it would read as "this daemon is too old to know".
			m := map[string]any{"status": "not_running"}
			// This arm builds its own map rather than going through
			// addLocalDaemonJSON (there is no local daemon to describe), so it
			// carries the harness-session keys explicitly — the resolution is a
			// fact about THIS CALL and is known whether or not a graph server
			// is up.
			addHarnessSessionJSON(ctx, m)
			addVersionJSON(m, clientVer, "", false, serverBinVer, serverBinKnown)
			addClientVersionStateJSON(m)
			// The request's account resolution is a fact about the CALLER, not
			// about the local graph server, so this arm carries it too — see
			// addRequestAccountJSON.
			addRequestAccountJSON(ctx, deps, m)
			return jsonResult(m)
		}
		return textResult("Graph server: NOT RUNNING" + renderHarnessSessionText(ctx) +
			renderAccountStatusText(ctx, deps) +
			renderVersionLines(clientVer, "", false, serverBinVer, serverBinKnown) + renderClientVersionStateLines())
	}
	clientVer, daemonVer, daemonKnown := versionSection(deps)
	serverBinVer, serverBinKnown := serverBinarySection(deps)
	if format == "json" {
		// All the local-daemon facts (pid/graph_path/counts + pipeline counters +
		// coverage[] + doctor[] + transcript + collect_runs) go through the shared
		// addLocalDaemonJSON helper so the local AND cloud JSON paths stay in sync.
		m := map[string]any{}
		addLocalDaemonJSON(ctx, deps, m)
		m["status"] = "running"
		addVersionJSON(m, clientVer, daemonVer, daemonKnown, serverBinVer, serverBinKnown)
		addClientVersionStateJSON(m)
		return jsonResult(m)
	}
	// TEXT branch: fetch the local status directly for the human render (the JSON
	// branch above returns first, so this is the only Status RPC on that path).
	status, err := gc.Status()
	if err != nil {
		return errorResult("status failed: " + err.Error())
	}
	metrics, pipelineOK := overlayPipelineMetrics(deps, status)
	pipelineLine := "  Summarization: (pipeline disabled)\n  Embedding: (pipeline disabled)"
	if pipelineOK {
		// These counters are PROCESS-LIFETIME runtime metrics — they reset on
		// restart and on clear_llm_failures, and are NOT durable coverage (the
		// LLM Coverage table below is). The caption disambiguates the two so a
		// queue-empty process is not mistaken for an uncovered graph.
		pipelineLine = fmt.Sprintf(
			"  Pipeline runtime (this process only — resets on restart / clear_llm_failures; NOT durable coverage):\n"+
				"  Summarization: %d queued, %d running, %d succeeded, %d failed\n  Embedding: %d queued, %d running, %d succeeded, %d failed",
			metrics.SummaryQueued, metrics.SummaryRunning, metrics.SummarySucceeded, metrics.SummaryFailed,
			metrics.EmbedQueued, metrics.EmbedRunning, metrics.EmbedSucceeded, metrics.EmbedFailed)
	}
	transcriptBlock := ""
	if th, ok := transcriptUploadHealth(deps); ok {
		transcriptBlock = renderTranscriptHealthText(th)
	}
	if uh, ok := updateCheckHealth(deps); ok {
		transcriptBlock += renderUpdateHealthText(uh)
	}
	doctorBlock := ""
	if checks, ok := doctorChecks(ctx, deps); ok {
		doctorBlock = renderDoctorText(checks)
	}
	return textResult(fmt.Sprintf(
		"Graph server: RUNNING\n  PID: %.0f\n  Nodes: %.0f\n  Edges: %.0f\n  Vectors: %.0f\n  BM25 docs: %.0f\n  Path: %s%s\n%s%s%s%s%s%s%s",
		status["pid"], status["nodes"], status["edges"], status["binary_vectors"], status["bm25_docs"], status["graph_path"],
		renderHarnessSessionText(ctx),
		pipelineLine, renderLLMCoverage(ctx, deps), transcriptBlock, collectRunSection(deps), doctorBlock,
		renderAccountStatusText(ctx, deps),
		renderVersionLines(clientVer, daemonVer, daemonKnown, serverBinVer, serverBinKnown)+renderClientVersionStateLines()))
}

// textResult and errorResult mirror the repo-root helpers byte-for-byte.
// We replicate them here (rather than calling kgtools.TextResult /
// kgtools.ErrorResult) because kgtools.ErrorResult prepends "Error: " to
// every message, and the MCP output shape for these client-intercepted
// paths matches the prefix-free repo-root errorResult. Preserving output
// shape is worth 5 lines of duplication.

func textResult(text string) kgtools.ToolResult {
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: text}}}
}

func errorResult(msg string) kgtools.ToolResult {
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: msg}}, IsError: true}
}

// jsonResult marshals data as JSON and returns a text result carrying the
// JSON body. Errors fall through to errorResult.
func jsonResult(data any) kgtools.ToolResult {
	b, err := json.Marshal(data)
	if err != nil {
		return errorResult("json marshal: " + err.Error())
	}
	return textResult(string(b))
}
