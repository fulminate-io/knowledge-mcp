// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
)

// pinnedColumns is the AUTHORITATIVE transcript-parquet column contract. It is the
// SOLE schema source: this client-pinned list — not any struct on the receiving
// side — defines the column set, because nothing over there parses the uploaded
// parquet's contents. What IS live across the boundary is a name correspondence
// for the breakdown dimensions, so ADDING a column is additive while RENAMING one
// is a coordinated change. The two sides cannot share a Go package (AGENTS.md
// NO-shared-packages invariant). record_ts is a STRING (ByteArray) column — NOT a
// native TIMESTAMP.
var pinnedColumns = []struct {
	parquetTag string
	kind       string // "int64" | "bool" | "" (string/ByteArray)
}{
	{"source", ""},
	{"session_id", ""},
	{"project", ""},
	{"git_branch", ""},
	{"record_ts", ""},
	{"record_type", ""},
	{"model", ""},
	{"input_tokens", "int64"},
	{"output_tokens", "int64"},
	{"cache_read_tokens", "int64"},
	{"cache_creation_tokens", "int64"},
	{"tool_name", ""},
	{"is_error", "bool"},
	{"cli_version", ""},
	{"uuid", ""},
	{"parent_uuid", ""},
	// Enrichment columns (Row order).
	{"duration_ms", "int64"},
	{"tool_use_id", ""},
	{"is_sidechain", "bool"},
	{"agent_id", ""},
	{"subagent_type", ""},
	{"tool_input_hash", ""},
	{"tool_input_preview", ""},
	{"cache_creation_1h_tokens", "int64"},
	{"cache_creation_5m_tokens", "int64"},
	{"service_tier", ""},
	{"web_search_count", "int64"},
	{"web_fetch_count", "int64"},
	{"stop_reason", ""},
	{"is_api_error", "bool"},
	{"is_meta", "bool"},
	{"interrupted", "bool"},
	{"mcp_server", ""},
	{"mcp_tool", ""},
	{"skill", ""},
	{"tool_result_bytes", "int64"},
	{"tool_result_images", "int64"},
	{"tool_result_spilled", "bool"},
	{"run_in_background", "bool"},
}

// TestRowToParquetRecordTSMatchesJSON proves rowToParquet carries record_ts as the
// EXACT string time.Time marshals to in JSON — so the client parquet column is
// byte-compatible with what the historical NDJSON wire path carried and what the
// agent's CAST(record_ts AS TIMESTAMP) parses.
func TestRowToParquetRecordTSMatchesJSON(t *testing.T) {
	cases := []time.Time{
		time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 29, 12, 0, 0, 123456789, time.UTC),
		time.Date(2026, 1, 2, 3, 4, 5, 600000000, time.FixedZone("x", 5*3600)),
	}
	for _, ts := range cases {
		r := Row{RecordTS: ts}
		// The record_ts value json.Marshal emits for the whole Row (unquoted).
		blob, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal Row: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(blob, &m); err != nil {
			t.Fatalf("unmarshal Row: %v", err)
		}
		wantJSON, _ := m["record_ts"].(string)
		if got := rowToParquet(r).RecordTS; got != wantJSON {
			t.Errorf("rowToParquet(%v).RecordTS = %q, want JSON record_ts %q", ts, got, wantJSON)
		}
	}
}

