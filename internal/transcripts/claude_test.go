// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"strings"
	"testing"
)

// claudeFixture is a MAIN-agent conversation (no attributionAgent, so every row
// must keep an empty subagent_type). rec1 is an assistant turn with usage + a
// tool_use (Bash, input {"command":"ls -la"}); rec2 is the user record whose
// tool_result reports is_error:true (and anchors the next turn's latency); rec3
// is a second assistant turn with usage + a tool_use whose input is IDENTICAL to
// rec1's (so the two share a tool_input_hash). rec4 is a <synthetic> record that
// must be skipped, and rec5 is a truncated trailing line that must be tolerated.
const claudeFixture = `{"type":"assistant","uuid":"u1","parentUuid":"u0","timestamp":"2026-06-12T04:01:08.617Z","cwd":"/Users/me/code/knowledge","gitBranch":"feature/x","version":"2.1.174","sessionId":"sess-1","message":{"role":"assistant","model":"claude-fable-5","stop_reason":"tool_use","usage":{"input_tokens":8982,"output_tokens":383,"cache_creation_input_tokens":62601,"cache_read_input_tokens":7,"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":600,"ephemeral_5m_input_tokens":62001},"server_tool_use":{"web_search_requests":2,"web_fetch_requests":1}},"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls -la"}}]}}
{"type":"user","uuid":"u2","parentUuid":"u1","timestamp":"2026-06-12T04:01:10.000Z","cwd":"/Users/me/code/knowledge","gitBranch":"feature/x","version":"2.1.174","sessionId":"sess-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true}]}}
{"type":"assistant","uuid":"u3","parentUuid":"u2","timestamp":"2026-06-12T04:01:12.000Z","cwd":"/Users/me/code/knowledge","gitBranch":"feature/x","version":"2.1.174","sessionId":"sess-1","message":{"role":"assistant","model":"claude-fable-5","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":9000},"content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"ls -la"}}]}}
{"type":"assistant","uuid":"u4","timestamp":"2026-06-12T04:01:13.000Z","cwd":"/Users/me/code/knowledge","gitBranch":"feature/x","version":"2.1.174","sessionId":"sess-1","message":{"role":"assistant","model":"<synthetic>","usage":{"input_tokens":1,"output_tokens":1},"content":[]}}
{"type":"assistant","uuid":"u5","message":{"model":"claude-`

