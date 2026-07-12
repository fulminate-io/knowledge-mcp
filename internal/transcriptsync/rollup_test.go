// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// base is a fixed reference instant every fixture hangs off, so day/record_ts assertions
// are deterministic.
var base = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func dayOf(ts time.Time) string { return ts.Format("2006-01-02") }

func findFacts(p rollupPayload, pred func(factRow) bool) []factRow {
	var out []factRow
	for _, f := range p.Facts {
		if pred(f) {
			out = append(out, f)
		}
	}
	return out
}

func findHist(p rollupPayload, pred func(latencyHistRow) bool) []latencyHistRow {
	var out []latencyHistRow
	for _, h := range p.LatencyHist {
		if pred(h) {
			out = append(out, h)
		}
	}
	return out
}

func findDups(p rollupPayload, pred func(duplicateRow) bool) []duplicateRow {
	var out []duplicateRow
	for _, d := range p.DuplicateCommands {
		if pred(d) {
			out = append(out, d)
		}
	}
	return out
}

func slowCallsForTool(p rollupPayload, tool string) []slowCallRow {
	var out []slowCallRow
	for _, s := range p.SlowCalls {
		if s.ToolName == tool {
			out = append(out, s)
		}
	}
	return out
}

// TestRollupTrustworthy pins the idle-guard predicate at its boundaries.
func TestRollupTrustworthy(t *testing.T) {
	assert.True(t, rollupTrustworthy(transcripts.Row{DurationMs: 1}), "1ms is trustworthy")
	assert.True(t, rollupTrustworthy(transcripts.Row{DurationMs: rollupIdleGuardCeilingMs}), "ceiling is trustworthy")
	assert.False(t, rollupTrustworthy(transcripts.Row{DurationMs: 0}), "0ms is not trustworthy")
	assert.False(t, rollupTrustworthy(transcripts.Row{DurationMs: rollupIdleGuardCeilingMs + 1}), "over-ceiling is not trustworthy")
	assert.False(t, rollupTrustworthy(transcripts.Row{DurationMs: 100, Interrupted: true}), "interrupted is not trustworthy")
}

// TestDurationBucket pins the log2-bucket boundary math and the 31 clamp.
func TestDurationBucket(t *testing.T) {
	assert.Equal(t, 0, durationBucket(-5), "negative → 0")
	assert.Equal(t, 0, durationBucket(0), "zero → 0")
	assert.Equal(t, 0, durationBucket(1), "1ms → bucket 0")
	assert.Equal(t, 1, durationBucket(2), "2ms → bucket 1")
	assert.Equal(t, 1, durationBucket(3), "3ms → bucket 1")
	assert.Equal(t, 2, durationBucket(4), "4ms → bucket 2")
	assert.Equal(t, rollupBucketMaxExp, durationBucket(1<<40), "huge → clamped to 31")
}

// TestComputeSessionRollup_IdleGuardBoundaries proves a non-trustworthy row still counts
// toward record_count/duration_ms(RAW) but contributes 0ms to trustworthy_duration_ms and
// is excluded from latency_hist + slow_calls.
func TestComputeSessionRollup_IdleGuardBoundaries(t *testing.T) {
	// All rows share one fact grain (same tool/model/project/day/flags) so the dual
	// duration is checked on a single fact row. Durations: 1 (ok), 7_200_000 (ok),
	// 0 (not), 7_200_001 (not), and one interrupted (in-range but not).
	rows := []transcripts.Row{
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "m", ToolName: "Bash", RecordTS: base, DurationMs: 1},
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "m", ToolName: "Bash", RecordTS: base, DurationMs: 7_200_000},
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "m", ToolName: "Bash", RecordTS: base, DurationMs: 0},
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "m", ToolName: "Bash", RecordTS: base, DurationMs: 7_200_001},
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "m", ToolName: "Bash", RecordTS: base, DurationMs: 500, Interrupted: true},
	}
	p := computeSessionRollup(rows)

	facts := findFacts(p, func(f factRow) bool { return f.ToolName == "Bash" })
	require.Len(t, facts, 1, "all rows collapse into one fact grain")
	f := facts[0]
	assert.Equal(t, int64(5), f.RecordCount, "every row counts")
	assert.Equal(t, int64(1+7_200_000+0+7_200_001+500), f.DurationMs, "duration_ms is the RAW sum over ALL rows")
	assert.Equal(t, int64(1+7_200_000), f.TrustworthyDurationMs, "trustworthy_duration_ms sums only idle-guarded rows")
	assert.Equal(t, int64(1), f.InterruptedCount, "the interrupted row is counted")

	// Only the two trustworthy tool rows are bucketed (both in different buckets: 1ms→0,
	// 7_200_000ms→22), so exactly two hist rows for this tool with call_count 1 each.
	hist := findHist(p, func(h latencyHistRow) bool { return h.ToolName == "Bash" })
	var histCalls int64
	for _, h := range hist {
		histCalls += h.CallCount
	}
	assert.Equal(t, int64(2), histCalls, "only the two trustworthy tool rows are bucketed")
	assert.Len(t, slowCallsForTool(p, "Bash"), 2, "only trustworthy tool rows are slow-call candidates")
}

