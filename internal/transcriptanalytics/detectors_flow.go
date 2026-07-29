// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import "sort"

// This file holds the FLOW detectors — where work is redundant (duplicate commands) and
// where orchestration fans out (subagent wall-time + agent-chain over-orchestration) — as
// pure-Go folds over the loaded *corpus.

// DuplicateCommandRow is one redundantly-rerun command: the SAME tool + input fingerprint
// executed more than once WITHIN a single session.
//
// It carries wasted TIME only. Wasted-TOKENS per duplicate is a conscious out-of-scope
// deferral: a tool-call row carries ZERO tokens (the parser splits a turn into a zero-tool
// token row plus zero-token tool_use rows), so wasted-tokens at tool granularity is
// ill-defined and needs an unsolved tool→token attribution — a separate follow-up.
type DuplicateCommandRow struct {
	SessionID        string `json:"session_id"`
	ToolName         string `json:"tool_name"`
	ToolInputHash    string `json:"tool_input_hash"`
	RunCount         int64  `json:"run_count"`
	WastedDurationMs int64  `json:"wasted_duration_ms"`
	SamplePreview    string `json:"sample_preview"`
}

// SubagentWallTime is one subagent's wall-clock span + token cost. Wall-time is the
// floor-epoch span of MAX−MIN record_ts over the agent's rows (QUERY-side aggregate, NOT a
// stored per-row scalar) — a subagent's elapsed time is the span of its records.
type SubagentWallTime struct {
	AgentID      string `json:"agent_id"`
	SubagentType string `json:"subagent_type"`
	WallMs       int64  `json:"wall_ms"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// AgentChainRow is one MAIN session's over-orchestration proxy: how many distinct subagents
// it spawned, how diverse their types were, and their combined + longest wall-time. It is a
// PROXY, not a true recursive spawn-tree depth: the transcript has no agent-spawn-parent
// column (parent_uuid is message-level), so true recursive spawn-depth is a separate
// schema-enrichment follow-up, deliberately not built here.
type AgentChainRow struct {
	SessionID             string `json:"session_id"`
	SubagentCount         int64  `json:"subagent_count"`
	SubagentTypeDiversity int64  `json:"subagent_type_diversity"`
	TotalSubagentWallMs   int64  `json:"total_subagent_wall_ms"`
	MaxSubagentWallMs     int64  `json:"max_subagent_wall_ms"`
}

// duplicateCommands emits the (session,tool,hash) groups rerun more than once — mirroring
// the agent's QueryDuplicateCommands (rollup_query.go:287): RunCount over all rows in the
// grain, WastedDurationMs summing trustworthy rows only (de-idled), MIN(preview), HAVING
// run_count>1, ordered by wasted desc → run_count desc → session asc, bounded to the top-N.
func (c *corpus) duplicateCommands() []DuplicateCommandRow {
	out := make([]DuplicateCommandRow, 0, len(c.dupes))
	for k, d := range c.dupes {
		if d.count <= 1 { // HAVING COUNT(*) > 1
			continue
		}
		out = append(out, DuplicateCommandRow{
			SessionID: k.session, ToolName: k.tool, ToolInputHash: k.hash,
			RunCount: d.count, WastedDurationMs: d.wastedSum, SamplePreview: d.preview,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].WastedDurationMs != out[j].WastedDurationMs {
			return out[i].WastedDurationMs > out[j].WastedDurationMs
		}
		if out[i].RunCount != out[j].RunCount {
			return out[i].RunCount > out[j].RunCount
		}
		return out[i].SessionID < out[j].SessionID
	})
	if len(out) > duplicateCommandsLimit {
		out = out[:duplicateCommandsLimit]
	}
	return out
}

// subagentWallTime returns per-subagent wall-clock span + token cost — mirroring the
// agent's QuerySubagentWallTime (rollup_query.go:257): MIN(subagent_type), the floor-epoch
// MAX−MIN record_ts span, SUM tokens; ordered by wall desc → agent asc. Only sidechain rows
// with an agent_id populate c.subagents.
func (c *corpus) subagentWallTime() []SubagentWallTime {
	out := make([]SubagentWallTime, 0, len(c.subagents))
	for id, sa := range c.subagents {
		out = append(out, SubagentWallTime{
			AgentID:      id,
			SubagentType: sa.subagentType,
			WallMs:       wallMs(sa.minTS, sa.maxTS),
			InputTokens:  sa.inSum,
			OutputTokens: sa.outSum,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].WallMs != out[j].WallMs {
			return out[i].WallMs > out[j].WallMs
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out
}

// agentChains returns the per-MAIN-session over-orchestration proxy — mirroring the agent's
// agentChainPG (rollup_insights.go:149): the inner per-(session,agent) wall span, then per
// session the subagent count, the count of DISTINCT (per-agent MIN) subagent types, and the
// total + max subagent wall-time; ordered by count desc → total-wall desc → session asc.
func (c *corpus) agentChains() []AgentChainRow {
	out := make([]AgentChainRow, 0, len(c.chains))
	for sess, agents := range c.chains {
		types := make(map[string]struct{}, len(agents))
		var total, maxWall int64
		for _, ca := range agents {
			w := wallMs(ca.minTS, ca.maxTS)
			total += w
			if w > maxWall {
				maxWall = w
			}
			types[ca.subagentType] = struct{}{}
		}
		out = append(out, AgentChainRow{
			SessionID:             sess,
			SubagentCount:         int64(len(agents)),
			SubagentTypeDiversity: int64(len(types)),
			TotalSubagentWallMs:   total,
			MaxSubagentWallMs:     maxWall,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubagentCount != out[j].SubagentCount {
			return out[i].SubagentCount > out[j].SubagentCount
		}
		if out[i].TotalSubagentWallMs != out[j].TotalSubagentWallMs {
			return out[i].TotalSubagentWallMs > out[j].TotalSubagentWallMs
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}
