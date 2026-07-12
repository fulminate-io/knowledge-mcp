// SPDX-License-Identifier: Apache-2.0

package transcripts

import "time"

// Source names the CLI that produced a transcript record.
type Source string

const (
	// SourceClaude is a ~/.claude/projects/**/*.jsonl record.
	SourceClaude Source = "claude"
	// SourceCodex is a ~/.codex/sessions/**/rollout-*.jsonl record.
	SourceCodex Source = "codex"
)

// Row is the single normalized column contract both parsers emit — one row per
// extracted transcript record. The json/parquet tags are the wire columns the
// client parquet writer produces and the agent's DuckDB reader consumes by name;
// they MUST match verbatim (a cross-ticket review caught name drift). Token
// columns carry RAW counts and the model id only; cost is computed agent-side,
// never here.
//
// There is deliberately NO `role` column (record_type subsumes it) and NO
// `reasoning_output_tokens` column (Codex reasoning is folded into output_tokens
// by the parser, since it bills at the output rate).
//
// The block after parent_uuid is the flow-analytics ENRICHMENT set: derived
// per-operation timing (duration_ms), subagent identity/type, tool-input
// fingerprint, cache-pricing splits, server-tool and error/interrupt/meta
// signals, and MCP/skill attribution. Its column ORDER is a client-internal
// contract (the agent reads by name), but it MUST stay in lockstep with the
// on-disk parquetRow mirror.
type Row struct {
	Source              Source    `json:"source" parquet:"source"`
	SessionID           string    `json:"session_id" parquet:"session_id"`
	Project             string    `json:"project" parquet:"project"` // the in-record cwd
	GitBranch           string    `json:"git_branch" parquet:"git_branch"`
	RecordTS            time.Time `json:"record_ts" parquet:"record_ts"`
	RecordType          string    `json:"record_type" parquet:"record_type"`
	Model               string    `json:"model" parquet:"model"`
	InputTokens         int64     `json:"input_tokens" parquet:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens" parquet:"output_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens" parquet:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens" parquet:"cache_creation_tokens"`
	ToolName            string    `json:"tool_name" parquet:"tool_name"`
	IsError             bool      `json:"is_error" parquet:"is_error"`
	CLIVersion          string    `json:"cli_version" parquet:"cli_version"`
	UUID                string    `json:"uuid" parquet:"uuid"`
	ParentUUID          string    `json:"parent_uuid" parquet:"parent_uuid"`

	// Enrichment columns (flow analytics). Kept in this fixed order to match the
	// parquetRow mirror; all are zero/empty on records where they do not apply.
	DurationMs            int64  `json:"duration_ms" parquet:"duration_ms"`                           // derived per-op ms: tool = result.ts−use.ts; turn = assistant.ts−prev user-role ts (model-latency proxy)
	ToolUseID             string `json:"tool_use_id" parquet:"tool_use_id"`                           // the tool_use block id (Claude) — pairs a tool Row to its result
	IsSidechain           bool   `json:"is_sidechain" parquet:"is_sidechain"`                         // record belongs to a subagent side conversation
	AgentID               string `json:"agent_id" parquet:"agent_id"`                                 // per-subagent identity; query-side wall-time groups on this
	SubagentType          string `json:"subagent_type" parquet:"subagent_type"`                       // the subagent's own attributionAgent (== parent Task subagent_type)
	ToolInputHash         string `json:"tool_input_hash" parquet:"tool_input_hash"`                   // fnv64a of canonical tool input — duplicate-command detection
	ToolInputPreview      string `json:"tool_input_preview" parquet:"tool_input_preview"`             // short single-line rendering of the tool input
	CacheCreation1hTokens int64  `json:"cache_creation_1h_tokens" parquet:"cache_creation_1h_tokens"` // usage.cache_creation.ephemeral_1h_input_tokens
	CacheCreation5mTokens int64  `json:"cache_creation_5m_tokens" parquet:"cache_creation_5m_tokens"` // usage.cache_creation.ephemeral_5m_input_tokens
	ServiceTier           string `json:"service_tier" parquet:"service_tier"`                         // usage.service_tier
	WebSearchCount        int64  `json:"web_search_count" parquet:"web_search_count"`                 // usage.server_tool_use.web_search_requests
	WebFetchCount         int64  `json:"web_fetch_count" parquet:"web_fetch_count"`                   // usage.server_tool_use.web_fetch_requests
	StopReason            string `json:"stop_reason" parquet:"stop_reason"`                           // message.stop_reason
	IsAPIError            bool   `json:"is_api_error" parquet:"is_api_error"`                         // isApiErrorMessage
	IsMeta                bool   `json:"is_meta" parquet:"is_meta"`                                   // isMeta — injected non-conversational record
	Interrupted           bool   `json:"interrupted" parquet:"interrupted"`                           // interruptedMessageId != ""
	MCPServer             string `json:"mcp_server" parquet:"mcp_server"`                             // attributionMcpServer
	MCPTool               string `json:"mcp_tool" parquet:"mcp_tool"`                                 // attributionMcpTool
	Skill                 string `json:"skill" parquet:"skill"`                                       // attributionSkill

	// SourceOffset is the byte offset of this record's START in its source file.
	// It is a TRANSIENT, client-only field — it MUST NEVER serialize to JSON or
	// parquet. KN-2 offset-filters parsed Rows past the raw-file watermark
	// (Codex parsing is stateful, so the whole file is parsed then filtered).
	SourceOffset int64 `json:"-" parquet:"-"`
}