// TestComputeSessionRollup_SyntheticAndMetaVerbatim proves synthetic-model and is_meta
// rows are shipped as their own fact rows (NOT dropped, unlike the daemon-local analyzer).
func TestComputeSessionRollup_SyntheticAndMetaVerbatim(t *testing.T) {
	rows := []transcripts.Row{
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "<synthetic>", RecordTS: base, InputTokens: 5},
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "sonnet", RecordTS: base, IsMeta: true, InputTokens: 7},
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "sonnet", RecordTS: base, InputTokens: 9},
	}
	p := computeSessionRollup(rows)

	assert.Len(t, findFacts(p, func(f factRow) bool { return f.Model == "<synthetic>" }), 1,
		"synthetic-model row ships as its own fact row")
	assert.Len(t, findFacts(p, func(f factRow) bool { return f.IsMeta && f.Model == "sonnet" }), 1,
		"is_meta row ships as its own fact row")
}

// TestComputeSessionRollup_FactGrainAndDualDuration proves the (day × 12-dim tuple) grain
// collapses identical rows and splits on any changed dimension, and the dual-duration sums.
func TestComputeSessionRollup_FactGrainAndDualDuration(t *testing.T) {
	twin := func(dur int64) transcripts.Row {
		return transcripts.Row{
			Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "sonnet",
			ToolName: "Bash", SubagentType: "impl", AgentID: "a1", IsSidechain: false, IsMeta: false,
			MCPServer: "srv", MCPTool: "mt", Skill: "sk", ServiceTier: "std", StopReason: "end_turn",
			RecordTS: base, DurationMs: dur, InputTokens: 10,
		}
	}
	rows := []transcripts.Row{twin(100), twin(0)} // 2 rows in ONE grain; one non-trustworthy.
	// A third row differing only in Project must split into its own grain.
	split := twin(100)
	split.Project = "/other"
	rows = append(rows, split)

	p := computeSessionRollup(rows)

	collapsed := findFacts(p, func(f factRow) bool { return f.Project == "/w" && f.ToolName == "Bash" })
	require.Len(t, collapsed, 1, "the two full-tuple-identical rows collapse")
	assert.Equal(t, int64(2), collapsed[0].RecordCount)
	assert.Equal(t, int64(20), collapsed[0].InputTokens, "token metrics sum across the grain")
	assert.Equal(t, int64(100), collapsed[0].DurationMs, "duration_ms is RAW sum (100+0)")
	assert.Equal(t, int64(100), collapsed[0].TrustworthyDurationMs, "only the 100ms row is trustworthy")

	assert.Len(t, findFacts(p, func(f factRow) bool { return f.Project == "/other" }), 1,
		"changing one dimension splits the grain")
}

// TestComputeSessionRollup_LatencyHistMetaGrain proves is_meta is a hist grain column: two
// otherwise-identical trustworthy tool rows differing only in IsMeta produce TWO hist rows.
func TestComputeSessionRollup_LatencyHistMetaGrain(t *testing.T) {
	rows := []transcripts.Row{
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "m", ToolName: "Bash", RecordTS: base, DurationMs: 100, IsMeta: false},
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "m", ToolName: "Bash", RecordTS: base, DurationMs: 100, IsMeta: true},
	}
	p := computeSessionRollup(rows)
	hist := findHist(p, func(h latencyHistRow) bool { return h.ToolName == "Bash" })
	require.Len(t, hist, 2, "is_meta splits the histogram grain")
	assert.NotEqual(t, hist[0].IsMeta, hist[1].IsMeta, "the two hist rows differ in is_meta")
}

