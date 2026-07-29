// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import "sort"

// This file holds the per-tool LATENCY-DISTRIBUTION detector (p50/p90/p99) and the
// per-tool TIME-TOTAL detector — the "where does wall-time go" pair — as pure-Go folds
// over the loaded *corpus. Both consume accumulators built from idle-guarded rows so a
// genuine long run keeps its full weight while an idle-straddling row is excluded.

// ToolLatencyRow is one tool's trustworthy-execution-time distribution: the call count
// plus p50/p90/p99 (milliseconds), approximated from the log2 latency histogram.
type ToolLatencyRow struct {
	ToolName string `json:"tool_name"`
	Count    int64  `json:"count"`
	P50      int64  `json:"p50"`
	P90      int64  `json:"p90"`
	P99      int64  `json:"p99"`
}

// ToolTimeTotalRow is one tool's TOTAL trustworthy wall-time — the direct
// "which tools consume the most time" ranking the synthesis stage reasons over.
type ToolTimeTotalRow struct {
	ToolName        string `json:"tool_name"`
	TotalDurationMs int64  `json:"total_duration_ms"`
	CallCount       int64  `json:"call_count"`
}

// toolLatency computes the per-tool p50/p90/p99 latency distribution from the corpus
// latency histogram — mirroring the agent's QueryToolLatency (rollup_insights.go:49):
// histPercentile over each tool's per-bucket counts, Count = the tool's trustworthy total.
// Only trustworthy named-tool rows are in the histogram (built at fold-in). Ordered by p90
// desc, then tool asc (the tie-break).
func (c *corpus) toolLatency() []ToolLatencyRow {
	out := make([]ToolLatencyRow, 0, len(c.latencyHist))
	for tool, counts := range c.latencyHist {
		total := c.latencyTotal[tool]
		out = append(out, ToolLatencyRow{
			ToolName: tool,
			Count:    total,
			P50:      histPercentile(counts, total, 0.5),
			P90:      histPercentile(counts, total, 0.9),
			P99:      histPercentile(counts, total, 0.99),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].P90 != out[j].P90 {
			return out[i].P90 > out[j].P90
		}
		return out[i].ToolName < out[j].ToolName
	})
	return out
}

// toolTimeTotals sums the trustworthy per-tool wall-time (TotalDurationMs) with the all-row
// CallCount — mirroring the agent's toolTimeTotalPG (rollup_insights.go:100): SUM of the
// idle-guarded durations, count over ALL rows with that tool (an interrupted/idle row still
// counts, contributing 0ms). Ordered by total desc, then tool asc.
func (c *corpus) toolTimeTotals() []ToolTimeTotalRow {
	out := make([]ToolTimeTotalRow, 0, len(c.toolTime))
	for tool, acc := range c.toolTime {
		out = append(out, ToolTimeTotalRow{ToolName: tool, TotalDurationMs: acc.trustSum, CallCount: acc.count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalDurationMs != out[j].TotalDurationMs {
			return out[i].TotalDurationMs > out[j].TotalDurationMs
		}
		return out[i].ToolName < out[j].ToolName
	})
	return out
}
