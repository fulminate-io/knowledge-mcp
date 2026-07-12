// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"database/sql"
	"fmt"
)

// This file holds the FLOW detectors — where work is redundant (duplicate commands)
// and where orchestration fans out (subagent wall-time + agent-chain over-orchestration).
// Each is a single bounded DuckDB query reusing fromClause + f.where() so the base
// filters apply uniformly.

// DuplicateCommandRow is one redundantly-rerun command: the SAME tool + input
// fingerprint executed more than once WITHIN a single session.
//
// It carries wasted TIME only. Wasted-TOKENS per duplicate is a conscious
// out-of-scope deferral: a tool-call row carries ZERO tokens (the parser splits a
// turn into a zero-tool token row plus zero-token tool_use rows), so wasted-tokens at
// tool granularity is ill-defined and needs an unsolved tool→token attribution — a
// separate follow-up, not built here.
type DuplicateCommandRow struct {
	SessionID        string `json:"session_id"`
	ToolName         string `json:"tool_name"`
	ToolInputHash    string `json:"tool_input_hash"`
	RunCount         int64  `json:"run_count"`
	WastedDurationMs int64  `json:"wasted_duration_ms"`
	SamplePreview    string `json:"sample_preview"`
}

// SubagentWallTime is one subagent's wall-clock span + token cost. Wall-time is a
// QUERY-side aggregate (MAX−MIN of record_ts per agent_id), NOT a stored per-row
// scalar — a subagent's elapsed time is the span of its records.
type SubagentWallTime struct {
	AgentID      string `json:"agent_id"`
	SubagentType string `json:"subagent_type"`
	WallMs       int64  `json:"wall_ms"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// AgentChainRow is one MAIN session's over-orchestration proxy: how many distinct
// subagents it spawned, how diverse their types were, and their combined + longest
// wall-time. It is a PROXY, not a true recursive spawn-tree depth: the transcript has
// no agent-spawn-parent column (parent_uuid is message-level), so true recursive
// spawn-depth is a separate schema-enrichment follow-up, deliberately not built here.
type AgentChainRow struct {
	SessionID             string `json:"session_id"`
	SubagentCount         int64  `json:"subagent_count"`
	SubagentTypeDiversity int64  `json:"subagent_type_diversity"`
	TotalSubagentWallMs   int64  `json:"total_subagent_wall_ms"`
	MaxSubagentWallMs     int64  `json:"max_subagent_wall_ms"`
}

// duplicateCommandsFrom finds tool calls rerun with the SAME input WITHIN a session.
// The grouping is SESSION-SCOPED — (session_id, tool_name, tool_input_hash) — so the
// signal is "the same command rerun inside one session". WastedDurationMs is a
// DE-IDLED sum (idle-inflated runs contribute 0 via the CASE-WHEN idle guard); the
// run_count is untouched. Only tool rows participate (tool_input_hash <> ”). Bounded
// to the top-N by wasted time.
func (s *Service) duplicateCommandsFrom(ctx context.Context, paths []string, f Filters) ([]DuplicateCommandRow, error) {
	where, args := f.where()
	query := fmt.Sprintf(
		`SELECT
			%s, %s, %s,
			COUNT(*) AS run_count,
			CAST(COALESCE(SUM(CASE WHEN %s THEN %s ELSE 0 END), 0) AS BIGINT) AS wasted_duration_ms,
			MIN(%s) AS sample
		FROM %s %s AND %s <> ''
		GROUP BY %s, %s, %s
		HAVING COUNT(*) > 1
		ORDER BY wasted_duration_ms DESC, run_count DESC, %s ASC
		LIMIT %d`,
		colSessionID, colToolName, colToolInputHash,
		idleGuardExpr(), colDurationMs, colToolInputPreview,
		fromClause(paths), where, colToolInputHash,
		colSessionID, colToolName, colToolInputHash,
		colSessionID,
		duplicateCommandsLimit,
	)

	var out []DuplicateCommandRow
	err := s.queryRows(ctx, query, args, func(rows *sql.Rows) error {
		var r DuplicateCommandRow
		if err := rows.Scan(&r.SessionID, &r.ToolName, &r.ToolInputHash,
			&r.RunCount, &r.WastedDurationMs, &r.SamplePreview); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

// subagentWallTimeFrom returns per-subagent wall-clock span + token cost. Wall-time is
// MAX−MIN(record_ts) over the agent's rows (epoch_ms difference). Only sidechain rows
// carry an agent_id, so agent_id <> ” scopes the group to real subagents.
func (s *Service) subagentWallTimeFrom(ctx context.Context, paths []string, f Filters) ([]SubagentWallTime, error) {
	where, args := f.where()
	query := fmt.Sprintf(
		`SELECT
			%s,
			MIN(%s) AS subagent_type,
			CAST(epoch_ms(MAX(CAST(%s AS TIMESTAMP))) - epoch_ms(MIN(CAST(%s AS TIMESTAMP))) AS BIGINT) AS wall_ms,
			CAST(COALESCE(SUM(%s), 0) AS BIGINT),
			CAST(COALESCE(SUM(%s), 0) AS BIGINT)
		FROM %s %s AND %s <> ''
		GROUP BY %s
		ORDER BY wall_ms DESC, %s ASC`,
		colAgentID, colSubagentType,
		colRecordTS, colRecordTS,
		colInputTokens, colOutputTokens,
		fromClause(paths), where, colAgentID,
		colAgentID, colAgentID,
	)

	var out []SubagentWallTime
	err := s.queryRows(ctx, query, args, func(rows *sql.Rows) error {
		var r SubagentWallTime
		if err := rows.Scan(&r.AgentID, &r.SubagentType, &r.WallMs, &r.InputTokens, &r.OutputTokens); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

// agentChainFrom returns the per-MAIN-session over-orchestration proxy. It first
// derives each subagent's wall-span (inner GROUP BY session_id, agent_id over the
// sidechain rows), then aggregates per session: the subagent count, the count of
// DISTINCT subagent types, and the total + max subagent wall-time.
func (s *Service) agentChainFrom(ctx context.Context, paths []string, f Filters) ([]AgentChainRow, error) {
	where, args := f.where()
	query := fmt.Sprintf(
		`SELECT
			%s,
			COUNT(*) AS subagent_count,
			COUNT(DISTINCT subagent_type) AS type_diversity,
			CAST(COALESCE(SUM(wall_ms), 0) AS BIGINT) AS total_wall_ms,
			CAST(COALESCE(MAX(wall_ms), 0) AS BIGINT) AS max_wall_ms
		FROM (
			SELECT
				%s,
				%s AS agent_id,
				MIN(%s) AS subagent_type,
				epoch_ms(MAX(CAST(%s AS TIMESTAMP))) - epoch_ms(MIN(CAST(%s AS TIMESTAMP))) AS wall_ms
			FROM %s %s AND %s AND %s <> ''
			GROUP BY %s, %s
		) per_agent
		GROUP BY %s
		ORDER BY subagent_count DESC, total_wall_ms DESC, %s ASC`,
		colSessionID,
		colSessionID, colAgentID, colSubagentType,
		colRecordTS, colRecordTS,
		fromClause(paths), where, colIsSidechain, colAgentID,
		colSessionID, colAgentID,
		colSessionID, colSessionID,
	)

	var out []AgentChainRow
	err := s.queryRows(ctx, query, args, func(rows *sql.Rows) error {
		var r AgentChainRow
		if err := rows.Scan(&r.SessionID, &r.SubagentCount, &r.SubagentTypeDiversity,
			&r.TotalSubagentWallMs, &r.MaxSubagentWallMs); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}
