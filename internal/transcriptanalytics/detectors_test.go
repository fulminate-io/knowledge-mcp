// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDetectors_AllFamilies exercises every detector family against the shared fixture
// and asserts the exact expected aggregates. The is_meta and <synthetic> rows carry
// inflated values that would perturb every total if not excluded — so a broken baseline
// (dropping the exclusion) fails these assertions red.
func TestDetectors_AllFamilies(t *testing.T) {
	svc, path := newDetectorFixture(t)
	ctx := context.Background()
	src := []string{path}
	f := Filters{}

	t.Run("duplicate commands (time-only, session-scoped)", func(t *testing.T) {
		got, err := svc.duplicateCommandsFrom(ctx, src, f)
		require.NoError(t, err)
		require.Len(t, got, 1, "only the twice-run Bash/h1 command qualifies")
		assert.Equal(t, "S1", got[0].SessionID)
		assert.Equal(t, "Bash", got[0].ToolName)
		assert.Equal(t, "h1", got[0].ToolInputHash)
		assert.Equal(t, int64(2), got[0].RunCount)
		assert.Equal(t, int64(2000), got[0].WastedDurationMs)
		assert.Equal(t, "ls", got[0].SamplePreview)
	})

	t.Run("tool latency percentiles (idle-guarded)", func(t *testing.T) {
		got, err := svc.toolLatencyFrom(ctx, src, f)
		require.NoError(t, err)
		// Read (3000, one call) ranks above Bash by p90; the interrupted Bash row and
		// the synthetic Read row are both excluded.
		require.Len(t, got, 2)
		assert.Equal(t, "Read", got[0].ToolName)
		assert.Equal(t, int64(1), got[0].Count)
		assert.Equal(t, int64(3000), got[0].P90)
		assert.Equal(t, "Bash", got[1].ToolName)
		assert.Equal(t, int64(2), got[1].Count)
		assert.Equal(t, int64(1000), got[1].P50)
	})

	t.Run("per-tool time totals (de-idled)", func(t *testing.T) {
		got, err := svc.toolTimeTotalFrom(ctx, src, f)
		require.NoError(t, err)
		require.Len(t, got, 2)
		// Read total 3000 > Bash total 2000 (interrupted 10000ms Bash row de-idled to 0).
		assert.Equal(t, "Read", got[0].ToolName)
		assert.Equal(t, int64(3000), got[0].TotalDurationMs)
		assert.Equal(t, "Bash", got[1].ToolName)
		assert.Equal(t, int64(2000), got[1].TotalDurationMs)
		assert.Equal(t, int64(3), got[1].CallCount, "all three Bash rows are counted; only the interrupted one's time is dropped")
	})

	t.Run("avg tokens per session", func(t *testing.T) {
		got, err := svc.avgTokensPerSessionFrom(ctx, src, f)
		require.NoError(t, err)
		assert.Equal(t, int64(1), got.SessionCount)
		assert.InDelta(t, 2000.0, got.AvgInputTokens, 0.001)
		assert.InDelta(t, 4850.0, got.AvgOutputTokens, 0.001)
		assert.InDelta(t, 6850.0, got.AvgTotalTokens, 0.001)
	})

	t.Run("token hotspots by subagent type", func(t *testing.T) {
		got, err := svc.tokenByDimensionFrom(ctx, src, colSubagentType, f)
		require.NoError(t, err)
		byKey := map[string]TokenByDimensionRow{}
		for _, r := range got {
			byKey[r.Key] = r
		}
		assert.Equal(t, int64(500), byKey["researcher"].InputTokens)
		assert.Equal(t, int64(250), byKey["researcher"].OutputTokens)
		assert.Equal(t, int64(200), byKey["planner"].InputTokens)
		assert.Equal(t, int64(100), byKey["planner"].OutputTokens)
	})

	t.Run("cache efficiency ratio + split", func(t *testing.T) {
		got, err := svc.cacheEfficiencyFrom(ctx, src, f)
		require.NoError(t, err)
		assert.Equal(t, int64(800), got.CacheReadTokens)
		assert.Equal(t, int64(2000), got.InputTokens)
		assert.InDelta(t, 0.4, got.CacheReadRatio, 0.001)
		assert.Equal(t, int64(40), got.CacheCreation1hTokens)
		assert.Equal(t, int64(60), got.CacheCreation5mTokens)
	})

	t.Run("subagent wall time", func(t *testing.T) {
		got, err := svc.subagentWallTimeFrom(ctx, src, f)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "agent-1", got[0].AgentID)
		assert.Equal(t, "researcher", got[0].SubagentType)
		assert.Equal(t, int64(120000), got[0].WallMs)
		assert.Equal(t, int64(500), got[0].InputTokens)
		assert.Equal(t, "agent-2", got[1].AgentID)
		assert.Equal(t, int64(60000), got[1].WallMs)
	})

	t.Run("agent-chain over-orchestration proxy", func(t *testing.T) {
		got, err := svc.agentChainFrom(ctx, src, f)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "S1", got[0].SessionID)
		assert.Equal(t, int64(2), got[0].SubagentCount)
		assert.Equal(t, int64(2), got[0].SubagentTypeDiversity)
		assert.Equal(t, int64(180000), got[0].TotalSubagentWallMs)
		assert.Equal(t, int64(120000), got[0].MaxSubagentWallMs)
	})

	t.Run("waste summary (incl. max_tokens truncation + is_meta exclusion)", func(t *testing.T) {
		got, err := svc.wasteSummaryFrom(ctx, src, f)
		require.NoError(t, err)
		assert.Equal(t, int64(1), got.APIErrorCount, "the is_meta api-error row is excluded")
		assert.Equal(t, int64(1), got.InterruptedCount, "the is_meta interrupted row is excluded")
		assert.Equal(t, int64(1), got.MaxTokensTruncationCount, "only the non-meta max_tokens row counts")
		assert.Equal(t, int64(4000), got.MaxTokensOutputTokens)
		assert.Equal(t, int64(5000), got.MaxTokensDurationMs)
		assert.Equal(t, int64(40), got.CacheCreation1hTokens)
		assert.Equal(t, int64(60), got.CacheCreation5mTokens)
	})
}
