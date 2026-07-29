// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// This file is the characterization safety net. It freezes the expected detector outputs
// over a RICHER, multi-file corpus computed to the platform's server-side usage-analytics
// semantics (histogram percentiles for ToolLatency) and asserts all 10 families
// value-for-value against the pure-Go engine (TestGoldenCorpus_PureGoParity).
//
// Provenance: these frozen goldens were FIRST characterized against the LIVE duckdb engine
// over this same corpus — the 9 non-percentile families are byte-identical between duckdb
// and the histogram method, which proved the goldens correct before the pure-Go
// reimplementation. With the duckdb engine removed, RunDetectors is the pure-Go engine, so
// the goldens are now asserted against it — INCLUDING the ToolLatency histogram family that
// duckdb's exact quantile_cont could not match.
//
// The corpus is DRIFT-FREE by construction: every file is written via
// transcripts.WriteSessionParquet, which always emits the is_meta column. The
// is_meta-MISSING drift case (missing→kept, a deliberate divergence from the old duckdb
// NOT-NULL exclusion) is NOT here — it lives in the dedicated drift test
// (detectors_drift_test.go).

// buildGoldenCorpus writes the two-file characterization corpus into root's
// {source}/{session}.parquet layout and returns an analyzer over it. Coverage designed
// into the corpus (research-identified gaps):
//   - odd-count tool (Bash: 3 trustworthy) + even-count tool (Grep: 4) for histPercentile.
//   - a percentile whose ceil(q*total) lands exactly on a bucket boundary (Grep p50:
//     ceil(0.5*4)=2, exactly the end of bucket 6).
//   - a (session,tool,hash) group with EXACTLY 2 runs (Edit/e1 — qualifies) and one with
//     EXACTLY 1 (Fetch/f1 — excluded) — the HAVING COUNT>1 boundary.
//   - an is_meta=true row (M1) excluded from every total (baseline exclusion, agrees
//     under both engines) + a <synthetic>-model row (SY1) excluded.
//   - a duplicate group with previews inserted in DESCENDING order to pin MIN(preview)
//     byte-wise (Edit/e1: "m-b" then "m-a" → MIN "m-a").
//   - 2 files, each session (SA, SB) SPANNING both files → cross-file aggregation.
//   - ORDER BY tie-breaks: ToolLatency p90-tie→tool asc (Bash/Grep at 4095; Edit/Fetch at
//     511), duplicate wasted+run_count-tie→session asc (SA/SB Edit at 1000/2),
//     SubagentWallTime wall-tie→agent asc (agent-2/agent-3 at 60000),
//     TokensBySubagentType (in+out)-tie→key asc (planner/tester at 300).
func buildGoldenCorpus(t *testing.T) *Service {
	t.Helper()
	const m = "m"
	root := t.TempDir()

	// File 1: latency/time-total tool rows (empty hash → not duplicates) + the duplicate
	// groups. SA and SB both contribute rows here.
	f1 := []transcripts.Row{
		// SA Bash: 3 trustworthy (odd count) — 1000,1000,3000.
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:00Z"), ToolName: "Bash", DurationMs: 1000},
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:01Z"), ToolName: "Bash", DurationMs: 1000},
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:02Z"), ToolName: "Bash", DurationMs: 3000},
		// SA Grep: 4 trustworthy (even count) — 100,100,3000,3000.
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:03Z"), ToolName: "Grep", DurationMs: 100},
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:04Z"), ToolName: "Grep", DurationMs: 100},
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:05Z"), ToolName: "Grep", DurationMs: 3000},
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:06Z"), ToolName: "Grep", DurationMs: 3000},
		// SA Write: 1 trustworthy — 100.
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:07Z"), ToolName: "Write", DurationMs: 100},
		// SA Edit/e1: 2 runs (qualifies), previews DESCENDING to pin MIN byte-wise.
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:08Z"), ToolName: "Edit", DurationMs: 500, ToolInputHash: "e1", ToolInputPreview: "m-b"},
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:09Z"), ToolName: "Edit", DurationMs: 500, ToolInputHash: "e1", ToolInputPreview: "m-a"},
		// SA Fetch/f1: 1 run (excluded by HAVING COUNT>1).
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:10Z"), ToolName: "Fetch", DurationMs: 500, ToolInputHash: "f1", ToolInputPreview: "fetch"},
		// SB Edit/e1: 2 runs (qualifies) — same wasted+run_count as SA → session-asc tie-break.
		{Model: m, SessionID: "SB", RecordTS: mustTS(t, "2026-06-01T10:00:08Z"), ToolName: "Edit", DurationMs: 500, ToolInputHash: "e1", ToolInputPreview: "sb-x"},
		{Model: m, SessionID: "SB", RecordTS: mustTS(t, "2026-06-01T10:00:09Z"), ToolName: "Edit", DurationMs: 500, ToolInputHash: "e1", ToolInputPreview: "sb-y"},
	}

	// File 2: the interrupted Bash row, token/turn rows, sidechain agent spans, and the
	// two exclusion rows. SA and SB both contribute rows here (cross-file aggregation).
	f2 := []transcripts.Row{
		// SA interrupted Bash (idle-EXCLUDED from time, still COUNTED in the tool + waste).
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:11Z"), ToolName: "Bash", DurationMs: 10000, Interrupted: true},
		// SA token/turn rows.
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:20Z"), InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 800, CacheCreationTokens: 100, CacheCreation1hTokens: 40, CacheCreation5mTokens: 60, StopReason: "end_turn"},
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:30Z"), InputTokens: 200, OutputTokens: 4000, DurationMs: 5000, StopReason: "max_tokens"},
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:40Z"), InputTokens: 100, IsAPIError: true},
		// SA sidechain agents: agent-1 (researcher) span 120000ms, agent-2 (planner) 60000ms.
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:02:00Z"), InputTokens: 300, OutputTokens: 150, IsSidechain: true, AgentID: "agent-1", SubagentType: "researcher"},
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:04:00Z"), InputTokens: 200, OutputTokens: 100, IsSidechain: true, AgentID: "agent-1", SubagentType: "researcher"},
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:05:00Z"), InputTokens: 100, OutputTokens: 50, IsSidechain: true, AgentID: "agent-2", SubagentType: "planner"},
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:06:00Z"), InputTokens: 100, OutputTokens: 50, IsSidechain: true, AgentID: "agent-2", SubagentType: "planner"},
		// SA is_meta=true row — inflated values that MUST be excluded from ALL totals.
		{Model: m, SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:00:50Z"), ToolName: "Bash", DurationMs: 99999, InputTokens: 99999, OutputTokens: 99999, CacheReadTokens: 99999, CacheCreationTokens: 99999, ToolInputHash: "hmeta", ToolInputPreview: "x", CacheCreation1hTokens: 99999, CacheCreation5mTokens: 99999, StopReason: "max_tokens", IsAPIError: true, IsMeta: true, Interrupted: true},
		// SA <synthetic>-model row — MUST be excluded from ALL totals.
		{Model: "<synthetic>", SessionID: "SA", RecordTS: mustTS(t, "2026-06-01T10:01:00Z"), ToolName: "Read", DurationMs: 7777, InputTokens: 88888, OutputTokens: 88888},
		// SB sidechain agent-3 (tester) span 60000ms → wall-tie with agent-2 for tie-break.
		{Model: m, SessionID: "SB", RecordTS: mustTS(t, "2026-06-01T10:02:00Z"), InputTokens: 100, OutputTokens: 50, IsSidechain: true, AgentID: "agent-3", SubagentType: "tester"},
		{Model: m, SessionID: "SB", RecordTS: mustTS(t, "2026-06-01T10:03:00Z"), InputTokens: 100, OutputTokens: 50, IsSidechain: true, AgentID: "agent-3", SubagentType: "tester"},
	}

	writeSessionFixture(t, root, "claude", "F1", f1)
	writeSessionFixture(t, root, "claude", "F2", f2)

	svc, err := NewService(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// detectorGoldens is the FROZEN expected-value table for all 10 detector families over
// the buildGoldenCorpus corpus, computed to the AGENT's usageanalytics semantics. Slice
// families carry their full ORDER-BY order; token families are asserted by key.
type detectorGoldens struct {
	Duplicates       []DuplicateCommandRow
	ToolLatency      []ToolLatencyRow // histogram percentiles — asserted Phase 3 only.
	ToolTimeTotals   []ToolTimeTotalRow
	Avg              AvgTokensPerSession
	TokensByTool     map[string]TokenByDimensionRow
	TokensBySubagent map[string]TokenByDimensionRow
	Cache            CacheEfficiency
	SubagentWall     []SubagentWallTime
	AgentChains      []AgentChainRow
	Waste            WasteSummary
}

// goldenTable returns the frozen goldens. See buildGoldenCorpus for the per-row
// derivation; every value here is hand-computed to the agent semantics and confirmed
// against the live duckdb engine (Phase 2) + the pure-Go engine (Phase 3).
func goldenTable() detectorGoldens {
	return detectorGoldens{
		// GROUP (session,tool,hash) HAVING count>1; ORDER wasted desc, run_count desc,
		// session asc. SA & SB Edit/e1 tie on wasted(1000)+run_count(2) → session asc.
		Duplicates: []DuplicateCommandRow{
			{SessionID: "SA", ToolName: "Edit", ToolInputHash: "e1", RunCount: 2, WastedDurationMs: 1000, SamplePreview: "m-a"},
			{SessionID: "SB", ToolName: "Edit", ToolInputHash: "e1", RunCount: 2, WastedDurationMs: 1000, SamplePreview: "sb-x"},
		},
		// Histogram percentiles (bucketRepresentative(b)=2^(b+1)-1). ORDER p90 desc, tool
		// asc: Bash/Grep tie at 4095 → Bash,Grep; Edit/Fetch tie at 511 → Edit,Fetch.
		ToolLatency: []ToolLatencyRow{
			{ToolName: "Bash", Count: 3, P50: 1023, P90: 4095, P99: 4095},
			{ToolName: "Grep", Count: 4, P50: 127, P90: 4095, P99: 4095},
			{ToolName: "Edit", Count: 4, P50: 511, P90: 511, P99: 511},
			{ToolName: "Fetch", Count: 1, P50: 511, P90: 511, P99: 511},
			{ToolName: "Write", Count: 1, P50: 127, P90: 127, P99: 127},
		},
		// SUM trustworthy duration by tool, CallCount = all rows w/ that tool; ORDER total
		// desc, tool asc. Bash CallCount 4 (incl. the interrupted row), time 5000.
		ToolTimeTotals: []ToolTimeTotalRow{
			{ToolName: "Grep", TotalDurationMs: 6200, CallCount: 4},
			{ToolName: "Bash", TotalDurationMs: 5000, CallCount: 4},
			{ToolName: "Edit", TotalDurationMs: 2000, CallCount: 4},
			{ToolName: "Fetch", TotalDurationMs: 500, CallCount: 1},
			{ToolName: "Write", TotalDurationMs: 100, CallCount: 1},
		},
		// AVG over per-session token sums. SA(in2000,out4850), SB(in200,out100). 2 sessions.
		Avg: AvgTokensPerSession{AvgInputTokens: 1100, AvgOutputTokens: 2475, AvgTotalTokens: 3575, SessionCount: 2},
		// All token-bearing rows carry an empty tool_name → the "" key holds every token.
		TokensByTool: map[string]TokenByDimensionRow{
			"":     {Key: "", InputTokens: 2200, OutputTokens: 4950},
			"Bash": {Key: "Bash", InputTokens: 0, OutputTokens: 0},
		},
		TokensBySubagent: map[string]TokenByDimensionRow{
			"researcher": {Key: "researcher", InputTokens: 500, OutputTokens: 250},
			"planner":    {Key: "planner", InputTokens: 200, OutputTokens: 100},
			"tester":     {Key: "tester", InputTokens: 200, OutputTokens: 100},
		},
		// SUM cache_read/input/1h/5m over kept rows; ratio 800/2200.
		Cache: CacheEfficiency{CacheReadTokens: 800, InputTokens: 2200, CacheReadRatio: 800.0 / 2200.0, CacheCreation1hTokens: 40, CacheCreation5mTokens: 60},
		// Per agent_id; ORDER wall desc, agent asc. agent-2/agent-3 tie at 60000 → agent asc.
		SubagentWall: []SubagentWallTime{
			{AgentID: "agent-1", SubagentType: "researcher", WallMs: 120000, InputTokens: 500, OutputTokens: 250},
			{AgentID: "agent-2", SubagentType: "planner", WallMs: 60000, InputTokens: 200, OutputTokens: 100},
			{AgentID: "agent-3", SubagentType: "tester", WallMs: 60000, InputTokens: 200, OutputTokens: 100},
		},
		// Per session; ORDER count desc, total-wall desc, session asc.
		AgentChains: []AgentChainRow{
			{SessionID: "SA", SubagentCount: 2, SubagentTypeDiversity: 2, TotalSubagentWallMs: 180000, MaxSubagentWallMs: 120000},
			{SessionID: "SB", SubagentCount: 1, SubagentTypeDiversity: 1, TotalSubagentWallMs: 60000, MaxSubagentWallMs: 60000},
		},
		// max_tokens FILTER on RAW duration_ms (T2: out 4000, dur 5000). is_meta row excluded.
		Waste: WasteSummary{CacheCreation1hTokens: 40, CacheCreation5mTokens: 60, APIErrorCount: 1, InterruptedCount: 1, MaxTokensTruncationCount: 1, MaxTokensOutputTokens: 4000, MaxTokensDurationMs: 5000},
	}
}

// tokenByKey indexes a TokenByDimensionRow slice by its key.
func tokenByKey(rows []TokenByDimensionRow) map[string]TokenByDimensionRow {
	out := make(map[string]TokenByDimensionRow, len(rows))
	for _, r := range rows {
		out[r.Key] = r
	}
	return out
}

// assertNineFamilies asserts the 9 non-percentile families of rep against the goldens.
// Shared by the Phase-2 (duckdb) and Phase-3 (pure-Go) parity tests; ToolLatency is
// asserted separately (histogram-only, Phase 3).
func assertNineFamilies(t *testing.T, rep *DetectorReport, g detectorGoldens) {
	t.Helper()
	assert.Equal(t, g.Duplicates, rep.DuplicateCommands, "duplicate commands")
	assert.Equal(t, g.ToolTimeTotals, rep.ToolTimeTotals, "tool time totals")
	assert.Equal(t, g.Avg, rep.AvgTokensPerSession, "avg tokens per session")
	assert.Equal(t, g.AgentChains, rep.AgentChains, "agent chains")
	assert.Equal(t, g.SubagentWall, rep.SubagentWallTime, "subagent wall time")
	assert.Equal(t, g.Waste, rep.Waste, "waste summary")

	gotTool := tokenByKey(rep.TokensByTool)
	for k, want := range g.TokensByTool {
		assert.Equal(t, want, gotTool[k], "tokens by tool key %q", k)
	}
	gotSub := tokenByKey(rep.TokensBySubagentType)
	for k, want := range g.TokensBySubagent {
		assert.Equal(t, want, gotSub[k], "tokens by subagent key %q", k)
	}

	assert.Equal(t, g.Cache.CacheReadTokens, rep.CacheEfficiency.CacheReadTokens, "cache read tokens")
	assert.Equal(t, g.Cache.InputTokens, rep.CacheEfficiency.InputTokens, "cache input tokens")
	assert.Equal(t, g.Cache.CacheCreation1hTokens, rep.CacheEfficiency.CacheCreation1hTokens, "cache 1h")
	assert.Equal(t, g.Cache.CacheCreation5mTokens, rep.CacheEfficiency.CacheCreation5mTokens, "cache 5m")
	assert.InDelta(t, g.Cache.CacheReadRatio, rep.CacheEfficiency.CacheReadRatio, 0.0001, "cache read ratio")
}

// TestGoldenCorpus_PureGoParity runs the pure-Go engine over the richer drift-free corpus
// and asserts ALL 10 families value-for-value against the frozen goldens — INCLUDING the
// ToolLatency histogram family. The 9 non-percentile families were characterized against the
// live duckdb engine during characterization (same goldens, same values), proving
// duckdb==pure-Go over the drift-free corpus; the ToolLatency assertion proves the
// histogram-representative percentiles duckdb's quantile_cont could not match (Bash/Grep
// p90=4095, Edit/Fetch p90=511, Write=127).
func TestGoldenCorpus_PureGoParity(t *testing.T) {
	svc := buildGoldenCorpus(t)
	rep, err := svc.RunDetectors(context.Background())
	require.NoError(t, err)
	require.NotNil(t, rep)

	g := goldenTable()
	assertNineFamilies(t, rep, g)
	assert.Equal(t, g.ToolLatency, rep.ToolLatency, "tool latency histogram percentiles")
}
