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

// residencyByTool indexes the residency family by tool name.
func residencyByTool(rows []ResultResidencyRow) map[string]ResultResidencyRow {
	out := make(map[string]ResultResidencyRow, len(rows))
	for _, r := range rows {
		out[r.ToolName] = r
	}
	return out
}

// TestResidencyByTool_WeightsResultTokensBySubsequentCalls drives a hand-computable lane.
//
// The lane is laid out so each of the three claims has its own witness:
//   - WEIGHTING: two tools return the SAME number of bytes at different points in the lane,
//     so identical result sizes produce different residency. A fold that ignored the
//     weighting would report them equal.
//   - IMAGES: a result carrying only images accrues cost from the flat per-image estimate,
//     so an implementation that priced results by bytes alone reports zero for it.
//   - THE ISSUING TURN IS EXCLUDED: one tool call shares its instant with a model call. That
//     call was billed before the result existed, so it must not be counted. The lane is
//     built so including it would change the number.
func TestResidencyByTool_WeightsResultTokensBySubsequentCalls(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")
	const sess = "S1"

	// 3600 bytes is exactly 1000 result tokens at the bytes-per-token estimate, which keeps
	// every expected value below a whole number rather than a rounding artifact.
	const bytes3600 = 3600
	const tokens1000 = 1000

	modelRow := func(ts time.Time) transcripts.Row {
		return transcripts.Row{Model: "m", SessionID: sess, RecordTS: ts, InputTokens: 100}
	}
	toolRow := func(ts time.Time, tool string, nbytes, images int64) transcripts.Row {
		return transcripts.Row{
			Model: "m", SessionID: sess, RecordTS: ts, ToolName: tool, DurationMs: 10,
			ToolResultBytes: nbytes, ToolResultImages: images,
		}
	}

	rows := []transcripts.Row{
		// t+0: a model call, and Early's tool call at the SAME instant. The model call at
		// t+0 issued it, so it is NOT one of Early's subsequent calls.
		modelRow(base),
		toolRow(base, "Early", bytes3600, 0),
		// Four model calls follow Early: t+1m, t+2m, t+3m, t+4m.
		modelRow(base.Add(1 * time.Minute)),
		modelRow(base.Add(2 * time.Minute)),
		// t+2m30s: Late returns the SAME bytes as Early, but only one model call follows.
		toolRow(base.Add(150*time.Second), "Late", bytes3600, 0),
		modelRow(base.Add(3 * time.Minute)),
		// t+3m30s: Snapshot returns two images and no text, with no model call after it.
		toolRow(base.Add(210*time.Second), "Snapshot", 0, 2),
		// t+4m: a final model call, so Snapshot has exactly one subsequent call.
		modelRow(base.Add(4 * time.Minute)),
	}

	rep, err := serviceOverRows(t, rows).RunDetectors(context.Background(), Filters{})
	require.NoError(t, err)
	byTool := residencyByTool(rep.ResultResidencyByTool)
	require.Len(t, byTool, 3, "one row per tool that measured a result")

	early := byTool["Early"]
	assert.Equal(t, int64(1), early.Calls)
	assert.Equal(t, int64(bytes3600), early.ResultBytes)
	assert.Equal(t, int64(tokens1000), early.ResultTokens)
	assert.Equal(t, int64(4*tokens1000), early.ResidentTokens,
		"four model calls follow it; counting the call that ISSUED it would give %d", 5*tokens1000)

	late := byTool["Late"]
	assert.Equal(t, late.ResultTokens, early.ResultTokens, "identical result sizes")
	assert.Equal(t, int64(2*tokens1000), late.ResidentTokens, "but only two model calls follow it")
	assert.Less(t, late.ResidentTokens, early.ResidentTokens,
		"so the same bytes cost less when returned later — which is the whole weighting claim")

	snap := byTool["Snapshot"]
	assert.Equal(t, int64(0), snap.ResultBytes, "no text at all")
	assert.Equal(t, int64(2*imageResultTokens), snap.ResultTokens, "images are priced as images")
	assert.Equal(t, int64(2*imageResultTokens), snap.ResidentTokens, "one model call follows")

	// Ordering: resident desc, then tool asc. Early 4000, Snapshot 3000, Late 2000.
	require.Len(t, rep.ResultResidencyByTool, 3)
	assert.Equal(t, "Early", rep.ResultResidencyByTool[0].ToolName, "4000 resident tokens leads")
	assert.Equal(t, "Snapshot", rep.ResultResidencyByTool[1].ToolName)
	assert.Equal(t, "Late", rep.ResultResidencyByTool[2].ToolName)

	// PctBilledInput is measured against the corpus's total billed input, which this fixture
	// makes 5 model rows x 100 input tokens = 500.
	assert.InDelta(t, float64(early.ResidentTokens)/500.0*100, early.PctBilledInput, 0.0001)
}

