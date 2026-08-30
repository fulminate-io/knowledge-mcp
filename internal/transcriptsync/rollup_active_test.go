// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// activeParityFixture is the shared parity fixture's shape. The file is DECLARED ONCE in
// this package's testdata and read by a second test in the daemon-local analyzer package,
// so neither side of the hand-mirrored reduction can drift alone.
type activeParityFixture struct {
	Cases []struct {
		Name             string   `json:"name"`
		Instants         []string `json:"instants"`
		ExpectedActiveMs int64    `json:"expected_active_ms"`
	} `json:"cases"`
}

// TestRollupActiveMs_DaemonParityFixture runs the producer-side mirror over every case of
// the shared fixture. Its counterpart in the analyzer package runs the daemon's own
// activeMsFromInstants over the same file, so the two reductions are pinned to the same
// numbers rather than to two readings of the same description.
func TestRollupActiveMs_DaemonParityFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "active_parity_instants.json"))
	require.NoError(t, err, "the shared parity fixture must be readable from this package")
	var fx activeParityFixture
	require.NoError(t, json.Unmarshal(raw, &fx))
	require.NotEmpty(t, fx.Cases, "the fixture must carry cases")

	for _, c := range fx.Cases {
		instants := make([]time.Time, 0, len(c.Instants))
		for _, s := range c.Instants {
			ts, err := time.Parse(time.RFC3339Nano, s)
			require.NoError(t, err, "case %s instant %q", c.Name, s)
			instants = append(instants, ts)
		}
		assert.Equal(t, c.ExpectedActiveMs, rollupActiveMs(instants), "case %s", c.Name)
	}
}

// sidechainRow builds a sidechain agent row at the given instant. Every dimension other
// than tool_name is held constant so a caller varying tool_name splits the fact grain and
// nothing else does.
func sidechainRow(agentID, tool string, ts time.Time) transcripts.Row {
	return transcripts.Row{
		Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "sonnet",
		ToolName: tool, SubagentType: "impl", AgentID: agentID, IsSidechain: true,
		RecordTS: ts, DurationMs: 10,
	}
}

// TestRollupActive_WholeLifeAndPerDay pins the base case: one sidechain agent, three
// instants on one day, every gap under the mirrored idle threshold, so both grains report
// the same total.
func TestRollupActive_WholeLifeAndPerDay(t *testing.T) {
	rows := []transcripts.Row{
		sidechainRow("a1", "Bash", base),
		sidechainRow("a1", "Bash", base.Add(30*time.Second)),
		sidechainRow("a1", "Bash", base.Add(90*time.Second)),
	}
	p := computeSessionRollup(rows)

	facts := findFacts(p, func(f factRow) bool { return f.AgentID == "a1" })
	require.Len(t, facts, 1, "all three rows collapse into one fact grain")
	f := facts[0]
	require.NotNil(t, f.ActiveMs, "a qualifying row always carries a measured whole-life active")
	require.NotNil(t, f.DayActiveMs, "a qualifying row always carries a measured per-day active")
	assert.Equal(t, int64(90_000), *f.ActiveMs, "30s + 60s of sub-threshold gaps")
	assert.Equal(t, int64(90_000), *f.DayActiveMs, "one day, so the per-day bucket equals the whole-life total")
}

// TestRollupActive_MidnightCrossing is the property the whole-life field exists for: the
// agent's only gap spans midnight, so it falls in neither day's instant list and both
// per-day buckets are a measured 0 against a true whole-life 2999.
func TestRollupActive_MidnightCrossing(t *testing.T) {
	before := time.Date(2026, 6, 20, 23, 59, 59, 0, time.UTC)
	after := time.Date(2026, 6, 21, 0, 0, 1, 999_500_000, time.UTC)
	rows := []transcripts.Row{
		sidechainRow("a-mid", "Bash", before),
		sidechainRow("a-mid", "Bash", after),
	}
	p := computeSessionRollup(rows)

	facts := findFacts(p, func(f factRow) bool { return f.AgentID == "a-mid" })
	require.Len(t, facts, 2, "the two instants land on different days, so different fact grains")
	for _, f := range facts {
		require.NotNil(t, f.ActiveMs, "day %s carries a whole-life active", f.Day)
		require.NotNil(t, f.DayActiveMs, "day %s carries a per-day active", f.Day)
		assert.Equal(t, int64(2_999), *f.ActiveMs,
			"whole-life active spans midnight: 23:59:59.000 to 00:00:01.999 on the floor-epoch ms")
		assert.Equal(t, int64(0), *f.DayActiveMs,
			"day %s has a single instant, so its bucket is a MEASURED zero — the midnight gap is dropped", f.Day)
	}
}

