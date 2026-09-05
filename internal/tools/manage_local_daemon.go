package tools

import (
	"context"
	"maps"

	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
)

// addLocalDaemonJSON populates the LOCAL-DAEMON facts a running local daemon
// knows independent of cloud login — pid, graph_path, the local
// node/edge/vector/bm25 counts, the client-side pipeline counters +
// pipeline_enabled, transcript-upload health, in-flight/last collect runs, the
// per-graph coverage[] table, and the doctor[] diagnostic block — into m. It is
// called from BOTH the logged-out local status branch AND the logged-in cloud
// branch (CEO decision: always show local-daemon fields) so a logged-in user's
// Daemon Status page still populates the Doctor / Pipeline / Coverage cards + the
// header. The cloud caller layers backend/host + CLOUD graph node/edge/vector
// totals ON TOP after this returns.
//
// Degrades cleanly: when the local graph server is not up (nil LocalLiveness or
// not Healthy) the server-derived fields (pid, graph_path, local counts) are
// omitted — the web types mark them optional. pipeline_enabled always lands
// (false when no pipeline is wired). Each overlay reuses the existing
// optional-interface degrade helpers (overlayPipelineMetrics /
// transcriptUploadHealth / collectRunSnapshot / collectCoverageRows /
// doctorChecks) — one call site each, so this is the single place the Phase-1
// contract rule (every web field present on every path) is enforced.
func addLocalDaemonJSON(ctx context.Context, deps ClientDeps, m map[string]any) {
	if gc := deps.LocalLiveness(); gc != nil && gc.Healthy() {
		if status, err := gc.Status(); err == nil {
			// Copy the local server-status core (pid/graph_path + local
			// node/edge/vector/bm25 counts). A cloud caller overwrites
			// nodes/edges/binary_vectors with CLOUD totals after this returns.
			maps.Copy(m, status)
		}
	}
	_, pipelineOK := overlayPipelineMetrics(deps, m)
	m["pipeline_enabled"] = pipelineOK
	if th, ok := transcriptUploadHealth(deps); ok {
		addTranscriptHealthJSON(m, th)
	}
	if uh, ok := updateCheckHealth(deps); ok {
		addUpdateHealthJSON(m, uh)
	}
	if runs, ok := collectRunSnapshot(deps); ok {
		addCollectRunsJSON(m, runs)
	}
	if rows := collectCoverageRows(ctx, deps); len(rows) > 0 {
		m["coverage"] = rows
	}
	if checks, ok := doctorChecks(ctx, deps); ok {
		m["doctor"] = checks
	}
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
