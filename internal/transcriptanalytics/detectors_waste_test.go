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

// gatedRunMs is one permission-gated tool call's measured tool_use-to-tool_result span: ~52
// minutes, the shape the ticket reported. It is trustworthy (under idleGuardCeilingMs), so
// the detector admits it and the bound is the only thing that can tell it apart from work.
const gatedRunMs = 52 * 60 * 1000

// fastLatencySamples is how many fast rows a fixture needs for a tool's p99.9 to be
// dominated by them. It is fixed by the arithmetic rather than chosen per test: at q=0.999
// the target bucket is reached by roughly 999 fast rows per slow one, so a fixture carrying
// two slow rows needs at least ~2,000 fast ones for the bound to sit at the fast bucket.
const fastLatencySamples = 3000

// fastLatencyRows returns fastLatencySamples hash-free rows for tool, each 10ms, which
// populate the tool's latency histogram without forming a duplicate group.
func fastLatencyRows(t *testing.T, tool string) []transcripts.Row {
	t.Helper()
	base := mustTS(t, "2026-06-01T09:00:00Z")
	rows := make([]transcripts.Row, 0, fastLatencySamples)
	for i := range fastLatencySamples {
		rows = append(rows, transcripts.Row{
			Model: "m", SessionID: "FILLER", RecordTS: base.Add(time.Duration(i) * time.Millisecond),
			ToolName: tool, DurationMs: 10,
		})
	}
	return rows
}

// dupRowByHash indexes a duplicate family by its tool input hash.
func dupRowByHash(rows []DuplicateCommandRow) map[string]DuplicateCommandRow {
	out := make(map[string]DuplicateCommandRow, len(rows))
	for _, r := range rows {
		out[r.ToolInputHash] = r
	}
	return out
}

// dupIndexOf returns the position of the group with hash in the emitted order, or -1.
func dupIndexOf(rows []DuplicateCommandRow, hash string) int {
	for i, r := range rows {
		if r.ToolInputHash == hash {
			return i
		}
	}
	return -1
}

// TestDuplicateCommands_ImplausibleWasteFlagged reproduces the ticket's measured shape: a
// permission-gated command rerun several times, whose per-call "waste" is three orders of
// magnitude above the tool's own p99.9 because the span contains a human answering rather
// than a tool running.
//
// The group is seeded with one INTERRUPTED run so RunCount (3) and the trustworthy run
// count (2) differ. That is what makes the per-call assertion discriminating: the right
// denominator gives 3,120,000 and the wrong one gives 2,080,000.
func TestDuplicateCommands_ImplausibleWasteFlagged(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")

	rows := fastLatencyRows(t, "Edit")
	rows = append(rows,
		// The gated group: two trustworthy 52-minute runs plus one interrupted run.
		transcripts.Row{Model: "m", SessionID: "S1", RecordTS: base, ToolName: "Edit", DurationMs: gatedRunMs, ToolInputHash: "gated", ToolInputPreview: "edit lefthook.yml"},
		transcripts.Row{Model: "m", SessionID: "S1", RecordTS: base.Add(time.Hour), ToolName: "Edit", DurationMs: gatedRunMs, ToolInputHash: "gated", ToolInputPreview: "edit lefthook.yml"},
		transcripts.Row{Model: "m", SessionID: "S1", RecordTS: base.Add(2 * time.Hour), ToolName: "Edit", DurationMs: gatedRunMs, ToolInputHash: "gated", ToolInputPreview: "edit lefthook.yml", Interrupted: true},
		// A genuinely-rerun fast command: strictly LESS wasted time, and plausible.
		transcripts.Row{Model: "m", SessionID: "S2", RecordTS: base, ToolName: "Edit", DurationMs: 10, ToolInputHash: "real", ToolInputPreview: "edit main.go"},
		transcripts.Row{Model: "m", SessionID: "S2", RecordTS: base.Add(time.Minute), ToolName: "Edit", DurationMs: 10, ToolInputHash: "real", ToolInputPreview: "edit main.go"},
	)

	rep, err := serviceOverRows(t, rows).RunDetectors(context.Background(), Filters{})
	require.NoError(t, err)

	byHash := dupRowByHash(rep.DuplicateCommands)
	require.Contains(t, byHash, "gated", "the implausible row is FLAGGED, not deleted")
	require.Contains(t, byHash, "real")

	gated := byHash["gated"]
	assert.Equal(t, wasteVerdictImplausible, gated.WasteVerdict)
	assert.Equal(t, int64(3), gated.RunCount, "all three runs count")
	assert.Equal(t, int64(2*gatedRunMs), gated.WastedDurationMs, "but only the two trustworthy ones carry time")
	assert.Equal(t, int64(gatedRunMs), gated.PerCallWasteMs,
		"per-call divides by the TRUSTWORTHY run count (2), not by RunCount (3), which would give %d", 2*gatedRunMs/3)
	assert.Positive(t, gated.ToolP999Ms, "Edit clears the sample floor, so a bound exists")
	assert.Less(t, gated.ToolP999Ms, gated.PerCallWasteMs/1000,
		"and the bound is orders of magnitude below the observed per-call span")

	real := byHash["real"]
	assert.Equal(t, wasteVerdictPlausible, real.WasteVerdict)
	assert.Less(t, real.WastedDurationMs, gated.WastedDurationMs,
		"the plausible group wasted strictly less time")

	// The ordering assertion, which is what proves the ranking changed rather than just the
	// label: under the old wasted-desc-only sort the gated row led the family.
	assert.Less(t, dupIndexOf(rep.DuplicateCommands, "real"), dupIndexOf(rep.DuplicateCommands, "gated"),
		"an implausible row sorts BELOW a plausible one even with vastly more wasted time")
}

