// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{Name: "manage", Arguments: raw})
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
// code-staleness metadata (nil exec + empty repo => empty code footer). Manual
// pause is whole-pipeline (both axes), so the footer names BOTH axes while
// preserving the verbatim reason.
func TestStalenessFooterSurfacesPausedPipeline(t *testing.T) {
	deps := newPipelineControlDeps()
	deps.p.PausePipeline("circuit-break test reason")

	res := textResult("search results body")
	out := appendStalenessFooter(context.Background(), deps, nil, "", "", res)
	txt := resultTextLocal(out)
	for _, want := range []string{"search results body", "PAUSED", "circuit-break test reason", "resume_pipeline", "summary axis", "embed axis"} {
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
	out := appendStalenessFooter(context.Background(), deps, nil, "", "", res)
	if txt := resultTextLocal(out); txt != "search results body" {
		t.Fatalf("running-pipeline footer mutated body: %q", txt)
	}
}

// fixedStatusDeps is a pipelinePauser fake that returns a hand-built per-axis
// PipelineStatus, so the renderers can be exercised against a SUMMARY-only
// auto-trip (the embed axis running) without driving the worker pool.
type fixedStatusDeps struct {
	*fakeDeps
	st pipeline.PipelineStatus
}

func (d *fixedStatusDeps) PausePipeline(string) {}
func (d *fixedStatusDeps) ResumePipeline()      {}
func (d *fixedStatusDeps) PipelineStatus() (pipeline.PipelineStatus, bool) {
	return d.st, true
}

// summaryAutoTripStatus builds a status snapshot for a SUMMARY-axis auth/quota
// auto-trip while the embed axis runs: summary paused with a dominant-class
// reason + breakdown, embed clear, aggregate taken from the summary axis.
func summaryAutoTripStatus() pipeline.PipelineStatus {
	summary := pipeline.AxisStatus{
		Paused:        true,
		Reason:        "full error round — 18/20 auth/quota",
		Since:         time.Unix(1700000000, 0),
		DominantClass: pipeline.ClassAuthQuota,
		DominantCount: 18,
		Breakdown:     "auth=18, timeout=2",
	}
	return pipeline.PipelineStatus{
		Paused:        true,
		Reason:        summary.Reason,
		Since:         summary.Since,
		DominantClass: summary.DominantClass,
		DominantCount: summary.DominantCount,
		Breakdown:     summary.Breakdown,
		Summary:       summary,
		Embed:         pipeline.AxisStatus{}, // running
	}
}

// TestPipelineStatusNamesSummaryAxisOnly is the fails-when-absent guard for the
// per-axis carry-through: with the SUMMARY axis auto-tripped on an auth/quota
// window, pipeline_status names the SUMMARY axis paused AND carries its dominant
// class + breakdown (the dominant-class surfacing), while the embed axis renders
// RUNNING. RED if the carry-through drops the DominantClass/Breakdown or fails to
// name the per-axis state.
func TestPipelineStatusNamesSummaryAxisOnly(t *testing.T) {
	deps := &fixedStatusDeps{fakeDeps: &fakeDeps{}, st: summaryAutoTripStatus()}

	txt := resultTextLocal(handlePipelineStatus(deps, ""))
	for _, want := range []string{"summary: PAUSED", "auth/quota", "auth=18, timeout=2", "embed: RUNNING"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("pipeline_status = %q, missing %q", txt, want)
		}
	}
}

// TestPipelinePausedFooterNamesSummaryAxisOnly is the search-footer counterpart:
// the footer names the SUMMARY axis paused (so an operator knows embeddings still
// flow) and preserves the verbatim reason, with no embed-axis paused line.
func TestPipelinePausedFooterNamesSummaryAxisOnly(t *testing.T) {
	deps := &fixedStatusDeps{fakeDeps: &fakeDeps{}, st: summaryAutoTripStatus()}

	footer := pipelinePausedFooter(deps)
	if !strings.Contains(footer, "summary axis PAUSED") {
		t.Fatalf("footer %q does not name the summary axis paused", footer)
	}
	if !strings.Contains(footer, "full error round — 18/20 auth/quota") {
		t.Fatalf("footer %q dropped the verbatim summary reason", footer)
	}
	if strings.Contains(footer, "embed axis PAUSED") {
		t.Fatalf("footer %q names the embed axis paused, but only summary tripped", footer)
	}
}

