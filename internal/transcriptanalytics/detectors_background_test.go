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

// TestDuplicateCommands_SurfacesBackgroundFlag pins the background disclosure and, more
// importantly, pins that it is ONLY a disclosure.
//
// The second arm is the one that matters. A backgrounded call returns a task handle
// immediately, so it is FASTER than its foreground equivalent — measured over the corpus,
// background Bash runs p50 36ms against foreground p50 117ms. A long background duration is
// therefore MORE anomalous, not less, and exempting background rows from the plausibility
// bound would suppress exactly the rows most worth seeing. That arm goes red if anyone adds
// such a carve-out.
func TestDuplicateCommands_SurfacesBackgroundFlag(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")

	bashRow := func(ts time.Time, hash string, ms int64, bg bool) transcripts.Row {
		return transcripts.Row{
			Model: "m", SessionID: "S1", RecordTS: ts, ToolName: "Edit", DurationMs: ms,
			ToolInputHash: hash, ToolInputPreview: "edit " + hash, RunInBackground: bg,
		}
	}

	t.Run("a mixed group counts its background runs", func(t *testing.T) {
		rows := []transcripts.Row{
			bashRow(base, "mixed", 10, false),
			bashRow(base.Add(1*time.Minute), "mixed", 10, true),
			bashRow(base.Add(2*time.Minute), "mixed", 10, false),
			bashRow(base.Add(3*time.Minute), "mixed", 10, true),
			bashRow(base.Add(4*time.Minute), "mixed", 10, false),
		}
		rep, err := serviceOverRows(t, rows).RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)

		byHash := dupRowByHash(rep.DuplicateCommands)
		require.Contains(t, byHash, "mixed")
		assert.Equal(t, int64(5), byHash["mixed"].RunCount)
		assert.Equal(t, int64(2), byHash["mixed"].BackgroundRuns,
			"a count, not a bool: this group genuinely mixes both, and a bool would have to lie about three rows")
	})

	t.Run("the flag exempts no row from the plausibility bound", func(t *testing.T) {
		// Edit clears the sample floor via fast foreground rows, so a p99.9 exists.
		rows := append(fastLatencyRows(t, "Edit"),
			// A BACKGROUNDED duplicate group whose runs are absurdly long for a call that
			// returns a handle immediately.
			bashRow(base, "bg-slow", gatedRunMs, true),
			bashRow(base.Add(time.Hour), "bg-slow", gatedRunMs, true),
		)
		rep, err := serviceOverRows(t, rows).RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)

		byHash := dupRowByHash(rep.DuplicateCommands)
		require.Contains(t, byHash, "bg-slow")
		row := byHash["bg-slow"]

		assert.Equal(t, int64(2), row.BackgroundRuns, "both runs were backgrounded")
		assert.Equal(t, wasteVerdictImplausible, row.WasteVerdict,
			"and it is STILL implausible; a background carve-out would report plausible here")
		assert.Positive(t, row.ToolP999Ms)
		assert.Greater(t, row.PerCallWasteMs, row.ToolP999Ms)
	})

	t.Run("a group with no background runs reports zero", func(t *testing.T) {
		rows := []transcripts.Row{
			bashRow(base, "fg", 10, false),
			bashRow(base.Add(time.Minute), "fg", 10, false),
		}
		rep, err := serviceOverRows(t, rows).RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)

		byHash := dupRowByHash(rep.DuplicateCommands)
		require.Contains(t, byHash, "fg")
		assert.Equal(t, int64(0), byHash["fg"].BackgroundRuns,
			"reported as zero rather than omitted, so a reader can tell it was measured")
	})
}

// TestLaneDetail_TopWaitsCarryBackgroundFlag covers the other half of this disclosure. A
// wait row is a single call, so the flag is exact there rather than a count — and both
// values must appear, or the field could be hardwired.
func TestLaneDetail_TopWaitsCarryBackgroundFlag(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")

	rep, err := serviceOverRows(t, []transcripts.Row{
		{Model: "m", SessionID: "SL", RecordTS: base, InputTokens: 10},
		{Model: "m", SessionID: "SL", RecordTS: base.Add(time.Minute), ToolName: "Bash", DurationMs: 9000, ToolInputPreview: "long background", RunInBackground: true},
		{Model: "m", SessionID: "SL", RecordTS: base.Add(2 * time.Minute), ToolName: "Bash", DurationMs: 5000, ToolInputPreview: "long foreground"},
	}).RunDetectors(context.Background(), Filters{Scope: ScopeSingle, SessionID: "SL"})
	require.NoError(t, err)
	require.NotNil(t, rep.LaneDetail)
	require.Len(t, rep.LaneDetail.TopWaits, 2)

	assert.True(t, rep.LaneDetail.TopWaits[0].Background, "the 9s background call leads and is flagged")
	assert.False(t, rep.LaneDetail.TopWaits[1].Background, "the 5s foreground call is not")
}