func TestParseClaude(t *testing.T) {
	rows, err := ParseClaude(strings.NewReader(claudeFixture))
	if err != nil {
		t.Fatalf("ParseClaude returned error (truncated trailing line must be tolerated): %v", err)
	}

	// Expect exactly 4 rows: rec1 usage, rec1 tool, rec3 usage, rec3 tool.
	// (user record emits none; synthetic + truncated are skipped.)
	if len(rows) != 4 {
		t.Fatalf("want 4 rows, got %d: %+v", len(rows), rows)
	}

	var usage1, tool1, usage3, tool3 *Row
	for i := range rows {
		switch {
		case rows[i].UUID == "u1" && rows[i].ToolName == "Bash":
			tool1 = &rows[i]
		case rows[i].UUID == "u1":
			usage1 = &rows[i]
		case rows[i].UUID == "u3" && rows[i].ToolName == "Bash":
			tool3 = &rows[i]
		case rows[i].UUID == "u3":
			usage3 = &rows[i]
		}
	}
	if usage1 == nil || tool1 == nil || usage3 == nil || tool3 == nil {
		t.Fatalf("missing expected rows: usage1=%v tool1=%v usage3=%v tool3=%v", usage1, tool1, usage3, tool3)
	}

	// Token Row: 4 raw token counts + model + envelope enrichment.
	if usage1.InputTokens != 8982 || usage1.OutputTokens != 383 ||
		usage1.CacheCreationTokens != 62601 || usage1.CacheReadTokens != 7 {
		t.Errorf("token Row counts wrong: in=%d out=%d cc=%d cr=%d (want 8982/383/62601/7)",
			usage1.InputTokens, usage1.OutputTokens, usage1.CacheCreationTokens, usage1.CacheReadTokens)
	}
	if usage1.Model != "claude-fable-5" {
		t.Errorf("token Row model = %q, want claude-fable-5", usage1.Model)
	}
	if usage1.Source != SourceClaude {
		t.Errorf("token Row source = %q, want claude", usage1.Source)
	}
	if usage1.SessionID != "sess-1" || usage1.Project != "/Users/me/code/knowledge" || usage1.GitBranch != "feature/x" {
		t.Errorf("token Row envelope wrong: session=%q project=%q branch=%q", usage1.SessionID, usage1.Project, usage1.GitBranch)
	}
	if usage1.UUID != "u1" || usage1.ParentUUID != "u0" {
		t.Errorf("token Row uuid/parent wrong: uuid=%q parent=%q", usage1.UUID, usage1.ParentUUID)
	}
	if usage1.RecordType != "assistant" {
		t.Errorf("token Row record_type = %q, want assistant", usage1.RecordType)
	}
	if usage1.RecordTS.IsZero() {
		t.Error("token Row record_ts not parsed from timestamp")
	}

	// Enrichment usage splits + service_tier + stop_reason + web counts (rec1).
	if usage1.CacheCreation1hTokens != 600 || usage1.CacheCreation5mTokens != 62001 {
		t.Errorf("cache split wrong: 1h=%d 5m=%d (want 600/62001)", usage1.CacheCreation1hTokens, usage1.CacheCreation5mTokens)
	}
	if usage1.ServiceTier != "standard" {
		t.Errorf("service_tier = %q, want standard", usage1.ServiceTier)
	}
	if usage1.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", usage1.StopReason)
	}
	if usage1.WebSearchCount != 2 || usage1.WebFetchCount != 1 {
		t.Errorf("web counts wrong: search=%d fetch=%d (want 2/1)", usage1.WebSearchCount, usage1.WebFetchCount)
	}

	// duration_ms: rec1 token has no preceding user record → 0 (clamped); rec3
	// token measures assistant.ts(04:01:12.000) − preceding user rec2(04:01:10.000)
	// = 2000ms (a model-latency proxy). Tool rec1 = result(10.000)−use(08.617)=1383ms.
	if usage1.DurationMs != 0 {
		t.Errorf("rec1 token duration_ms = %d, want 0 (no preceding user record)", usage1.DurationMs)
	}
	if usage3.DurationMs != 2000 {
		t.Errorf("rec3 token duration_ms = %d, want 2000 (assistant − preceding user, model-latency proxy)", usage3.DurationMs)
	}
	if tool1.DurationMs != 1383 {
		t.Errorf("tool1 duration_ms = %d, want 1383 (tool_result − tool_use)", tool1.DurationMs)
	}

	// Tool Row: ToolName + ToolUseID; IsError flipped by the following tool_result.
	if tool1.ToolName != "Bash" {
		t.Errorf("tool Row ToolName = %q, want Bash", tool1.ToolName)
	}
	if tool1.ToolUseID != "toolu_1" {
		t.Errorf("tool Row tool_use_id = %q, want toolu_1", tool1.ToolUseID)
	}
	if !tool1.IsError {
		t.Error("tool Row IsError = false, want true (correlated from tool_result is_error)")
	}

	// Identical tool inputs share a non-empty hash; preview is the command string.
	if tool1.ToolInputHash == "" {
		t.Error("tool1 tool_input_hash is empty, want a fnv64a hex")
	}
	if tool1.ToolInputHash != tool3.ToolInputHash {
		t.Errorf("identical inputs must share tool_input_hash: %q vs %q", tool1.ToolInputHash, tool3.ToolInputHash)
	}
	if tool1.ToolInputPreview != "ls -la" {
		t.Errorf("tool_input_preview = %q, want %q", tool1.ToolInputPreview, "ls -la")
	}

	// Main conversation: no attributionAgent → every row keeps empty subagent_type,
	// is not a sidechain, and has no agent_id.
	for i := range rows {
		if rows[i].SubagentType != "" {
			t.Errorf("row %d subagent_type = %q, want empty (main conversation)", i, rows[i].SubagentType)
		}
		if rows[i].IsSidechain {
			t.Errorf("row %d is_sidechain = true, want false (main conversation)", i)
		}
		if rows[i].AgentID != "" {
			t.Errorf("row %d agent_id = %q, want empty (main conversation)", i, rows[i].AgentID)
		}
	}

	// SourceOffset non-negative and non-decreasing with record position;
	// CLIVersion populated from the top-level version on every Row.
	var prev int64 = -1
	for i := range rows {
		if rows[i].SourceOffset < 0 {
			t.Errorf("row %d SourceOffset negative: %d", i, rows[i].SourceOffset)
		}
		if rows[i].SourceOffset < prev {
			t.Errorf("row %d SourceOffset %d decreased from %d", i, rows[i].SourceOffset, prev)
		}
		prev = rows[i].SourceOffset
		if rows[i].CLIVersion != "2.1.174" {
			t.Errorf("row %d CLIVersion = %q, want 2.1.174", i, rows[i].CLIVersion)
		}
	}

	// The rec3 rows must sit at a strictly larger offset than the rec1 rows
	// (proves the running offset advances per record).
	if usage3.SourceOffset <= usage1.SourceOffset {
		t.Errorf("rec3 Row offset %d not past rec1 offset %d", usage3.SourceOffset, usage1.SourceOffset)
	}
}

// claudeSidechainFixture is a SUBAGENT conversation file: every record carries
// isSidechain:true and the same agentId, and attributionAgent ("researcher")
// appears on the FIRST record only — the parser must capture it once and stamp
// subagent_type onto every row of the file, including the second record that
// omits it.
const claudeSidechainFixture = `{"type":"assistant","uuid":"s1","parentUuid":"s0","timestamp":"2026-06-12T05:00:00.000Z","cwd":"/Users/me/code/agent","gitBranch":"main","version":"2.1.174","sessionId":"sess-2","isSidechain":true,"agentId":"agent-abc","attributionAgent":"researcher","message":{"role":"assistant","model":"claude-fable-5","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[]}}
{"type":"assistant","uuid":"s2","parentUuid":"s1","timestamp":"2026-06-12T05:00:02.000Z","cwd":"/Users/me/code/agent","gitBranch":"main","version":"2.1.174","sessionId":"sess-2","isSidechain":true,"agentId":"agent-abc","message":{"role":"assistant","model":"claude-fable-5","usage":{"input_tokens":20,"output_tokens":6,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[]}}`

func TestParseClaudeSidechain(t *testing.T) {
	rows, err := ParseClaude(strings.NewReader(claudeSidechainFixture))
	if err != nil {
		t.Fatalf("ParseClaude(sidechain): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 sidechain token rows, got %d: %+v", len(rows), rows)
	}
	for i := range rows {
		if !rows[i].IsSidechain {
			t.Errorf("row %d is_sidechain = false, want true", i)
		}
		if rows[i].AgentID != "agent-abc" {
			t.Errorf("row %d agent_id = %q, want agent-abc", i, rows[i].AgentID)
		}
		// subagent_type is stamped file-wide from the first attributionAgent, so
		// the second record (which omits it) still resolves to "researcher".
		if rows[i].SubagentType != "researcher" {
			t.Errorf("row %d subagent_type = %q, want researcher (file-constant stamp)", i, rows[i].SubagentType)
		}
	}
}
