// SPDX-License-Identifier: Apache-2.0

package pipeline

import "sync/atomic"

// metricsState holds the live per-counter atomics. One owning Pipeline
// holds one *metricsState; collectors and workers increment via the
// helper methods. Snapshot() reads atomically into a Metrics struct for
// status reporting.
//
// Channel-depth counters (summaryQueued / embedQueued) are NOT incremented
// directly — Pipeline.Metrics() reads len(p.summaryCh) / len(p.embedCh)
// at snapshot time. Keeping them OUT of metricsState avoids the producer/
// consumer race between channel-send and counter-bump.
type metricsState struct {
	summaryRunning   atomic.Int64
	summarySucceeded atomic.Int64
	summaryFailed    atomic.Int64
	embedRunning     atomic.Int64
	embedSucceeded   atomic.Int64
	embedFailed      atomic.Int64
}

// snapshot returns a Metrics value with the current atomic counter
// readings. Channel-depth fields are zero-filled here; the caller
// (Pipeline.Metrics) overlays len(channel) before returning.
func (m *metricsState) snapshot() Metrics {
	return Metrics{
		SummaryRunning:   m.summaryRunning.Load(),
		SummarySucceeded: m.summarySucceeded.Load(),
		SummaryFailed:    m.summaryFailed.Load(),
		EmbedRunning:     m.embedRunning.Load(),
		EmbedSucceeded:   m.embedSucceeded.Load(),
		EmbedFailed:      m.embedFailed.Load(),
	}
}

// Per-counter increment helpers. Workers call these around their LLM/embedder
// invocation and on terminal-failure marker write. Transient errors do not
// touch the counters (the next tick re-discovers the same node).
func (m *metricsState) summaryRun()  { m.summaryRunning.Add(1) }
func (m *metricsState) summaryDone() { m.summaryRunning.Add(-1) }
func (m *metricsState) summaryOK()   { m.summarySucceeded.Add(1) }
func (m *metricsState) summaryFail() { m.summaryFailed.Add(1) }
func (m *metricsState) embedRun()    { m.embedRunning.Add(1) }
func (m *metricsState) embedDone()   { m.embedRunning.Add(-1) }
func (m *metricsState) embedOK()     { m.embedSucceeded.Add(1) }
func (m *metricsState) embedFail()   { m.embedFailed.Add(1) }

func (m *metricsState) resetFailed() {
	m.summaryFailed.Store(0)
	m.embedFailed.Store(0)
}