// TestComputeSessionRollup_SlowCallsPerToolTruncation proves the top-100 truncation is
// PER (session, tool_name), not a cross-tool top-100.
func TestComputeSessionRollup_SlowCallsPerToolTruncation(t *testing.T) {
	var rows []transcripts.Row
	for i := range 150 { // 150 trustworthy Bash calls
		rows = append(rows, transcripts.Row{Source: transcripts.SourceClaude, SessionID: "s", Model: "m", ToolName: "Bash", RecordTS: base, DurationMs: int64(i + 1)})
	}
	for i := range 120 { // 120 trustworthy Read calls
		rows = append(rows, transcripts.Row{Source: transcripts.SourceClaude, SessionID: "s", Model: "m", ToolName: "Read", RecordTS: base, DurationMs: int64(i + 1)})
	}
	p := computeSessionRollup(rows)

	bash := slowCallsForTool(p, "Bash")
	read := slowCallsForTool(p, "Read")
	assert.Len(t, bash, 100, "Bash truncated to top-100")
	assert.Len(t, read, 100, "Read truncated to top-100 independently (not starved by Bash)")
	assert.Len(t, p.SlowCalls, 200, "200 total = 100 per tool")
	assert.Equal(t, int64(150), bash[0].DurationMs, "Bash slow calls ordered by duration desc")
	assert.Equal(t, int64(51), bash[99].DurationMs, "the 100th kept Bash call is the 100th-slowest (150..51)")
}

// TestComputeSessionRollup_DuplicateSessionTotalGate proves the v1-amendment-2 emission:
// fine rows are shipped when their parent (tool,hash) SESSION-TOTAL run_count > 1, even
// when each fine row is a singleton (is_meta split, midnight split), and NOT shipped for a
// truly unique (tool,hash).
func TestComputeSessionRollup_DuplicateSessionTotalGate(t *testing.T) {
	nextDay := base.Add(12 * time.Hour) // 2026-06-02 00:00 — a different calendar day.
	require.NotEqual(t, dayOf(base), dayOf(nextDay), "fixture spans a day boundary")

	rows := []transcripts.Row{
		// (a) is_meta split: same tool+hash+day, differ only in is_meta → 2 fine grains,
		// session-total 2 → BOTH shipped (a coarse group would have collapsed them).
		{Source: transcripts.SourceClaude, SessionID: "s", Model: "m", ToolName: "Grep", ToolInputHash: "h1", RecordTS: base, DurationMs: 100, IsMeta: false},
		{Source: transcripts.SourceClaude, SessionID: "s", Model: "m", ToolName: "Grep", ToolInputHash: "h1", RecordTS: base, DurationMs: 100, IsMeta: true},

		// (b) midnight split: identical fine tuple except day → 2 fine grains, session-total
		// 2 → BOTH shipped on their own days (reversal of the old coarse behavior).
		{Source: transcripts.SourceClaude, SessionID: "s", Model: "m", ToolName: "Edit", ToolInputHash: "h2", RecordTS: base, DurationMs: 100},
		{Source: transcripts.SourceClaude, SessionID: "s", Model: "m", ToolName: "Edit", ToolInputHash: "h2", RecordTS: nextDay, DurationMs: 100},

		// (c) truly unique (tool,hash): session-total 1 → NOTHING shipped.
		{Source: transcripts.SourceClaude, SessionID: "s", Model: "m", ToolName: "Write", ToolInputHash: "h3", RecordTS: base, DurationMs: 100},

		// hash-empty rows are excluded from the dup accumulators entirely.
		{Source: transcripts.SourceClaude, SessionID: "s", Model: "m", ToolName: "Bash", ToolInputHash: "", RecordTS: base, DurationMs: 100},
	}
	p := computeSessionRollup(rows)

	// (a) is_meta split.
	grep := findDups(p, func(d duplicateRow) bool { return d.ToolName == "Grep" && d.ToolInputHash == "h1" })
	require.Len(t, grep, 2, "is_meta split: both fine rows shipped (session-total 2)")
	assert.NotEqual(t, grep[0].IsMeta, grep[1].IsMeta)
	for _, d := range grep {
		assert.Equal(t, int64(1), d.RunCount, "each fine row is a singleton")
	}

	// (b) midnight split.
	edit := findDups(p, func(d duplicateRow) bool { return d.ToolName == "Edit" && d.ToolInputHash == "h2" })
	require.Len(t, edit, 2, "midnight split: both fine rows shipped on different days")
	assert.NotEqual(t, edit[0].Day, edit[1].Day, "the two fine rows are on different days")

	// (c) unique.
	assert.Empty(t, findDups(p, func(d duplicateRow) bool { return d.ToolName == "Write" }),
		"a truly unique (tool,hash) is not shipped")

	// hash-empty excluded.
	assert.Empty(t, findDups(p, func(d duplicateRow) bool { return d.ToolInputHash == "" }),
		"hash-empty rows never enter the dup accumulators")
}