// TestDuplicateCommands_UndeterminedBelowSampleFloor guards the failure the sample floor
// exists to prevent. histPercentile returns the LAST bucket once ceil(q*total) reaches
// total, and 0 when total is 0 — so without the floor a rarely-called tool's groups would
// be measured against a meaningless or zero bound and branded implausible.
//
// The second arm is what proves the floor is a threshold rather than a permanent excuse:
// the same group, with the same runs, crosses into a real verdict once its tool has enough
// samples.
func TestDuplicateCommands_UndeterminedBelowSampleFloor(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")

	// The rare tool's group, identical in both arms.
	rareGroup := []transcripts.Row{
		{Model: "m", SessionID: "S1", RecordTS: base, ToolName: "Rare", DurationMs: gatedRunMs, ToolInputHash: "rare", ToolInputPreview: "rare call"},
		{Model: "m", SessionID: "S1", RecordTS: base.Add(time.Hour), ToolName: "Rare", DurationMs: gatedRunMs, ToolInputHash: "rare", ToolInputPreview: "rare call"},
	}
	// A well-sampled tool with a plausible group, so the fixture has something to rank against.
	plausible := append(fastLatencyRows(t, "Edit"),
		transcripts.Row{Model: "m", SessionID: "S2", RecordTS: base, ToolName: "Edit", DurationMs: 10, ToolInputHash: "real", ToolInputPreview: "edit main.go"},
		transcripts.Row{Model: "m", SessionID: "S2", RecordTS: base.Add(time.Minute), ToolName: "Edit", DurationMs: 10, ToolInputHash: "real", ToolInputPreview: "edit main.go"},
	)

	t.Run("below the floor", func(t *testing.T) {
		rows := append(append([]transcripts.Row{}, plausible...), rareGroup...)
		rep, err := serviceOverRows(t, rows).RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)

		byHash := dupRowByHash(rep.DuplicateCommands)
		require.Contains(t, byHash, "rare")
		rare := byHash["rare"]

		assert.Equal(t, wasteVerdictUndetermined, rare.WasteVerdict,
			"Rare has 2 samples, far under minSamplesForP999")
		assert.Equal(t, int64(0), rare.ToolP999Ms, "no bound is claimed when none can be established")
		assert.Equal(t, int64(gatedRunMs), rare.PerCallWasteMs,
			"the per-call figure is still reported; only the bound is withheld")

		// Undetermined is NOT a demotion: the row keeps its place among the un-flagged rows,
		// which for this fixture means above the plausible group it out-wastes.
		assert.Less(t, dupIndexOf(rep.DuplicateCommands, "rare"), dupIndexOf(rep.DuplicateCommands, "real"),
			"an undetermined row ranks with the plausible ones, not below them")
	})

	t.Run("across the floor", func(t *testing.T) {
		rows := append(append([]transcripts.Row{}, plausible...), rareGroup...)
		rows = append(rows, fastLatencyRows(t, "Rare")...)

		rep, err := serviceOverRows(t, rows).RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)

		byHash := dupRowByHash(rep.DuplicateCommands)
		require.Contains(t, byHash, "rare")
		rare := byHash["rare"]

		assert.NotEqual(t, wasteVerdictUndetermined, rare.WasteVerdict,
			"the same group crosses into a real verdict once its tool clears the floor")
		assert.Equal(t, wasteVerdictImplausible, rare.WasteVerdict,
			"and with a 52-minute per-call span against a 10ms distribution, that verdict is implausible")
		assert.Positive(t, rare.ToolP999Ms, "a bound now exists")
	})
}
