// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"encoding/json"
	"sort"
	"testing"
	"time"
)

// TestRowJSONColumns pins the wire contract: a fully-populated Row must marshal
// to EXACTLY the agreed columns (no more, no fewer) and the transient
// SourceOffset must never appear. This guards the known drift points
// (`is_error` not `error`, `uuid` not `message_uuid`, no `role`, no
// `reasoning_output_tokens`, no cost field). The tail block is the flow-analytics
// enrichment set added on top of the original 16 columns.
func TestRowJSONColumns(t *testing.T) {
	row := Row{
		Source:              SourceClaude,
		SessionID:           "sess-1",
		Project:             "/Users/me/code/knowledge",
		GitBranch:           "main",
		RecordTS:            time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC),
		RecordType:          "assistant",
		Model:               "claude-fable-5",
		InputTokens:         11,
		OutputTokens:        22,
		CacheReadTokens:     33,
		CacheCreationTokens: 44,
		ToolName:            "Bash",
		IsError:             true,
		CLIVersion:          "2.1.174",
		UUID:                "uuid-1",
		ParentUUID:          "uuid-0",
		// Enrichment columns — all populated so every key must appear.
		DurationMs:            123,
		ToolUseID:             "toolu-1",
		IsSidechain:           true,
		AgentID:               "agent-1",
		SubagentType:          "researcher",
		ToolInputHash:         "deadbeef",
		ToolInputPreview:      "ls -la",
		CacheCreation1hTokens: 55,
		CacheCreation5mTokens: 66,
		ServiceTier:           "standard",
		WebSearchCount:        2,
		WebFetchCount:         3,
		StopReason:            "end_turn",
		IsAPIError:            true,
		IsMeta:                true,
		Interrupted:           true,
		MCPServer:             "knowledge",
		MCPTool:               "search",
		Skill:                 "research",
		// Non-zero transient field: must NOT emit a key.
		SourceOffset: 9999,
	}

	b, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal Row: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal Row JSON: %v", err)
	}

	want := []string{
		"source", "session_id", "project", "git_branch", "record_ts",
		"record_type", "model", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_creation_tokens", "tool_name",
		"is_error", "cli_version", "uuid", "parent_uuid",
		"duration_ms", "tool_use_id", "is_sidechain", "agent_id",
		"subagent_type", "tool_input_hash", "tool_input_preview",
		"cache_creation_1h_tokens", "cache_creation_5m_tokens", "service_tier",
		"web_search_count", "web_fetch_count", "stop_reason", "is_api_error",
		"is_meta", "interrupted", "mcp_server", "mcp_tool", "skill",
		"tool_result_bytes", "tool_result_images", "tool_result_spilled",
		"run_in_background",
	}
	wantSet := make(map[string]bool, len(want))
	for _, k := range want {
		wantSet[k] = true
	}

	if len(m) != len(want) {
		got := make([]string, 0, len(m))
		for k := range m {
			got = append(got, k)
		}
		sort.Strings(got)
		t.Fatalf("Row marshaled to %d keys, want %d.\n got: %v\nwant: %v", len(m), len(want), got, want)
	}
	for k := range m {
		if !wantSet[k] {
			t.Errorf("unexpected JSON key %q (drift — not in the column contract)", k)
		}
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("missing expected JSON key %q", k)
		}
	}

	// Explicit drift guards: none of these must ever appear.
	for _, forbidden := range []string{"error", "message_uuid", "role", "reasoning_output_tokens", "cost", "price", "SourceOffset"} {
		if _, ok := m[forbidden]; ok {
			t.Errorf("forbidden key %q present in Row JSON", forbidden)
		}
	}
}