// TestRollupActive_DenormalizedAcrossFactGrains proves the same value is repeated onto
// every fact row of the grain and never split across them. The agent's whole active time
// is the gap BETWEEN its two fact rows, inside neither, which is why the reader takes MAX
// at each field's own grain and never SUM.
func TestRollupActive_DenormalizedAcrossFactGrains(t *testing.T) {
	rows := []transcripts.Row{
		sidechainRow("a-xyz", "", base),
		sidechainRow("a-xyz", "Read", base.Add(90*time.Second)),
	}
	p := computeSessionRollup(rows)

	facts := findFacts(p, func(f factRow) bool { return f.AgentID == "a-xyz" })
	require.Len(t, facts, 2, "the differing tool_name splits the agent-day across two fact grains")
	for _, f := range facts {
		require.NotNil(t, f.ActiveMs, "tool_name %q carries a whole-life active", f.ToolName)
		require.NotNil(t, f.DayActiveMs, "tool_name %q carries a per-day active", f.ToolName)
		assert.Equal(t, int64(90_000), *f.ActiveMs,
			"tool_name %q carries the agent's FULL whole-life active, not a per-row share", f.ToolName)
		assert.Equal(t, int64(90_000), *f.DayActiveMs,
			"tool_name %q carries the agent-day's FULL active, not a per-row share", f.ToolName)
	}
}

// TestRollupActive_AbsentNotZero pins the four statements that keep "never measured" apart
// from "measured zero", each on a row shape no other case covers.
func TestRollupActive_AbsentNotZero(t *testing.T) {
	mainLane := transcripts.Row{
		Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "sonnet",
		ToolName: "Grep", RecordTS: base, DurationMs: 10,
	}
	// The agent-id-WITHOUT-is_sidechain shape: the ONLY row shape on which the daemon's
	// conjunction and an agent_id-only rule produce different output. Two rows 90 seconds
	// apart, so an agent_id-only rule would report 90000 here rather than a 0 that is easy
	// to confuse with absent.
	noSidechain := func(ts time.Time) transcripts.Row {
		return transcripts.Row{
			Source: transcripts.SourceClaude, SessionID: "s", Project: "/w", Model: "sonnet",
			ToolName: "Edit", SubagentType: "impl", AgentID: "a-nosc", IsSidechain: false,
			RecordTS: ts, DurationMs: 10,
		}
	}
	rows := []transcripts.Row{
		mainLane,
		sidechainRow("a-one", "Bash", base),
		noSidechain(base),
		noSidechain(base.Add(90 * time.Second)),
	}
	p := computeSessionRollup(rows)

	// (a) a main-lane row is never measured at all.
	mains := findFacts(p, func(f factRow) bool { return f.AgentID == "" })
	require.Len(t, mains, 1, "the main-lane row ships as its own fact row")
	require.Nil(t, mains[0].ActiveMs, "a main-lane row carries no whole-life active")
	require.Nil(t, mains[0].DayActiveMs, "a main-lane row carries no per-day active")

	// (b) a qualifying agent with a single record is MEASURED, and its measurement is 0.
	ones := findFacts(p, func(f factRow) bool { return f.AgentID == "a-one" })
	require.Len(t, ones, 1, "the single sidechain record ships as its own fact row")
	require.NotNil(t, ones[0].ActiveMs, "a qualifying row is measured even when the measurement is 0")
	require.NotNil(t, ones[0].DayActiveMs, "a qualifying row is measured even when the measurement is 0")
	assert.Equal(t, int64(0), *ones[0].ActiveMs, "one instant means no gap exists: a MEASURED zero")
	assert.Equal(t, int64(0), *ones[0].DayActiveMs, "one instant means no gap exists: a MEASURED zero")

	// (d) the conjunction: an agent_id without is_sidechain is not the daemon's population,
	// so it is never measured and never denormalized onto.
	nosc := findFacts(p, func(f factRow) bool { return f.AgentID == "a-nosc" })
	require.NotEmpty(t, nosc, "the agent-id-without-sidechain rows ship as fact rows")
	for _, f := range nosc {
		require.Nil(t, f.ActiveMs,
			"an agent_id without is_sidechain is outside the collection predicate, so it is UNMEASURED")
		require.Nil(t, f.DayActiveMs,
			"an agent_id without is_sidechain is outside the collection predicate, so it is UNMEASURED")
	}

	// (c) the WIRE carries the distinction, not merely the in-memory struct: marshal the
	// payload and decode it back into a fresh one.
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	var back rollupPayload
	require.NoError(t, json.Unmarshal(raw, &back))

	backMains := findFacts(back, func(f factRow) bool { return f.AgentID == "" })
	require.Len(t, backMains, 1)
	require.Nil(t, backMains[0].ActiveMs, "nil survives the wire round-trip as nil")
	require.Nil(t, backMains[0].DayActiveMs, "nil survives the wire round-trip as nil")

	backOnes := findFacts(back, func(f factRow) bool { return f.AgentID == "a-one" })
	require.Len(t, backOnes, 1)
	require.NotNil(t, backOnes[0].ActiveMs, "a measured 0 survives the wire round-trip as a pointer, not as nil")
	require.NotNil(t, backOnes[0].DayActiveMs, "a measured 0 survives the wire round-trip as a pointer, not as nil")
	assert.Equal(t, int64(0), *backOnes[0].ActiveMs, "the measured zero decodes back as 0")
	assert.Equal(t, int64(0), *backOnes[0].DayActiveMs, "the measured zero decodes back as 0")
}