// TestHandlePipelineStatus_JSON is the step criterion for the format:json
// branch: the output unmarshals to snake_case keys (guarding against the
// default-PascalCase-marshal regression the tagless pipeline structs would
// cause), dominant_class is a STRING (ErrClass.Label()) not a number, a disabled
// pipeline degrades to {enabled:false}, and the text format output is unchanged.
func TestHandlePipelineStatus_JSON(t *testing.T) {
	t.Run("summary-axis auto-trip serializes snake_case with string dominant_class", func(t *testing.T) {
		deps := &fixedStatusDeps{fakeDeps: &fakeDeps{}, st: summaryAutoTripStatus()}

		res := handlePipelineStatus(deps, "json")
		if res.IsError {
			t.Fatalf("json pipeline_status errored: %q", resultTextLocal(res))
		}
		// Decode into a raw map so the KEYS themselves are asserted snake_case —
		// a PascalCase regression (tagless struct marshal) fails these lookups.
		var got map[string]any
		if err := json.Unmarshal([]byte(resultTextLocal(res)), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if got["enabled"] != true {
			t.Fatalf("enabled = %v, want true", got["enabled"])
		}
		if got["paused"] != true {
			t.Fatalf("paused = %v, want true", got["paused"])
		}
		summary, ok := got["summary"].(map[string]any)
		if !ok {
			t.Fatalf("summary axis missing / wrong shape: %v", got["summary"])
		}
		if summary["paused"] != true {
			t.Fatalf("summary.paused = %v, want true", summary["paused"])
		}
		// dominant_class must be the STRING label, never the ErrClass int.
		dc, ok := summary["dominant_class"].(string)
		if !ok {
			t.Fatalf("summary.dominant_class = %v (%T), want a string via Label()", summary["dominant_class"], summary["dominant_class"])
		}
		if !strings.Contains(dc, "auth/quota") {
			t.Fatalf("summary.dominant_class = %q, want the auth/quota label", dc)
		}
		if _, hasActive := summary["active_summarizer"]; !hasActive {
			t.Fatalf("summary axis missing active_summarizer key: %v", summary)
		}
		// Embed axis is running (not paused) in this fixture.
		embed, ok := got["embed"].(map[string]any)
		if !ok {
			t.Fatalf("embed axis missing / wrong shape: %v", got["embed"])
		}
		if embed["paused"] != false {
			t.Fatalf("embed.paused = %v, want false", embed["paused"])
		}
	})

	t.Run("disabled pipeline degrades to enabled:false", func(t *testing.T) {
		disabled := &disabledPipelineDeps{fakeDeps: &fakeDeps{}}
		res := handlePipelineStatus(disabled, "json")
		if res.IsError {
			t.Fatalf("disabled json pipeline_status errored: %q", resultTextLocal(res))
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(resultTextLocal(res)), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if got["enabled"] != false {
			t.Fatalf("disabled enabled = %v, want false", got["enabled"])
		}
	})

	t.Run("text format unchanged", func(t *testing.T) {
		deps := &fixedStatusDeps{fakeDeps: &fakeDeps{}, st: summaryAutoTripStatus()}
		txt := resultTextLocal(handlePipelineStatus(deps, ""))
		for _, want := range []string{"summary: PAUSED", "embed: RUNNING", "resume_pipeline"} {
			if !strings.Contains(txt, want) {
				t.Fatalf("text pipeline_status = %q, missing %q", txt, want)
			}
		}
	})
}

// disabledPipelineDeps is a pipelinePauser whose PipelineStatus reports NOT wired
// (the --no-llm-pipeline / no-provider degrade), so the json branch emits
// {enabled:false}.
type disabledPipelineDeps struct{ *fakeDeps }

func (d *disabledPipelineDeps) PausePipeline(string) {}
func (d *disabledPipelineDeps) ResumePipeline()      {}
func (d *disabledPipelineDeps) PipelineStatus() (pipeline.PipelineStatus, bool) {
	return pipeline.PipelineStatus{}, false
}

// TestRenderAxisStatus_ActiveSummarizerOnRunningPath asserts the live active
// summarizer entry is surfaced on the summary axis even while it is RUNNING —
// failover happens during normal operation, so an operator must see the current
// entry without waiting for a pause. The paused path also carries it.
func TestRenderAxisStatus_ActiveSummarizerOnRunningPath(t *testing.T) {
	running := renderAxisStatus("summary", pipeline.AxisStatus{
		Paused:           false,
		ActiveSummarizer: "openai/gpt-5-mini",
	})
	if !strings.Contains(running, "RUNNING") {
		t.Fatalf("running render = %q; want RUNNING", running)
	}
	if !strings.Contains(running, "Active summarizer: openai/gpt-5-mini") {
		t.Fatalf("running render = %q; want the active-summarizer line", running)
	}

	paused := renderAxisStatus("summary", pipeline.AxisStatus{
		Paused:           true,
		Reason:           "chain exhausted",
		Since:            time.Now(),
		ActiveSummarizer: "gemini/gemini-2.5-flash",
	})
	if !strings.Contains(paused, "Active summarizer: gemini/gemini-2.5-flash") {
		t.Fatalf("paused render = %q; want the active-summarizer line", paused)
	}

	// No chain wired (embed axis / single entry): no active-summarizer line.
	plain := renderAxisStatus("embed", pipeline.AxisStatus{Paused: false})
	if strings.Contains(plain, "Active summarizer") {
		t.Fatalf("plain render = %q; want NO active-summarizer line when empty", plain)
	}
}
