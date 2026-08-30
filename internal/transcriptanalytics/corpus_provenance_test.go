// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// TestCorpusProvenance_ReportsLanesRecordsSessionsAndWindow pins the disclosed basis over
// the two-file golden corpus. RecordCount is the discriminating value: the corpus holds 28
// rows of which one is_meta row and one <synthetic>-model row are dropped at intake, so an
// implementation counting rows READ rather than rows KEPT lands on 28 and goes red here.
//
// The empty-cache sub-case is the other half: it is the only arm that fails when the two
// timestamps are rendered unconditionally, because a zero time.Time formats to a
// year-1 literal that reads as a real record window rather than as "no records".
func TestCorpusProvenance_ReportsLanesRecordsSessionsAndWindow(t *testing.T) {
	t.Run("golden corpus", func(t *testing.T) {
		svc := buildGoldenCorpus(t)
		rep, err := svc.RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)
		require.NotNil(t, rep)

		assert.Equal(t, int64(2), rep.Corpus.LaneCount, "two parquet files in the cache")
		assert.Equal(t, int64(26), rep.Corpus.RecordCount, "28 written rows less the is_meta and <synthetic> rows")
		assert.Equal(t, int64(2), rep.Corpus.SessionCount, "SA and SB")
		assert.Equal(t, int64(4), rep.Corpus.AgentCount, "agent-1 through agent-4")
		assert.Equal(t, "2026-06-01T10:00:00Z", rep.Corpus.FirstRecordTS, "earliest KEPT record")
		assert.Equal(t, "2026-06-01T10:41:00Z", rep.Corpus.LastRecordTS, "latest KEPT record: agent-4's third row")
		assert.NotEmpty(t, rep.Corpus.CacheRoot, "the report names the cache root it read")
	})

	t.Run("empty cache root", func(t *testing.T) {
		svc, err := NewService(t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { _ = svc.Close() })

		rep, err := svc.RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)
		require.NotNil(t, rep)

		assert.Equal(t, int64(0), rep.Corpus.LaneCount)
		assert.Equal(t, int64(0), rep.Corpus.RecordCount)
		assert.Empty(t, rep.Corpus.FirstRecordTS, "an empty corpus renders no window, not a zero-time literal")
		assert.Empty(t, rep.Corpus.LastRecordTS)
	})
}

// TestCorpusProvenance_CountsLanesAcrossEverySourceDirectory pins that the loader reads
// EVERY source directory under the cache root, not just the claude one.
//
// The cache layout is {source}/{session}.parquet and the real cache holds both a claude and
// a codex directory, so a glob that hardcoded one source would silently analyze a fraction
// of the corpus and report a lane count that looked entirely plausible. Every other fixture
// in this package writes claude lanes only, so this is the sole place that would notice.
func TestCorpusProvenance_CountsLanesAcrossEverySourceDirectory(t *testing.T) {
	root := t.TempDir()
	base := mustTS(t, "2026-06-01T10:00:00Z")

	writeSessionFixture(t, root, "claude", "C1", []transcripts.Row{mainRow("SC", base)})
	writeSessionFixture(t, root, "codex", "X1", []transcripts.Row{mainRow("SX", base.Add(time.Minute))})

	svc, err := NewService(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	rep, err := svc.RunDetectors(context.Background(), Filters{})
	require.NoError(t, err)

	assert.Equal(t, int64(2), rep.Corpus.LaneCount, "one lane from each source directory")
	assert.Equal(t, int64(2), rep.Corpus.RecordCount)
	assert.Equal(t, int64(2), rep.Corpus.SessionCount, "the codex lane's session is aggregated too")
}

// TestFamilyTruncation_DisclosesReturnedAgainstTotal pins the disclosure over a corpus that
// fits inside both caps: every returned count equals its total and Truncated is false. That
// negative is only meaningful because Phase 2 and Phase 3 drive the same fields to their
// truncated state over an oversized corpus; here it fixes the un-truncated end of the range,
// so a hardwired `true` cannot pass.
func TestFamilyTruncation_DisclosesReturnedAgainstTotal(t *testing.T) {
	svc := buildGoldenCorpus(t)
	rep, err := svc.RunDetectors(context.Background(), Filters{})
	require.NoError(t, err)

	assert.False(t, rep.Truncation.Truncated, "the golden corpus is far under both caps")
	assert.Equal(t, int64(2), rep.Truncation.DuplicateCommandsTotal)
	assert.Equal(t, int64(2), rep.Truncation.DuplicateCommandsReturned)
	assert.Equal(t, int64(4), rep.Truncation.SubagentWallTimeTotal)
	assert.Equal(t, int64(4), rep.Truncation.SubagentWallTimeReturned)
	assert.Equal(t, int64(len(rep.DuplicateCommands)), rep.Truncation.DuplicateCommandsReturned)
	assert.Equal(t, int64(len(rep.SubagentWallTime)), rep.Truncation.SubagentWallTimeReturned)
}
