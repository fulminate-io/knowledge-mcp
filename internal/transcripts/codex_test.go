// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"strings"
	"testing"
)

// codexFixture: a layered rollout — session_meta (line 1, carries id/cwd/
// cli_version/git.branch), a turn_context model, a token_count whose
// last_token_usage differs from total_token_usage (proving we read last), a
// function_call + a function_call_output reporting a non-zero exit (error
// heuristic), and a second token_count.
const codexFixture = `{"timestamp":"2026-05-02T16:03:39.200Z","type":"session_meta","payload":{"id":"sess-cx","cwd":"/Users/me/code/knowledge","cli_version":"0.128.0","git":{"branch":"main","commit_hash":"abc"}}}
{"timestamp":"2026-05-02T16:03:39.300Z","type":"turn_context","payload":{"model":"gpt-5.5","cwd":"/Users/me/code/knowledge"}}
{"timestamp":"2026-05-02T16:03:40.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":9999,"cached_input_tokens":999,"output_tokens":999,"reasoning_output_tokens":99},"last_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":20,"reasoning_output_tokens":5}}}}
{"timestamp":"2026-05-02T16:03:41.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call_1","arguments":"{}"}}
{"timestamp":"2026-05-02T16:03:42.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"Wall time: 1.2s\nProcess exited with code 1\n"}}
{"timestamp":"2026-05-02T16:03:43.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":8888,"cached_input_tokens":888,"output_tokens":888,"reasoning_output_tokens":88},"last_token_usage":{"input_tokens":200,"cached_input_tokens":20,"output_tokens":30,"reasoning_output_tokens":7}}}}`

func TestParseCodex(t *testing.T) {
	rows, err := ParseCodex(strings.NewReader(codexFixture))
	if err != nil {
		t.Fatalf("ParseCodex error: %v", err)
	}

	// Expect 3 rows: token#1, tool, token#2.
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rows), rows)
	}

	var tokenRows []Row
	var tool *Row
	for i := range rows {
		if rows[i].ToolName != "" {
			tool = &rows[i]
		} else {
			tokenRows = append(tokenRows, rows[i])
		}
	}
	if len(tokenRows) != 2 {
		t.Fatalf("want 2 token rows, got %d", len(tokenRows))
	}
	if tool == nil {
		t.Fatal("no tool Row found")
	}

	first := tokenRows[0]
	// Tokens come from last_token_usage, NOT total (would be 9999).
	if first.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (last, not total 9999)", first.InputTokens)
	}
	// reasoning folded into output: 20 + 5.
	if first.OutputTokens != 25 {
		t.Errorf("OutputTokens = %d, want 25 (output 20 + reasoning 5 folded)", first.OutputTokens)
	}
	// cached_input → its own cache_read column, NOT folded into input.
	if first.CacheReadTokens != 10 {
		t.Errorf("CacheReadTokens = %d, want 10 (cached_input_tokens)", first.CacheReadTokens)
	}
	if first.CacheCreationTokens != 0 {
		t.Errorf("CacheCreationTokens = %d, want 0 (codex has no cache_creation)", first.CacheCreationTokens)
	}
	// Carried scan state: session/cwd/branch + turn model.
	if first.Source != SourceCodex {
		t.Errorf("Source = %q, want codex", first.Source)
	}
	if first.SessionID != "sess-cx" || first.Project != "/Users/me/code/knowledge" || first.GitBranch != "main" {
		t.Errorf("carried state wrong: session=%q project=%q branch=%q", first.SessionID, first.Project, first.GitBranch)
	}
	if first.Model != "gpt-5.5" {
		t.Errorf("Model = %q, want gpt-5.5 (carried from turn_context)", first.Model)
	}

	// Second token row uses its own last_token_usage.
	if tokenRows[1].InputTokens != 200 || tokenRows[1].OutputTokens != 37 || tokenRows[1].CacheReadTokens != 20 {
		t.Errorf("second token row wrong: in=%d out=%d cr=%d (want 200/37/20)",
			tokenRows[1].InputTokens, tokenRows[1].OutputTokens, tokenRows[1].CacheReadTokens)
	}

	// Tool row: name + best-effort error from the non-zero exit output.
	if tool.ToolName != "shell" {
		t.Errorf("tool ToolName = %q, want shell", tool.ToolName)
	}
	if !tool.IsError {
		t.Error("tool IsError = false, want true ('Process exited with code 1' heuristic)")
	}
	// Optional Codex duration: function_call_output.ts(42.000) − call.ts(41.000).
	if tool.DurationMs != 1000 {
		t.Errorf("tool duration_ms = %d, want 1000 (output.ts − call.ts)", tool.DurationMs)
	}

	// Codex has no subagent/attribution/cache-split/service-tier analog: those
	// enrichment columns stay at their zero/empty values on every row (schema
	// parity with claude rows).
	for i := range rows {
		r := rows[i]
		if r.IsSidechain || r.AgentID != "" || r.SubagentType != "" ||
			r.CacheCreation1hTokens != 0 || r.CacheCreation5mTokens != 0 ||
			r.ServiceTier != "" || r.WebSearchCount != 0 || r.WebFetchCount != 0 ||
			r.StopReason != "" || r.IsAPIError || r.IsMeta || r.Interrupted ||
			r.MCPServer != "" || r.MCPTool != "" || r.Skill != "" || r.ToolInputHash != "" {
			t.Errorf("row %d carries a non-zero enrichment column with no Codex analog: %+v", i, r)
		}
	}

	// SourceOffset non-decreasing; CLIVersion carried from line-1 session_meta on every Row.
	var prev int64 = -1
	for i := range rows {
		if rows[i].SourceOffset < prev {
			t.Errorf("row %d SourceOffset %d decreased from %d", i, rows[i].SourceOffset, prev)
		}
		prev = rows[i].SourceOffset
		if rows[i].CLIVersion != "0.128.0" {
			t.Errorf("row %d CLIVersion = %q, want 0.128.0 (carried from session_meta)", i, rows[i].CLIVersion)
		}
		// Codex carries no per-record uuid.
		if rows[i].UUID != "" || rows[i].ParentUUID != "" {
			t.Errorf("row %d has non-empty uuid/parent (codex carries none): uuid=%q parent=%q", i, rows[i].UUID, rows[i].ParentUUID)
		}
	}
}
