// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunDetectors_FullReport proves the concurrent fan-out populates every family
// section + waste over the shared fixture cache (resolved via the cache glob, not an
// explicit path), matching the per-family assertions.
func TestRunDetectors_FullReport(t *testing.T) {
	svc := newDetectorFixture(t)

	rep, err := svc.RunDetectors(context.Background())
	require.NoError(t, err)
	require.NotNil(t, rep)

	assert.Len(t, rep.DuplicateCommands, 1, "duplicate family populated")
	assert.Len(t, rep.ToolLatency, 2, "tool latency family populated")
	assert.Len(t, rep.ToolTimeTotals, 2, "per-tool time totals populated")
	assert.Equal(t, int64(1), rep.AvgTokensPerSession.SessionCount)
	assert.NotEmpty(t, rep.TokensBySubagentType, "subagent token hotspots populated")
	assert.Equal(t, int64(800), rep.CacheEfficiency.CacheReadTokens)
	assert.Len(t, rep.SubagentWallTime, 2, "subagent wall-time populated")
	assert.Len(t, rep.AgentChains, 1, "agent-chain family populated")
	assert.Equal(t, int64(1), rep.Waste.MaxTokensTruncationCount, "waste incl. max_tokens populated")
	assert.Equal(t, int64(1), rep.Waste.APIErrorCount)
	assert.Equal(t, int64(1), rep.Waste.InterruptedCount)
}

// TestRunDetectors_EmptyCache proves the zero-path short-circuit: an empty cache returns a
// zero-value report and a nil error WITHOUT decoding (loadCorpus over a zero-match glob
// yields an empty corpus, and every fold returns empty over empty accumulators).
func TestRunDetectors_EmptyCache(t *testing.T) {
	svc, err := NewService(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	rep, err := svc.RunDetectors(context.Background())
	require.NoError(t, err)
	require.NotNil(t, rep)
	assert.Empty(t, rep.DuplicateCommands)
	assert.Empty(t, rep.ToolLatency)
	assert.Empty(t, rep.SubagentWallTime)
	assert.Equal(t, int64(0), rep.AvgTokensPerSession.SessionCount)
	assert.Equal(t, int64(0), rep.Waste.MaxTokensTruncationCount)
}
