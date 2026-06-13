// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
	"github.com/fulminate-io/knowledge-mcp/internal/llm/anthropic"
)

// TestSummarizeBatchAnthropicFallbackBridge is the consumer-terminating test
// for the forced-tool fallback content bridge. It drives a REAL anthropic
// Service (not a fake llm.Client) on a GATED, non-native model
// (claude-3-haiku-20240307) so the structured-output request takes the
// forced-tool_use fallback. The fake server replies with a structured_output
// tool_use block carrying the items JSON and an EMPTY text body — the exact
// shape that, without the Generate-level bridge, leaves resp.Content empty and
// makes parseSummariesContent fail.
//
// The assertion terminates at the CONSUMER (SummarizeBatch yields a non-empty
// summary map), not at resp.ToolCalls: if the bridge is dropped, resp.Content
// is empty, parseSummariesContent returns a parse error, and SummarizeBatch
// returns an error with no summaries — making this test go RED.
func TestSummarizeBatchAnthropicFallbackBridge(t *testing.T) {
	// One chunk → one summary item. The model "returns" the structured
	// output as the synthesized tool's input, with no text block.
	itemsJSON := `{"items":[{"summary":"a concise summary","keywords":["alpha","beta","gamma"]}]}`

	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the request body for assertion AFTER Generate returns —
		// asserting inside the handler trips testifylint's go-require rule
		// (a failed require in a handler goroutine wouldn't abort the test).
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id":"msg_fb","type":"message","role":"assistant","model":"claude-3-haiku-20240307",
			"content":[{"type":"tool_use","id":"tu_1","name":"structured_output","input":` + itemsJSON + `}],
			"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":20}
		}`))
	}))
	t.Cleanup(srv.Close)

	// Real anthropic Service against the fake server, gated model.
	svc := anthropic.New("test-key", srv.URL, "claude-3-haiku-20240307", srv.Client())
	s := NewLLMSummarizer(svc, llm.ProviderAnthropic, "claude-3-haiku-20240307")

	out, err := s.SummarizeBatch(context.Background(), []BatchChunk{
		{ID: "chunk-1", Content: "some source code"},
	})
	require.NoError(t, err, "fallback bridge must let SummarizeBatch parse the tool_use output")
	require.Len(t, out, 1, "exactly one summary expected")
	assert.Equal(t, "a concise summary", out["chunk-1"].Summary)
	assert.Equal(t, "alpha beta gamma", out["chunk-1"].Keywords)

	// Confirm the request actually took the fallback (forced-tool) path:
	// tool_choice pins the synthesized structured_output tool, and the native
	// output_config knob is absent.
	var sent struct {
		ToolChoice json.RawMessage `json:"tool_choice"`
		Tools      []struct {
			Name string `json:"name"`
		} `json:"tools"`
		OutputConfig json.RawMessage `json:"output_config"`
	}
	require.NoError(t, json.Unmarshal(capturedBody, &sent))
	require.NotEmpty(t, sent.ToolChoice, "fallback request must carry tool_choice")
	require.Empty(t, sent.OutputConfig, "fallback request must NOT carry output_config")
	require.Len(t, sent.Tools, 1)
	assert.Equal(t, "structured_output", sent.Tools[0].Name)
}
