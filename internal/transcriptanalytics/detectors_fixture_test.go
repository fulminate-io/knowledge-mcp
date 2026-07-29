// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// mustTS parses an RFC3339 fixture timestamp, failing the test on a malformed literal.
func mustTS(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return ts
}

// detectorFixtureRows builds the S1 characterization corpus as a []transcripts.Row —
// the pure-Go replacement for the former duckdb VALUES-to-parquet COPY writer.
// It encodes the SAME 13-row S1 scenario every detector family is asserted against:
//   - dup Bash/h1 x2 (rows 1,2)          → duplicate command, wasted=2000ms
//   - Read/h2 (row 3, 3000ms)            → tool latency + time-total
//   - token row (row 4)                  → cache-efficiency (read 800/input, 1h=40 5m=60)
//   - max_tokens truncation (row 5)      → waste (out=4000, dur=5000)
//   - api-error row (row 6)              → waste
//   - interrupted Bash/h3 (row 7)        → idle-EXCLUDED from time, counted in waste
//   - is_meta row (row 8)                → MUST be excluded from ALL totals
//   - <synthetic> row (row 9)            → MUST be excluded from ALL totals
//   - agent-1 researcher span (rows 10,11) wall=120000ms in=500 out=250
//   - agent-2 planner span (rows 12,13)  wall=60000ms  in=200 out=100
func detectorFixtureRows(t *testing.T) []transcripts.Row {
	t.Helper()
	const model = "claude-sonnet-4"
	return []transcripts.Row{
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:00:00Z"), ToolName: "Bash", DurationMs: 1000, ToolInputHash: "h1", ToolInputPreview: "ls"},
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:00:05Z"), ToolName: "Bash", DurationMs: 1000, ToolInputHash: "h1", ToolInputPreview: "ls"},
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:00:10Z"), ToolName: "Read", DurationMs: 3000, ToolInputHash: "h2", ToolInputPreview: "read a"},
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:00:20Z"), InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 800, CacheCreationTokens: 100, CacheCreation1hTokens: 40, CacheCreation5mTokens: 60, StopReason: "end_turn"},
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:00:30Z"), InputTokens: 200, OutputTokens: 4000, DurationMs: 5000, StopReason: "max_tokens"},
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:00:40Z"), InputTokens: 100, IsAPIError: true},
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:00:50Z"), ToolName: "Bash", DurationMs: 10000, ToolInputHash: "h3", ToolInputPreview: "build", Interrupted: true},
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:01:00Z"), InputTokens: 99999, OutputTokens: 99999, CacheReadTokens: 99999, CacheCreationTokens: 99999, ToolName: "Bash", DurationMs: 99999, ToolInputHash: "hmeta", ToolInputPreview: "x", CacheCreation1hTokens: 99999, CacheCreation5mTokens: 99999, StopReason: "max_tokens", IsAPIError: true, IsMeta: true, Interrupted: true},
		{Model: "<synthetic>", SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:01:10Z"), InputTokens: 88888, OutputTokens: 88888, ToolName: "Read", DurationMs: 7777, ToolInputHash: "h2", ToolInputPreview: "y"},
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:02:00Z"), InputTokens: 300, OutputTokens: 150, IsSidechain: true, AgentID: "agent-1", SubagentType: "researcher"},
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:04:00Z"), InputTokens: 200, OutputTokens: 100, IsSidechain: true, AgentID: "agent-1", SubagentType: "researcher"},
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:05:00Z"), InputTokens: 100, OutputTokens: 50, IsSidechain: true, AgentID: "agent-2", SubagentType: "planner"},
		{Model: model, SessionID: "S1", RecordTS: mustTS(t, "2026-06-01T10:06:00Z"), InputTokens: 100, OutputTokens: 50, IsSidechain: true, AgentID: "agent-2", SubagentType: "planner"},
	}
}

// writeSessionFixture writes rows to a {source}/{session}.parquet file via the pure-Go
// transcripts.WriteSessionParquet writer (no duckdb).
func writeSessionFixture(t *testing.T, root, source, session string, rows []transcripts.Row) {
	t.Helper()
	srcDir := filepath.Join(root, source)
	require.NoError(t, os.MkdirAll(srcDir, 0o750))
	f, err := os.Create(filepath.Join(srcDir, session+".parquet")) //nolint:gosec // t.TempDir() path, not user input.
	require.NoError(t, err)
	require.NoError(t, transcripts.WriteSessionParquet(rows, f))
	require.NoError(t, f.Close())
}

// newDetectorFixture writes the shared S1 fixture parquet into the fixed
// {source}/{session}.parquet cache layout via the pure-Go writer and returns the analyzer
// over that cache root. Callers load the corpus / run detectors via the cache glob.
func newDetectorFixture(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	writeSessionFixture(t, root, "claude", "S1", detectorFixtureRows(t))

	svc, err := NewService(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}
