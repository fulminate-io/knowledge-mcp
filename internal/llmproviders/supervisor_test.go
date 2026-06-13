// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"

	// Blank import registers the OpenAI provider factory so BuildHiveSupervisor
	// can construct a real client from the config read.
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/openai"
)

// TestBuildHiveSupervisor_UnloadedConfig: with config unloaded, BuildHiveSupervisor
// returns (nil, nil) — supervision disabled, the escalation path degrades to the
// conservative log-and-resume fallback.
func TestBuildHiveSupervisor_UnloadedConfig(t *testing.T) {
	// SetForTest(nil) installs a nil singleton so Loaded() reports false; the
	// cleanup restores whatever the prior value was.
	t.Cleanup(config.SetForTest(nil))
	require.False(t, config.Loaded(), "precondition: config must be unloaded")

	sup, err := BuildHiveSupervisor(context.Background())
	require.NoError(t, err)
	require.Nil(t, sup, "unloaded config should yield a nil Supervisor")
}

// TestBuildHiveSupervisor_ValidSection: a valid [default]/[supervisor] config
// (keyless base_url, openai) builds a non-nil Supervisor end-to-end.
func TestBuildHiveSupervisor_ValidSection(t *testing.T) {
	cfg := &config.Config{
		Default: config.Section{
			Provider: config.ProviderOpenAI,
			Model:    "gpt-5-mini",
			BaseURL:  "http://127.0.0.1:9/v1",
		},
		Supervisor: &config.Section{Model: "gpt-5"},
	}
	t.Setenv("OPENAI_API_KEY", "")
	t.Cleanup(config.SetForTest(cfg))

	sup, err := BuildHiveSupervisor(context.Background())
	require.NoError(t, err)
	require.NotNil(t, sup, "valid [supervisor]/[default] config should build a Supervisor")
}

// TestSupervisorJudge_Happy: a well-formed verdict JSON decodes all four fields,
// and the Generate call carries the json_schema response format + system prompt.
func TestSupervisorJudge_Happy(t *testing.T) {
	canned := `{"state":"done","confidence":0.9,"reason":"task complete","result":"added the widget"}`
	fake := llm.NewFakeClient(&llm.Response{
		Content:      canned,
		FinishReason: llm.FinishReasonEndTurn,
	})
	s := NewLLMSupervisor(fake, "fake-model")

	v, err := s.Judge(context.Background(), "assistant: done\ntool: Bash\n")
	require.NoError(t, err)
	assert.Equal(t, "done", v.State)
	assert.InDelta(t, 0.9, v.Confidence, 1e-9)
	assert.Equal(t, "task complete", v.Reason)
	assert.Equal(t, "added the widget", v.Result)

	calls := fake.Calls()
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].Options.ResponseFormat)
	assert.Equal(t, "json_schema", calls[0].Options.ResponseFormat.Type)
	assert.Equal(t, supervisorSystemPrompt, calls[0].Options.SystemPrompt)
}

// TestSupervisorJudge_Malformed: a non-JSON response is an error (caller treats
// any error conservatively).
func TestSupervisorJudge_Malformed(t *testing.T) {
	fake := llm.NewFakeClient(&llm.Response{Content: "not json at all"})
	s := NewLLMSupervisor(fake, "fake-model")

	_, err := s.Judge(context.Background(), "assistant: hi\n")
	require.Error(t, err)
}

// TestSupervisorJudge_EmptyTranscript: an empty transcript is rejected before
// any LLM call.
func TestSupervisorJudge_EmptyTranscript(t *testing.T) {
	fake := llm.NewFakeClient()
	s := NewLLMSupervisor(fake, "fake-model")

	_, err := s.Judge(context.Background(), "   ")
	require.Error(t, err)
	require.Empty(t, fake.Calls(), "empty transcript must short-circuit before Generate")
}

// TestVerdictSchema_AdditionalPropertiesFalse asserts the response schema sets
// additionalProperties:false on the object (codex-cli --output-schema gate).
func TestVerdictSchema_AdditionalPropertiesFalse(t *testing.T) {
	assert.Contains(t, verdictSchema, `"additionalProperties":false`,
		"verdict schema must set additionalProperties:false on every object")
}
