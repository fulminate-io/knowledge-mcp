// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// laneToolByName indexes a lane's per-tool rows by tool name.
func laneToolByName(rows []LaneToolRow) map[string]LaneToolRow {
	out := make(map[string]LaneToolRow, len(rows))
	for _, r := range rows {
		out[r.ToolName] = r
	}
	return out
}

// TestLaneDetail_SplitsModelAndToolTime pins the drill-down over a lane built so that every
// quantity it reports would move under a plausible wrong implementation.
//
// The durations differ BY ROW KIND on purpose: model rows are 700ms and tool rows are 100ms
// and 5000ms, so a swapped split reports the other number rather than the same one. The lane
// also carries an above-threshold idle gap, so a turns-per-minute using span rather than
// active is a visibly different figure.
func TestLaneDetail_SplitsModelAndToolTime(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")
	agent := "agent-solo"

	rows := []transcripts.Row{
		// Three model/turn rows (no tool name): 700ms each => ModelMs 2100, Turns 3.
		{Model: "m", SessionID: "SA", RecordTS: base, IsSidechain: true, AgentID: agent, SubagentType: "researcher", DurationMs: 700, InputTokens: 10},
		{Model: "m", SessionID: "SA", RecordTS: base.Add(time.Minute), IsSidechain: true, AgentID: agent, SubagentType: "researcher", DurationMs: 700, InputTokens: 10},
		{Model: "m", SessionID: "SA", RecordTS: base.Add(2 * time.Minute), IsSidechain: true, AgentID: agent, SubagentType: "researcher", DurationMs: 700, InputTokens: 10},
		// Tool rows: Bash 5000, Read 100 and 100 => ToolMs 5200.
		{Model: "m", SessionID: "SA", RecordTS: base.Add(3 * time.Minute), IsSidechain: true, AgentID: agent, SubagentType: "researcher", ToolName: "Bash", DurationMs: 5000, ToolInputPreview: "make test"},
		{Model: "m", SessionID: "SA", RecordTS: base.Add(4 * time.Minute), IsSidechain: true, AgentID: agent, SubagentType: "researcher", ToolName: "Read", DurationMs: 100, ToolInputPreview: "read a"},
		{Model: "m", SessionID: "SA", RecordTS: base.Add(5 * time.Minute), IsSidechain: true, AgentID: agent, SubagentType: "researcher", ToolName: "Read", DurationMs: 100, ToolInputPreview: "read b"},
		// An INTERRUPTED tool row and an over-idleGuardCeilingMs one. Both are counted as
		// calls and both contribute ZERO time, matching trustworthy().
		{Model: "m", SessionID: "SA", RecordTS: base.Add(6 * time.Minute), IsSidechain: true, AgentID: agent, SubagentType: "researcher", ToolName: "Read", DurationMs: 99999, ToolInputPreview: "interrupted", Interrupted: true},
		{Model: "m", SessionID: "SA", RecordTS: base.Add(7 * time.Minute), IsSidechain: true, AgentID: agent, SubagentType: "researcher", ToolName: "Bash", DurationMs: idleGuardCeilingMs + 1, ToolInputPreview: "over the guard"},
		// The idle gap: 45 minutes with no event, so span and active diverge.
		{Model: "m", SessionID: "SA", RecordTS: base.Add(52 * time.Minute), IsSidechain: true, AgentID: agent, SubagentType: "researcher", DurationMs: 0, InputTokens: 1},
	}

	svc := serviceOverRows(t, rows)

	t.Run("subagent lane", func(t *testing.T) {
		rep, err := svc.RunDetectors(context.Background(), Filters{Scope: ScopeSingle, AgentID: agent})
		require.NoError(t, err)
		require.NotNil(t, rep.LaneDetail)
		d := rep.LaneDetail

		assert.Equal(t, agent, d.AgentID)
		assert.Equal(t, "researcher", d.SubagentType)

		assert.Equal(t, int64(2100), d.ModelMs, "the three 700ms token rows, not the tool rows")
		assert.Equal(t, int64(5200), d.ToolMs, "Bash 5000 plus Read 100+100; the interrupted and over-guard rows contribute nothing")
		assert.Equal(t, int64(4), d.Turns, "four rows carry no tool name")

		byTool := laneToolByName(d.PerTool)
		assert.Equal(t, int64(5000), byTool["Bash"].TotalDurationMs)
		assert.Equal(t, int64(2), byTool["Bash"].CallCount, "the over-guard call is COUNTED even though its time is dropped")
		assert.Equal(t, int64(200), byTool["Read"].TotalDurationMs)
		assert.Equal(t, int64(3), byTool["Read"].CallCount, "the interrupted call is counted too")

		// Span covers the 52-minute window; active excludes the 45-minute gap.
		assert.Equal(t, int64(52*60*1000), d.SpanMs)
		assert.Equal(t, int64(7*60*1000), d.ActiveMs, "seven one-minute gaps; the 45-minute one is idle")
		assert.Less(t, d.ActiveMs, d.SpanMs)

		// Turns per minute over ACTIVE time: 4 turns / 7 minutes. Over SPAN it would be
		// 4/52 = 0.077, which is the number a span denominator would report.
		assert.InDelta(t, 4.0/7.0, d.TurnsPerMin, 0.0001,
			"a span denominator would give %.4f", 4.0/52.0)

		require.NotEmpty(t, d.TopWaits)
		assert.Equal(t, "Bash", d.TopWaits[0].ToolName, "the longest trustworthy wait leads")
		assert.Equal(t, int64(5000), d.TopWaits[0].DurationMs)
		assert.Equal(t, "make test", d.TopWaits[0].Preview)
	})

	t.Run("main-session lane has a real span and active time", func(t *testing.T) {
		// A main lane carries no agent_id, so it has no subagent accumulator to read span
		// and active from. This is the arm that catches it reporting zero.
		mainBase := mustTS(t, "2026-06-01T09:00:00Z")
		mainSvc := serviceOverRows(t, []transcripts.Row{
			mainRow("SM", mainBase),
			mainRow("SM", mainBase.Add(time.Minute)),
			mainRow("SM", mainBase.Add(2*time.Minute)),
		})

		rep, err := mainSvc.RunDetectors(context.Background(), Filters{Scope: ScopeSingle, SessionID: "SM"})
		require.NoError(t, err)
		require.NotNil(t, rep.LaneDetail)

		assert.Empty(t, rep.LaneDetail.AgentID, "a main lane has no agent id")
		assert.Equal(t, int64(2*60*1000), rep.LaneDetail.SpanMs, "and still reports a real span")
		assert.Equal(t, int64(2*60*1000), rep.LaneDetail.ActiveMs, "and a real active time")
	})

	t.Run("a wider scope omits the key entirely", func(t *testing.T) {
		rep, err := svc.RunDetectors(context.Background(), Filters{})
		require.NoError(t, err)
		assert.Nil(t, rep.LaneDetail)

		body, err := json.Marshal(rep)
		require.NoError(t, err)
		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.NotContains(t, payload, "lane_detail",
			"omitempty on a pointer omits the key; a zero-valued object would read as a measured lane of zeros")

		// The known-positive on the same instrument: the key IS present under single scope.
		single, err := svc.RunDetectors(context.Background(), Filters{Scope: ScopeSingle, AgentID: agent})
		require.NoError(t, err)
		singleBody, err := json.Marshal(single)
		require.NoError(t, err)
		var singlePayload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(singleBody, &singlePayload))
		assert.Contains(t, singlePayload, "lane_detail")
	})

	t.Run("top waits are bounded and ordered", func(t *testing.T) {
		waitBase := mustTS(t, "2026-06-01T08:00:00Z")
		over := laneDetailTopWaits + 7
		waitRows := make([]transcripts.Row, 0, over)
		for i := range over {
			waitRows = append(waitRows, transcripts.Row{
				Model: "m", SessionID: "SW", RecordTS: waitBase.Add(time.Duration(i) * time.Second),
				ToolName: "Bash", DurationMs: int64(i+1) * 1000,
				ToolInputPreview: fmt.Sprintf("call-%02d", i),
			})
		}

		rep, err := serviceOverRows(t, waitRows).RunDetectors(context.Background(), Filters{Scope: ScopeSingle, SessionID: "SW"})
		require.NoError(t, err)
		require.NotNil(t, rep.LaneDetail)
		waits := rep.LaneDetail.TopWaits

		require.Len(t, waits, laneDetailTopWaits, "the list is capped")
		for i := 1; i < len(waits); i++ {
			assert.GreaterOrEqual(t, waits[i-1].DurationMs, waits[i].DurationMs, "ordered duration desc")
		}
		assert.Equal(t, int64(over)*1000, waits[0].DurationMs, "the single longest wait leads")
		assert.Equal(t, int64(over-laneDetailTopWaits+1)*1000, waits[len(waits)-1].DurationMs,
			"and the cap keeps the LONGEST ones, not an arbitrary window")
	})
}
