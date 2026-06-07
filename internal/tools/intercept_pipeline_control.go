// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// manualPauseReason is the default reason stamped when an operator runs
// pause_pipeline without supplying one.
const manualPauseReason = "manually paused by operator"

// resumeHint is the one-line instruction every paused-state render ends with,
// since resume_pipeline is the ONLY exit from a circuit break.
const resumeHint = "run manage(operation:\"resume_pipeline\") to re-enable."

// handlePausePipeline latches the LLM summary/embed worker pool paused. Both
// axes stop pulling new batches until resume_pipeline is called — there is no
// self-heal. The operator may pass a reason (surfaced by pipeline_status);
// empty falls back to a generic string.
func handlePausePipeline(deps ClientDeps, a manageArgs) kgtools.ToolResult {
	pp, ok := deps.(pipelinePauser)
	if !ok {
		return errorResult("manage(pause_pipeline): pipeline control is unavailable — the client is running in degraded mode")
	}
	reason := a.Reason
	if reason == "" {
		reason = manualPauseReason
	}
	pp.PausePipeline(reason)
	return textResult(fmt.Sprintf(
		"Pipeline PAUSED: %s\nThe summary + embed workers are latched off and will not process new batches. %s",
		reason, resumeHint))
}

// handleResumePipeline clears the paused latch (auto-trip or manual) and wakes
// every parked worker. It is the only exit from a circuit break.
func handleResumePipeline(deps ClientDeps) kgtools.ToolResult {
	pp, ok := deps.(pipelinePauser)
	if !ok {
		return errorResult("manage(resume_pipeline): pipeline control is unavailable — the client is running in degraded mode")
	}
	pp.ResumePipeline()
	return textResult("Pipeline RESUMED: the summary + embed workers are re-enabled and will resume processing on the next batch.")
}

// handlePipelineStatus reports whether the worker pool is running or latched
// paused, including the breaker's reason and how to resume. Degrades to
// "(pipeline disabled)" when no pipeline is wired (--no-llm-pipeline, or
// neither summarizer nor embedder configured at boot).
func handlePipelineStatus(deps ClientDeps) kgtools.ToolResult {
	pp, ok := deps.(pipelinePauser)
	if !ok {
		return errorResult("manage(pipeline_status): pipeline control is unavailable — the client is running in degraded mode")
	}
	st, wired := pp.PipelineStatus()
	if !wired {
		return textResult("Pipeline: (pipeline disabled) — no LLM summary/embed pipeline is wired in this process.")
	}
	if !st.Paused {
		return textResult("Pipeline: RUNNING — summary + embed workers are processing normally.")
	}
	return textResult(fmt.Sprintf(
		"Pipeline: PAUSED since %s\n  Reason: %s\n  %s",
		st.Since.Format("2006-01-02 15:04:05 MST"), st.Reason, resumeHint))
}
