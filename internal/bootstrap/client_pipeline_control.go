// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "github.com/fulminate-io/knowledge-mcp/internal/pipeline"

// Manual pipeline-control delegates. These satisfy the optional tools
// interfaces (pipelinePauser / pipelineStatuser) the pause_pipeline /
// resume_pipeline / pipeline_status manage ops read. Each is nil-guarded so a
// boot without an LLM pipeline (--no-llm-pipeline, or neither summarizer nor
// embedder configured) degrades cleanly rather than panicking.

// PausePipeline latches the LLM pipeline paused with an operator-supplied
// reason. Satisfies the optional tools.pipelinePauser interface called by the
// pause_pipeline manage op. Nil-guarded: a no-op when no pipeline is wired.
func (c *client) PausePipeline(reason string) {
	if c.pipeline != nil {
		c.pipeline.PausePipeline(reason)
	}
}

// ResumePipeline clears the paused latch — the only exit from a circuit break.
// Satisfies the optional tools.pipelinePauser interface called by the
// resume_pipeline manage op. Nil-guarded: a no-op when no pipeline is wired.
func (c *client) ResumePipeline() {
	if c.pipeline != nil {
		c.pipeline.ResumePipeline()
	}
}

// PipelineStatus reports the pipeline's paused state for the pipeline_status
// manage op and the search staleness footer. ok=false when no pipeline is
// wired (mirrors the (Metrics,bool) shape of PipelineMetrics), letting callers
// distinguish "running" from "pipeline never wired."
func (c *client) PipelineStatus() (pipeline.PipelineStatus, bool) {
	if c.pipeline == nil {
		return pipeline.PipelineStatus{}, false
	}
	return c.pipeline.PipelineStatus(), true
}
