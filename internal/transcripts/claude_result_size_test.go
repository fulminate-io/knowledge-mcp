// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolRowsByName indexes parsed tool rows by tool name.
func toolRowsByName(t *testing.T, rows []Row) map[string]Row {
	t.Helper()
	out := make(map[string]Row, len(rows))
	for _, r := range rows {
		if r.ToolName != "" {
			out[r.ToolName] = r
		}
	}
	return out
}

// TestParseClaude_MeasuresResultBytesImagesSpillAndBackground drives one transcript whose
// tool results vary EVERY axis the four columns claim to discriminate: a string result, a
// list result mixing text and images, an unknown block kind, both spill notice formats, a
// notice-shaped result whose size does not parse, a backgrounded call, and — the one that
// matters most — the Read tool's own refusal, which shares the spill notice's
// "exceeds maximum allowed tokens" literal and is NOT a spill.
//
// Without that known-negative the spill regex could be loosened to the shorter phrase and
// every such refusal would be recorded as a spill with a fabricated byte count, which is
// worse than not measuring spills at all: it would be a wrong number rather than a low one.
func TestParseClaude_MeasuresResultBytesImagesSpillAndBackground(t *testing.T) {
	// The two long inline results are declared as Go strings and JSON-quoted into the
	// fixture, so the byte-length assertions below can be stated against len() of the same
	// literal rather than a hand-counted number that would rot on any edit.
	const readRefusal = "Error: File content (30816 tokens) exceeds maximum allowed tokens (25000). " +
		"Please use offset and limit parameters to read specific portions of the file."
	const unparsedNotice = "Output too large (enormousKB). Full output saved to: /tmp/x.txt"
	const persistedNotice = "<persisted-output>\nOutput too large (58.7KB). Full output saved to: /tmp/big.txt"

	q := func(s string) string {
		b, err := json.Marshal(s)
		require.NoError(t, err)
		return string(b)
	}

	// One record per line: the scanner is JSONL.
	transcript := strings.Join([]string{
		`{"type":"assistant","uuid":"u1","timestamp":"2026-06-01T10:00:00Z","sessionId":"S1","message":{"role":"assistant","model":"m","content":[` +
			`{"type":"tool_use","id":"t-string","name":"StringTool","input":{"q":"a"}},` +
			`{"type":"tool_use","id":"t-list","name":"ListTool","input":{"q":"b"}},` +
			`{"type":"tool_use","id":"t-spill-a","name":"SpillCharsTool","input":{"q":"c"}},` +
			`{"type":"tool_use","id":"t-spill-b","name":"SpillKBTool","input":{"q":"d"}},` +
			`{"type":"tool_use","id":"t-refusal","name":"Read","input":{"file_path":"/big.txt"}},` +
			`{"type":"tool_use","id":"t-unparsed","name":"UnparsedNoticeTool","input":{"q":"e"}},` +
			`{"type":"tool_use","id":"t-bg","name":"Bash","input":{"command":"sleep 1","run_in_background":true}},` +
			`{"type":"tool_use","id":"t-fg","name":"Grep","input":{"pattern":"x","run_in_background":false}}` +
			`]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"u1","timestamp":"2026-06-01T10:00:01Z","sessionId":"S1","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t-string","content":"0123456789"},` +
			`{"type":"tool_result","tool_use_id":"t-list","content":[{"type":"text","text":"abcde"},{"type":"image","source":{"data":"..."}},{"type":"image","source":{"data":"..."}},{"type":"tool_reference","name":"other"},{"type":"text","text":"fg"}]},` +
			`{"type":"tool_result","tool_use_id":"t-spill-a","content":"Error: result (85,217 characters across 1 line) exceeds maximum allowed tokens. Output has been saved to /tmp/out.txt."},` +
			`{"type":"tool_result","tool_use_id":"t-spill-b","content":[{"type":"text","text":` + q(persistedNotice) + `}]},` +
			`{"type":"tool_result","tool_use_id":"t-refusal","content":` + q(readRefusal) + `},` +
			`{"type":"tool_result","tool_use_id":"t-unparsed","content":` + q(unparsedNotice) + `},` +
			`{"type":"tool_result","tool_use_id":"t-bg","content":"started"},` +
			`{"type":"tool_result","tool_use_id":"t-fg","content":"ok"}` +
			`]}}`,
	}, "\n") + "\n"

	rows, err := ParseClaude(strings.NewReader(transcript))
	require.NoError(t, err)
	byName := toolRowsByName(t, rows)
	require.Len(t, byName, 8, "every tool_use yielded a row")

	t.Run("a string result is its own byte length", func(t *testing.T) {
		r := byName["StringTool"]
		assert.Equal(t, int64(10), r.ToolResultBytes)
		assert.Equal(t, int64(0), r.ToolResultImages)
		assert.False(t, r.ToolResultSpilled)
	})

	t.Run("a list result sums text and counts images", func(t *testing.T) {
		r := byName["ListTool"]
		assert.Equal(t, int64(2), r.ToolResultImages, "two image blocks, counted not sized")
		// "abcde" (5) + "fg" (2) + the tool_reference block's raw encoded length, which is
		// measured rather than dropped so an unknown kind is under-attributed, never invisible.
		assert.Greater(t, r.ToolResultBytes, int64(7), "the unknown tool_reference block contributes its encoded length")
		assert.Less(t, r.ToolResultBytes, int64(100), "and only that block, not the whole content")
		assert.False(t, r.ToolResultSpilled)
	})

	t.Run("both spill notice formats recover the real size", func(t *testing.T) {
		a := byName["SpillCharsTool"]
		assert.True(t, a.ToolResultSpilled)
		assert.Equal(t, int64(85217), a.ToolResultBytes, "the comma-grouped character count is parsed")
		assert.Greater(t, a.ToolResultBytes, int64(1000), "and it is the RECOVERED size, not the short notice's length")

		b := byName["SpillKBTool"]
		assert.True(t, b.ToolResultSpilled)
		assert.Equal(t, int64(60109), b.ToolResultBytes, "58.7KB rounded at 1024 bytes per KB")
	})

	t.Run("the Read tool's own refusal is NOT a spill", func(t *testing.T) {
		r := byName["Read"]
		assert.False(t, r.ToolResultSpilled,
			"it shares the 'exceeds maximum allowed tokens' literal but nothing was saved to a file")
		assert.Equal(t, int64(len(readRefusal)), r.ToolResultBytes, "it is measured at its real inline length")
	})

	t.Run("a notice whose size does not parse records the inline length", func(t *testing.T) {
		r := byName["UnparsedNoticeTool"]
		assert.False(t, r.ToolResultSpilled, "an unparseable size is an honest under-measurement, not a guess")
		assert.Equal(t, int64(len(unparsedNotice)), r.ToolResultBytes)
	})

	t.Run("run_in_background is read from the tool_use input", func(t *testing.T) {
		assert.True(t, byName["Bash"].RunInBackground)
		assert.False(t, byName["Grep"].RunInBackground, "an explicit false is false, not merely absent")
		assert.False(t, byName["StringTool"].RunInBackground, "and an absent key is false")
	})
}

