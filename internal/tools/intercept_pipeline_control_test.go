// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
)

// pipelineControlDeps satisfies ClientDeps (via the embedded nil-method
// fakeDeps) AND pipelinePauser (backed by a real Pipeline's circuit breaker),
// so the manage dispatch + handlers run against a genuine breaker.
type pipelineControlDeps struct {
	*fakeDeps
	p *pipeline.Pipeline
}

func (d *pipelineControlDeps) PausePipeline(reason string) { d.p.PausePipeline(reason) }
func (d *pipelineControlDeps) ResumePipeline()             { d.p.ResumePipeline() }
func (d *pipelineControlDeps) PipelineStatus() (pipeline.PipelineStatus, bool) {
	return d.p.PipelineStatus(), true
}

func newPipelineControlDeps() *pipelineControlDeps {
	return &pipelineControlDeps{
		fakeDeps: &fakeDeps{},
		p:        pipeline.New(pipeline.Config{}, nil, nil, nil),
	}
}

func pipelineControlCall(t *testing.T, deps ClientDeps, op, reason string) kgtools.ToolResult {
	t.Helper()
	args := map[string]string{"operation": op}
	if reason != "" {
		args["reason"] = reason
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	handled, res := InterceptManage(deps, kgtools.CallToolParams{Name: "manage", Arguments: raw})
	if !handled {
		t.Fatalf("manage(%s) was not handled by InterceptManage", op)
	}
	return res
}

// TestManagePausePipelineLifecycle drives the full pause -> status -> resume ->
// status lifecycle through InterceptManage against a real breaker.
func TestManagePausePipelineLifecycle(t *testing.T) {
	deps := newPipelineControlDeps()

	// Initially running.
	st := resultTextLocal(pipelineControlCall(t, deps, "pipeline_status", ""))
	if !strings.Contains(st, "RUNNING") {
		t.Fatalf("initial pipeline_status = %q, want RUNNING", st)
	}

	// Pause with an operator reason.
	pa := pipelineControlCall(t, deps, "pause_pipeline", "quota investigation")
	if pa.IsError {
		t.Fatalf("pause_pipeline returned error: %q", resultTextLocal(pa))
	}
	if txt := resultTextLocal(pa); !strings.Contains(txt, "PAUSED") || !strings.Contains(txt, "resume_pipeline") {
		t.Fatalf("pause_pipeline text = %q, want PAUSED + resume instruction", txt)
	}

	// Status now reports PAUSED + reason + resume instruction.
	st = resultTextLocal(pipelineControlCall(t, deps, "pipeline_status", ""))
	for _, want := range []string{"PAUSED", "quota investigation", "resume_pipeline"} {
		if !strings.Contains(st, want) {
			t.Fatalf("paused pipeline_status = %q, missing %q", st, want)
		}
	}

	// Resume re-enables.
	re := pipelineControlCall(t, deps, "resume_pipeline", "")
	if re.IsError {
		t.Fatalf("resume_pipeline returned error: %q", resultTextLocal(re))
	}
	if !strings.Contains(resultTextLocal(re), "RESUMED") {
		t.Fatalf("resume_pipeline text = %q, want RESUMED", resultTextLocal(re))
	}

	// Status back to running.
	st = resultTextLocal(pipelineControlCall(t, deps, "pipeline_status", ""))
	if !strings.Contains(st, "RUNNING") {
		t.Fatalf("post-resume pipeline_status = %q, want RUNNING", st)
	}
}

// TestManagePausePipelineDefaultReason verifies an empty reason falls back to
// the generic manual-pause string.
func TestManagePausePipelineDefaultReason(t *testing.T) {
	deps := newPipelineControlDeps()
	pipelineControlCall(t, deps, "pause_pipeline", "")
	st := resultTextLocal(pipelineControlCall(t, deps, "pipeline_status", ""))
	if !strings.Contains(st, manualPauseReason) {
		t.Fatalf("default-reason pipeline_status = %q, want %q", st, manualPauseReason)
	}
}

// TestManagePipelineControlDegradedDeps verifies the handlers degrade to an
// errorResult when deps do not satisfy pipelinePauser.
func TestManagePipelineControlDegradedDeps(t *testing.T) {
	deps := &fakeDeps{} // satisfies ClientDeps but NOT pipelinePauser
	for _, op := range []string{"pause_pipeline", "resume_pipeline", "pipeline_status"} {
		res := pipelineControlCall(t, deps, op, "")
		if !res.IsError {
			t.Fatalf("manage(%s) on degraded deps should error, got %q", op, resultTextLocal(res))
		}
	}
}

// TestStalenessFooterSurfacesPausedPipeline verifies the search staleness
// footer emits the loud paused line when the pipeline is paused, even with NO
// code-staleness metadata (nil exec + empty repo => empty code footer).
func TestStalenessFooterSurfacesPausedPipeline(t *testing.T) {
	deps := newPipelineControlDeps()
	deps.p.PausePipeline("circuit-break test reason")

	res := textResult("search results body")
	out := appendStalenessFooter(context.Background(), deps, nil, "", res)
	txt := resultTextLocal(out)
	for _, want := range []string{"search results body", "PAUSED", "circuit-break test reason", "resume_pipeline"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("paused footer = %q, missing %q", txt, want)
		}
	}
}

// TestStalenessFooterNoSpuriousLineWhenRunning verifies that, with no code
// metadata and a RUNNING pipeline, the footer leaves the body unchanged.
func TestStalenessFooterNoSpuriousLineWhenRunning(t *testing.T) {
	deps := newPipelineControlDeps() // pipeline running (not paused)

	res := textResult("search results body")
	out := appendStalenessFooter(context.Background(), deps, nil, "", res)
	if txt := resultTextLocal(out); txt != "search results body" {
		t.Fatalf("running-pipeline footer mutated body: %q", txt)
	}
}
