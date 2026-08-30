// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// subagentRow builds one sidechain record for agent id at ts.
func subagentRow(session, id string, ts time.Time) transcripts.Row {
	return transcripts.Row{
		Model:        "m",
		SessionID:    session,
		RecordTS:     ts,
		IsSidechain:  true,
		AgentID:      id,
		SubagentType: "worker",
		InputTokens:  1,
	}
}

// serviceOverRows writes rows as a single-lane cache and returns an analyzer over it.
func serviceOverRows(t *testing.T, rows []transcripts.Row) *Service {
	t.Helper()
	root := t.TempDir()
	writeSessionFixture(t, root, "claude", "L1", rows)
	svc, err := NewService(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// subagentRowByID indexes a subagent family by agent id.
func subagentRowByID(rows []SubagentWallTime) map[string]SubagentWallTime {
	out := make(map[string]SubagentWallTime, len(rows))
	for _, r := range rows {
		out[r.AgentID] = r
	}
	return out
}

// TestSubagentWallTime_SplitsSpanFromActive pins the whole point of the split: span and
// active are DIFFERENT numbers on an interrupted lane and the SAME number on a continuous
// one. Both arms are needed. The idle arm alone would pass an implementation that returns
// the span, since it never checks a case where they agree; the continuous arm alone would
// pass one that returns 0 or drops every gap.
//
// The existing golden corpus cannot discriminate this: its agents' gaps are 60s and 120s,
// all under the threshold, so active == span there for every agent. The idle lane below is
// the only place in the suite where the two diverge.
func TestSubagentWallTime_SplitsSpanFromActive(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")

	rows := []transcripts.Row{
		// idle-agent: gaps of 60s, 45min, 60s. Only the two 60s gaps are active.
		subagentRow("S", "idle-agent", base),
		subagentRow("S", "idle-agent", base.Add(60*time.Second)),
		subagentRow("S", "idle-agent", base.Add(60*time.Second+45*time.Minute)),
		subagentRow("S", "idle-agent", base.Add(120*time.Second+45*time.Minute)),
		// busy-agent: three gaps of 60s, every one below the threshold.
		subagentRow("S", "busy-agent", base),
		subagentRow("S", "busy-agent", base.Add(60*time.Second)),
		subagentRow("S", "busy-agent", base.Add(120*time.Second)),
		subagentRow("S", "busy-agent", base.Add(180*time.Second)),
		// lone-agent: a single record, so there is no gap at all.
		subagentRow("S", "lone-agent", base),
	}

	rep, err := serviceOverRows(t, rows).RunDetectors(context.Background(), Filters{})
	require.NoError(t, err)
	byID := subagentRowByID(rep.SubagentWallTime)
	require.Len(t, byID, 3)

	idle := byID["idle-agent"]
	assert.Equal(t, int64(47*60*1000), idle.SpanMs, "span is the full first-to-last window, idle included")
	assert.Equal(t, int64(120_000), idle.ActiveMs, "active counts only the two sub-threshold gaps")
	assert.Less(t, idle.ActiveMs, idle.SpanMs, "the 45-minute pause is excluded from active")

	busy := byID["busy-agent"]
	assert.Equal(t, int64(180_000), busy.SpanMs)
	assert.Equal(t, busy.SpanMs, busy.ActiveMs, "no gap reaches the threshold, so active == span exactly")

	lone := byID["lone-agent"]
	assert.Equal(t, int64(0), lone.SpanMs, "one record spans nothing")
	assert.Equal(t, int64(0), lone.ActiveMs, "and has no gap to be active across")

	// The family ranks by active, so the busy lane outranks the idle one despite the idle
	// lane's span being over 15x larger. This is the reordering the ticket asked for.
	require.NotEmpty(t, rep.SubagentWallTime)
	assert.Equal(t, "busy-agent", rep.SubagentWallTime[0].AgentID,
		"ranking is by active time, not by span")
}

// TestSubagentWallTime_BoundedWithDisclosedTotal proves the cap keeps the RIGHT rows and
// discloses what it dropped. Leg (c) is what makes this a test of the property rather than
// of the number: a cap that kept an arbitrary 100 agents would satisfy the length and the
// total and still fail here.
func TestSubagentWallTime_BoundedWithDisclosedTotal(t *testing.T) {
	t.Run("over the limit", func(t *testing.T) {
		base := mustTS(t, "2026-06-01T10:00:00Z")
		const overBy = 5
		total := subagentWallTimeLimit + overBy

		// Agent i gets a single gap of (i+1) seconds, so ActiveMs is strictly increasing in
		// i and the five lowest-active agents are exactly agent-000..agent-004.
		rows := make([]transcripts.Row, 0, total*2)
		for i := range total {
			id := fmt.Sprintf("agent-%03d", i)
			rows = append(rows,
				subagentRow("S", id, base),
				subagentRow("S", id, base.Add(time.Duration(i+1)*time.Second)))
		}

		rep, err := serviceOverRows(t, rows).RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)

		assert.Len(t, rep.SubagentWallTime, subagentWallTimeLimit, "the family is capped")
		assert.Equal(t, int64(total), rep.Truncation.SubagentWallTimeTotal, "the pre-cap total is disclosed")
		assert.Equal(t, int64(subagentWallTimeLimit), rep.Truncation.SubagentWallTimeReturned)
		assert.True(t, rep.Truncation.Truncated)

		byID := subagentRowByID(rep.SubagentWallTime)
		for i := range overBy {
			dropped := fmt.Sprintf("agent-%03d", i)
			assert.NotContains(t, byID, dropped,
				"the cap drops the LOWEST-active agents; %s has the %d-lowest active time", dropped, i+1)
		}
		// And the known-positive for the same measurement: the highest-active agent is kept.
		assert.Contains(t, byID, fmt.Sprintf("agent-%03d", total-1))
	})

	t.Run("under the limit", func(t *testing.T) {
		rep, err := buildGoldenCorpus(t).RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)

		assert.False(t, rep.Truncation.Truncated, "the flag cannot be hardwired true")
		assert.Equal(t, rep.Truncation.SubagentWallTimeTotal, rep.Truncation.SubagentWallTimeReturned)
		assert.Equal(t, int64(len(rep.SubagentWallTime)), rep.Truncation.SubagentWallTimeReturned)
	})
}
