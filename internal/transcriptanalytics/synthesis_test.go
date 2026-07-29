// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestSynthesize_WithFakeClient proves a configured synthesizer feeds the detector
// report to the LLM under the json_schema and returns the ranked recommendations the
// model produced, and that it sends the response-format + system prompt.
func TestSynthesize_WithFakeClient(t *testing.T) {
	fake := llm.NewFakeClient(&llm.Response{
		Content: `{"recommendations":[
			{"title":"Stop rerunning the same Bash command","category":"duplicate_work","impact":"high","rationale":"Bash/h1 ran twice wasting 2000ms"},
			{"title":"Reduce Read latency","category":"tool_latency","impact":"medium","rationale":"Read p90 is 4095ms"}
		]}`,
	})
	s := &Synthesizer{client: fake, model: "test-model"}

	got, err := s.Synthesize(context.Background(), &DetectorReport{
		DuplicateCommands: []DuplicateCommandRow{{SessionID: "S1", ToolName: "Bash", RunCount: 2, WastedDurationMs: 2000}},
	})
	require.NoError(t, err)
	require.Len(t, got.Recommendations, 2)
	assert.Equal(t, "high", got.Recommendations[0].Impact)
	assert.Equal(t, "duplicate_work", got.Recommendations[0].Category)

	// The call carried the json_schema response format + the system prompt.
	calls := fake.Calls()
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].Options.ResponseFormat)
	assert.Equal(t, "json_schema", calls[0].Options.ResponseFormat.Type)
	assert.NotEmpty(t, calls[0].Options.SystemPrompt)
}

// TestSynthesize_NilClientDegrades proves the detector-only degrade: a nil Synthesizer
// and a Synthesizer with a nil client both return an empty, NON-error result, so the
// analyzer still returns its deterministic detector output when no LLM is configured.
func TestSynthesize_NilClientDegrades(t *testing.T) {
	report := &DetectorReport{
		DuplicateCommands: []DuplicateCommandRow{{SessionID: "S1", ToolName: "Bash", RunCount: 2}},
	}

	var nilSynth *Synthesizer
	got, err := nilSynth.Synthesize(context.Background(), report)
	require.NoError(t, err)
	assert.Empty(t, got.Recommendations, "a nil synthesizer degrades to empty recommendations")

	unconfigured := &Synthesizer{client: nil}
	got, err = unconfigured.Synthesize(context.Background(), report)
	require.NoError(t, err)
	assert.Empty(t, got.Recommendations, "a nil client degrades to empty recommendations")
}