// TestComputeSessionRollup_DuplicateRunCountAndMin proves run_count counts a
// non-trustworthy member but wasted_duration_ms excludes it, and that sample_preview /
// first_record_ts are true MINs (fixtures inserted in DESCENDING order to discriminate MIN
// from first-seen).
func TestComputeSessionRollup_DuplicateRunCountAndMin(t *testing.T) {
	// One fine grain, two rows inserted in DESCENDING preview + record_ts order so a
	// naive first-seen would pick the WRONG (max) value.
	rows := []transcripts.Row{
		// first-seen: later ts, larger preview, non-trustworthy (DurationMs 0).
		{Source: transcripts.SourceClaude, SessionID: "s", Model: "m", ToolName: "Bash", ToolInputHash: "h", Project: "/w", RecordTS: base.Add(10 * time.Minute), DurationMs: 0, ToolInputPreview: "zzz"},
		// later-seen: earlier ts, smaller preview, trustworthy.
		{Source: transcripts.SourceClaude, SessionID: "s", Model: "m", ToolName: "Bash", ToolInputHash: "h", Project: "/w", RecordTS: base.Add(1 * time.Minute), DurationMs: 500, ToolInputPreview: "aaa"},
	}
	p := computeSessionRollup(rows)
	dups := findDups(p, func(d duplicateRow) bool { return d.ToolName == "Bash" && d.ToolInputHash == "h" })
	require.Len(t, dups, 1, "both rows collapse into one fine grain")
	d := dups[0]
	assert.Equal(t, int64(2), d.RunCount, "run_count counts the non-trustworthy member")
	assert.Equal(t, int64(500), d.WastedDurationMs, "wasted_duration_ms excludes the non-trustworthy member")
	assert.Equal(t, "aaa", d.SamplePreview, "sample_preview is the byte-wise MIN, not first-seen")
	assert.Equal(t, base.Add(1*time.Minute).Format(time.RFC3339Nano), d.FirstRecordTS,
		"first_record_ts is the instant MIN, not first-seen")
}

// TestComputeSessionRollup_AgentChainDepth proves the depth is the distinct-subagent count
// among sidechain rows (a non-sidechain row does not contribute).
func TestComputeSessionRollup_AgentChainDepth(t *testing.T) {
	rows := []transcripts.Row{
		{Source: transcripts.SourceClaude, SessionID: "s", RecordTS: base, IsSidechain: true, AgentID: "a1"},
		{Source: transcripts.SourceClaude, SessionID: "s", RecordTS: base, IsSidechain: true, AgentID: "a2"},
		{Source: transcripts.SourceClaude, SessionID: "s", RecordTS: base, IsSidechain: true, AgentID: "a1"}, // duplicate id
		{Source: transcripts.SourceClaude, SessionID: "s", RecordTS: base, IsSidechain: false, AgentID: "a3"},
	}
	p := computeSessionRollup(rows)
	assert.Equal(t, int64(2), p.Session.AgentChainDepth, "two distinct sidechain agent ids")
}

// TestComputeSessionRollup_SessionScalars proves the per-session scalar aggregates.
func TestComputeSessionRollup_SessionScalars(t *testing.T) {
	rows := []transcripts.Row{
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "", RecordTS: base.Add(2 * time.Minute), InputTokens: 10, OutputTokens: 5, WebSearchCount: 1, IsError: true},
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/work", RecordTS: base, InputTokens: 3, WebFetchCount: 2, IsAPIError: true},
		{Source: transcripts.SourceClaude, SessionID: "s", Project: "/other", RecordTS: base.Add(5 * time.Minute), OutputTokens: 7, Interrupted: true},
	}
	p := computeSessionRollup(rows)
	s := p.Session
	assert.Equal(t, int64(3), s.RecordCount)
	assert.Equal(t, int64(13), s.InputTokens)
	assert.Equal(t, int64(12), s.OutputTokens)
	assert.Equal(t, int64(1), s.WebSearchCount)
	assert.Equal(t, int64(2), s.WebFetchCount)
	assert.Equal(t, int64(1), s.ErrorCount)
	assert.Equal(t, int64(1), s.APIErrorCount)
	assert.Equal(t, int64(1), s.InterruptedCount)
	assert.Equal(t, "/work", s.Project, "project = first non-empty in record order (skips the leading empty)")
	assert.Equal(t, base.Format(time.RFC3339Nano), s.FirstRecordTS, "first_record_ts = min instant")
	assert.Equal(t, base.Add(5*time.Minute).Format(time.RFC3339Nano), s.LastRecordTS, "last_record_ts = max instant")
}
