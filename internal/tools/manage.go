// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	clientlinker "github.com/fulminate-io/knowledge-mcp/internal/linker"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/profiling"
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
//   - pprof_start / pprof_stop: bracket a CPU profile of the stdio client
//     (collector work runs here), retrievable from the loopback pprof
//     endpoint. See package profiling.
//
// There is no manage(reindex) operation — the per-graph collector + global
// pipeline drains naturally. Code collect runs via the dedicated `collect` MCP
// tool (or `make collect`) which calls codegraph.Sync / SyncBranch against
// RemoteUploadSink.
func InterceptManage(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "manage" {
		return false, kgtools.ToolResult{}
	}
	var a manageArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	switch a.Operation {
	case "status":
		return true, handleServerStatus(deps, a.Format)
	case "pprof_start":
		return true, handlePprofStart()
	case "pprof_stop":
		return true, handlePprofStop()
	case "link":
		return true, handleClientLinker(deps, a)
	case "promote_metadata":
		return true, handleManagePromoteMetadata(context.Background(), deps, a, params.Arguments)
	case "clear_llm_failures":
		return true, handleClientClearLLMFailures(context.Background(), deps, a)
	case "pause_pipeline":
		return true, handlePausePipeline(deps, a)
	case "resume_pipeline":
		return true, handleResumePipeline(deps)
	case "pipeline_status":
		return true, handlePipelineStatus(deps)
	case "set_metadata_overrides":
		return true, handleClientSetMetadataOverrides(context.Background(), deps, a)
	case "delete_branch":
		return true, handleClientDeleteBranch(context.Background(), deps, a)
	case "list_branches":
		return true, handleClientListBranches(context.Background(), deps, a)
	case "prune":
		return true, handleClientPrune(context.Background(), deps, a)
	case "rebuild_cache":
		return true, handleClientRebuildCache(context.Background(), deps, a)
	case "rebuild_segments":
		return true, handleClientRebuildSegments(context.Background(), deps, a)
	case "drop_graph":
		return true, handleClientDropGraph(context.Background(), deps, a)
	}
	return false, kgtools.ToolResult{}
}

