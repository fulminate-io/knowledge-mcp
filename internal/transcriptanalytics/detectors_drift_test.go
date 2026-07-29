// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// driftRow is a pre-enrichment parquet shape that OMITS the is_meta column (and the rest of
// the enrichment tail). A file written from it stands in for a cache file created BEFORE the
// is_meta column existed — its physical schema simply has no is_meta leaf.
type driftRow struct {
	Source           string `parquet:"source"`
	SessionID        string `parquet:"session_id"`
	Model            string `parquet:"model"`
	RecordTS         string `parquet:"record_ts"`
	InputTokens      int64  `parquet:"input_tokens"`
	OutputTokens     int64  `parquet:"output_tokens"`
	ToolName         string `parquet:"tool_name"`
	DurationMs       int64  `parquet:"duration_ms"`
	ToolInputHash    string `parquet:"tool_input_hash"`
	ToolInputPreview string `parquet:"tool_input_preview"`
}

func writeDriftParquet(t *testing.T, path string, rows []driftRow) {
	t.Helper()
	f, err := os.Create(path) //nolint:gosec // path is a t.TempDir() location, not user input.
	require.NoError(t, err)
	w := parquet.NewGenericWriter[driftRow](f, parquet.Compression(&parquet.Zstd))
	_, err = w.Write(rows)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
}

// TestDetectors_IsMetaMissingKept proves the CEO is_meta fix under the PURE-GO engine: rows
// from a parquet file MISSING the is_meta column are KEPT in every total (missing→false→
// keep, the natural parquet-go zero value) — the OPPOSITE of the old duckdb `NOT is_meta` =
// NULL exclusion. This is a DELIBERATE duckdb≠pure-Go divergence, so it is asserted ONLY
// against the pure-Go engine (duckdb is removed regardless). It uses its OWN small corpus +
// goldens, separate from the drift-free Phase-2 parity corpus (where every file carries
// is_meta so the two engines genuinely agree).
func TestDetectors_IsMetaMissingKept(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "claude")
	require.NoError(t, os.MkdirAll(srcDir, 0o750))
	writeDriftParquet(t, filepath.Join(srcDir, "D.parquet"), []driftRow{
		{Source: "claude", SessionID: "D", Model: "m", RecordTS: "2026-06-01T10:00:00Z", InputTokens: 100, OutputTokens: 50, ToolName: "Bash", DurationMs: 1000, ToolInputHash: "d1", ToolInputPreview: "x"},
		{Source: "claude", SessionID: "D", Model: "m", RecordTS: "2026-06-01T10:00:05Z", InputTokens: 100, OutputTokens: 50, ToolName: "Bash", DurationMs: 1000, ToolInputHash: "d1", ToolInputPreview: "x"},
	})

	svc, err := NewService(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	rep, err := svc.RunDetectors(context.Background())
	require.NoError(t, err)
	require.NotNil(t, rep)

	// The is_meta-missing rows are KEPT — they appear in every total. Under the old duckdb
	// NOT-NULL exclusion these rows (is_meta=NULL) would have been DROPPED and every total
	// below would be empty/zero.
	require.Equal(t, int64(1), rep.AvgTokensPerSession.SessionCount, "the drift session is kept")
	assert.InDelta(t, 200.0, rep.AvgTokensPerSession.AvgInputTokens, 0.001, "both drift rows' input tokens kept")
	require.Len(t, rep.ToolLatency, 1, "the drift tool rows are kept in the latency histogram")
	assert.Equal(t, "Bash", rep.ToolLatency[0].ToolName)
	assert.Equal(t, int64(2), rep.ToolLatency[0].Count)
	require.Len(t, rep.DuplicateCommands, 1, "the twice-run drift command is kept")
	assert.Equal(t, int64(2), rep.DuplicateCommands[0].RunCount)
	assert.Equal(t, int64(2000), rep.DuplicateCommands[0].WastedDurationMs)
	assert.Equal(t, int64(200), rep.CacheEfficiency.InputTokens, "cache input sum keeps both drift rows")
}
