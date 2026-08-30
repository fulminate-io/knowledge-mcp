// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// pipeline_control.go carries Pipeline's small operator-control surface —
// metrics snapshot, failed-counter reset, whole-pipeline pause/resume, and the
// late-wired setter seams — split from pipeline.go to keep that file (the
// struct and lifecycle) under the repo's 500-line ceiling.

// Metrics returns a Snapshot of the current pipeline counters with the
// channel-depth fields populated from len(channel).
func (p *Pipeline) Metrics() Metrics {
	m := p.metrics.snapshot()
	m.SummaryQueued = int64(len(p.summaryCh))
	m.EmbedQueued = int64(len(p.embedCh))
	return m
}

// ResetFailedCounters zeroes the session-lifetime failed counters. Called
// after clear_llm_failures removes the on-disk markers so the status
// output reflects the live state.
func (p *Pipeline) ResetFailedCounters() {
	p.metrics.resetFailed()
}

// PausePipeline latches BOTH axes paused with an operator-supplied reason —
// manual pause is deliberately WHOLE-PIPELINE (it pauses the summary AND embed
// breakers), unlike an auto-trip which is per-axis. Both summary and embed
// workers block at their wait sites until ResumePipeline is called. Manual pause
// and an auto-trip share the same per-axis latch — there is no self-heal from
// either.
func (p *Pipeline) PausePipeline(reason string) {
	p.summaryCircuit.pause(reason)
	p.embedCircuit.pause(reason)
}

// ResumePipeline clears the paused latch on BOTH axes and wakes every parked
// worker. It is the ONLY exit from a circuit break (auto-trip or manual pause),
// and resumes whichever axis/axes are paused regardless of how they tripped.
func (p *Pipeline) ResumePipeline() {
	p.summaryCircuit.resume()
	p.embedCircuit.resume()
}

// SetActiveSummarizer installs the live active-summarizer accessor the wiring
// layer builds over the fallback chain's health state. fn returns the
// "provider/model" label of the highest-priority healthy entry (or "" when the
// chain is exhausted). Called once at wiring time, before status calls begin.
func (p *Pipeline) SetActiveSummarizer(fn func() string) {
	p.activeSummarizer = fn
}

// SetSegmentNudger installs the segment reconcile-nudge recorder the gen poll
// calls when a graph's segment cheap-tick stamp advances past the last stamp this
// client poked on. Production wires the segment manager's NudgeSegmentDelta;
// leaving it unset (a degraded client with no segment manager) means the segment
// axis is still SAMPLED and tracked but nudges nothing.
//
// Called once at wiring time, before the poll loop starts. Takes genMu because the
// poll reads the field.
func (p *Pipeline) SetSegmentNudger(fn func(gt kgtypes.GraphType, name string)) {
	p.genMu.Lock()
	defer p.genMu.Unlock()
	p.segmentNudge = fn
}

// segmentNudger reads the installed recorder under genMu.
func (p *Pipeline) segmentNudger() func(gt kgtypes.GraphType, name string) {
	p.genMu.Lock()
	defer p.genMu.Unlock()
	return p.segmentNudge
}