// handleClientLinker dispatches manage(link) to the client-side cross-
// graph linker (cmd/knowledge/internal/linker). The server's
// handleLinker is now stubbed to a client-intercept-required sentinel — the
// linker body lives client-side because it walks the graphs via gc.Call
// and emits derived edges through mutate(link, link_graph:"linkage").
func handleClientLinker(deps ClientDeps, a manageArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("manage(link): GraphCaller is unavailable — the client is running in degraded mode")
	}
	res, err := clientlinker.RunAll(context.Background(), gc, clientlinker.LinkOptions{})
	if err != nil {
		return errorResult("manage(link): " + err.Error())
	}
	if a.Format == "json" {
		payload := map[string]any{
			"image_links":             res.ImageLinks,
			"helm_links":              res.HelmLinks,
			"dockerfile_links":        res.DockerfileLinks,
			"workload_identity_links": res.WorkloadIdentityLinks,
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
	total := res.ImageLinks + res.HelmLinks + res.DockerfileLinks + res.WorkloadIdentityLinks
	return textResult(fmt.Sprintf(
		"Linker complete: %d total links (image=%d, helm=%d, dockerfile=%d, workload_identity=%d), errors=%d",
		total, res.ImageLinks, res.HelmLinks, res.DockerfileLinks, res.WorkloadIdentityLinks, len(res.Errors)))
}

// handlePprofStart begins a CPU profile of the client process and lazily
// brings up the loopback pprof endpoint so the result is fetchable.
func handlePprofStart() kgtools.ToolResult {
	addr, err := profiling.StartCPU()
	if err != nil {
		return errorResult("pprof_start: " + err.Error())
	}
	return textResult(fmt.Sprintf(
		"CPU profile started. Reproduce the slow operation now, then call manage(operation:\"pprof_stop\"). pprof endpoint: http://%s/debug/pprof/",
		addr))
}

// handlePprofStop stops the CPU profile and reports where to pull it.
func handlePprofStop() kgtools.ToolResult {
	url, size, err := profiling.StopCPU()
	if err != nil {
		return errorResult("pprof_stop: " + err.Error())
	}
	return textResult(fmt.Sprintf(
		"CPU profile stopped (%d bytes). Fetch + open it:\n  go tool pprof %s\nor save a copy:\n  curl -s %s -o cpu.pprof",
		size, url, url))
}

// manageArgs covers the fields the client-side manage intercepts read,
// including the log-backend + log-graph management fields. The
// configure_log_backend / list_log_backends / list_logs / discard_logs
// dispatchers (tools_logs_manage_backend.go, tools_logs_manage_graphs.go) reach
// for these same field names.
type manageArgs struct {
	Operation   string `json:"operation"`
	Graph       string `json:"graph"`
	Name        string `json:"name"`
	Branch      string `json:"branch"`
	Root        string `json:"root"`
	Format      string `json:"format"`
	Provider    string `json:"provider"`
	URL         string `json:"url"`
	AuthType    string `json:"auth_type"`
	Credential  string `json:"credential"`
	KubeContext string `json:"kube_context"`
	// promote_metadata flags read by the client-side
	// intercept to gate batch-narrative emission and re-marshal the
	// payload with format=json forced.
	DryRun bool `json:"dry_run"`
	Force  bool `json:"force"`

	// set_metadata_overrides force-lists, read by the client
	// intercept and lowered onto the Index RPC params payload. Mirror the
	// server manageArgs fields handleSetMetadataOverrides reads.
	ForceScalar []string `json:"force_scalar"`
	ForceEdge   []string `json:"force_edge"`

	// prune cutoff: a relative window ("24h"/"2d") or an absolute RFC3339
	// timestamp. Tombstones tombstoned BEFORE it are hard-deleted; empty
	// prunes ALL tombstoned nodes.
	Before string `json:"before"`

	// pause_pipeline operator reason, surfaced verbatim by pipeline_status.
	// Empty falls back to a generic "manually paused by operator" string.
	Reason string `json:"reason"`
}

// handleServerStatus reports liveness (and, when the server is up, basic
// graph stats). Rendered either as text or JSON per the format arg.
//
// Pipeline counters (summary_*/embed_*) come from the CLIENT-side
// pipeline via overlayPipelineMetrics. The server's response always
// returns zero for those fields — the LLM pipeline moved to the stdio
// client so its live counts only exist here. When the
// pipeline is disabled (--no-llm-pipeline, or neither summarizer nor
// embedder configured), the counters render as "(pipeline disabled)"
// instead of zeros so the operator can tell the difference between
// "queue empty" and "pipeline never wired."
func handleServerStatus(deps ClientDeps, format string) kgtools.ToolResult {
	// Logged-in users target the CLOUD graph: report node/edge/vector/bm25
	// counts via the already-routed Stats RPC, omitting local-daemon-only
	// fields (PID, graph_path, pipeline counters). The type-assert + the
	// loggedIn check degrade to the existing local-daemon path below when
	// either misses, so the logged-out behavior is unchanged.
	if csi, ok := deps.(cloudStatusInfo); ok {
		if loggedIn, host := csi.CloudStatusInfo(); loggedIn {
			return handleCloudStatus(deps, host, format)
		}
	}
	gc := deps.LocalLiveness()
	if !gc.Healthy() {
		if format == "json" {
			return jsonResult(map[string]any{"status": "not_running"})
		}
		return textResult("Graph server: NOT RUNNING")
	}
	status, err := gc.Status()
	if err != nil {
		return errorResult("status failed: " + err.Error())
	}
	metrics, pipelineOK := overlayPipelineMetrics(deps, status)
	if format == "json" {
		status["status"] = "running"
		status["pipeline_enabled"] = pipelineOK
		return jsonResult(status)
	}
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
	return textResult(fmt.Sprintf(
		"Graph server: RUNNING\n  PID: %.0f\n  Nodes: %.0f\n  Edges: %.0f\n  Vectors: %.0f\n  BM25 docs: %.0f\n  Path: %s\n%s%s",
		status["pid"], status["nodes"], status["edges"], status["binary_vectors"], status["bm25_docs"], status["graph_path"],
		pipelineLine, renderLLMCoverage(context.Background(), deps)))
}

// handleCloudStatus reports the CLOUD graph stats for a logged-in user via
// the already-routed Stats RPC (deps.GraphCaller() is the *Router,
// which satisfies the statsRPC seam). It renders the shared
// engine.RenderStatsBreakdown body under a "Backend: cloud (<host>)"
// preamble — naturally emitting Nodes/Edges/Vectors/BM25 and OMITTING
// PID/graph_path/summary_*/embed_* because GraphStats carries none of those
// local-daemon fields. The empty GraphSelector targets the default
// knowledge graph, identical to intercept_query_stats.go.
func handleCloudStatus(deps ClientDeps, host, format string) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("manage(status): graph client unavailable")
	}
	sc, ok := gc.(statsRPC)
	if !ok {
		return errorResult("manage(status): stats seam unavailable")
	}
	resp, err := sc.Stats(context.Background(), &knowledgev1.StatsRequest{
		Target: &knowledgev1.GraphSelector{Graph: ""},
	})
	if err != nil {
		return errorResult("manage(status): cloud stats failed: " + err.Error())
	}
	stats := resp.GetGraphStats()
	if format == "json" {
		return jsonResult(map[string]any{
			"status":         "running",
			"backend":        "cloud",
			"host":           host,
			"nodes":          stats.GetNodeCount(),
			"edges":          stats.GetEdgeCount(),
			"binary_vectors": stats.GetBinaryVectorCount(),
		})
	}
	return textResult(fmt.Sprintf(
		"## Graph server: cloud\n  Backend: cloud (%s)\n\n%s%s",
		host, engine.RenderStatsBreakdown(stats), renderLLMCoverage(context.Background(), deps)))
}

// overlayPipelineMetrics reads the client-side LLM pipeline counters and
// merges them into the status map (so format=json carriers the live
// values too). Returns (Metrics, true) when the deps satisfy the
// optional pipelineMetricser interface AND the pipeline was wired;
// (zero, false) when either the type assert misses (test fakes) or the
// pipeline is disabled (--no-llm-pipeline). Callers render the disabled
// case visibly so it doesn't look like the pipeline silently failed.
func overlayPipelineMetrics(deps ClientDeps, status map[string]any) (pipeline.Metrics, bool) {
	pm, ok := deps.(pipelineMetricser)
	if !ok {
		return pipeline.Metrics{}, false
	}
	m, wired := pm.PipelineMetrics()
	if !wired {
		return pipeline.Metrics{}, false
	}
	status["summary_queued"] = float64(m.SummaryQueued)
	status["summary_running"] = float64(m.SummaryRunning)
	status["summary_succeeded"] = float64(m.SummarySucceeded)
	status["summary_failed"] = float64(m.SummaryFailed)
	status["embed_queued"] = float64(m.EmbedQueued)
	status["embed_running"] = float64(m.EmbedRunning)
	status["embed_succeeded"] = float64(m.EmbedSucceeded)
	status["embed_failed"] = float64(m.EmbedFailed)
	return m, true
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
