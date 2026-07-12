// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"fmt"
	"io"
	"time"

	"github.com/parquet-go/parquet-go"
)

// parquetRow is the on-disk column shape one transcript record is written as. It
// is the SOLE schema source for the transcript parquet: the agent has NO Go row
// DTO (its cmd/agent/internal/transcripts package was deleted), and its DuckDB
// query reads these physical columns BY NAME via read_parquet
// (usageanalytics/query.go). The two repos cannot share a Go package (the only
// sanctioned cross-module contract is generated protobuf — see AGENTS.md), so the
// client pins this column set and it IS the cross-repo contract; any column change
// here is a coordinated agent change proven by the agent's client-golden parity
// test. This mirror stays field-for-field, in the SAME order, with the normalized
// Row (row.go) — the enrichment tail included.
//
// It deliberately DIVERGES from Row in one place: record_ts is a `string`, NOT a
// time.Time. Writing Row.RecordTS (time.Time) directly would emit a parquet
// TIMESTAMP leaf, but the agent's query CASTs record_ts AS TIMESTAMP over a STRING
// column (the RFC3339 wire form the old NDJSON path carried). This writer is a dumb
// data-shape mirror, so it carries the timestamp as that same string and never
// emits a native TIMESTAMP column.
type parquetRow struct {
	Source              string `parquet:"source"`
	SessionID           string `parquet:"session_id"`
	Project             string `parquet:"project"`
	GitBranch           string `parquet:"git_branch"`
	RecordTS            string `parquet:"record_ts"`
	RecordType          string `parquet:"record_type"`
	Model               string `parquet:"model"`
	InputTokens         int64  `parquet:"input_tokens"`
	OutputTokens        int64  `parquet:"output_tokens"`
	CacheReadTokens     int64  `parquet:"cache_read_tokens"`
	CacheCreationTokens int64  `parquet:"cache_creation_tokens"`
	ToolName            string `parquet:"tool_name"`
	IsError             bool   `parquet:"is_error"`
	CLIVersion          string `parquet:"cli_version"`
	UUID                string `parquet:"uuid"`
	ParentUUID          string `parquet:"parent_uuid"`

	// Enrichment columns — same order as Row's enrichment block.
	DurationMs            int64  `parquet:"duration_ms"`
	ToolUseID             string `parquet:"tool_use_id"`
	IsSidechain           bool   `parquet:"is_sidechain"`
	AgentID               string `parquet:"agent_id"`
	SubagentType          string `parquet:"subagent_type"`
	ToolInputHash         string `parquet:"tool_input_hash"`
	ToolInputPreview      string `parquet:"tool_input_preview"`
	CacheCreation1hTokens int64  `parquet:"cache_creation_1h_tokens"`
	CacheCreation5mTokens int64  `parquet:"cache_creation_5m_tokens"`
	ServiceTier           string `parquet:"service_tier"`
	WebSearchCount        int64  `parquet:"web_search_count"`
	WebFetchCount         int64  `parquet:"web_fetch_count"`
	StopReason            string `parquet:"stop_reason"`
	IsAPIError            bool   `parquet:"is_api_error"`
	IsMeta                bool   `parquet:"is_meta"`
	Interrupted           bool   `parquet:"interrupted"`
	MCPServer             string `parquet:"mcp_server"`
	MCPTool               string `parquet:"mcp_tool"`
	Skill                 string `parquet:"skill"`
}

// rowToParquet maps a normalized Row to its on-disk parquetRow. record_ts is set to
// RecordTS.Format(time.RFC3339Nano) — the EXACT string time.Time marshals to in
// JSON, which is what the NDJSON wire path historically carried and what the agent's
// CAST(record_ts AS TIMESTAMP) parses. Source is a typed alias, flattened to its
// underlying string.
func rowToParquet(r Row) parquetRow {
	return parquetRow{
		Source:              string(r.Source),
		SessionID:           r.SessionID,
		Project:             r.Project,
		GitBranch:           r.GitBranch,
		RecordTS:            r.RecordTS.Format(time.RFC3339Nano),
		RecordType:          r.RecordType,
		Model:               r.Model,
		InputTokens:         r.InputTokens,
		OutputTokens:        r.OutputTokens,
		CacheReadTokens:     r.CacheReadTokens,
		CacheCreationTokens: r.CacheCreationTokens,
		ToolName:            r.ToolName,
		IsError:             r.IsError,
		CLIVersion:          r.CLIVersion,
		UUID:                r.UUID,
		ParentUUID:          r.ParentUUID,

		DurationMs:            r.DurationMs,
		ToolUseID:             r.ToolUseID,
		IsSidechain:           r.IsSidechain,
		AgentID:               r.AgentID,
		SubagentType:          r.SubagentType,
		ToolInputHash:         r.ToolInputHash,
		ToolInputPreview:      r.ToolInputPreview,
		CacheCreation1hTokens: r.CacheCreation1hTokens,
		CacheCreation5mTokens: r.CacheCreation5mTokens,
		ServiceTier:           r.ServiceTier,
		WebSearchCount:        r.WebSearchCount,
		WebFetchCount:         r.WebFetchCount,
		StopReason:            r.StopReason,
		IsAPIError:            r.IsAPIError,
		IsMeta:                r.IsMeta,
		Interrupted:           r.Interrupted,
		MCPServer:             r.MCPServer,
		MCPTool:               r.MCPTool,
		Skill:                 r.Skill,
	}
}

// parquetRowGroupSize is the number of rows accumulated before a row group is
// flushed to the underlying writer. It bounds the writer's resident row buffer so a
// huge session's parquet never holds every row in RAM at once — the client-side
// bounded-conversion invariant (memory is corpus-independent). It is a plain window
// count, not a byte budget; Zstd handles per-group compression.
const parquetRowGroupSize = 4096

// WriteSessionParquet serializes one session's normalized Rows to a Zstd-compressed
// parquet stream on w, flushing a row group every parquetRowGroupSize rows so peak
// memory stays bounded regardless of session size. The GenericWriter (and its Zstd
// config) is constructed ONCE per call. An empty row set yields a valid header-only
// parquet (no rows). w is typically a temp file; a row-group flush lands each window
// on w rather than buffering all rows in RAM.
func WriteSessionParquet(rows []Row, w io.Writer) error {
	pw := parquet.NewGenericWriter[parquetRow](w, parquet.Compression(&parquet.Zstd))
	batch := make([]parquetRow, 0, min(len(rows), parquetRowGroupSize))
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := pw.Write(batch); err != nil {
			return fmt.Errorf("transcripts: write parquet rows: %w", err)
		}
		if err := pw.Flush(); err != nil {
			return fmt.Errorf("transcripts: flush parquet row group: %w", err)
		}
		batch = batch[:0]
		return nil
	}
	for i := range rows {
		batch = append(batch, rowToParquet(rows[i]))
		if len(batch) >= parquetRowGroupSize {
			if err := flush(); err != nil {
				_ = pw.Close()
				return err
			}
		}
	}
	if err := flush(); err != nil {
		_ = pw.Close()
		return err
	}
	if err := pw.Close(); err != nil {
		return fmt.Errorf("transcripts: close parquet writer: %w", err)
	}
	return nil
}