// TestResidencyByTool_DisclosesUnbackfilledLanes pins the field that tells a caller whether
// a residency zero is a measurement or an artifact.
//
// The two arms are the point: over a lane whose rows carry no result sizes — the shape every
// lane cached before the columns existed has — the family is EMPTY and the disclosure reads
// zero; over a lane that does carry them, the disclosure counts it. A single arm could not
// distinguish "nothing measured" from "nothing to measure".
func TestResidencyByTool_DisclosesUnbackfilledLanes(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")

	t.Run("un-backfilled lane", func(t *testing.T) {
		rep, err := serviceOverRows(t, []transcripts.Row{
			{Model: "m", SessionID: "S1", RecordTS: base, InputTokens: 100},
			{Model: "m", SessionID: "S1", RecordTS: base.Add(time.Minute), ToolName: "Bash", DurationMs: 10},
		}).RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)

		assert.Empty(t, rep.ResultResidencyByTool,
			"a tool whose results measured nothing is omitted, not emitted as a row of zeros")
		assert.Equal(t, int64(1), rep.Corpus.LaneCount)
		assert.Equal(t, int64(0), rep.Corpus.LanesWithResultBytes,
			"and the report says the zero is an artifact of an un-backfilled lane")
	})

	t.Run("backfilled lane", func(t *testing.T) {
		rep, err := serviceOverRows(t, []transcripts.Row{
			{Model: "m", SessionID: "S1", RecordTS: base, InputTokens: 100},
			{Model: "m", SessionID: "S1", RecordTS: base.Add(time.Minute), ToolName: "Bash", DurationMs: 10, ToolResultBytes: 3600},
			{Model: "m", SessionID: "S1", RecordTS: base.Add(2 * time.Minute), InputTokens: 100},
		}).RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)

		require.Len(t, rep.ResultResidencyByTool, 1)
		assert.Equal(t, int64(1), rep.Corpus.LanesWithResultBytes)
		assert.Equal(t, rep.Corpus.LaneCount, rep.Corpus.LanesWithResultBytes,
			"every lane in scope carries the measurement")
	})

	t.Run("spilled results are counted separately", func(t *testing.T) {
		rep, err := serviceOverRows(t, []transcripts.Row{
			{Model: "m", SessionID: "S1", RecordTS: base, InputTokens: 100},
			{Model: "m", SessionID: "S1", RecordTS: base.Add(time.Minute), ToolName: "Bash", DurationMs: 10, ToolResultBytes: 3600, ToolResultSpilled: true},
			{Model: "m", SessionID: "S1", RecordTS: base.Add(90 * time.Second), ToolName: "Bash", DurationMs: 10, ToolResultBytes: 3600},
			{Model: "m", SessionID: "S1", RecordTS: base.Add(2 * time.Minute), InputTokens: 100},
		}).RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)

		require.Len(t, rep.ResultResidencyByTool, 1)
		row := rep.ResultResidencyByTool[0]
		assert.Equal(t, int64(2), row.Calls)
		assert.Equal(t, int64(1), row.SpilledResults,
			"so a reader can tell how much of this figure rests on a RECOVERED size")
	})
}
