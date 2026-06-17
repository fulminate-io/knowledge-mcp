// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
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

// handlePipelineStatus reports the PER-AXIS paused/running state of the summary
// and embed breakers, including each paused axis's reason, since, and
// dominant-class breakdown, plus how to resume. The breakers are independent, so
// one axis can be paused while the other runs — the render names each axis
// explicitly. Degrades to "(pipeline disabled)" when no pipeline is wired
// (--no-llm-pipeline, or neither summarizer nor embedder configured at boot).
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
		"Pipeline: PAUSED\n%s%s  %s",
		renderAxisStatus("summary", st.Summary),
		renderAxisStatus("embed", st.Embed),
		resumeHint))
}

// renderAxisStatus renders one axis's status block. A running axis reads as a
// single RUNNING line; a paused axis renders its since, reason, and (when more
// than one error class is present) its PRE-RENDERED per-class Breakdown. The
// breakdown is the pipeline-side string tally — tools reads only the string
// fields (Reason, Breakdown) and never the typed error-class enum.
//
// The live ACTIVE summarizer entry (set only on the summary axis when a fallback
// chain is wired) is surfaced on BOTH the RUNNING and PAUSED paths: failover
// happens during normal operation, so an operator must see which entry is
// serving even while the axis is running. Empty (no chain / single entry / embed
// axis) renders nothing.
func renderAxisStatus(axis string, a pipeline.AxisStatus) string {
	activeLine := ""
	if a.ActiveSummarizer != "" {
		activeLine = fmt.Sprintf("    Active summarizer: %s\n", a.ActiveSummarizer)
	}
	if !a.Paused {
		return fmt.Sprintf("  %s: RUNNING\n%s", axis, activeLine)
	}
	breakdownLine := ""
	if a.Breakdown != "" {
		breakdownLine = fmt.Sprintf("    Breakdown: %s\n", a.Breakdown)
	}
	return fmt.Sprintf(
		"  %s: PAUSED since %s\n    Reason: %s\n%s%s",
		axis, a.Since.Format("2006-01-02 15:04:05 MST"), a.Reason, breakdownLine, activeLine)
}
