// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import "sort"

// This file holds the LANE-DETAIL family — the single-lane drill-down that answers "where
// did THIS lane spend its time" — as a pure-Go fold over the loaded *corpus. It is rendered
// only when the corpus was narrowed to one lane; over any wider population the question has
// no single answer, so the family is omitted rather than aggregated into meaninglessness.

// LaneToolRow is one tool's share of a single lane's time. It deliberately mirrors
// ToolTimeTotalRow's field set and json tags so the corpus-wide and per-lane views read
// side by side.
type LaneToolRow struct {
	ToolName        string `json:"tool_name"`
	TotalDurationMs int64  `json:"total_duration_ms"`
	CallCount       int64  `json:"call_count"`
}

// LaneWaitRow is one long single tool call — the lane's individual longest waits, as
// opposed to the per-tool totals, because one 50-minute call and five hundred fast ones sum
// alike but mean entirely different things.
type LaneWaitRow struct {
	ToolName   string `json:"tool_name"`
	DurationMs int64  `json:"duration_ms"`
	// Background is exact here, unlike the duplicate family's count: a wait row is one call.
	Background bool   `json:"background"`
	Preview    string `json:"preview"`
}

// LaneDetail is one lane's time breakdown.
//
// ModelMs and ToolMs are the two halves of where the lane's time went, and they come from
// DIFFERENT row kinds rather than from a re-derivation: a tool row's duration is already
// tool_result minus tool_use, and a token row's is already the assistant record minus the
// preceding user-role one. Both are summed under the same trustworthy() guard every other
// time family in this engine uses, so an interrupted or over-2h row contributes zero.
type LaneDetail struct {
	AgentID      string `json:"agent_id"`
	SubagentType string `json:"subagent_type"`
	SpanMs       int64  `json:"span_ms"`
	ActiveMs     int64  `json:"active_ms"`
	ModelMs      int64  `json:"model_ms"`
	ToolMs       int64  `json:"tool_ms"`
	Turns        int64  `json:"turns"`
	// TurnsPerMin divides by ACTIVE time, not span. A lane that sat idle for two days
	// between two turns has a real working rate; dividing by its span would report a rate
	// near zero and say nothing about how the lane actually ran. It is 0 when ActiveMs is 0,
	// which is a lane with fewer than two events rather than an infinitely fast one.
	TurnsPerMin float64       `json:"turns_per_min"`
	PerTool     []LaneToolRow `json:"per_tool"`
	TopWaits    []LaneWaitRow `json:"top_waits"`
}

// waitRanksAbove reports whether a should sort before b in the longest-wait list: duration
// desc, then tool asc, then preview asc. The two tie-breaks are what make the list
// deterministic — without them, two equal-duration waits would order by whichever file the
// parallel loader happened to finish first.
func waitRanksAbove(a, b LaneWaitRow) bool {
	if a.DurationMs != b.DurationMs {
		return a.DurationMs > b.DurationMs
	}
	if a.ToolName != b.ToolName {
		return a.ToolName < b.ToolName
	}
	return a.Preview < b.Preview
}

// insertWait places row into a descending-ordered list bounded at laneDetailTopWaits.
// A row that cannot beat the list's current minimum is dropped without an allocation, so
// the accumulator stays O(laneDetailTopWaits) on a lane with tens of thousands of calls
// rather than growing with the row count.
func insertWait(list []LaneWaitRow, row LaneWaitRow) []LaneWaitRow {
	if len(list) >= laneDetailTopWaits && !waitRanksAbove(row, list[len(list)-1]) {
		return list
	}
	i := sort.Search(len(list), func(i int) bool { return waitRanksAbove(row, list[i]) })
	list = append(list, LaneWaitRow{})
	copy(list[i+1:], list[i:])
	list[i] = row
	if len(list) > laneDetailTopWaits {
		list = list[:laneDetailTopWaits]
	}
	return list
}

// mergeWaits combines two already-ordered bounded lists into one, keeping the order and the
// bound. It is a k-way merge over at most 2*laneDetailTopWaits entries, so the merge cost
// does not grow with the corpus.
func mergeWaits(dst, src []LaneWaitRow) []LaneWaitRow {
	for _, row := range src {
		dst = insertWait(dst, row)
	}
	return dst
}

// laneDetail materializes the single-lane breakdown. The caller decides WHEN it applies —
// this fold assumes the corpus has already been narrowed to one lane at intake, which is
// what makes it correct to read corpus-wide accumulators as this lane's.
//
// PerTool and ToolMs are read from the same per-tool accumulator toolTimeTotals reports
// from, rather than from a second per-tool map folded alongside it: over a one-lane corpus
// they are the same quantity, and two accumulations of one quantity drift.
//
// The identity fields come from the subagent accumulator when the lane IS a subagent. A
// MAIN-session lane carries no agent_id and therefore has no such accumulator, which is why
// span and active are read from the corpus-level window and active-time fold for BOTH
// cases — a main lane silently reporting a zero span is the omission this shape prevents.
func (c *corpus) laneDetail(agentID string) *LaneDetail {
	d := &LaneDetail{
		SpanMs:   wallMs(c.minTS, c.maxTS),
		ActiveMs: c.laneActiveMs,
		ModelMs:  c.laneModelMs,
		Turns:    c.laneTurns,
		PerTool:  make([]LaneToolRow, 0, len(c.toolTime)),
		TopWaits: c.laneWaits,
	}
	if sa := c.subagents[agentID]; agentID != "" && sa != nil {
		d.AgentID, d.SubagentType = agentID, sa.subagentType
	}
	for tool, acc := range c.toolTime {
		d.ToolMs += acc.trustSum
		d.PerTool = append(d.PerTool, LaneToolRow{ToolName: tool, TotalDurationMs: acc.trustSum, CallCount: acc.count})
	}
	sort.Slice(d.PerTool, func(i, j int) bool {
		if d.PerTool[i].TotalDurationMs != d.PerTool[j].TotalDurationMs {
			return d.PerTool[i].TotalDurationMs > d.PerTool[j].TotalDurationMs
		}
		return d.PerTool[i].ToolName < d.PerTool[j].ToolName
	})
	if d.TopWaits == nil {
		d.TopWaits = []LaneWaitRow{}
	}
	if d.ActiveMs > 0 {
		d.TurnsPerMin = float64(d.Turns) / (float64(d.ActiveMs) / 60000.0)
	}
	return d
}