// TestRecoverSpilledBytes_KnownNegatives pins the recovery's REFUSALS directly, so the
// boundary is asserted on the function rather than only through the parser. Each string
// here is a shape the corpus actually contains.
func TestRecoverSpilledBytes_KnownNegatives(t *testing.T) {
	negatives := map[string]string{
		"read refusal":             "Error: File content (30816 tokens) exceeds maximum allowed tokens (25000). Please use offset and limit parameters",
		"prose mentioning a spill": "I saw that the output was saved to a file earlier in this session.",
		"empty":                    "",
		"ordinary result":          "ok",
	}
	for name, s := range negatives {
		t.Run(name, func(t *testing.T) {
			_, ok := recoverSpilledBytes(s)
			assert.False(t, ok, "must not be read as a spill")
		})
	}

	// The known-positives on the same instrument, so a recovery that refused EVERYTHING
	// would not pass this test by refusing.
	n, ok := recoverSpilledBytes("Error: result (1,024 characters across 2 lines) exceeds maximum allowed tokens. Output has been saved to /tmp/a.")
	require.True(t, ok)
	assert.Equal(t, int64(1024), n)

	n, ok = recoverSpilledBytes("Output too large (2.0KB). Full output saved to: /tmp/b")
	require.True(t, ok)
	assert.Equal(t, int64(2048), n)
}
