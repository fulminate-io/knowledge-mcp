// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// TestService_CachePathsAndLoadCorpus proves the pure-Go engine contract: an EMPTY cache
// resolves to zero explicit paths and loadCorpus short-circuits to an empty corpus + nil
// error WITHOUT decoding; a populated cache resolves to the explicit local file list and
// loadCorpus decodes + folds the rows into the corpus accumulators.
func TestService_CachePathsAndLoadCorpus(t *testing.T) {
	root := t.TempDir()
	svc, err := NewService(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	// Empty cache: filepath.Glob zero-match → empty result, no error.
	paths, err := svc.cachePaths()
	require.NoError(t, err)
	require.Empty(t, paths, "an empty cache resolves to zero explicit paths")

	// loadCorpus over an empty cache root: empty corpus, nil error, nothing decoded.
	empty, err := svc.loadCorpus(context.Background(), Filters{})
	require.NoError(t, err)
	require.NotNil(t, empty)
	require.Empty(t, empty.sessions, "empty cache yields an empty corpus (no sessions)")
	require.Empty(t, empty.toolTime, "empty cache yields an empty corpus (no tools)")

	// Populate the fixed {source}/{session}.parquet layout with one fixture file.
	srcDir := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(srcDir, 0o750))
	f, err := os.Create(filepath.Join(srcDir, "S.parquet"))
	require.NoError(t, err)
	require.NoError(t, transcripts.WriteSessionParquet([]transcripts.Row{
		{Model: "m", SessionID: "S", RecordTS: mustTS(t, "2026-06-01T10:00:00Z"), InputTokens: 100, ToolName: "Bash", DurationMs: 1000},
	}, f))
	require.NoError(t, f.Close())

	paths, err = svc.cachePaths()
	require.NoError(t, err)
	require.Len(t, paths, 1, "the glob returns the one explicit cache file")

	// loadCorpus decodes the file and folds the row into the accumulators.
	got, err := svc.loadCorpus(context.Background(), Filters{})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Contains(t, got.sessions, "S", "the decoded row's session is aggregated")
	require.Contains(t, got.toolTime, "Bash", "the decoded tool row is aggregated")
	require.Equal(t, int64(100), got.inputTokens, "the decoded input tokens are summed")
}
