// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPresignResponse_DecodesAgentBody asserts the presignResponse is the
// verbatim json-tag mirror of the agent's presign reply: an agent-shaped
// {upload_url, object_path, agent_public_key, expiry} body decodes field-for-field
// (the same shape graph-sync's syncPresignResponse decodes).
func TestPresignResponse_DecodesAgentBody(t *testing.T) {
	body := `{"upload_url":"https://storage.googleapis.com/bucket/obj?sig=x",` +
		`"object_path":"transcripts/acct/cli/claude/sess/gen/0.part",` +
		`"agent_public_key":"-----BEGIN PUBLIC KEY-----\nMII...\n-----END PUBLIC KEY-----\n",` +
		`"expiry":"2099-01-01T00:00:00Z"}`

	var resp presignResponse
	require.NoError(t, json.Unmarshal([]byte(body), &resp))

	assert.Equal(t, "https://storage.googleapis.com/bucket/obj?sig=x", resp.UploadURL)
	assert.Equal(t, "transcripts/acct/cli/claude/sess/gen/0.part", resp.ObjectPath)
	assert.Contains(t, resp.AgentPublicKey, "BEGIN PUBLIC KEY",
		"agent_public_key decodes into AgentPublicKey")
	assert.Equal(t, "2099-01-01T00:00:00Z", resp.Expiry)
}

// TestBatchModeRequestDTOs_MarshalShape pins the batch wire contract: the batch
// envelopes carry a top-level mode + chunks array, the per-object identity is now
// just source+session (the generation/chunk_seq/start_offset fields are GONE — one
// parquet object per session), and the confirm response carries results with
// ok/error. The keys must byte-match the agent's sync_batch.go contract exactly.
func TestBatchModeRequestDTOs_MarshalShape(t *testing.T) {
	presignBatch, err := json.Marshal(presignBatchRequest{
		Mode: ModeTranscript,
		Chunks: []presignBatchChunk{
			{Source: "claude", Session: "sess-abc"},
		},
	})
	require.NoError(t, err)
	ps := string(presignBatch)
	assert.Contains(t, ps, `"mode":"transcript"`, "presign-batch mode discriminator")
	assert.Contains(t, ps, `"chunks":[`, "presign-batch chunks array")
	assert.Contains(t, ps, `"source":"claude"`)
	assert.Contains(t, ps, `"session":"sess-abc"`)
	assert.NotContains(t, ps, `"generation"`, "generation dropped from the object identity")
	assert.NotContains(t, ps, `"chunk_seq"`, "chunk_seq dropped from the object identity")
	assert.NotContains(t, ps, `"start_offset"`, "start_offset dropped from the object identity")

	confirmBatch, err := json.Marshal(confirmBatchRequest{
		Mode: ModeTranscript,
		Chunks: []confirmBatchChunk{
			{ObjectPath: "transcripts-staging/acct/obj-abc", Source: "claude", Session: "sess-abc"},
		},
	})
	require.NoError(t, err)
	cs := string(confirmBatch)
	assert.Contains(t, cs, `"mode":"transcript"`)
	assert.Contains(t, cs, `"chunks":[`)
	assert.Contains(t, cs, `"object_path":"transcripts-staging/acct/obj-abc"`)
	assert.NotContains(t, cs, `"generation"`, "generation dropped from confirm identity")
	assert.NotContains(t, cs, `"chunk_seq"`, "chunk_seq dropped from confirm identity")

	// confirm-batch response: results array with ok + (omitempty) error.
	resp, err := json.Marshal(confirmBatchResponse{Results: []confirmElementResult{{OK: true}, {OK: false, Error: "forbidden_path"}}})
	require.NoError(t, err)
	rs := string(resp)
	assert.Contains(t, rs, `"results":[`, "confirm-batch results array")
	assert.Contains(t, rs, `"ok":true`)
	assert.Contains(t, rs, `"ok":false`)
	assert.Contains(t, rs, `"error":"forbidden_path"`)
	assert.NotContains(t, rs, `"error":""`, "error is omitempty on a clean result")

	// presign-batch response: each element carries upload_url, object_path, agent_public_key.
	presignResp, err := json.Marshal(presignBatchResponse{Chunks: []presignResponse{
		{UploadURL: "https://gcs/put", ObjectPath: "transcripts/acct/x.part", AgentPublicKey: "PEM", Expiry: "2099-01-01T00:00:00Z"},
	}})
	require.NoError(t, err)
	prs := string(presignResp)
	assert.Contains(t, prs, `"chunks":[`)
	assert.Contains(t, prs, `"upload_url":"https://gcs/put"`)
	assert.Contains(t, prs, `"object_path":"transcripts/acct/x.part"`)
	assert.Contains(t, prs, `"agent_public_key":"PEM"`)
}