// TestWriteSessionParquetRoundTrip proves a multi-row fixture round-trips through
// WriteSessionParquet and a parquet-go reader to the identical parquetRows, and that
// record_ts is the RFC3339Nano string form.
func TestWriteSessionParquetRoundTrip(t *testing.T) {
	rows := []Row{
		{
			Source: SourceClaude, SessionID: "s1", Project: "/repo", GitBranch: "main",
			RecordTS: time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC), RecordType: "assistant",
			Model: "claude-opus-4-8", InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5,
			CacheCreationTokens: 2, ToolName: "Bash", IsError: true, CLIVersion: "1.2.3",
			UUID: "u1", ParentUUID: "p1",
		},
		{Source: SourceCodex, SessionID: "s1", RecordTS: time.Date(2026, 6, 29, 13, 0, 0, 0, time.UTC), InputTokens: 1, UUID: "u2"},
	}

	var buf bytes.Buffer
	if err := WriteSessionParquet(rows, &buf); err != nil {
		t.Fatalf("WriteSessionParquet: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("WriteSessionParquet produced empty output")
	}

	got, err := parquet.Read[parquetRow](bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("parquet.Read: %v", err)
	}
	want := []parquetRow{rowToParquet(rows[0]), rowToParquet(rows[1])}
	if len(got) != len(want) {
		t.Fatalf("read %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d round-trip mismatch:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
	if got[0].RecordTS != "2026-06-29T12:00:00Z" {
		t.Errorf("record_ts = %q, want RFC3339 string 2026-06-29T12:00:00Z", got[0].RecordTS)
	}
}

// TestReadSessionParquetRoundTrip proves WriteSessionParquet → ReadSessionParquet
// round-trips a Row slice back to equal Rows: record_ts is parsed from its on-disk
// RFC3339Nano string back to time.Time (compared via time.Time.Equal, which is
// offset-insensitive), Source is restored to the typed alias, and every other field is
// the field-inverse of rowToParquet. This exercises the path-based reader the analytics
// corpus loader consumes.
func TestReadSessionParquetRoundTrip(t *testing.T) {
	want := []Row{
		{
			Source: SourceClaude, SessionID: "s1", Project: "/repo", GitBranch: "main",
			RecordTS: time.Date(2026, 6, 29, 12, 0, 0, 123456789, time.UTC), RecordType: "assistant",
			Model: "claude-opus-4-8", InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5,
			CacheCreationTokens: 2, ToolName: "Bash", IsError: true, CLIVersion: "1.2.3",
			UUID: "u1", ParentUUID: "p1", DurationMs: 1500, ToolInputHash: "h1",
			ToolInputPreview: "ls", CacheCreation1hTokens: 40, CacheCreation5mTokens: 60,
			ServiceTier: "std", WebSearchCount: 1, StopReason: "end_turn", IsMeta: true,
			MCPServer: "srv", MCPTool: "mt", Skill: "sk",
		},
		{Source: SourceCodex, SessionID: "s1", RecordTS: time.Date(2026, 6, 29, 13, 0, 0, 0, time.UTC), InputTokens: 1, UUID: "u2"},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "S1.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if err := WriteSessionParquet(want, f); err != nil {
		_ = f.Close()
		t.Fatalf("WriteSessionParquet: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	got, err := ReadSessionParquet(path)
	if err != nil {
		t.Fatalf("ReadSessionParquet: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("read %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if !got[i].RecordTS.Equal(want[i].RecordTS) {
			t.Errorf("row %d RecordTS = %v, want %v", i, got[i].RecordTS, want[i].RecordTS)
		}
		// Compare everything except RecordTS (time.Time == is loc-pointer sensitive) by
		// zeroing the timestamp on copies.
		g, w := got[i], want[i]
		g.RecordTS, w.RecordTS = time.Time{}, time.Time{}
		if g != w {
			t.Errorf("row %d round-trip mismatch:\n got  %+v\n want %+v", i, g, w)
		}
	}
}

// TestGoldenSchemaColumns reads back WriteSessionParquet's output and asserts the
// parquet LEAF columns (names, order, physical kinds) EXACTLY equal the pinned
// tags — a name/type drift fails HERE instead of surfacing as a silent agent
// query-time null. *_tokens/duration_ms/*_count=INT64, the bool flags=BOOLEAN, the
// rest (incl. record_ts) = BYTE_ARRAY (string). This pins the client-authored
// schema that the agent's TestClientGoldenParquetQueryParity reads by name.
func TestGoldenSchemaColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSessionParquet([]Row{{Source: SourceClaude}}, &buf); err != nil {
		t.Fatalf("WriteSessionParquet: %v", err)
	}
	r := parquet.NewGenericReader[parquetRow](bytes.NewReader(buf.Bytes()))
	defer r.Close()

	fields := r.Schema().Fields()
	if len(fields) != len(pinnedColumns) {
		t.Fatalf("parquet schema has %d leaf columns, want %d", len(fields), len(pinnedColumns))
	}
	for i, want := range pinnedColumns {
		f := fields[i]
		if f.Name() != want.parquetTag {
			t.Errorf("column %d: name = %q, want %q", i, f.Name(), want.parquetTag)
		}
		var wantKind parquet.Kind
		switch want.kind {
		case "int64":
			wantKind = parquet.Int64
		case "bool":
			wantKind = parquet.Boolean
		default:
			wantKind = parquet.ByteArray
		}
		if got := f.Type().Kind(); got != wantKind {
			t.Errorf("column %d (%s): physical kind = %s, want %s", i, f.Name(), got, wantKind)
		}
	}
}