// TestRollupPayload_MarshalShape is the client half of the hand-mirrored rollup
// round-trip (its backend counterpart pins the identical shapes). It populates one
// rollupPayload with a non-zero row of every kind, points a confirmBatchChunk at it,
// marshals, and asserts (1) every frozen snake_case tag is present verbatim, (2) the
// payload rides under "rollup", and (3) NO identity field (account_id/user_id/
// session_id/source) leaks into any row object.
func TestRollupPayload_MarshalShape(t *testing.T) {
	payload := rollupPayload{
		SchemaVersion: rollupSchemaVersion,
		Session: sessionScalars{
			Project: "/work", FirstRecordTS: "2026-06-01T00:00:00Z", LastRecordTS: "2026-06-01T01:00:00Z",
			AgentChainDepth: 2, RecordCount: 200, InputTokens: 1000, OutputTokens: 500,
			CacheReadTokens: 10, CacheCreationTokens: 20, CacheCreation1hTokens: 3, CacheCreation5mTokens: 4,
			DurationMs: 9000, ErrorCount: 1, APIErrorCount: 2, InterruptedCount: 3, WebSearchCount: 4, WebFetchCount: 5,
		},
		Facts: []factRow{{
			Day: "2026-06-01", Model: "sonnet", ToolName: "Bash", Project: "/work", SubagentType: "impl",
			AgentID: "a1", IsSidechain: true, IsMeta: true, MCPServer: "srv", MCPTool: "mtool", Skill: "sk",
			ServiceTier: "std", StopReason: "end_turn", RecordCount: 3, InputTokens: 30, OutputTokens: 15,
			CacheReadTokens: 1, CacheCreationTokens: 2, CacheCreation1hTokens: 1, CacheCreation5mTokens: 1,
			DurationMs: 500, TrustworthyDurationMs: 400, APIErrorCount: 1, InterruptedCount: 1, ErrorCount: 1,
			WebSearchCount: 1, WebFetchCount: 1, MinRecordTS: "2026-06-01T00:00:00Z", MaxRecordTS: "2026-06-01T00:30:00Z",
		}},
		LatencyHist: []latencyHistRow{{
			Day: "2026-06-01", ToolName: "Bash", Model: "sonnet", Project: "/work",
			IsSidechain: false, IsMeta: true, Bucket: 7, CallCount: 12,
		}},
		SlowCalls: []slowCallRow{{
			Day: "2026-06-01", ToolName: "Bash", Model: "sonnet", Project: "/work", IsSidechain: false,
			MCPServer: "srv", MCPTool: "mtool", DurationMs: 4200, RecordTS: "2026-06-01T00:10:00Z",
			ToolInputPreview: "ls -la",
		}},
		DuplicateCommands: []duplicateRow{{
			Day: "2026-06-01", ToolName: "Bash", ToolInputHash: "deadbeef", Model: "sonnet", Project: "/work",
			IsSidechain: false, IsMeta: true, RunCount: 3, WastedDurationMs: 1200,
			SamplePreview: "git status", FirstRecordTS: "2026-06-01T00:05:00Z",
		}},
	}
	chunk := confirmBatchChunk{ObjectPath: "transcripts/acct/obj", Source: "claude", Session: "sess-1", Rollup: &payload}

	raw, err := json.Marshal(chunk)
	require.NoError(t, err)
	s := string(raw)

	// The payload rides under "rollup" on the confirm chunk.
	assert.Contains(t, s, `"rollup":{`, "rollup payload rides under the rollup key")

	// Top-level payload keys + schema version.
	for _, tag := range []string{
		`"schema_version":1`, `"session":{`, `"facts":[`, `"latency_hist":[`, `"slow_calls":[`, `"duplicate_commands":[`,
	} {
		assert.Contains(t, s, tag, "frozen top-level tag present verbatim")
	}

	// Per-row frozen tags (a representative set spanning every row kind).
	for _, tag := range []string{
		`"agent_chain_depth":`, `"first_record_ts":`, `"last_record_ts":`, // sessionScalars
		`"trustworthy_duration_ms":`, `"cache_creation_1h_tokens":`, `"cache_creation_5m_tokens":`, // factRow
		`"min_record_ts":`, `"max_record_ts":`, `"subagent_type":`, `"service_tier":`, `"stop_reason":`,
		`"bucket":`, `"call_count":`, // latencyHistRow
		`"tool_input_preview":`, `"record_ts":`, // slowCallRow
		`"tool_input_hash":`, `"run_count":`, `"wasted_duration_ms":`, `"sample_preview":`, // duplicateRow
	} {
		assert.Contains(t, s, tag, "frozen per-row tag present verbatim")
	}

	// is_meta must appear on BOTH the latency_hist row and the duplicate_commands row
	// (the contract v1-amendment grain field). The two rows differing only in IsSidechain
	// (false here) still each carry is_meta; assert the exact count is 3 (fact + hist +
	// duplicate), proving it joins those grains and is absent from slow_calls.
	assert.Equal(t, 3, strings.Count(s, `"is_meta":`),
		"is_meta present on fact + latency_hist + duplicate_commands rows, absent from slow_calls")

	// Identity is NEVER in any row — the backend stamps it from the chunk + bearer context.
	// Assert against the marshaled ROLLUP object only (the enclosing chunk legitimately
	// carries source/session as its own identity fields).
	rollupOnly, err := json.Marshal(payload)
	require.NoError(t, err)
	rs := string(rollupOnly)
	for _, forbidden := range []string{`"account_id"`, `"user_id"`, `"session_id"`, `"source"`} {
		assert.NotContains(t, rs, forbidden, "identity field must not leak into the rollup payload")
	}
}

// TestClampBatchSize pins the batch-size clamp: an unset/non-positive size defaults
// to 32 and an over-cap size is clamped DOWN to the 512 ceiling (never passed
// through), so an over-cap misconfig can never 400 every batch and strand files.
func TestClampBatchSize(t *testing.T) {
	assert.Equal(t, defaultBatchSize, clampBatchSize(0), "unset → default 32")
	assert.Equal(t, defaultBatchSize, clampBatchSize(-5), "negative → default 32")
	assert.Equal(t, 32, defaultBatchSize, "default is 32")
	assert.Equal(t, 8, clampBatchSize(8), "in-range value passes through")
	assert.Equal(t, maxBatchChunks, clampBatchSize(maxBatchChunks), "the ceiling itself passes through")
	assert.Equal(t, maxBatchChunks, clampBatchSize(1000), "over-cap → clamped down to 512, never passed through")
	assert.Equal(t, 512, maxBatchChunks, "the ceiling is 512")
}
